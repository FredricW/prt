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
// The published prices of claude-haiku-5-5 and claude-opus-5-5 could not be
// confirmed offline, so both rows are Assumed and deliberately high: $1/$5
// (fast) and $15/$75 (deep) per million input/output tokens. Budgets and
// caps compare against these, so spend is over- rather than under-counted.
// Replace a row (and drop Assumed) once the price is confirmed.
var Prices = map[string]Price{
	"claude-haiku-5-5": {InPerMTok: 1, OutPerMTok: 5, Assumed: true},
	"claude-opus-5-5":  {InPerMTok: 15, OutPerMTok: 75, Assumed: true},
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

// DefaultMaxTokens is the output cap used when a Request leaves MaxTokens 0.
const DefaultMaxTokens = 1024
