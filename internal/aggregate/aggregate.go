// Package aggregate is pipeline stage 8: plain arithmetic over labels,
// reviews and git facts (spec: "Ranking and aggregation"). It computes
// UnitAgg, GroupAgg, ItemAgg and the urgency score with per-term
// contributions. No I/O, no model calls; the TL;DR is filled in by the
// pipeline from the returned record.
//
// M3 state: every per-unit and per-item formula over labels, plus
//   - urgency label terms (breaking, risk, test gap, relevance) taken from
//     the record being built, or from Input.Prior when Urgency is called on
//     its own (discovery);
//   - hot-spot reasons from review findings (Input.Reviews), falling back to
//     the label that fired;
//   - the group fields routing needs: GroupAgg.Kind, HotSpot and Findings.
//
// M5: branches add w_ready_stale × log1p(IdleDays) when their state is
// ready for PR (Input.Branch for Item, Prior.Branch for Urgency); Item
// copies Input.Branch into the record. IdleDays is the shared idle-time
// helper.
//
// M5.2 (measured with internal/quality, docs/QUALITY.md):
//   - headlines list a claim only when its own question is at least 0.60
//     likely (candidates down to 0.50 are kept in HeadlineCandidates, one
//     per unit without a row, and counted in HeadlinesUnsure), at most
//     one breaking-or-contract row per unit, never for tests, noise or
//     dependency units, no row for a non-breaking signature change, and one
//     external-contract row per file and kind (unitHeadlines,
//     mergeContracts);
//   - Unsure, MinConfidence and the unsure counts read only the attention
//     and risk labels (unsureQuestions) at their decision confidence;
//   - attention caps test units at skim, decides an unknown depth by risk
//     and behaviour, and gives eyes to every unit the Overview headlines as
//     an interface change (attention, interfaceHeadline).
package aggregate

import (
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
	Item   model.Item
	Files  []model.File
	Units  []model.ChangeUnit
	Labels []model.Label // with overrides already applied
	Groups []model.Group
	// Links are the item's stored links (structural and Jev-confirmed);
	// M2 fidelity uses incoming calls links (see the M2 fidelity rule).
	Links []model.Link
	// Reviews are the routing-table rows (one per group, with findings):
	// they give hot spots their finding reasons and GroupAgg.Findings.
	Reviews  []model.Review
	Weights  config.Rank
	Pipeline config.Pipeline
	AgeDays  float64
	// IdleDays is the days since the branch's head commit
	// (Item.LastCommitAt; 0 for PRs or when unknown). The ready-for-PR
	// staleness bonus reads it (M5; filled by the pipeline and by
	// discovery).
	IdleDays           float64
	Release            bool // base is a release branch
	CIRedAfterApproval bool
	// Owns are the user's owned globs (config user.owns), used for "Your
	// slice" module names; File.Owned already says which files are owned.
	Owns []string
	// LabelsMissing is set by the pipeline when Jev was unavailable for this
	// run; it is copied to ItemAgg.LabelsMissing.
	LabelsMissing bool
	// Prior, when set, is the item's stored aggregate: Urgency then takes
	// its label terms (breaking, risk, test gap, relevance) from it instead
	// of zeros. Discovery sets it so a poll keeps the label-based ranking
	// (M3; DECISIONS "M3 shared contracts"). Item ignores it; Urgency also
	// reads the branch state (ready-for-PR bonus) from Prior.Branch.
	Prior *model.ItemAgg
	// Branch is the branch state block the pipeline computed for a branch
	// item (nil for PRs or when not computed). Item copies it into the
	// record (ItemAgg.Branch) and its ready-for-PR state drives the
	// staleness bonus (M5).
	Branch *model.BranchInfo
}

// IdleDays is the days since a branch's head commit at now (0 when the
// commit time is unknown or in the future): aggregate.Input.IdleDays, so
// the pipeline and discovery compute it the same way.
func IdleDays(lastCommitAt, now time.Time) float64 {
	if lastCommitAt.IsZero() || !now.After(lastCommitAt) {
		return 0
	}
	return now.Sub(lastCommitAt).Hours() / 24
}

// Item computes the overview record from units, labels and git facts: per
// unit aggregates, composition, attention and minutes, risk, hot spots,
// headlines, description fidelity, the Code page file list, "Your slice",
// group aggregates and urgency. It is pure and deterministic: every slice is
// ordered independently of map iteration.
//
// Minutes are the label-based estimate when at least one label is a real
// answer; otherwise (no labels, or Jev unreachable) the M0 line-count
// stand-in, so an unlabelled item is not ranked as if every line needed eyes.
func Item(in Input) model.ItemAgg {
	unsureBelow := in.Pipeline.UnsureBelow
	if unsureBelow <= 0 {
		unsureBelow = defaultUnsureBelow
	}
	idx := newIndex(in.Item.ID, in.Labels, in.Units)
	us := units(in, idx, unsureBelow)
	s := summarise(us, unsureBelow)

	agg := model.ItemAgg{
		ItemID:         in.Item.ID,
		Composition:    s.composition,
		EffectiveLines: s.effective,
		UnitsTotal:     len(us),
		RiskMax:        float64(s.riskMax),
		RiskAny:        riskAny(us),
		RiskTags:       s.riskTags,
		TestGap:        s.testGap,
		RiskyUnits:     s.risky,
		Unsure:         s.unsure,
		HotSpots:       hotSpots(us, findingReasons(in.Reviews)),
		Undocumented:   []string{},
		Files:          files(in.Files, us),
		Slice:          slice(in.Owns, us),
		Groups:         groups(in.Groups, us, in.Reviews, unsureBelow),
		Units:          make([]model.UnitAgg, 0, len(us)),
		LabelsMissing:  in.LabelsMissing,
		Branch:         in.Branch,
	}
	agg.Headlines, agg.HeadlineCandidates = headlines(us)
	agg.HeadlinesUnsure = len(agg.HeadlineCandidates)
	for _, f := range us {
		countUnit(&agg, f)
	}
	agg.Bullets, agg.Fidelity = bullets(in.Item.Body, us, unwiredUnits(in.Units, in.Links))
	if l, ok := idx.item[model.QShape]; ok && !model.IsUnknown(l) {
		agg.Shape, agg.ShapeP = l.Value, l.P
	}
	agg.Minutes = StandInMinutes(agg.Added + agg.Removed)
	if idx.anyKnown {
		agg.Minutes = s.minutes
	}
	agg.UrgencyTerms, agg.Urgency, agg.Why = urgency(in, agg.Minutes, labelTermsOf(&agg))
	return agg
}

// countUnit adds one unit's lines, attention and flags to the item totals.
func countUnit(agg *model.ItemAgg, f unitFacts) {
	a := f.agg
	agg.Units = append(agg.Units, a)
	agg.Added += f.unit.Adds
	agg.Removed += f.unit.Dels
	if a.Noise {
		agg.NoiseLines += f.unit.Adds + f.unit.Dels
	}
	switch a.Attention {
	case model.AttnEyes:
		agg.UnitsEyes++
	case model.AttnSkim:
		agg.UnitsSkim++
	default:
		agg.UnitsSkip++
	}
	if a.Undocumented {
		agg.Undocumented = append(agg.Undocumented, a.UnitID)
	}
	if a.Unsure {
		agg.UnsureUnits = append(agg.UnsureUnits, a.UnitID)
	}
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
