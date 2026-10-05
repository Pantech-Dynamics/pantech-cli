// Package api talks to the Pantech Dynamics public API
// (https://docs.pantechdynamics.com/api): API-key auth, an Idempotency-Key
// on every write, retries the way the API's conventions ask, and errors as
// problem documents with a stable code.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the production API. Paths are relative to /public/v1.
const DefaultBaseURL = "https://api.pantechdynamics.com"

const prefix = "/public/v1"

// Client calls the public API with one API key.
type Client struct {
	BaseURL   string // e.g. https://api.pantechdynamics.com, no trailing slash
	Key       string // PAN_…
	UserAgent string
	HTTP      *http.Client
	// Retries is how many times a request is retried after a network error,
	// a 429, a 500 or a 503, with the same Idempotency-Key. Default 3.
	Retries int
}

// New returns a client with sensible timeouts.
func New(baseURL, key, userAgent string) *Client {
	return &Client{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		Key:       key,
		UserAgent: userAgent,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
		Retries:   3,
	}
}

// Problem is an error response: the API's problem document.
type Problem struct {
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	RequestID string `json:"request_id"`
	Errors    []struct {
		Field   string `json:"field"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (p *Problem) Error() string {
	msg := p.Detail
	if msg == "" {
		msg = p.Title
	}
	if msg == "" {
		msg = http.StatusText(p.Status)
	}
	var b strings.Builder
	b.WriteString(msg)
	for _, e := range p.Errors {
		text := e.Message
		if text == "" {
			text = e.Code
		}
		if e.Field == "" {
			fmt.Fprintf(&b, "\n  %s", text)
		} else {
			fmt.Fprintf(&b, "\n  %s: %s", e.Field, text)
		}
	}
	if p.Code != "" {
		fmt.Fprintf(&b, "\n(%s", p.Code)
		if p.RequestID != "" {
			fmt.Fprintf(&b, ", request %s", p.RequestID)
		}
		b.WriteString(")")
	}
	return b.String()
}

// IsCode reports whether err is a Problem with the given code.
func IsCode(err error, code string) bool {
	var p *Problem
	return errors.As(err, &p) && p.Code == code
}

// Request is one call. Path is relative to /public/v1, e.g. "/instances".
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Body   any // marshalled as JSON when not nil; a json.RawMessage is sent as is
}

// Response is what came back: the status and the raw body, for --json.
type Response struct {
	Status int
	Body   []byte
	// retryAfter is the Retry-After header, when the API sent one.
	retryAfter time.Duration
}

// Do sends req and decodes a 2xx JSON body into out (if out is not nil).
// Writes carry an Idempotency-Key, reused across retries so a change cannot
// happen twice.
func (c *Client) Do(ctx context.Context, req Request, out any) (*Response, error) {
	var payload []byte
	if req.Body != nil {
		var err error
		if raw, ok := req.Body.(json.RawMessage); ok {
			payload = raw
		} else if payload, err = json.Marshal(req.Body); err != nil {
			return nil, fmt.Errorf("encoding the request: %w", err)
		}
	}
	idempotencyKey := ""
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		idempotencyKey = newUUID()
	}

	target := c.BaseURL + prefix + req.Path
	if len(req.Query) > 0 {
		target += "?" + req.Query.Encode()
	}

	for attempt := 0; ; attempt++ {
		res, err := c.send(ctx, req.Method, target, payload, idempotencyKey)
		wait, retry := c.retryAfter(attempt, res, err)
		if retry {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		if res.Status >= 400 {
			return res, problemFrom(res)
		}
		if out != nil && len(res.Body) > 0 {
			if err := json.Unmarshal(res.Body, out); err != nil {
				return res, fmt.Errorf("reading the API's answer: %w", err)
			}
		}
		return res, nil
	}
}

func (c *Client) send(ctx context.Context, method, target string, payload []byte, idempotencyKey string) (*Response, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.Key)
	httpReq.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		httpReq.Header.Set("User-Agent", c.UserAgent)
	}
	if payload != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		httpReq.Header.Set("Idempotency-Key", idempotencyKey)
	}
	res, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, &NetworkError{Err: err}
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	if err != nil {
		return nil, &NetworkError{Err: err}
	}
	out := &Response{Status: res.StatusCode, Body: data}
	if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s >= 0 {
		out.retryAfter = time.Duration(s) * time.Second
	}
	return out, nil
}

// retryAfter decides whether to try again and how long to wait: network
// errors, 429 (after Retry-After) and 500/503 (backing off 1s, 2s, 4s).
func (c *Client) retryAfter(attempt int, res *Response, err error) (time.Duration, bool) {
	if attempt >= c.Retries || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return 0, false
	}
	backoff := time.Duration(1<<attempt) * time.Second
	switch {
	case err != nil:
		return backoff, true
	case res.Status == http.StatusTooManyRequests:
		if res.retryAfter > 0 {
			return res.retryAfter, true
		}
		return backoff, true
	case res.Status == http.StatusInternalServerError || res.Status == http.StatusServiceUnavailable:
		return backoff, true
	}
	return 0, false
}

// NetworkError is a request that never got an answer.
type NetworkError struct{ Err error }

func (e *NetworkError) Error() string {
	return "could not reach the API: " + e.Err.Error()
}
func (e *NetworkError) Unwrap() error { return e.Err }

func problemFrom(res *Response) error {
	p := &Problem{Status: res.Status}
	// A problem document is kept whatever part of it came: a code, a
	// detail, a title or field errors. Anything else is shown as sent.
	if err := json.Unmarshal(res.Body, p); err != nil || (p.Code == "" && p.Detail == "" && p.Title == "" && len(p.Errors) == 0) {
		*p = Problem{}
		p.Detail = strings.TrimSpace(string(res.Body))
		if len(p.Detail) > 300 || p.Detail == "" {
			p.Detail = http.StatusText(res.Status)
		}
	}
	p.Status = res.Status
	return p
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
