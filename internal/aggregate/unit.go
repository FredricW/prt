package aggregate

import (
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
	a.Attention = attention(ul.value(model.QDepth), a.Noise, a.Unsure)
	a.Minutes = minutes(a.Attention, a.EffectiveLines)
	a.Untested = a.RiskLevel >= 3 && a.CoveredP < 0.5
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

// confidence returns the lowest label confidence of a unit and whether the
// unit is unsure. A unit without any label is unsure with confidence 0.
func confidence(ul *unitLabels, unsureBelow float64) (float64, bool) {
	if ul == nil || len(ul.all) == 0 {
		return 0, true
	}
	lowest := ul.all[0].Confidence
	for _, l := range ul.all[1:] {
		lowest = min(lowest, l.Confidence)
	}
	return lowest, lowest < unsureBelow
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

// attention maps the depth label to an attention level. Unknown or missing
// depth needs eyes; noise is skipped; an unsure unit is never skipped.
func attention(depth string, noise, unsure bool) model.Attention {
	var at model.Attention
	switch {
	case noise:
		at = model.AttnSkip
	case depth == model.DepthSkim:
		at = model.AttnSkim
	case depth == model.DepthSkip:
		at = model.AttnSkip
	default: // read, run locally, unknown, missing
		at = model.AttnEyes
	}
	if unsure && at == model.AttnSkip {
		at = model.AttnSkim
	}
	return at
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
