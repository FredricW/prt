package review

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

// AnthropicKeyEnv is the env var holding the Anthropic key. It is deliberately
// not ANTHROPIC_API_KEY, which would switch Claude Code itself to API billing.
const AnthropicKeyEnv = "PRT_ANTHROPIC_API_KEY"

// AnthropicBaseURL is the Messages API base.
const AnthropicBaseURL = "https://api.anthropic.com"

// Anthropic calls the Anthropic Messages API (POST /v1/messages) through a
// thin HTTP client.
type Anthropic struct {
	Name    string
	BaseURL string
	apiKey  string
	HC      *http.Client
}

var _ Reviewer = (*Anthropic)(nil)

// NewAnthropic builds the adapter; an empty key makes every call ErrUnavailable.
func NewAnthropic(model, apiKey string) *Anthropic {
	return &Anthropic{Name: model, BaseURL: AnthropicBaseURL, apiKey: apiKey, HC: &http.Client{Timeout: DeepTimeout + 10*time.Second}}
}

// AnthropicKeyFromEnv reads PRT_ANTHROPIC_API_KEY.
func AnthropicKeyFromEnv() string { return os.Getenv(AnthropicKeyEnv) }

// Model implements Reviewer.
func (a *Anthropic) Model() string { return "anthropic:" + a.Name }

// Review implements Reviewer.
//
// TODO(M3): build the Messages request (system + user prompt + context
// blocks), parse the JSON findings/verdict/confidence for TaskReview, plain
// text otherwise, and compute cost from usage and a per-model price table.
func (a *Anthropic) Review(context.Context, Request) (Result, error) {
	if a.apiKey == "" {
		return Result{}, fmt.Errorf("%w: %s not set", ErrUnavailable, AnthropicKeyEnv)
	}
	return Result{}, fmt.Errorf("%w: anthropic adapter not implemented yet", ErrUnavailable)
}
