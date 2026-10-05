// Package auth signs the CLI in through the console, the way `gh auth login`
// does: a browser approval, a loopback callback with a one-time code, and a
// PKCE-guarded exchange of that code for an API key; docs/cli-auth-protocol.md.
package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type Grant struct {
	APIKey           string   `json:"api_key"`
	KeyID            string   `json:"key_id"`
	OrganizationID   string   `json:"organization_id"`
	OrganizationName *string  `json:"organization_name"`
	Scopes           []string `json:"scopes"`
	ExpiresAt        string   `json:"expires_at"`
	// Ignored: the CLI always calls the production API.
	APIURL string `json:"api_url"`
}

var ErrDenied = errors.New("the sign-in was cancelled in the browser")

var ErrNotOffered = errors.New("this console does not offer browser sign-in for the CLI yet\n" +
	"Create an API key in the console under Organization › API keys and run:\n" +
	"  pantech auth login --with-token < key.txt")

// ErrGated is a console behind an access gate (pantech-console's staging
// gate answers the CLI's cookie-less requests with a redirect to
// /staging-access or a 401). Browser sign-in cannot finish there by design.
var ErrGated = errors.New("this console is behind an access gate (staging), so browser sign-in for the CLI is not available here\n" +
	"Create an API key in the console under Organization › API keys and run:\n" +
	"  pantech auth login --with-token < key.txt")

// probe asks the console whether it has the authorize page, so a console
// without the CLI sign-in fails at once instead of after a browser shows a
// 404 and the wait times out. Only a 404 counts: any other answer, or none,
// is left to the sign-in itself.
func (b *Browser) probe(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, strings.TrimRight(b.ConsoleURL, "/")+"/cli/authorize", nil)
	if err != nil {
		return nil
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		// A redirect (to the console's sign-in page) means the route exists.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if b.HTTP != nil {
		client.Transport = b.HTTP.Transport
	}
	res, err := client.Do(req)
	if err != nil {
		return nil
	}
	_ = res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return ErrNotOffered
	}
	if strings.Contains(res.Header.Get("Location"), "/staging-access") || res.StatusCode == http.StatusUnauthorized {
		return ErrGated
	}
	return nil
}

type Browser struct {
	ConsoleURL string // e.g. https://console.pantechdynamics.com
	Host       string // this machine's name, for the key's name
	HTTP       *http.Client
	// Open opens a URL in the browser; nil means only Prompt is called.
	Open   func(string) error
	Prompt func(authorizeURL string, opened bool)
	// Timeout for the whole approval. Default 5 minutes.
	Timeout time.Duration
	// Verify checks the grant (e.g. that the key works) before the browser is
	// told the sign-in worked. Optional.
	Verify func(context.Context, *Grant) error
}

func (b *Browser) Login(ctx context.Context) (*Grant, error) {
	if err := b.probe(ctx); err != nil {
		return nil, err
	}
	verifier := randomString(32)
	state := randomString(24)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("could not listen for the browser's answer: %w", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	type result struct {
		code string
		err  error
	}
	results := make(chan result, 1)
	// The sign-in's outcome, for the browser tab still waiting on its callback.
	outcome := make(chan error, 1)
	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/callback" {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			// Anything without our state is not the console answering us.
			if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
				http.Error(w, "This sign-in link does not match the one the CLI started. Run pantech auth login again.", http.StatusBadRequest)
				return
			}
			switch {
			case q.Get("error") != "":
				page(w, "Sign-in cancelled", "Nothing was created. You can close this tab.")
				select {
				case results <- result{err: ErrDenied}:
				default:
				}
			case q.Get("code") != "":
				select {
				case results <- result{code: q.Get("code")}:
				default:
					page(w, "Already signed in", "This sign-in has already been used. You can close this tab.")
					return
				}
				// Answer only once the key is collected and checked, so the tab
				// never says "signed in" when the terminal says otherwise.
				select {
				case err := <-outcome:
					if err != nil {
						page(w, "Sign-in failed", err.Error()+" Your terminal has the details.")
					} else {
						page(w, "You're signed in", "The Pantech CLI has its key. You can close this tab and go back to your terminal.")
					}
				case <-time.After(45 * time.Second):
					page(w, "Still working", "The CLI is still collecting its key. Your terminal will say when it is done.")
				}
			default:
				http.Error(w, "Missing code.", http.StatusBadRequest)
			}
		}),
	}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	authorize := strings.TrimRight(b.ConsoleURL, "/") + "/cli/authorize?" + url.Values{
		"port":      {fmt.Sprint(port)},
		"state":     {state},
		"challenge": {challenge},
		"host":      {sanitizeHost(b.Host)},
	}.Encode()

	opened := false
	if b.Open != nil {
		opened = b.Open(authorize) == nil
	}
	if b.Prompt != nil {
		b.Prompt(authorize, opened)
	}

	timeout := b.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	wait, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var code string
	select {
	case <-wait.Done():
		if errors.Is(wait.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("no approval within %s: run pantech auth login again\nIf the browser showed \"page not found\", sign in with a key instead: pantech auth login --with-token < key.txt", timeout)
		}
		return nil, wait.Err()
	case r := <-results:
		if r.err != nil {
			return nil, r.err
		}
		code = r.code
	}
	grant, err := b.exchange(ctx, code, verifier)
	if err == nil && b.Verify != nil {
		err = b.Verify(ctx, grant)
	}
	outcome <- err
	if err != nil {
		return nil, err
	}
	return grant, nil
}

func (b *Browser) exchange(ctx context.Context, code, verifier string) (*Grant, error) {
	body, _ := json.Marshal(map[string]string{"code": code, "code_verifier": verifier})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.ConsoleURL, "/")+"/api/cli/token", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := b.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach the console to collect the key: %w", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNotOffered
	}
	if res.StatusCode == http.StatusUnauthorized {
		return nil, ErrGated
	}
	if res.StatusCode != http.StatusOK {
		var problem struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(data, &problem) == nil && problem.Detail != "" {
			return nil, errors.New(problem.Detail)
		}
		return nil, fmt.Errorf("the console refused the sign-in code (%s)", res.Status)
	}
	var grant Grant
	if err := json.Unmarshal(data, &grant); err != nil || grant.APIKey == "" {
		return nil, errors.New("the console's answer had no key in it")
	}
	return &grant, nil
}

func OpenBrowser(target string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "linux":
		cmd = exec.Command("xdg-open", target)
	default:
		return errors.New("no browser opener for " + runtime.GOOS)
	}
	return cmd.Start()
}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// sanitizeHost keeps the characters the console accepts in a key's name.
func sanitizeHost(h string) string {
	var b strings.Builder
	for _, r := range h {
		if r == ' ' || r == '.' || r == '@' || r == '-' || r == '_' || ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9') {
			b.WriteRune(r)
		}
		if b.Len() >= 60 {
			break
		}
	}
	return b.String()
}

func page(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%[1]s · Pantech CLI</title>
<style>:root{color-scheme:light dark}body{margin:0;min-height:100vh;display:grid;place-items:center;font:16px/1.5 system-ui,-apple-system,sans-serif;background:Canvas;color:CanvasText}main{max-width:28rem;padding:2rem}h1{font-size:1.5rem;margin:0 0 .5rem}p{margin:0;opacity:.7}</style></head>
<body><main><h1>%[1]s</h1><p>%[2]s</p></main></body></html>`, html.EscapeString(title), html.EscapeString(body))
}
