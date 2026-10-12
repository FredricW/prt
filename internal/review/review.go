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
}

// Result is a parsed LLM response. For TaskReview, Findings/Verdict/Confidence
// are filled from the model's JSON; for prose tasks only Text is set.
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
