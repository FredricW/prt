package review

import (
	"errors"
	"fmt"
	"strings"
)

// StopMaxTokens is the provider stop reason of a response cut off at its
// max_tokens.
const StopMaxTokens = "max_tokens"

// MaxTokensCeiling bounds the output cap a truncation retry grows a request
// to (Grow). The configured models accept far more; the ceiling keeps one
// retry's worst-case cost small.
const MaxTokensCeiling = 8192

// ErrTruncated marks a response cut off at max_tokens before it was usable:
// a review whose JSON is incomplete, or prose without any text. The models
// spend output tokens on reasoning first, so a tight cap can end the
// response before the answer. It wraps ErrBadResponse.
var ErrTruncated = fmt.Errorf("%w: cut off at max_tokens", ErrBadResponse)

// IsTruncated reports whether a failed call was cut off at max_tokens: err
// wraps ErrTruncated, or the result stopped at max_tokens and err wraps
// ErrBadResponse (a caller's own parse of the text failed). The pipeline
// retries such a call once with a larger cap (Grow).
func IsTruncated(res Result, err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrTruncated) || (res.StopReason == StopMaxTokens && errors.Is(err, ErrBadResponse))
}

// truncatedProse reports whether a prose response (TL;DR, titles) is
// unusable because it was cut off: it stopped at max_tokens with no text.
func truncatedProse(res Result) bool {
	return res.StopReason == StopMaxTokens && strings.TrimSpace(res.Text) == ""
}

// Grow returns req with its output cap doubled (from its MaxTokens, or
// DefaultMaxTokens when unset), at most MaxTokensCeiling. ok is false when
// the cap is already at the ceiling. The grown request is a different
// request: its recording key differs from req's.
func Grow(req Request) (Request, bool) {
	cur := maxTokens(req)
	if cur >= MaxTokensCeiling {
		return req, false
	}
	req.MaxTokens = min(2*cur, MaxTokensCeiling)
	return req, true
}

// truncatedText is the partial review JSON FakeTruncate answers with.
const truncatedText = `{"findings": [{"file": "`

// FakeTruncate wraps a Fake.Respond for tests: a request whose MaxTokens
// (DefaultMaxTokens when unset) is below atLeast comes back cut off at
// max_tokens — a partial review JSON or empty prose, all of MaxTokens
// billed as output, and an error wrapping ErrTruncated — the way the
// Anthropic adapter reports it; every other request goes to next.
func FakeTruncate(atLeast int, next func(Request) (Result, error)) func(Request) (Result, error) {
	return func(req Request) (Result, error) {
		if maxTokens(req) >= atLeast {
			return next(req)
		}
		res := Result{InputTokens: EstimateInputTokens(req), OutputTokens: maxTokens(req), StopReason: StopMaxTokens}
		if req.Task == TaskReview {
			res.Text = truncatedText
		}
		return res, fmt.Errorf("review: fake (stop_reason %s): %w", StopMaxTokens, ErrTruncated)
	}
}
