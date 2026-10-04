package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// server answers with the given statuses in turn and records each request's Idempotency-Key.
func server(t *testing.T, statuses []int, body string) (*Client, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		i := len(keys)
		keys = append(keys, r.Header.Get("Idempotency-Key"))
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer PAN_test" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		status := statuses[min(i, len(statuses)-1)]
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "0")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := New(srv.URL, "PAN_test", "test")
	return c, &keys
}

func TestRetriesReuseTheIdempotencyKey(t *testing.T) {
	c, keys := server(t, []int{503, 429, 202}, `{"operation_id":"op_1","resource_id":"vm_1","status":"submitting"}`)
	var got Accepted
	if _, err := c.Do(context.Background(), Request{Method: http.MethodPost, Path: "/instances/vm_1/start"}, &got); err != nil {
		t.Fatal(err)
	}
	if len(*keys) != 3 {
		t.Fatalf("%d attempts, want 3", len(*keys))
	}
	if (*keys)[0] == "" || (*keys)[0] != (*keys)[1] || (*keys)[1] != (*keys)[2] {
		t.Fatalf("keys %q: a retry must resend the same Idempotency-Key", *keys)
	}
	if got.OperationID != "op_1" {
		t.Fatalf("operation %q", got.OperationID)
	}
}

func TestReadsCarryNoIdempotencyKey(t *testing.T) {
	c, keys := server(t, []int{200}, `{}`)
	if _, err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/me"}, nil); err != nil {
		t.Fatal(err)
	}
	if (*keys)[0] != "" {
		t.Fatalf("GET sent Idempotency-Key %q", (*keys)[0])
	}
}

func TestClientErrorsAreNotRetried(t *testing.T) {
	body := `{"status":422,"code":"VALIDATION_FAILED","detail":"Check the fields.","request_id":"req_1","errors":[{"field":"name","code":"REQUIRED","message":"Give it a name."}]}`
	c, keys := server(t, []int{422}, body)
	_, err := c.Do(context.Background(), Request{Method: http.MethodPost, Path: "/instances", Body: map[string]string{}}, nil)
	var p *Problem
	if !errors.As(err, &p) {
		t.Fatalf("err = %v, want a Problem", err)
	}
	if len(*keys) != 1 {
		t.Fatalf("%d attempts: a 422 is not retried", len(*keys))
	}
	if p.Code != "VALIDATION_FAILED" || len(p.Errors) != 1 || !IsCode(err, "VALIDATION_FAILED") {
		t.Fatalf("problem = %+v", p)
	}
	want := "Check the fields.\n  name: Give it a name.\n(VALIDATION_FAILED, request req_1)"
	if p.Error() != want {
		t.Fatalf("Error() = %q, want %q", p.Error(), want)
	}
}

func TestRetriesGiveUp(t *testing.T) {
	c, keys := server(t, []int{500}, `{"status":500,"code":"INTERNAL","detail":"Oops."}`)
	c.Retries = 1
	start := time.Now()
	_, err := c.Do(context.Background(), Request{Method: http.MethodDelete, Path: "/ssh-keys/x"}, nil)
	if !IsCode(err, "INTERNAL") || len(*keys) != 2 {
		t.Fatalf("err %v after %d attempts, want INTERNAL after 2", err, len(*keys))
	}
	if time.Since(start) < time.Second {
		t.Fatal("no backoff before the retry")
	}
}

func TestNonJSONErrorBody(t *testing.T) {
	c, _ := server(t, []int{502}, `<html>Bad gateway</html>`)
	c.Retries = 0
	_, err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/me"}, nil)
	var p *Problem
	if !errors.As(err, &p) || p.Status != 502 {
		t.Fatalf("err = %v", err)
	}
}

func TestListAllFollowsCursors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next := map[string]any{"data": []map[string]string{{"id": "a"}}, "next_cursor": "c2"}
		if r.URL.Query().Get("cursor") == "c2" {
			next = map[string]any{"data": []map[string]string{{"id": "b"}}, "next_cursor": nil}
		}
		_ = json.NewEncoder(w).Encode(next)
	}))
	defer srv.Close()
	got, err := ListAll[SSHKey](context.Background(), New(srv.URL, "PAN_test", "test"), "/ssh-keys", nil)
	if err != nil || len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("got %+v, %v", got, err)
	}
}
