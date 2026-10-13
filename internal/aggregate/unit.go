package aggregate

import (
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Minutes per effective line by attention (spec "Ranking and aggregation").
const (
	eyesMinutesPerLine = 0.5
	skimMinutesPerLine = 0.1
	eyesMinMinutes     = 1.0
	// defaultUnsureBelow applies when config pipeline.unsure_below is unset.
	defaultUnsureBelow = 0.60
	// suspiciousMarkRung is the lowest suspicious rung that earns a ◆ mark.
	suspiciousMarkRung = 3
)

// unitFacts is a unit's aggregate plus the positional facts the item-level
// arithmetic needs but UnitAgg does not carry.
type unitFacts struct {
	unit   model.ChangeUnit
	agg    model.UnitAgg
	labels *unitLabels
	// line is the first changed new-side line (old side for pure deletions).
	line int
}

// unitAgg computes one unit's aggregate. counted is false for members of a
// mechanical group other than its example: they contribute no effective
// lines (and therefore no minutes).
func unitAgg(u model.ChangeUnit, ul *unitLabels, owned, counted bool, unsureBelow float64) model.UnitAgg {
	a := model.UnitAgg{UnitID: u.ID, Path: u.Path, Owned: owned, Kind: ul.value(model.QKind)}
	labelFacts(&a, ul)
	a.Lines = lineMarks(ul)
	a.RiskTags = riskTags(ul)
	a.MinConfidence, a.Unsure = confidence(ul, unsureBelow)
	a.NoiseReason = noiseReason(u, a.Kind, ul)
	a.Noise = a.NoiseReason != ""
	if !a.Noise && counted {
		a.EffectiveLines = u.Adds + u.Dels
	}
	a.Attention = attention(ul.value(model.QDepth), a, isTestUnit(u, a.Kind))
	if a.Attention != model.AttnEyes && interfaceHeadline(u, a, ul) {
		// The Overview headlines this unit as an interface change, so it
		// must not be skimmed, whatever its depth answer.
		a.Attention = model.AttnEyes
	}
	a.Minutes = minutes(a.Attention, a.EffectiveLines)
	a.Untested = a.RiskLevel >= 3 && !a.CoveredUnknown && a.CoveredP < 0.5
	a.Undocumented = ul.value(model.QDescBullet) == model.BulletNone && a.BehaviourP >= 0.5
	return a
}

// labelFacts copies the chip and filter facts from the unit-scope labels.
func labelFacts(a *model.UnitAgg, ul *unitLabels) {
	a.BehaviourP = ul.yesP(model.QBehaviour)
	a.Behaviour = a.BehaviourP >= 0.5
	a.BreakingP = ul.yesP(model.QBreaking)
	a.Breaking = a.BreakingP >= 0.5
	a.CoveredP = ul.yesP(model.QCovered)
	a.Covered = a.CoveredP >= 0.5
	_, ok := ul.known(model.QBehaviour)
	a.BehaviourUnknown = !ok
	_, ok = ul.known(model.QBreaking)
	a.BreakingUnknown = !ok
	_, ok = ul.known(model.QCovered)
	a.CoveredUnknown = !ok
	if l, ok := ul.known(model.QContract); ok {
		a.Contract = l.Value
	}
	if l, ok := ul.known(model.QFailMode); ok {
		a.FailMode = l.Value
	}
	if l, ok := ul.known(model.QRisk); ok {
		a.Risk = l.Score
		a.RiskLevel = model.ScoreLevel(l)
	}
	if l, ok := ul.known(model.QRelevance); ok {
		a.Relevance = l.Score
	}
	a.DescBullet = model.BulletIndex(ul.value(model.QDescBullet))
}

// riskTags returns the sub-ids of risk_tags answered yes, plus error_path
// when a line swallows an error and pii when a line may expose personal data
// or a secret, deduplicated, in model.RiskTags order.
func riskTags(ul *unitLabels) []string {
	set := map[string]bool{}
	for _, sub := range model.RiskTags {
		if ul.yesP(model.SubQuestion(model.QRiskTags, sub)) >= 0.5 {
			set[sub] = true
		}
	}
	if len(ul.linesYes(model.QSwallow)) > 0 {
		set["error_path"] = true
	}
	if len(ul.linesYes(model.QPIIOrSecret)) > 0 {
		set["pii"] = true
	}
	var out []string
	for _, sub := range model.RiskTags {
		if set[sub] {
			out = append(out, sub)
		}
	}
	return out
}

// unsureQuestions are the unit-scope labels that drive a unit's attention
// and risk: depth (attention, minutes), kind (noise, test units),
// behaviour and risk (attention when depth is unknown; risk max, test gap,
// hot spots), breaking (headlines, urgency) and covered (test gap, hot
// spots). Unsure, MinConfidence and the unsure counts read only these
// (M5.2, the user's narrower "unsure"): a unit with ~25 labels nearly
// always has some risk-tag sub-boolean or line label below 0.60, which made
// every fixture unit unsure and never skip.
var unsureQuestions = [...]string{model.QDepth, model.QKind, model.QBehaviour, model.QBreaking, model.QRisk, model.QCovered}

// UnsureQuestions returns the unit-scope questions Unsure, MinConfidence
// and ItemAgg.Unsure read (unsureQuestions), so a share of unsure labels
// can divide by the same labels.
func UnsureQuestions() []string { return append([]string(nil), unsureQuestions[:]...) }

// confidence returns the lowest decision confidence (decisionConfidence)
// over the unit's unsureQuestions labels (an unknown answer counts as 0)
// and whether the unit is unsure (lowest below unsureBelow). A unit with
// none of these labels is unsure with confidence 0, like a unit without
// labels.
func confidence(ul *unitLabels, unsureBelow float64) (float64, bool) {
	lowest, found := 1.0, false
	for _, q := range unsureQuestions {
		if l, ok := ul.get(q); ok {
			lowest, found = min(lowest, decisionConfidence(l)), true
		}
	}
	if !found {
		return 0, true
	}
	return lowest, lowest < unsureBelow
}

// riskThresholds are the risk rungs aggregate and routing decide on: risky
// (≥ 3: test gap, risk band MED) and high (≥ 4: risk_any, band HIGH, the
// built-in deep rule).
var riskThresholds = [...]int{3, 4}

// decisionConfidence is how sure a label is about the decision prt takes
// from it, which for a multi-option label is not the top option's
// probability:
//
//   - depth decides attention, so it is the probability mass of the chosen
//     answer's attention bucket (read + run locally for eyes; skim; skip):
//     {read .49, run locally .11, skim .38} is .60 sure the unit needs eyes;
//   - risk decides at rungs 3 and 4, so it is the smaller of the
//     probabilities that the risk is on the chosen rung's side of each:
//     {2 .36, 3 .55, 4 .08} is .63 sure it is risky and .92 sure it is not
//     high;
//   - every other label (booleans, kind) keeps its Confidence.
//
// Labels without Probs (overrides, hand-made labels) keep their Confidence.
func decisionConfidence(l model.Label) float64 {
	if model.IsUnknown(l) || len(l.Probs) == 0 {
		return l.Confidence
	}
	switch l.Question {
	case model.QDepth:
		return depthBucketMass(l)
	case model.QRisk:
		return riskSideConfidence(l)
	}
	return l.Confidence
}

// depthBucketMass is the probability of the attention bucket of the depth
// answer.
func depthBucketMass(l model.Label) float64 {
	bucket := func(v string) model.Attention {
		switch v {
		case model.DepthSkim:
			return model.AttnSkim
		case model.DepthSkip:
			return model.AttnSkip
		}
		return model.AttnEyes
	}
	want := bucket(l.Value)
	mass := 0.0
	for v, p := range l.Probs {
		if bucket(v) == want {
			mass += p
		}
	}
	return clamp01(mass)
}

// riskSideConfidence is the smallest, over riskThresholds, probability
// that the risk is on the same side of the threshold as the chosen rung.
func riskSideConfidence(l model.Label) float64 {
	rung := model.ScoreLevel(l)
	conf := 1.0
	for _, t := range riskThresholds {
		above := 0.0
		for r, p := range l.Probs {
			if n, err := strconv.Atoi(r); err == nil && n >= t {
				above += p
			}
		}
		side := above
		if rung < t {
			side = 1 - above
		}
		conf = min(conf, clamp01(side))
	}
	return conf
}

// kindTest is the kind label value of tests.
const kindTest = "test"

// isTestUnit reports whether a unit is test code: a test file by path
// (isTestPath) or a unit Jev labels kind test.
func isTestUnit(u model.ChangeUnit, kind string) bool {
	return kind == kindTest || isTestPath(u.Path)
}

// noiseReason returns why a unit is noise, or "" when it is signal.
// Precedence: moved, formatting (kind or parse), generated, fmt-only lines.
func noiseReason(u model.ChangeUnit, kind string, ul *unitLabels) string {
	switch {
	case u.Kind == model.UnitMoved:
		return model.NoiseMoved
	case kind == model.KindFormatting || u.Format:
		return model.NoiseFormatting
	case kind == model.KindGenerated:
		return model.NoiseGenerated
	case allAddedFmt(u, ul):
		return model.NoiseFmtLines
	}
	return ""
}

// allAddedFmt reports whether the unit adds lines and every added line is
// labelled fmt. Added line numbers come from the unit's diff; when the diff
// does not yield them, the count of fmt lines must cover Adds.
func allAddedFmt(u model.ChangeUnit, ul *unitLabels) bool {
	if u.Adds <= 0 || ul == nil {
		return false
	}
	added := walkDiff(u).added
	if len(added) == 0 {
		return len(ul.linesYes(model.QFmt)) >= u.Adds
	}
	for _, n := range added {
		if _, ok := ul.lineYes(n, model.QFmt); !ok {
			return false
		}
	}
	return true
}

// attention maps the depth label to an attention level (spec: read and
// run locally are eyes, skim is skim, skip is skip), with these M5.2 rules:
//
//   - noise (moved, formatting, generated, fmt-only lines) is skip;
//   - an unknown or missing depth needs eyes only when the unit is risky
//     (risk rung ≥ 3) or changes behaviour, else skim; with neither answer
//     known (Jev unreachable) it needs eyes;
//   - test units (test files, or kind test) cap at skim: tests are the
//     evidence for the code they cover, and gaps in them surface as hot
//     spots and the test gap on that code;
//   - an unsure unit is never skip (spec).
func attention(depth string, a model.UnitAgg, test bool) model.Attention {
	var at model.Attention
	switch {
	case a.Noise:
		at = model.AttnSkip
	case depth == model.DepthSkim:
		at = model.AttnSkim
	case depth == model.DepthSkip:
		at = model.AttnSkip
	case depth == model.DepthRead || depth == model.DepthRun:
		at = model.AttnEyes
	default: // unknown or missing
		at = unknownDepthAttention(a)
	}
	if test && at == model.AttnEyes {
		at = model.AttnSkim
	}
	if a.Unsure && at == model.AttnSkip {
		at = model.AttnSkim
	}
	return at
}

// unknownDepthAttention is the attention of a unit whose depth is unknown:
// eyes when it is risky (risk rung ≥ 3) or changes behaviour, eyes when
// neither is known either, else skim.
func unknownDepthAttention(a model.UnitAgg) model.Attention {
	switch {
	case a.RiskLevel >= 3 || a.Behaviour:
		return model.AttnEyes
	case a.RiskLevel == 0 && a.BehaviourUnknown:
		return model.AttnEyes
	}
	return model.AttnSkim
}

// minutes is the reading time of a unit: eyes 0.5/line with a 1-minute
// floor, skim 0.1/line, skip 0. Units with no effective lines (noise,
// mechanical repeats) take no time.
func minutes(at model.Attention, effective int) float64 {
	if effective <= 0 {
		return 0
	}
	switch at {
	case model.AttnEyes:
		return max(eyesMinutesPerLine*float64(effective), eyesMinMinutes)
	case model.AttnSkim:
		return skimMinutesPerLine * float64(effective)
	}
	return 0
}

// lineMarks returns one gutter mark per labelled line, ascending.
func lineMarks(ul *unitLabels) []model.LineMark {
	nums := ul.lineNumbers()
	if len(nums) == 0 {
		return nil
	}
	out := make([]model.LineMark, 0, len(nums))
	for _, n := range nums {
		out = append(out, lineMark(ul, n))
	}
	return out
}

// lineMark applies the precedence suspicious (rung >= 3) > value tweak >
// leftover > none; Fmt is independent of the mark.
func lineMark(ul *unitLabels, n int) model.LineMark {
	m := model.LineMark{Line: n, Mark: model.MarkNone}
	_, m.Fmt = ul.lineYes(n, model.QFmt)
	if s, ok := ul.lines[n][model.QSuspicious]; ok && !model.IsUnknown(s) && model.ScoreLevel(s) >= suspiciousMarkRung {
		m.Mark, m.Score, m.P = model.MarkSuspicious, s.Score, s.P
		return m
	}
	if l, ok := ul.lineYes(n, model.QValueTweak); ok {
		m.Mark, m.P = model.MarkValue, l.P
		return m
	}
	if l, ok := ul.lineYes(n, model.QLeftover); ok {
		m.Mark, m.P = model.MarkLeftover, l.P
	}
	return m
}

// diffPos is what walkDiff learns from a unit's diff body.
type diffPos struct {
	added    []int // new-side numbers of added lines
	firstNew int   // first added line, 0 if none
	firstOld int   // first deleted line (old side), 0 if none
}

// walkDiff numbers a unit's diff lines from its range starts. Hunk headers
// and "\ No newline" markers are skipped; anything else that is not "+" or
// "-" counts as context.
func walkDiff(u model.ChangeUnit) diffPos {
	var p diffPos
	if u.Diff == "" {
		return p
	}
	oldN, newN := u.Range.Old.Start, u.Range.New.Start
	for _, ln := range strings.Split(strings.TrimSuffix(u.Diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(ln, "@@"), strings.HasPrefix(ln, `\`):
			continue
		case strings.HasPrefix(ln, "+"):
			p.added = append(p.added, newN)
			if p.firstNew == 0 {
				p.firstNew = newN
			}
			newN++
		case strings.HasPrefix(ln, "-"):
			if p.firstOld == 0 {
				p.firstOld = oldN
			}
			oldN++
		default:
			oldN++
			newN++
		}
	}
	return p
}

// firstLine is the line a unit is anchored at: its first added line, else
// its first deleted line (old side), else its range start.
func firstLine(u model.ChangeUnit) int {
	p := walkDiff(u)
	switch {
	case p.firstNew > 0:
		return p.firstNew
	case p.firstOld > 0:
		return p.firstOld
	case u.Adds == 0 && u.Dels > 0:
		return u.Range.Old.Start
	}
	return u.Range.New.Start
}
