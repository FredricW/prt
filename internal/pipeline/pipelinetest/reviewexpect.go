package pipelinetest

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
)

// ReviewExpectations returns how the review pipeline's outcome over
// #101–#104 deviates from what the fixture PRs were designed for:
//
//	(a) every PR: one routing row per group;
//	(b) #102: every mechanical group routed none;
//	(c) #104 (a dependency bump): every group routed none;
//	(d) #101: the group holding the client.ts getUser change (breaking) is
//	    deep, or fast with "callers" context;
//	(e) when reviewers ran (Agg.Review set without a degradation note):
//	    every routed row done or failed (queued only over the cost cap,
//	    with a note) and the TL;DR non-empty and ≤ 160 runes;
//	(f) urgency: #101 (review requested, breaking, risky) above #104.
//
// Empty when all hold. The review recorder prints them as warnings after a
// live run.
func ReviewExpectations(outs map[int]Outcome) []string {
	var out []string
	for _, n := range PRNumbers {
		o := outs[n]
		out = append(out, rowPerGroup(n, o)...)
		if reviewed(o) {
			out = append(out, rowsFinished(n, o)...)
			out = append(out, tldrOK(n, o)...)
		}
	}
	out = append(out, mechanicalNone102(outs[102])...)
	out = append(out, allNone104(outs[104])...)
	out = append(out, getUserRouted101(outs[101])...)
	if a, b := outs[101].Item.Urgency, outs[104].Item.Urgency; a <= b {
		out = append(out, fmt.Sprintf("urgency: #101 (%.2f) not above #104 (%.2f)", a, b))
	}
	return out
}

// reviewed reports whether stage 7 ran with reviewers (no skip note).
func reviewed(o Outcome) bool {
	return o.Agg.Review != nil && o.Agg.Review.Note == ""
}

// rowsByGroup indexes the routing rows by group id.
func rowsByGroup(o Outcome) map[string]model.Review {
	m := make(map[string]model.Review, len(o.Reviews))
	for _, r := range o.Reviews {
		m[r.GroupID] = r
	}
	return m
}

// rowPerGroup checks (a).
func rowPerGroup(n int, o Outcome) []string {
	rows := rowsByGroup(o)
	var out []string
	if len(o.Reviews) != len(o.Groups) {
		out = append(out, fmt.Sprintf("#%d: %d routing rows for %d groups", n, len(o.Reviews), len(o.Groups)))
	}
	for _, g := range o.Groups {
		if _, ok := rows[g.ID]; !ok {
			out = append(out, fmt.Sprintf("#%d: group %q has no routing row", n, g.Name))
		}
	}
	return out
}

// rowsFinished checks (e) for the rows.
func rowsFinished(n int, o Outcome) []string {
	var out []string
	for _, r := range o.Reviews {
		if r.Tier == model.TierNone {
			continue
		}
		switch r.Status {
		case model.ReviewDone, model.ReviewFailed:
		case model.ReviewQueued:
			if r.QueueNote == "" || o.Agg.Review.OverCap == 0 {
				out = append(out, fmt.Sprintf("#%d: group %q queued without hitting the cost cap", n, r.GroupName))
			}
		default:
			out = append(out, fmt.Sprintf("#%d: group %q (%s) status %q, want done or failed", n, r.GroupName, r.Tier, r.Status))
		}
	}
	return out
}

// tldrOK checks (e) for the TL;DR.
func tldrOK(n int, o Outcome) []string {
	t := o.Agg.TLDR
	switch {
	case strings.TrimSpace(t) == "":
		return []string{fmt.Sprintf("#%d: TL;DR empty", n)}
	case utf8.RuneCountInString(t) > review.MaxTLDR:
		return []string{fmt.Sprintf("#%d: TL;DR has %d runes, want ≤ %d", n, utf8.RuneCountInString(t), review.MaxTLDR)}
	}
	return nil
}

// mechanicalNone102 checks (b).
func mechanicalNone102(o Outcome) []string {
	rows := rowsByGroup(o)
	var out []string
	for _, g := range o.Groups {
		if r, ok := rows[g.ID]; ok && g.Mechanical && r.Tier != model.TierNone {
			out = append(out, fmt.Sprintf("#102: mechanical group %q routed %s (%s), want none", g.Name, r.Tier, r.RuleWords))
		}
	}
	return out
}

// allNone104 checks (c).
func allNone104(o Outcome) []string {
	var out []string
	for _, r := range o.Reviews {
		if r.Tier != model.TierNone {
			out = append(out, fmt.Sprintf("#104: group %q routed %s (%s), want none (dependency bump)", r.GroupName, r.Tier, r.RuleWords))
		}
	}
	return out
}

// getUserRouted101 checks (d).
func getUserRouted101(o Outcome) []string {
	units := unitMap(o)
	rows := rowsByGroup(o)
	for _, g := range o.Groups {
		for _, id := range g.UnitIDs {
			if u := units[id]; u.Path != clientPath || !strings.Contains(u.Symbol, "getUser") {
				continue
			}
			r, ok := rows[g.ID]
			switch {
			case !ok:
				return []string{"#101: the getUser group has no routing row"}
			case r.Tier == model.TierDeep:
				return nil
			// Routing decided callers (attached, or none changed in the PR).
			case r.Tier == model.TierFast && (hasString(r.Context, model.CtxCallers) || hasString(r.ContextNone, model.CtxCallers)):
				return nil
			}
			return []string{fmt.Sprintf("#101: getUser group %q routed %s with context %v, none found %v (%s), want deep or fast with callers", g.Name, r.Tier, r.Context, r.ContextNone, r.RuleWords)}
		}
	}
	return []string{"#101: no group holds the client.ts getUser unit"}
}

func hasString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
