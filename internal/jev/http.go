package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// HTTP is the real Client.
type HTTP struct {
	BaseURL string
	apiKey  string
	HC      *http.Client
	// RetryWait is the pause before the single retry of a 429 or 5xx
	// response (a Retry-After header of up to MaxRetryWait wins). Zero
	// means DefaultRetryWait; negative retries at once (tests).
	RetryWait time.Duration
}

// DefaultRetryWait and MaxRetryWait bound the pause before retrying a 429
// or 5xx once (DECISIONS "M1 review fixes 2").
const (
	DefaultRetryWait = time.Second
	MaxRetryWait     = 10 * time.Second
)

var _ Client = (*HTTP)(nil)

// NewHTTP builds a client. An empty key yields a client whose calls fail with
// ErrUnavailable, so callers can degrade without special-casing.
func NewHTTP(baseURL, apiKey string) *HTTP {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &HTTP{BaseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, HC: &http.Client{Timeout: 30 * time.Second}}
}

// FromEnv builds a client from JEV_BASE_URL and JEV_API_KEY.
func FromEnv() *HTTP { return NewHTTP(os.Getenv("JEV_BASE_URL"), os.Getenv("JEV_API_KEY")) }

// HasKey reports whether an API key is configured.
func (c *HTTP) HasKey() bool { return c.apiKey != "" }

// Evaluate implements Client.
func (c *HTTP) Evaluate(ctx context.Context, req Request) (Response, error) {
	if c.apiKey == "" {
		return Response{}, fmt.Errorf("%w: JEV_API_KEY not set", ErrUnavailable)
	}
	if req.Model == "" {
		req.Model = Model
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("jev: encode: %w", err)
	}
	status, raw, hdr, err := c.post(ctx, body)
	if err == nil && retryable(status) {
		// One retry for a rate limit or a transient server error, so a
		// single hiccup among many calls does not leave labels unknown.
		t := time.NewTimer(c.retryWait(hdr))
		select {
		case <-ctx.Done():
			t.Stop()
			return Response{}, fmt.Errorf("jev: %w", ctx.Err())
		case <-t.C:
		}
		status, raw, _, err = c.post(ctx, body)
	}
	if err != nil {
		return Response{}, err
	}
	if retryable(status) || status == http.StatusUnauthorized {
		return Response{}, fmt.Errorf("%w: HTTP %d: %s", ErrUnavailable, status, excerpt(raw))
	}
	if status/100 != 2 {
		return Response{}, fmt.Errorf("jev: HTTP %d: %s", status, excerpt(raw))
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, fmt.Errorf("jev: decode: %w", err)
	}
	return out, nil
}

// retryable reports whether an HTTP status is worth one retry.
func retryable(status int) bool {
	return status >= 500 || status == http.StatusTooManyRequests
}

// retryWait is the pause before a retry: Retry-After in whole seconds when
// given and at most MaxRetryWait, else RetryWait (default DefaultRetryWait).
func (c *HTTP) retryWait(h http.Header) time.Duration {
	if c.RetryWait < 0 {
		return 0
	}
	if s, err := strconv.Atoi(strings.TrimSpace(h.Get("Retry-After"))); err == nil && s >= 0 {
		if d := time.Duration(s) * time.Second; d <= MaxRetryWait {
			return d
		}
	}
	if c.RetryWait > 0 {
		return c.RetryWait
	}
	return DefaultRetryWait
}

// post sends one request and returns its status, body and headers. A
// transport failure is ErrUnavailable; cancellation is returned as is.
func (c *HTTP) post(ctx context.Context, body []byte) (int, []byte, http.Header, error) {
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/evaluate", bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("jev: build request: %w", err)
	}
	hr.Header.Set("Authorization", "Bearer "+c.apiKey)
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.HC.Do(hr)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Cancellation is the caller's decision, not an outage.
			return 0, nil, nil, fmt.Errorf("jev: %w", ctxErr)
		}
		return 0, nil, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, nil, nil, fmt.Errorf("%w: read: %w", ErrUnavailable, err)
	}
	return resp.StatusCode, raw, resp.Header, nil
}

func excerpt(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
