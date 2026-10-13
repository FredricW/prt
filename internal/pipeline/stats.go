package pipeline

import (
	"context"
	"sort"
	"time"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
)

// All-deep estimate constants (ReviewStats.AllDeepEstMs; DECISIONS
// "Pipeline stages 6–8 (M3)"): a row reviewed only on the fast tier counts
// its fast time × deepMsFactor, a row with no timed call deepMsPerGroup.
const (
	deepMsFactor   = 4
	deepMsPerGroup = 20_000
)

// calibrationWindow is the trailing window of the calibration panel.
const calibrationWindow = 30 * 24 * time.Hour

// tldr writes the TL;DR (stage 8): review.BuildTLDR from the aggregate
// record, keyed by review.RequestKey on the fast model. A cached text for
// that key is reused with no call ("regenerated only when the aggregate
// record changes"); a forced run or FromStage ≤ aggregate asks again. The
// text is CleanTLDR'd and capped at 160 runes. No fast reviewer leaves it
// empty; an error leaves it empty with a note (returned). A fast reviewer
// that is not ready (no key) still serves a cached TL;DR.
func (p *Pipeline) tldr(ctx context.Context, r *run, agg *model.ItemAgg, rl *model.RunLog) string {
	fast := p.d.Reviewers.Fast
	if fast == nil {
		return ""
	}
	bypass := r.opt.Force || r.opt.ignoresCache(model.StageAggregate)
	clean := func(s string) string { return capRunes(p.fn.cleanTLDR(s), review.MaxTLDR) }
	out := p.prose(ctx, nsTLDR, p.fn.buildTLDR(*agg), callMeta{itemID: r.it.ID, tier: model.TierFast, purpose: purposeTLDR}, bypass, clean)
	if out.notReady {
		// A cached TL;DR is served without a key; only a miss is skipped.
		return "TL;DR skipped: " + reasonText(out.err)
	}
	r.calls.add("", out.calls...)
	if !out.cached {
		rl.Calls += len(out.calls)
	}
	rl.Cost += out.spent()
	switch {
	case out.err != nil:
		p.d.Log.Warn("TL;DR failed", "item", r.it.ID, "err", out.err)
		return "TL;DR failed: " + reasonText(out.err)
	case out.text == "":
		return "TL;DR empty"
	}
	agg.TLDR, agg.TLDRKey = out.text, out.key
	if out.cached {
		rl.CacheHit = true
		return "TL;DR cached"
	}
	return "TL;DR written"
}

// reviewStats is the Pipeline page's totals block from the routing rows
// and this run's call log and Jev row.
func (p *Pipeline) reviewStats(r *run, groups []model.Group, rows []model.Review, ro *routeOut, rv reviewOut) *model.ReviewStats {
	s := &model.ReviewStats{
		Groups: len(rows), Tiers: map[model.Tier]int{}, Status: map[model.ReviewStatus]int{},
		ElapsedMs: rv.elapsedMs, CapUSD: ro.capUSD, JevCalls: r.jevCalls, JevCost: r.jevCost, Note: rv.note,
	}
	if ro.failed {
		s.Note = joinNotes(s.Note, "routing failed")
	}
	for _, row := range rows {
		s.Tiers[row.Tier]++
		s.Status[row.Status]++
		s.Findings += len(row.Findings)
		if row.EscalatedOf != nil {
			s.Escalations++
		}
		if row.Calibration != nil {
			s.Calibrations++
		}
		if row.Status == model.ReviewQueued && row.QueueNote != "" {
			s.OverCap++
		}
		if row.Tier != model.TierNone {
			s.AllDeepEstMs += deepEstMs(r.calls.get(row.GroupID))
		}
	}
	for _, id := range groupIDs(groups, "") {
		s.Cost += r.calls.cost(id)
	}
	return s
}

// deepEstMs is one routed row's deep-tier time: its last successful deep
// review call, else its last fast one × deepMsFactor, else deepMsPerGroup.
func deepEstMs(calls []model.ReviewCall) int64 {
	var deep, fast int64
	for _, c := range calls {
		if c.Task != string(review.TaskReview) || c.Err != "" || c.Ms <= 0 {
			continue
		}
		switch c.Tier {
		case model.TierDeep:
			deep = c.Ms
		case model.TierFast:
			fast = c.Ms
		}
	}
	switch {
	case deep > 0:
		return deep
	case fast > 0:
		return fast * deepMsFactor
	}
	return deepMsPerGroup
}

// calibrationStats is the calibration panel: sampled double reviews of the
// trailing 30 days (all items), the last sample's agreement, and label
// overrides by question in the same window. Store errors are logged and
// leave the affected part empty.
func (p *Pipeline) calibrationStats(ctx context.Context) *model.CalibrationStats {
	since := p.d.Now().Add(-calibrationWindow)
	cs := &model.CalibrationStats{Overrides: map[string]int{}}
	samples, err := p.d.Store.Calibrations(ctx, since)
	if err != nil {
		p.d.Log.Warn("load calibration samples failed", "err", err)
	}
	cs.Samples = len(samples)
	for _, s := range samples {
		if s.Missed > 0 {
			cs.Misses++
		}
	}
	if cs.Samples > 0 {
		cs.MissRate = float64(cs.Misses) / float64(cs.Samples)
		last := samples[len(samples)-1]
		agree := last.Agree
		cs.LastAgree, cs.LastAt = &agree, last.At.UTC().Format(time.RFC3339)
		cs.LastGroup = p.sampleGroupName(ctx, last)
	}
	ovs, err := p.d.Store.AllOverrides(ctx)
	if err != nil {
		p.d.Log.Warn("load overrides failed", "err", err)
	}
	for _, o := range ovs {
		if o.At.IsZero() || !o.At.Before(since) {
			cs.Overrides[o.Question]++
		}
	}
	cs.FlagAbove = overrideFlagAbove
	if len(cs.Overrides) > 0 {
		counts, err := p.d.Store.LabelCounts(ctx)
		if err != nil {
			p.d.Log.Warn("load label counts failed", "err", err)
		}
		cs.Questions = questionRates(cs.Overrides, counts, overrideFlagAbove)
	}
	return cs
}

// overrideFlagAbove is the override rate above which the calibration panel
// flags a question as a rewording candidate (spec: "A question over 15%
// gets flagged").
const overrideFlagAbove = 0.15

// questionRates is the per-question override rate (spec "Calibration
// metrics": overrides ÷ labels): one row per question with at least one
// override, Labels from the store's label counts (all stored labels:
// labels carry no time), Flagged when Rate > flagAbove; highest rate
// first, ties by question. A question without stored labels has rate 0.
func questionRates(overrides, labels map[string]int, flagAbove float64) []model.QuestionRate {
	out := make([]model.QuestionRate, 0, len(overrides))
	for q, n := range overrides {
		if n <= 0 {
			continue
		}
		qr := model.QuestionRate{Question: q, Overrides: n, Labels: labels[q]}
		if qr.Labels > 0 {
			qr.Rate = float64(n) / float64(qr.Labels)
		}
		qr.Flagged = qr.Rate > flagAbove
		out = append(out, qr)
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Rate != out[b].Rate {
			return out[a].Rate > out[b].Rate
		}
		return out[a].Question < out[b].Question
	})
	return out
}

// sampleGroupName is the name of the group a calibration sample
// double-reviewed, from its item's routing row ("" when the row is gone).
func (p *Pipeline) sampleGroupName(ctx context.Context, s model.CalibrationSample) string {
	rows, err := p.d.Store.ItemReviews(ctx, s.ItemID)
	if err != nil {
		p.d.Log.Debug("load routing rows for calibration sample failed", "item", s.ItemID, "err", err)
		return ""
	}
	for _, row := range rows {
		if row.GroupID == s.GroupID {
			return row.GroupName
		}
	}
	return ""
}
