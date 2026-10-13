package review

import (
	"strings"
)

// Price is a model's price in USD per million tokens. Assumed marks a
// conservative placeholder used because the published price was not
// confirmed when the table was written (see DECISIONS "M3 shared contracts").
type Price struct {
	InPerMTok  float64
	OutPerMTok float64
	Assumed    bool
}

// Prices maps a model name (without "anthropic:") to its price.
//
// Rows are the published base prices (platform.claude.com pricing page,
// checked 2026-10-13): claude-haiku-5-5 $0.10/$0.50 (prompts up to 100,000
// tokens; review requests are capped far below that) and claude-opus-5-5
// $4/$20 per million input/output tokens. Prompt caching is not used.
var Prices = map[string]Price{
	"claude-haiku-5-5": {InPerMTok: 0.10, OutPerMTok: 0.50},
	"claude-opus-5-5":  {InPerMTok: 4, OutPerMTok: 20},
}

// UnknownPrice is used for any model not in Prices (the most expensive
// assumption, so budgets never under-count).
var UnknownPrice = Price{InPerMTok: 15, OutPerMTok: 75, Assumed: true}

// PriceFor returns the price of a model given as "provider:model" or
// "model".
func PriceFor(model string) Price {
	if _, name, ok := strings.Cut(model, ":"); ok {
		model = name
	}
	if p, ok := Prices[model]; ok {
		return p
	}
	return UnknownPrice
}

// Cost is the USD cost of a call with the given token usage.
func Cost(model string, inTokens, outTokens int) float64 {
	p := PriceFor(model)
	return (float64(inTokens)*p.InPerMTok + float64(outTokens)*p.OutPerMTok) / 1e6
}

// Token estimate parameters. Claude tokenizers average roughly 3–4 bytes
// per token on English and source code, and newer tokenizers produce more
// tokens for the same text; counting one token per estimateBytesPerToken
// bytes keeps the estimate above the real count for prose, code, diffs with
// line numbers and multibyte text (a CJK rune is 3 bytes, at most one
// token). requestOverheadTokens covers the message framing (role markers,
// system block) the API adds to every request.
const (
	estimateBytesPerToken = 2
	requestOverheadTokens = 64
)

// EstimateInputTokens is a conservative upper bound on the input tokens of
// a request: the system prompt plus the user message exactly as the
// Anthropic adapter sends it (UserContent), at one token per two bytes,
// plus a fixed per-request overhead.
func EstimateInputTokens(req Request) int {
	n := len(req.System) + len(UserContent(req))
	return (n+estimateBytesPerToken-1)/estimateBytesPerToken + requestOverheadTokens
}

// EstimateCost is the pre-call upper bound for a request on a model: the
// estimated input tokens plus MaxTokens (or DefaultMaxTokens) of output.
// Budget guards compare it with the remaining cap before calling; it is
// never below the actual cost of a response that stays within MaxTokens.
func EstimateCost(model string, req Request) float64 {
	return Cost(model, EstimateInputTokens(req), maxTokens(req))
}

// ExpectedOutputTokens is the output length a planned review is budgeted
// at, reasoning included. It tracks the typical response, not the longest:
// the M5.2 recordings of the fixture items use 26–1414 output tokens per
// deep review (578 on average over #101's fifteen) and 156–868 per fast
// review, against max_tokens 4096. Together with the conservative input
// estimate the plan stays above the item's actual spend (#101: $0.356
// planned, $0.327 spent), while one long response can cost more than its
// own reservation.
const ExpectedOutputTokens = 512

// PlanCost is the cost the item's cap is charged for a call before it
// runs: the conservative input estimate plus ExpectedOutputTokens of output
// (MaxTokens when lower). Unlike EstimateCost it is not a hard upper bound:
// a response that runs to max_tokens costs more, so an item can overshoot
// its cap by that margin (at most (4096 − 512) × $20/MTok ≈ $0.07 per deep
// call). Charging every call its max_tokens let only two of fifteen deep
// groups of a 400-line PR fit the default $0.40 cap. A truncation retry
// reserves the grown request's EstimateCost instead (pipeline).
func PlanCost(model string, req Request) float64 {
	return Cost(model, EstimateInputTokens(req), min(maxTokens(req), ExpectedOutputTokens))
}

// DefaultMaxTokens is the output cap used when a Request leaves MaxTokens 0.
const DefaultMaxTokens = 1024
