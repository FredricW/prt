package review

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/model"
)

// AnthropicKeyEnv is the env var holding the Anthropic key. It is deliberately
// not ANTHROPIC_API_KEY, which would switch Claude Code itself to API billing.
const AnthropicKeyEnv = "PRT_ANTHROPIC_API_KEY"

// AnthropicBaseURL is the Messages API base.
const AnthropicBaseURL = "https://api.anthropic.com"

// AnthropicVersion is the anthropic-version header sent with every request.
const AnthropicVersion = "2023-06-01"

// maxResponseBytes bounds how much of a response body is read.
const maxResponseBytes = 4 << 20

// Anthropic calls the Anthropic Messages API (POST /v1/messages) through a
// thin HTTP client: one non-streaming call per Review, no retries (the
// pipeline retries once), temperature 0.
type Anthropic struct {
	Name    string
	BaseURL string
	apiKey  string
	HC      *http.Client
	// TimeoutFast and TimeoutDeep override the per-tier timeouts
	// (FastTimeout, DeepTimeout) when non-zero; tests set them tiny.
	TimeoutFast time.Duration
	TimeoutDeep time.Duration
}

var _ Reviewer = (*Anthropic)(nil)

// NewAnthropic builds the adapter; an empty key makes every call fail with
// an error wrapping ErrNoKey and ErrUnavailable before anything is sent.
func NewAnthropic(model, apiKey string) *Anthropic {
	return &Anthropic{Name: model, BaseURL: AnthropicBaseURL, apiKey: apiKey, HC: &http.Client{Timeout: DeepTimeout + 10*time.Second}}
}

// AnthropicKeyFromEnv reads PRT_ANTHROPIC_API_KEY.
func AnthropicKeyFromEnv() string { return os.Getenv(AnthropicKeyEnv) }

// Model implements Reviewer.
func (a *Anthropic) Model() string { return "anthropic:" + a.Name }

// Ready implements Checker: an error wrapping ErrNoKey and ErrUnavailable
// when PRT_ANTHROPIC_API_KEY is not set.
func (a *Anthropic) Ready() error {
	if a.apiKey == "" {
		return fmt.Errorf("%w: %w: %s not set", ErrUnavailable, ErrNoKey, AnthropicKeyEnv)
	}
	return nil
}

// Timeout returns the call timeout for a tier: TimeoutFast/FastTimeout for
// the fast tier, TimeoutDeep/DeepTimeout otherwise.
func (a *Anthropic) Timeout(t model.Tier) time.Duration {
	if t == model.TierFast {
		if a.TimeoutFast > 0 {
			return a.TimeoutFast
		}
		return FastTimeout
	}
	if a.TimeoutDeep > 0 {
		return a.TimeoutDeep
	}
	return DeepTimeout
}

// messagesRequest is the Messages API request body.
type messagesRequest struct {
	Model       string    `json:"model"`
	MaxTokens   int       `json:"max_tokens"`
	Temperature float64   `json:"temperature"`
	System      string    `json:"system,omitempty"`
	Messages    []message `json:"messages"`
}

// message is one conversation turn.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// messagesResponse is the part of a Messages API response prt reads.
type messagesResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// apiError is the Messages API error body.
type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// UserContent is the user message sent for req: the prompt followed by the
// rendered context blocks. EstimateInputTokens counts the same text.
func UserContent(req Request) string {
	if len(req.Context) == 0 {
		return req.Prompt
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(req.Prompt, "\n"))
	b.WriteString("\n\n# Context\n")
	for _, c := range req.Context {
		b.WriteString("\n")
		b.WriteString(c)
		b.WriteString("\n")
	}
	return b.String()
}

// maxTokens is the output cap of req (DefaultMaxTokens when unset).
func maxTokens(req Request) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	return DefaultMaxTokens
}

// Review implements Reviewer. For TaskReview it also parses findings,
// verdict and confidence (anchored to req.Units); an unparseable review
// returns the Result (tokens and cost kept) and an error wrapping
// ErrBadResponse.
func (a *Anthropic) Review(ctx context.Context, req Request) (Result, error) {
	if err := a.Ready(); err != nil {
		return Result{}, err
	}
	body, err := json.Marshal(messagesRequest{
		Model: a.Name, MaxTokens: maxTokens(req), System: req.System,
		Messages: []message{{Role: "user", Content: UserContent(req)}},
	})
	if err != nil {
		return Result{}, fmt.Errorf("review: anthropic encode: %w", err)
	}
	start := time.Now()
	raw, err := a.post(ctx, req.Tier, body)
	if err != nil {
		return Result{}, err
	}
	res, err := a.result(raw)
	res.Ms = time.Since(start).Milliseconds()
	if err != nil {
		return res, err
	}
	if req.Task != TaskReview {
		return res, nil
	}
	res.Findings, res.Verdict, res.Confidence, err = ParseReview(res.Text, req.Units)
	if err != nil {
		return res, fmt.Errorf("review: %s (stop_reason %s): %w", a.Model(), res.StopReason, err)
	}
	return res, nil
}

// post sends body under the tier's timeout and returns the 2xx response
// body, or a classified error.
func (a *Anthropic) post(ctx context.Context, tier model.Tier, body []byte) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, a.Timeout(tier))
	defer cancel()
	hreq, err := http.NewRequestWithContext(cctx, http.MethodPost, strings.TrimSuffix(a.BaseURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("review: anthropic request: %w", err)
	}
	hreq.Header.Set("x-api-key", a.apiKey)
	hreq.Header.Set("anthropic-version", AnthropicVersion)
	hreq.Header.Set("content-type", "application/json")
	resp, err := a.client().Do(hreq)
	if err != nil {
		return nil, a.transportErr(ctx, cctx, tier, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, a.transportErr(ctx, cctx, tier, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, a.statusErr(resp.StatusCode, raw)
	}
	return raw, nil
}

// client returns the HTTP client (http.DefaultClient when HC is nil).
func (a *Anthropic) client() *http.Client {
	if a.HC != nil {
		return a.HC
	}
	return http.DefaultClient
}

// transportErr classifies a failed round trip: the caller's cancellation
// is returned as ctx.Err(); a tier timeout and network errors wrap
// ErrUnavailable.
func (a *Anthropic) transportErr(parent, cctx context.Context, tier model.Tier, err error) error {
	if perr := parent.Err(); perr != nil {
		return perr
	}
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s: no response within %s (%s tier)", ErrUnavailable, a.Model(), a.Timeout(tier), tier)
	}
	return fmt.Errorf("%w: %s: %s", ErrUnavailable, a.Model(), a.redact(err.Error()))
}

// statusErr maps a non-2xx status: auth failures, rate limits, overload
// and server errors wrap ErrUnavailable; other 4xx are plain errors
// carrying the status and the API error type.
func (a *Anthropic) statusErr(status int, body []byte) error {
	var ae apiError
	_ = json.Unmarshal(body, &ae)
	typ := ae.Error.Type
	if typ == "" {
		typ = "unknown_error"
	}
	msg := a.redact(truncate(ae.Error.Message, 200))
	desc := fmt.Sprintf("%s: HTTP %d %s", a.Model(), status, typ)
	if msg != "" {
		desc += ": " + msg
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("%w: %s (check %s)", ErrUnavailable, desc, AnthropicKeyEnv)
	case status == http.StatusTooManyRequests || status == 529 || status >= 500:
		return fmt.Errorf("%w: %s", ErrUnavailable, desc)
	}
	return fmt.Errorf("review: %s", desc)
}

// result decodes a 2xx Messages response into a Result (text, tokens,
// cost, stop reason). A body that is not a Messages response wraps
// ErrBadResponse.
func (a *Anthropic) result(raw []byte) (Result, error) {
	var mr messagesResponse
	if err := json.Unmarshal(raw, &mr); err != nil {
		return Result{Model: a.Model()}, fmt.Errorf("review: %s: decode response: %w: %w", a.Model(), ErrBadResponse, err)
	}
	var text strings.Builder
	for _, c := range mr.Content {
		if c.Type == "text" || c.Type == "" {
			text.WriteString(c.Text)
		}
	}
	in := mr.Usage.InputTokens + mr.Usage.CacheCreationInputTokens + mr.Usage.CacheReadInputTokens
	out := mr.Usage.OutputTokens
	return Result{
		Text: text.String(), Model: a.Model(), InputTokens: in, OutputTokens: out,
		Cost: Cost(a.Model(), in, out), StopReason: mr.StopReason,
	}, nil
}

// redact removes the API key from s (defence in depth: no error string
// may carry it).
func (a *Anthropic) redact(s string) string {
	if a.apiKey == "" {
		return s
	}
	return strings.ReplaceAll(s, a.apiKey, "[redacted]")
}

// truncate cuts s to at most n bytes on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
