package pipeline

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
)

// staleNotePrefix starts every Review.StaleNote entry.
const staleNotePrefix = "label overridden: "

// staleGroups returns the groups whose stored routing row is stale-labels
// (read by stage 6 before it replaces the rows). A store error is logged
// and yields none.
func (p *Pipeline) staleGroups(ctx context.Context, itemID string) map[string]bool {
	rows, err := p.d.Store.ItemReviews(ctx, itemID)
	if err != nil {
		p.d.Log.Warn("load routing rows failed", "item", itemID, "err", err)
		return nil
	}
	out := map[string]bool{}
	for _, row := range rows {
		if row.Status == model.ReviewStaleLabels {
			out[row.GroupID] = true
		}
	}
	return out
}

// staleIn is what markStale reads besides the rows.
type staleIn struct {
	groups []model.Group
	units  []model.ChangeUnit
	// overrides are the user's label overrides of the units; labels the
	// units' stored (raw, not overridden) labels, for the old values.
	overrides []model.Override
	labels    []model.Label
	// eligible says whether a row may become stale-labels (a finished
	// review this run did not just make).
	eligible func(model.Review) bool
}

// markStale returns rows with every eligible done (or already stale) row
// whose group contains a unit with an override newer than the row's
// StartedAt (any override when StartedAt is zero) set to
// model.ReviewStaleLabels, with StaleNote listing those overrides
// ("label overridden: risk 4→2 (web/a.ts:12)", several joined by "; ").
// Findings, verdict and cost are kept. Other rows are unchanged.
func markStale(rows []model.Review, in staleIn) []model.Review {
	if len(in.overrides) == 0 {
		return rows
	}
	byGroup := make(map[string]model.Group, len(in.groups))
	for _, g := range in.groups {
		byGroup[g.ID] = g
	}
	units := unitsByID(in.units)
	old := oldValues(in.labels)
	byUnit := map[string][]model.Override{}
	for _, o := range in.overrides {
		byUnit[o.UnitID] = append(byUnit[o.UnitID], o)
	}
	out := append([]model.Review(nil), rows...)
	for i, row := range out {
		if row.Tier == model.TierNone || (row.Status != model.ReviewDone && row.Status != model.ReviewStaleLabels) {
			continue
		}
		if in.eligible != nil && !in.eligible(row) {
			continue
		}
		var newer []model.Override
		for _, id := range byGroup[row.GroupID].UnitIDs {
			for _, o := range byUnit[id] {
				if row.StartedAt.IsZero() || o.At.After(row.StartedAt) {
					newer = append(newer, o)
				}
			}
		}
		if len(newer) == 0 {
			continue
		}
		out[i].Status, out[i].StaleNote = model.ReviewStaleLabels, staleNote(newer, units, old)
	}
	return out
}

// oldValues maps (unit, question) to the stored unit-scope label value.
func oldValues(ls []model.Label) map[[2]string]string {
	out := make(map[[2]string]string, len(ls))
	for _, l := range ls {
		if l.Line == 0 {
			out[[2]string{l.UnitID, l.Question}] = l.Value
		}
	}
	return out
}

// staleNote renders overrides oldest first (ties by question, then unit).
func staleNote(ovs []model.Override, units map[string]model.ChangeUnit, old map[[2]string]string) string {
	sort.SliceStable(ovs, func(a, b int) bool {
		if !ovs[a].At.Equal(ovs[b].At) {
			return ovs[a].At.Before(ovs[b].At)
		}
		if ovs[a].Question != ovs[b].Question {
			return ovs[a].Question < ovs[b].Question
		}
		return ovs[a].UnitID < ovs[b].UnitID
	})
	parts := make([]string, 0, len(ovs))
	for _, o := range ovs {
		was, ok := old[[2]string{o.UnitID, o.Question}]
		if !ok {
			was = model.LabelUnknown
		}
		u := units[o.UnitID]
		parts = append(parts, fmt.Sprintf("%s%s %s→%s (%s:%d)", staleNotePrefix, o.Question, was, o.Value, u.Path, unitLine(u)))
	}
	return strings.Join(parts, "; ")
}

// unitLine is the line a unit is shown at: its first new-side line, else
// (a pure deletion) its first old-side line.
func unitLine(u model.ChangeUnit) int {
	if u.Range.New.Start > 0 {
		return u.Range.New.Start
	}
	return u.Range.Old.Start
}

// staleInput loads the overrides and stored labels of units for markStale.
func (p *Pipeline) staleInput(ctx context.Context, groups []model.Group, units []model.ChangeUnit) (staleIn, error) {
	ids := unitIDs(units)
	ovs, err := p.d.Store.Overrides(ctx, ids)
	if err != nil {
		return staleIn{}, fmt.Errorf("pipeline: load overrides: %w", err)
	}
	in := staleIn{groups: groups, units: units, overrides: ovs}
	if len(ovs) == 0 {
		return in, nil
	}
	if in.labels, err = p.d.Store.Labels(ctx, ids); err != nil {
		return staleIn{}, fmt.Errorf("pipeline: load labels: %w", err)
	}
	return in, nil
}

// keepStale is stage 7's stale-labels rule for a normal run: a row this
// run did not review afresh (a cached review served) keeps or gets the
// stale-labels status when its group was stale-labels before the run, or
// when the cached review has a time and an override of one of its units is
// newer. A row reviewed afresh (routed, retried or escalated call; a
// forced D/R deep re-review) is done. Store errors are logged and leave the
// rows as they are.
func (p *Pipeline) keepStale(ctx context.Context, r *run, ro *routeOut, rows []model.Review) []model.Review {
	in, err := p.staleInput(ctx, ro.groups, ro.units)
	if err != nil {
		p.d.Log.Warn("stale-labels check failed", "item", r.it.ID, "err", err)
		return rows
	}
	in.eligible = func(row model.Review) bool {
		if reviewedAfresh(r.calls.get(row.GroupID)) {
			return false
		}
		return ro.prevStale[row.GroupID] || !row.StartedAt.IsZero()
	}
	return markStale(rows, in)
}

// reviewedAfresh reports whether calls hold a successful review call made
// this run (not a cache hit) that produced the row's result (routed, retry
// or escalation; a calibration sample does not change the row's result).
func reviewedAfresh(calls []model.ReviewCall) bool {
	for _, c := range calls {
		if c.Task != string(review.TaskReview) || c.Cached || c.Err != "" {
			continue
		}
		switch c.Purpose {
		case purposeRouted, purposeRetry, purposeEscalation:
			return true
		}
	}
	return false
}
