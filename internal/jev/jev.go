// Package jev is a thin client for Jev (typesafe-ai/jev) reached through the
// Vercel AI Gateway decision endpoint:
//
//	POST ${JEV_BASE_URL:-https://ai-gateway.vercel.sh}/v1/evaluate
//	Authorization: Bearer $JEV_API_KEY
//
// The spec's question types map as: Noul (N) -> "boolean", Choice (C) ->
// "choice" with a criteria record, Score (S) -> "score" with a criteria array
// ordered lowest to highest. See docs/vendor/ai-gateway-decision.md.
//
// Everything outside this package talks to the Client interface; tests use
// Fake or Replay (recordings under testdata/recordings/jev).
package jev

import (
	"context"
	"errors"
	"strconv"
)

// Model is the decision model id on AI Gateway.
const Model = "typesafe-ai/jev"

// DefaultBaseURL is used when JEV_BASE_URL is unset.
const DefaultBaseURL = "https://ai-gateway.vercel.sh"

// Request size limits (tokens). Callers batch questions to stay under them.
const (
	MaxRequestTokens     = 64000
	MaxStatePlusQuestion = 32000
)

// ErrUnavailable means Jev cannot be reached (no key, network or 5xx). The
// pipeline then labels units "unknown" with confidence 0 (spec: failure handling).
var ErrUnavailable = errors.New("jev: unavailable")

// QuestionType is the wire question type.
type QuestionType string

// Question types.
const (
	Boolean QuestionType = "boolean"
	Choice  QuestionType = "choice"
	Score   QuestionType = "score"
)

// Question is one typed question. Criteria is:
//   - boolean: optional map with "true"/"false" descriptions
//   - choice: map of option name -> description
//   - score: []string ordered lowest to highest (at least two)
type Question struct {
	Type         QuestionType `json:"type"`
	Instructions string       `json:"instructions"`
	Criteria     any          `json:"criteria,omitempty"`
}

// Request is the /v1/evaluate body. State may be a string, object or array.
type Request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Answer is one answer, keyed by question name in Response.Answers.
type Answer struct {
	Type          QuestionType       `json:"type"`
	Probability   *float64           `json:"probability,omitempty"`   // boolean
	Choice        string             `json:"choice,omitempty"`        // choice
	Score         *float64           `json:"score,omitempty"`         // score (interpolated rung index)
	Probabilities map[string]float64 `json:"probabilities,omitempty"` // choice: by option; score: by rung index
}

// Confidence is the probability of the chosen answer: max(p, 1-p) for
// booleans, the chosen option's probability for choices, the top rung's
// probability for scores.
func (a Answer) Confidence() float64 {
	switch a.Type {
	case Boolean:
		if a.Probability == nil {
			return 0
		}
		p := *a.Probability
		return max(p, 1-p)
	case Choice:
		return a.Probabilities[a.Choice]
	case Score:
		best := 0.0
		for _, p := range a.Probabilities {
			best = max(best, p)
		}
		return best
	}
	return 0
}

// Usage is token usage of a request.
type Usage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// GatewayMeta is the AI Gateway metadata block; cost fields are decimal strings.
type GatewayMeta struct {
	Cost         string `json:"cost,omitempty"`
	GenerationID string `json:"generationId,omitempty"`
}

// ProviderMetadata wraps GatewayMeta.
type ProviderMetadata struct {
	Gateway GatewayMeta `json:"gateway"`
}

// Response is the /v1/evaluate response.
type Response struct {
	Model            string            `json:"model"`
	Answers          map[string]Answer `json:"answers"`
	Usage            Usage             `json:"usage"`
	ProviderMetadata ProviderMetadata  `json:"providerMetadata"`
}

// CostUSD parses the gateway cost; 0 when absent or malformed.
func (r Response) CostUSD() float64 {
	f, err := strconv.ParseFloat(r.ProviderMetadata.Gateway.Cost, 64)
	if err != nil {
		return 0
	}
	return f
}

// Client evaluates a batch of questions against one state.
type Client interface {
	Evaluate(ctx context.Context, req Request) (Response, error)
}

// EstimateTokens is a cheap upper bound used for batching: 2.5 bytes per
// token, rounded up. Every recorded request (testdata/recordings/jev) used
// between 2.54 and 3.93 bytes of compact request JSON per input token, so
// this never under-counts them (TestEstimateTokensCoversRecordings); the
// earlier 4 bytes per token under-counted every one.
func EstimateTokens(s string) int { return EstimateTokensLen(len(s)) }

// EstimateTokensLen is EstimateTokens for a text of n bytes.
func EstimateTokensLen(n int) int { return (2*n + 4) / 5 }

// RequestOverheadTokens is the fixed input the gateway counts per request
// beyond the request body. The M2 recordings (small pair/name requests of
// about 1 KB) were counted up to ~330 tokens above 2.5 bytes per token of
// their body; large M1 unit requests hid this under the per-byte margin.
const RequestOverheadTokens = 400

// EstimateRequestTokensLen bounds the input tokens of a whole request whose
// compact JSON body is n bytes (TestEstimateTokensCoversRecordings).
func EstimateRequestTokensLen(n int) int { return EstimateTokensLen(n) + RequestOverheadTokens }
