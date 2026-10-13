package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
)

// errRetryOverCap marks a failed call whose retry did not fit the item's
// cost cap.
var errRetryOverCap = errors.New("retry over cost cap")

// callMeta is the call-log metadata of one LLM call.
type callMeta struct {
	itemID, groupID string
	tier            model.Tier
	purpose         string
}

// callOutcome is the result of callRetry: the last result, every call made
// (for the call log), the number of attempts and the final error.
type callOutcome struct {
	res      review.Result
	calls    []model.ReviewCall
	attempts int
	err      error
}

// cost sums the cost of every call made.
func (o callOutcome) cost() float64 {
	total := 0.0
	for _, c := range o.calls {
		total += c.Cost
	}
	return total
}

// callOnce sends req to rev under the tier's timeout (spec failure
// handling: 30 s fast, 120 s deep) and returns the
// result (Cost and Ms filled in when the reviewer left them 0) and its
// call-log record.
func (p *Pipeline) callOnce(ctx context.Context, rev review.Reviewer, req review.Request, m callMeta) (review.Result, model.ReviewCall, error) {
	timeout := review.FastTimeout
	if m.tier == model.TierDeep {
		timeout = review.DeepTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := p.d.Now()
	res, err := rev.Review(cctx, req)
	if res.Ms == 0 {
		res.Ms = p.d.Now().Sub(start).Milliseconds()
	}
	if res.Cost == 0 && (res.InputTokens > 0 || res.OutputTokens > 0) {
		res.Cost = review.Cost(rev.Model(), res.InputTokens, res.OutputTokens)
	}
	if res.Model == "" {
		res.Model = rev.Model()
	}
	call := model.ReviewCall{
		ItemID: m.itemID, GroupID: m.groupID, Task: string(req.Task), Tier: m.tier, Purpose: m.purpose,
		Model: rev.Model(), System: req.System, Prompt: req.Prompt, Context: req.Context,
		Response: res.Text, InputTokens: res.InputTokens, OutputTokens: res.OutputTokens,
		Cost: res.Cost, Ms: res.Ms, At: start,
	}
	return res, withErr(call, err), err
}

// withErr returns call with Err set when err is not nil.
func withErr(call model.ReviewCall, err error) model.ReviewCall {
	if err != nil {
		call.Err = err.Error()
	}
	return call
}

// callRetry is callOnce with one retry (spec failure handling: "retried
// once, then left for R"). An error from the call or from check (e.g. an
// unparseable review) is retried unless ctx is done or the provider key is
// missing. The retry's call-log purpose is "retry". allowRetry (nil:
// always) is asked before the retry, e.g. to reserve its cost from the
// item's budget; false ends with the first error plus errRetryOverCap.
func (p *Pipeline) callRetry(ctx context.Context, rev review.Reviewer, req review.Request, m callMeta, check func(review.Result) error, allowRetry func() bool) callOutcome {
	var out callOutcome
	if rev == nil {
		out.err = fmt.Errorf("%w: no %s reviewer configured", review.ErrUnavailable, m.tier)
		return out
	}
	for attempt := 1; attempt <= 2; attempt++ {
		meta := m
		if attempt > 1 {
			if allowRetry != nil && !allowRetry() {
				out.err = fmt.Errorf("%w (%w)", out.err, errRetryOverCap)
				break
			}
			meta.purpose = purposeRetry
		}
		res, call, err := p.callOnce(ctx, rev, req, meta)
		if err == nil && check != nil {
			err = check(res)
			call = withErr(call, err)
		}
		out.res, out.attempts, out.err = res, attempt, err
		out.calls = append(out.calls, call)
		if err == nil || ctx.Err() != nil || errors.Is(err, review.ErrNoKey) {
			break
		}
	}
	return out
}

// skipReason is the status note for a reviewer that cannot run
// (review.Ready failed): "reviews skipped: PRT_ANTHROPIC_API_KEY not set"
// for a missing key, else "reviews skipped: <reason>".
func skipReason(err error) string {
	if errors.Is(err, review.ErrNoKey) {
		return "reviews skipped: " + review.AnthropicKeyEnv + " not set"
	}
	return "reviews skipped: " + reasonText(err)
}

// reasonText is an error message without the "review: unavailable: "
// prefix, cut to 100 runes, for status notes.
func reasonText(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, review.ErrUnavailable.Error()+": "); i >= 0 {
		s = s[i+len(review.ErrUnavailable.Error())+2:]
	}
	if r := []rune(s); len(r) > 100 {
		s = string(r[:99]) + "…"
	}
	return s
}
