package aggregate

import (
	"math"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// maxBreakingTerm bounds the breaking term's value: the count of breaking
// units saturates at three, so one PR with dozens of breaking rows (real
// labels on #101 give about 35) cannot drown every other term. With the
// default weight 2.0 the term is worth at most 6.0, twice "asked you".
const maxBreakingTerm = 3

// labelTerms are the urgency factors that come from labels.
type labelTerms struct {
	breaking  float64 // breaking units, bounded by maxBreakingTerm
	risk      float64 // RiskMax, 1..5, 0 unknown
	testGap   float64 // TestGap
	relevance float64 // max interpolated relevance over non-noise units
}

// labelTermsOf reads the label factors from an aggregate record; a nil
// record (no stored aggregate) yields zeros. Unknown labels already read as
// 0 in the record, so they contribute nothing.
func labelTermsOf(a *model.ItemAgg) labelTerms {
	if a == nil {
		return labelTerms{}
	}
	breaking, relevance := 0, 0.0
	for _, u := range a.Units {
		if u.Breaking {
			breaking++
		}
		if !u.Noise {
			relevance = max(relevance, u.Relevance)
		}
	}
	return labelTerms{
		breaking:  float64(min(breaking, maxBreakingTerm)),
		risk:      max(a.RiskMax, 0),
		testGap:   float64(max(a.TestGap, 0)),
		relevance: relevance,
	}
}

// Urgency computes the urgency terms, their sum and the "why" line. Terms
// that do not apply to the item's kind are omitted: PRs get review
// requested, release and CI terms; branches get unpushed commits instead
// (spec: "Branches replace the PR-only terms"). minutes enters negatively.
// The label terms (breaking, risk, test gap, relevance) come from in.Prior,
// the stored aggregate, and are 0 without one.
func Urgency(in Input, minutes float64) ([]model.UrgencyTerm, float64, string) {
	return urgency(in, minutes, labelTermsOf(in.Prior))
}

// urgency is Urgency with explicit label factors (Item passes the record it
// is building).
func urgency(in Input, minutes float64, lt labelTerms) ([]model.UrgencyTerm, float64, string) {
	w := in.Weights
	isPR := in.Item.Kind != model.KindBranch
	var terms []model.UrgencyTerm
	add := func(name, words string, value, weight float64, sign float64) {
		terms = append(terms, model.UrgencyTerm{Name: name, Words: words, Value: value, Weight: weight, Contrib: sign * weight * value})
	}
	if isPR {
		add("w_requested", "asked you", indicator(in.Item.ReviewRequested), w.WRequested, 1)
	}
	add("w_breaking", "breaking", lt.breaking, w.WBreaking, 1)
	add("w_risk", "risk", lt.risk, w.WRisk, 1)
	add("w_test_gap", "untested", lt.testGap, w.WTestGap, 1)
	add("w_relevance", "relevant to you", lt.relevance, w.WRelevance, 1)
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
