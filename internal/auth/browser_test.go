package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeConsole stands in for pantech-console's /api/cli/token: it hands out the
// grant only for the verifier whose S256 is the challenge the CLI sent.
func fakeConsole(t *testing.T, challenge *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code     string `json:"code"`
			Verifier string `json:"code_verifier"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		sum := sha256.Sum256([]byte(body.Verifier))
		if r.URL.Path != "/api/cli/token" || body.Code != "the-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != *challenge {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"detail":"This sign-in code is invalid or has expired."}`))
			return
		}
		_, _ = w.Write([]byte(`{"api_key":"PAN_x","key_id":"key_1","organization_id":"org_1","organization_name":"Acme","scopes":["write"],"expires_at":"2027-01-01T00:00:00Z","api_url":"https://api-dev.example"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// approve plays the browser: reads the authorize URL the CLI built and calls
// its callback with the given query, returning the page the CLI answered with.
func approve(t *testing.T, authorize string, query func(state string) url.Values, page chan<- string, challenge *string) error {
	u, err := url.Parse(authorize)
	if err != nil {
		return err
	}
	q := u.Query()
	*challenge = q.Get("challenge")
	go func() {
		res, err := http.Get("http://127.0.0.1:" + q.Get("port") + "/callback?" + query(q.Get("state")).Encode())
		if err != nil {
			page <- err.Error()
			return
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		page <- string(b)
	}()
	return nil
}

func TestLoginApproved(t *testing.T) {
	var challenge string
	console := fakeConsole(t, &challenge)
	page := make(chan string, 1)
	b := &Browser{
		ConsoleURL: console.URL,
		Host:       "my mac!",
		Open: func(authorize string) error {
			if !strings.Contains(authorize, "host=my+mac") {
				t.Errorf("host not passed (or not sanitized): %s", authorize)
			}
			return approve(t, authorize, func(state string) url.Values { return url.Values{"state": {state}, "code": {"the-code"}} }, page, &challenge)
		},
	}
	grant, err := b.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if grant.APIKey != "PAN_x" || grant.APIURL != "https://api-dev.example" {
		t.Fatalf("grant = %+v", grant)
	}
	if p := <-page; !strings.Contains(p, "signed in") {
		t.Fatalf("callback page: %s", p)
	}
}

func TestLoginRefused(t *testing.T) {
	var challenge string
	console := fakeConsole(t, &challenge)
	page := make(chan string, 1)
	b := &Browser{ConsoleURL: console.URL, Open: func(authorize string) error {
		return approve(t, authorize, func(state string) url.Values { return url.Values{"state": {state}, "error": {"access_denied"}} }, page, &challenge)
	}}
	if _, err := b.Login(context.Background()); !errors.Is(err, ErrDenied) {
		t.Fatalf("err = %v, want ErrDenied", err)
	}
}

func TestLoginVerifyFailureReachesTheBrowser(t *testing.T) {
	var challenge string
	console := fakeConsole(t, &challenge)
	page := make(chan string, 1)
	b := &Browser{
		ConsoleURL: console.URL,
		Open: func(authorize string) error {
			return approve(t, authorize, func(state string) url.Values { return url.Values{"state": {state}, "code": {"the-code"}} }, page, &challenge)
		},
		Verify: func(context.Context, *Grant) error { return errors.New("The key is not valid.") },
	}
	if _, err := b.Login(context.Background()); err == nil {
		t.Fatal("a failed check must fail the sign-in")
	}
	if p := <-page; !strings.Contains(p, "Sign-in failed") || strings.Contains(p, "signed in") {
		t.Fatalf("the tab must say the sign-in failed: %s", p)
	}
}

func TestLoginTimesOut(t *testing.T) {
	b := &Browser{ConsoleURL: "http://127.0.0.1:1", Timeout: 50 * time.Millisecond}
	if _, err := b.Login(context.Background()); err == nil || !strings.Contains(err.Error(), "no approval") {
		t.Fatalf("err = %v", err)
	}
}
