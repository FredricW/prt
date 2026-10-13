// Package review holds the LLM boundary: the Reviewer interface, provider
// adapters (Anthropic) and the fast/deep tier set. Routing decides tiers, not
// vendors; a model spec is "provider:model" (e.g. anthropic:claude-haiku-5-5).
//
// The LLM writes prose only: findings, verdicts, the TL;DR and short group
// titles. It is never asked for a number prt can compute.
package review

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fredricw/prt/internal/model"
)

// ErrUnavailable means the reviewer cannot run (no key, network, 5xx).
var ErrUnavailable = errors.New("review: unavailable")

// ErrNoKey means the provider key is not set. Errors wrapping it also wrap
// ErrUnavailable. The pipeline then skips reviews with a status note
// ("reviews skipped: PRT_ANTHROPIC_API_KEY not set"); nothing blocks.
var ErrNoKey = errors.New("review: no API key")

// Checker is optionally implemented by a Reviewer that can tell, without a
// call, that it cannot run (Anthropic without a key returns an error
// wrapping ErrNoKey and ErrUnavailable). The pipeline checks it once per run.
type Checker interface {
	Ready() error
}

// Ready returns r.Ready() when r implements Checker, ErrUnavailable for a
// nil reviewer, and nil otherwise.
func Ready(r Reviewer) error {
	if r == nil {
		return fmt.Errorf("%w: no reviewer configured", ErrUnavailable)
	}
	if c, ok := r.(Checker); ok {
		return c.Ready()
	}
	return nil
}

// Default per-tier timeouts (spec: failure handling).
const (
	FastTimeout = 30 * time.Second
	DeepTimeout = 120 * time.Second
)

// Task says what kind of prose is requested.
type Task string

// Tasks.
const (
	TaskReview     Task = "review"      // findings + verdict + confidence for a group
	TaskTLDR       Task = "tldr"        // one sentence ≤160 chars from the aggregate record
	TaskGroupTitle Task = "group-title" // three-word title for a group
	TaskPRTitle    Task = "pr-title"    // suggested PR title for a branch
)

// Request is one LLM call.
type Request struct {
	Task      Task       `json:"task"`
	Tier      model.Tier `json:"tier"`
	GroupID   string     `json:"groupId,omitempty"`
	System    string     `json:"system"`
	Prompt    string     `json:"prompt"`
	Context   []string   `json:"context,omitempty"` // attached context blocks, already rendered
	MaxTokens int        `json:"maxTokens,omitempty"`
	// Units are the group's units for TaskReview (set by BuildReview).
	// They are not sent and not part of RequestKey: the prompt already
	// carries their content. Reviewers use them to anchor findings
	// (ParseReview), so a replayed response anchors to the current units.
	Units []model.ChangeUnit `json:"-"`
}

// Result is a parsed LLM response. Text is always the raw response text
// (the call log shows it). For TaskReview, Findings/Verdict/Confidence are
// filled from the model's JSON (ParseReview); for prose tasks Text is the
// cleaned prose (CleanTLDR / CleanTitle are applied by the caller).
type Result struct {
	Text         string          `json:"text,omitempty"`
	Findings     []model.Finding `json:"findings,omitempty"`
	Verdict      string          `json:"verdict,omitempty"`
	Confidence   float64         `json:"confidence"`
	Model        string          `json:"model"`
	InputTokens  int             `json:"inputTokens"`
	OutputTokens int             `json:"outputTokens"`
	Cost         float64         `json:"cost"`
	Ms           int64           `json:"ms"`
	// StopReason is the provider's stop reason ("end_turn", "max_tokens").
	StopReason string `json:"stopReason,omitempty"`
}

// Reviewer runs one LLM request.
type Reviewer interface {
	Review(ctx context.Context, req Request) (Result, error)
	// Model returns the "provider:model" spec this reviewer calls.
	Model() string
}

// Tiers is the fast/deep pair routing chooses between.
type Tiers struct {
	Fast Reviewer
	Deep Reviewer
}

// For returns the reviewer for a tier (nil for TierNone).
func (t Tiers) For(tier model.Tier) Reviewer {
	switch tier {
	case model.TierFast:
		return t.Fast
	case model.TierDeep:
		return t.Deep
	}
	return nil
}

// FromSpec builds a Reviewer from "provider:model". apiKey is the provider key
// read from the environment by the caller (PRT_ANTHROPIC_API_KEY for anthropic).
func FromSpec(spec, apiKey string) (Reviewer, error) {
	provider, name, ok := strings.Cut(spec, ":")
	if !ok {
		return nil, fmt.Errorf("review: bad model spec %q (want provider:model)", spec)
	}
	switch provider {
	case "anthropic":
		return NewAnthropic(name, apiKey), nil
	}
	return nil, fmt.Errorf("review: unknown provider %q", provider)
}
