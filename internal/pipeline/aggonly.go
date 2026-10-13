package pipeline

import (
	"context"
	"fmt"

	"github.com/fredricw/prt/internal/model"
)

// storedAnalysis is what an aggregate-only run reads back from the store:
// the analysed fetch, stages 1–5's outputs, the routing rows and the
// previous aggregate.
type storedAnalysis struct {
	f    model.Fetch
	a    analysis
	rows []model.Review
	prev model.ItemAgg
}

// loadAnalysis reads the stored analysis of the item's AnalysedSHA. ok is
// false when there is none to aggregate (never analysed, fetch or
// aggregate missing, no units): the caller then runs normally.
func (p *Pipeline) loadAnalysis(ctx context.Context, it model.Item) (storedAnalysis, bool, error) {
	var s storedAnalysis
	if it.AnalysedSHA == "" {
		return s, false, nil
	}
	f, ok, err := p.d.Store.Fetch(ctx, it.ID, it.AnalysedSHA)
	if err != nil || !ok {
		return s, false, err
	}
	prev, ok, err := p.d.Store.Agg(ctx, it.ID)
	if err != nil || !ok {
		return s, false, err
	}
	files, err := p.d.Store.Files(ctx, it.ID)
	if err != nil {
		return s, false, fmt.Errorf("pipeline: load files: %w", err)
	}
	units, err := p.d.Store.Units(ctx, it.ID)
	if err != nil {
		return s, false, fmt.Errorf("pipeline: load units: %w", err)
	}
	if len(units) == 0 {
		return s, false, nil
	}
	links, err := p.d.Store.Links(ctx, it.ID)
	if err != nil {
		return s, false, fmt.Errorf("pipeline: load links: %w", err)
	}
	if links == nil {
		links = []model.Link{}
	}
	groups, err := p.d.Store.Groups(ctx, it.ID)
	if err != nil {
		return s, false, fmt.Errorf("pipeline: load groups: %w", err)
	}
	rows, err := p.d.Store.ItemReviews(ctx, it.ID)
	if err != nil {
		return s, false, fmt.Errorf("pipeline: load routing rows: %w", err)
	}
	s.f, s.rows, s.prev = f, rows, prev
	s.a = analysis{
		parsed: parseOut{files: files, units: units},
		jo:     jevOut{missing: prev.LabelsMissing},
		gro:    groupOut{groups: groups, links: links, stats: prev.Grouping, split: prev.Split},
	}
	return s, true, nil
}

// runAggregateOnly is RunOptions.AggregateOnly: stage 8 alone on the stored
// analysis with the current overrides (spec: "An override invalidates
// aggregate only, and marks the affected review as stale-labels without
// re-running it"). Routing rows of groups with a unit overridden after the
// row's review become stale-labels and are stored; the aggregate is
// recomputed with no Jev or review call (the branch block, "Since you
// looked" and Jev totals are carried from the stored aggregate; the TL;DR
// is asked only when its request key changed). ok is false when nothing is
// stored, and the caller runs normally.
func (p *Pipeline) runAggregateOnly(ctx context.Context, it model.Item, opt RunOptions) (string, bool, error) {
	cur := p.currentItem(ctx, it)
	s, ok, err := p.loadAnalysis(ctx, cur)
	if err != nil {
		p.d.Log.Warn("load stored analysis failed; running every stage", "item", it.ID, "err", err)
		return "", false, nil
	}
	if !ok {
		return "", false, nil
	}
	r := &run{it: cur, f: s.f, opt: opt, calls: newCallLog()}
	if err := p.preloadCalls(ctx, r, s.a.gro.groups); err != nil {
		p.d.Log.Warn("load call logs failed", "item", it.ID, "err", err)
	}
	_, err = runStage(ctx, p, it.ID, model.StageAggregate, func(rl *model.RunLog) (model.ItemAgg, error) {
		return p.aggregateOnly(ctx, r, s, rl)
	})
	return s.f.HeadSHA, true, err
}

// preloadCalls fills the run's call log with every group's stored calls,
// so the aggregate-only run keeps them (finishAgg rewrites the log) and the
// review totals count them. The item's own row ("": TL;DR, PR title) is
// not loaded: this run's prose calls replace it.
func (p *Pipeline) preloadCalls(ctx context.Context, r *run, groups []model.Group) error {
	for _, g := range groups {
		calls, err := storedCalls(ctx, p.d.Store, r.it.ID, g.ID)
		if err != nil {
			return err
		}
		r.calls.add(g.ID, calls...)
	}
	return nil
}

// aggregateOnly is the aggregate-only stage 8 (see runAggregateOnly).
func (p *Pipeline) aggregateOnly(ctx context.Context, r *run, s storedAnalysis, rl *model.RunLog) (model.ItemAgg, error) {
	in, err := p.staleInput(ctx, s.a.gro.groups, s.a.parsed.units)
	if err != nil {
		return model.ItemAgg{}, err
	}
	rows := markStale(s.rows, in)
	if err := p.d.Store.PutItemReviews(ctx, r.it.ID, rows); err != nil {
		return model.ItemAgg{}, fmt.Errorf("pipeline: store routing rows: %w", err)
	}
	agg, err := p.buildAgg(ctx, r, s.a, rows, s.prev.Branch, rl)
	if err != nil {
		return agg, err
	}
	ro := &routeOut{capUSD: capFor(r.it, p.Routing().Config)}
	rv := reviewOut{rows: rows}
	if pr := s.prev.Review; pr != nil {
		rv.elapsedMs, rv.note = pr.ElapsedMs, pr.Note
	}
	agg.Review = p.reviewStats(r, s.a.gro.groups, rows, ro, rv)
	if pr := s.prev.Review; pr != nil && agg.Review != nil {
		agg.Review.JevCalls, agg.Review.JevCost, agg.Review.JevCached = pr.JevCalls, pr.JevCost, pr.JevCalls > 0 || pr.JevCached
	}
	agg.Since = s.prev.Since
	if n := countStale(rows); n > 0 {
		rl.Note = joinNotes(rl.Note, fmt.Sprintf("aggregate only: %d review(s) stale-labels", n))
	} else {
		rl.Note = joinNotes(rl.Note, "aggregate only")
	}
	return agg, p.finishAgg(ctx, r, s.a, &agg)
}

// countStale counts stale-labels rows.
func countStale(rows []model.Review) int {
	n := 0
	for _, row := range rows {
		if row.Status == model.ReviewStaleLabels {
			n++
		}
	}
	return n
}
