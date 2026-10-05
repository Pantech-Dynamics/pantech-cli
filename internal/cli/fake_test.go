package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Pantech-Dynamics/pantech-cli/internal/api"
	"github.com/Pantech-Dynamics/pantech-cli/internal/config"
	"github.com/Pantech-Dynamics/pantech-cli/internal/output"
)

// call is one request the fake API received.
type call struct {
	Method, Path, Query, IdempotencyKey string
	Body                                []byte
}

// reply is one canned answer; a route with several answers gives them in
// turn and then repeats the last, the way polling sees a status change.
type reply struct {
	status int
	body   string
}

// fakeAPI stands in for the public API: routes are "METHOD /path" without
// the /public/v1 prefix.
type fakeAPI struct {
	t      *testing.T
	mu     sync.Mutex
	routes map[string][]reply
	served map[string]int
	calls  []call
}

func newFakeAPI(t *testing.T) (*fakeAPI, *httptest.Server) {
	t.Helper()
	f := &fakeAPI{t: t, routes: map[string][]reply{}, served: map[string]int{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeAPI) on(route string, status int, body string) *fakeAPI {
	f.routes[route] = append(f.routes[route], reply{status, body})
	return f
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	path := strings.TrimPrefix(r.URL.Path, "/public/v1")
	f.mu.Lock()
	f.calls = append(f.calls, call{r.Method, path, r.URL.RawQuery, r.Header.Get("Idempotency-Key"), body})
	key := r.Method + " " + path
	replies, ok := f.routes[key]
	i := f.served[key]
	f.served[key]++
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer PAN_test" {
		f.t.Errorf("%s: Authorization = %q", key, r.Header.Get("Authorization"))
	}
	if r.Method != http.MethodGet && r.Header.Get("Idempotency-Key") == "" {
		f.t.Errorf("%s: a write without an Idempotency-Key", key)
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"RESOURCE_NOT_FOUND","detail":"no fake route for ` + key + `"}`))
		return
	}
	rep := replies[min(i, len(replies)-1)]
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(rep.status)
	_, _ = w.Write([]byte(rep.body))
}

// sent returns the requests made to a route.
func (f *fakeAPI) sent(method, path string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.Method == method && c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

// sentJSON decodes the one body sent to a route.
func (f *fakeAPI) sentJSON(method, path string) map[string]any {
	f.t.Helper()
	calls := f.sent(method, path)
	if len(calls) != 1 {
		f.t.Fatalf("%s %s: %d requests, want 1", method, path, len(calls))
	}
	var m map[string]any
	if err := json.Unmarshal(calls[0].Body, &m); err != nil {
		f.t.Fatalf("%s %s: body %q: %v", method, path, calls[0].Body, err)
	}
	return m
}

// result is what one run of the CLI printed.
type result struct {
	stdout, stderr string
	err            error
}

// run runs the CLI against srv with a key in the environment, no keychain,
// and fast polling. stdin, when not empty, is what password prompts read.
func run(t *testing.T, srv *httptest.Server, stdinText string, args ...string) result {
	t.Helper()
	t.Setenv("PANTECH_API_KEY", "PAN_test")
	t.Setenv("PANTECH_CONFIG_DIR", t.TempDir())
	t.Setenv("PANTECH_NO_KEYRING", "1")
	poll := api.PollInterval
	api.PollInterval = time.Millisecond
	t.Cleanup(func() { api.PollInterval = poll })
	oldStdin := stdin
	stdin = strings.NewReader(stdinText)
	t.Cleanup(func() { stdin = oldStdin })

	var out, errOut bytes.Buffer
	a := &app{
		apiURL: srv.URL,
		cfg:    &config.Config{Profiles: map[string]config.Profile{}},
		out:    &output.Printer{Out: &out, Err: &errOut},
	}
	root := newRoot(a)
	root.SetArgs(args)
	root.SetOut(&errOut)
	root.SetErr(&errOut)
	err := root.Execute()
	return result{out.String(), errOut.String(), err}
}
