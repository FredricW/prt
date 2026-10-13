package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// HTTP is the real Client.
type HTTP struct {
	BaseURL string
	apiKey  string
	HC      *http.Client
}

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
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/evaluate", bytes.NewReader(body))
	if err != nil {
		return Response{}, fmt.Errorf("jev: build request: %w", err)
	}
	hr.Header.Set("Authorization", "Bearer "+c.apiKey)
	hr.Header.Set("Content-Type", "application/json")
	resp, err := c.HC.Do(hr)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// Cancellation is the caller's decision, not an outage.
			return Response{}, fmt.Errorf("jev: %w", ctxErr)
		}
		return Response{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return Response{}, fmt.Errorf("%w: read: %w", ErrUnavailable, err)
	}
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusUnauthorized {
		return Response{}, fmt.Errorf("%w: HTTP %d: %s", ErrUnavailable, resp.StatusCode, excerpt(raw))
	}
	if resp.StatusCode/100 != 2 {
		return Response{}, fmt.Errorf("jev: HTTP %d: %s", resp.StatusCode, excerpt(raw))
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, fmt.Errorf("jev: decode: %w", err)
	}
	return out, nil
}

func excerpt(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
