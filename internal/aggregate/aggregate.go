// Package aggregate is pipeline stage 8: plain arithmetic over labels,
// reviews and git facts (spec: "Ranking and aggregation"). It computes
// UnitAgg, GroupAgg, ItemAgg and the urgency score with per-term
// contributions. No I/O, no model calls; the TL;DR is filled in by the
// pipeline from the returned record.
//
// M0 state: line counts plus the urgency placeholder (DECISIONS "M0 urgency
// placeholder"): only terms that need no labels contribute; label terms are
// present with value 0 so M3 only has to fill them in.
//
// TODO(M1): composition, headlines, risk, test gap, attention/minutes.
// TODO(M3): hot spots with findings, label urgency terms.
package aggregate

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

// StandInMinutesPerLine is the M0 reading-time stand-in: 0.1 minute per
// changed line until attention labels exist (M1).
const StandInMinutesPerLine = 0.1

// Input is everything aggregate reads.
type Input struct {
	Item               model.Item
	Files              []model.File
	Units              []model.ChangeUnit
	Labels             []model.Label // with overrides already applied
	Groups             []model.Group
	Reviews            []model.Review
	Weights            config.Rank
	Pipeline           config.Pipeline
	AgeDays            float64
	Release            bool // base is a release branch
	CIRedAfterApproval bool
}

// Item computes the overview record: line counts, minutes and urgency.
func Item(in Input) model.ItemAgg {
	agg := model.ItemAgg{ItemID: in.Item.ID, UnitsTotal: len(in.Units)}
	for _, u := range in.Units {
		agg.Added += u.Adds
		agg.Removed += u.Dels
	}
	agg.Minutes = StandInMinutes(agg.Added + agg.Removed)
	agg.UrgencyTerms, agg.Urgency, agg.Why = Urgency(in, agg.Minutes)
	return agg
}

// StandInMinutes is the M0 reading-time estimate for a number of changed lines.
func StandInMinutes(changedLines int) float64 {
	return StandInMinutesPerLine * float64(max(changedLines, 0))
}

// Facts derives the git/GitHub facts the urgency score needs from an item:
// its age in days (PR creation, else last update), whether its base matches
// one of the release-branch globs, and whether CI is red on something the
// viewer approved.
func Facts(it model.Item, releaseBranches []string, now time.Time) (ageDays float64, release, ciRedAfterApproval bool) {
	start := it.CreatedAt
	if start.IsZero() {
		start = it.UpdatedAt
	}
	if !start.IsZero() && now.After(start) {
		ageDays = now.Sub(start).Hours() / 24
	}
	return ageDays, IsRelease(it.BaseRef, releaseBranches), it.ViewerApproved && it.CI == model.CIRed
}

// IsRelease reports whether base matches any of the release-branch globs.
func IsRelease(base string, globs []string) bool {
	if base == "" {
		return false
	}
	for _, g := range globs {
		if ok, err := doublestar.Match(g, base); err == nil && ok {
			return true
		}
	}
	return false
}

// Urgency computes the urgency terms, their sum and the "why" line. Terms
// that do not apply to the item's kind are omitted: PRs get review
// requested, release and CI terms; branches get unpushed commits instead
// (spec: "Branches replace the PR-only terms"). minutes enters negatively.
func Urgency(in Input, minutes float64) ([]model.UrgencyTerm, float64, string) {
	w := in.Weights
	isPR := in.Item.Kind != model.KindBranch
	var terms []model.UrgencyTerm
	add := func(name, words string, value, weight float64, sign float64) {
		terms = append(terms, model.UrgencyTerm{Name: name, Words: words, Value: value, Weight: weight, Contrib: sign * weight * value})
	}
	if isPR {
		add("w_requested", "asked you", indicator(in.Item.ReviewRequested), w.WRequested, 1)
	}
	// Label terms: 0 until M1/M3 fill them from labels.
	add("w_breaking", "breaking", 0, w.WBreaking, 1)
	add("w_risk", "risk", 0, w.WRisk, 1)
	add("w_test_gap", "untested", 0, w.WTestGap, 1)
	add("w_relevance", "relevant to you", 0, w.WRelevance, 1)
	if isPR {
		add("w_release", "release", indicator(in.Release), w.WRelease, 1)
		add("w_ci_red", "CI red after approval", indicator(in.CIRedAfterApproval), w.WCIRed, 1)
	}
	add("w_age", "age", math.Log1p(max(in.AgeDays, 0)), w.WAge, 1)
	add("w_minutes", "size", minutes, w.WMinutes, -1)
	if !isPR {
		add("w_unpushed", "unpushed", float64(max(in.Item.Unpushed, 0)), w.WUnpushed, 1)
	}
	total := 0.0
	for _, t := range terms {
		total += t.Contrib
	}
	return terms, total, Why(terms)
}

// Why returns the words of the three largest positive contributions, joined
// by " · " (ties keep term order).
func Why(terms []model.UrgencyTerm) string {
	pos := make([]model.UrgencyTerm, 0, len(terms))
	for _, t := range terms {
		if t.Contrib > 0 {
			pos = append(pos, t)
		}
	}
	sort.SliceStable(pos, func(i, j int) bool { return pos[i].Contrib > pos[j].Contrib })
	words := make([]string, 0, 3)
	for i := 0; i < len(pos) && i < 3; i++ {
		words = append(words, pos[i].Words)
	}
	return strings.Join(words, " · ")
}

func indicator(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
