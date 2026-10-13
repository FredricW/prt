package codeview

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/theme"
)

// UnsureBelow is the default threshold under which a claim is prefixed "?"
// (spec: Overview rendering rules; config pipeline.unsure_below).
const UnsureBelow = 0.60

// ProbText formats a probability the spec's way: two decimals without the
// leading zero (".86"), "1.0" for 1. The caller renders it dim.
func ProbText(p float64) string {
	switch {
	case p >= 0.995:
		return "1.0"
	case p < 0:
		p = 0
	}
	return strings.TrimPrefix(fmt.Sprintf("%.2f", p), "0")
}

// UnsurePrefix returns "?" when p is below UnsureBelow, else "".
func UnsurePrefix(p float64) string {
	if p < UnsureBelow {
		return "?"
	}
	return ""
}

// MarkGlyph maps a gutter mark to its glyph: ◆ suspicious, ~ value tweak,
// ⋯ leftover, · nothing.
func MarkGlyph(m model.Mark) string {
	switch m {
	case model.MarkSuspicious:
		return "◆"
	case model.MarkValue:
		return "~"
	case model.MarkLeftover:
		return "⋯"
	}
	return "·"
}

// AttentionGlyph is one minimap cell, readable without colour: █ eyes,
// ▒ skim, · skip; an unsure unit is drawn outlined (□ for eyes/skim, ◌ for
// skip) so low confidence shows in any theme.
func AttentionGlyph(a model.Attention, unsure bool) string {
	switch {
	case unsure && a == model.AttnSkip:
		return "◌"
	case unsure:
		return "□"
	case a == model.AttnEyes:
		return "█"
	case a == model.AttnSkim:
		return "▒"
	}
	return "·"
}

// RawLabelsText renders the `L` overlay body for one unit: unit-scope labels
// sorted by question, then line labels grouped by line, one per row as
// "unit  question  value  p .86  conf .91  model" ("L12   " instead of
// "unit  " for line labels, so the columns align; overrides marked "!").
// The tui overview reuses it.
func RawLabelsText(u model.ChangeUnit, labels []model.Label, width int) string {
	var unit, lines []model.Label
	added := model.AddedLineNumbers(u)
	for _, l := range labels {
		if l.UnitID != u.ID {
			continue
		}
		if l.Line > 0 {
			// Stored line labels hold the position among the unit's added
			// lines; show the new-side line number.
			if len(added) > 0 {
				if l.Line > len(added) {
					continue
				}
				l.Line = added[l.Line-1]
			}
			lines = append(lines, l)
		} else {
			unit = append(unit, l)
		}
	}
	sort.SliceStable(unit, func(i, j int) bool { return unit[i].Question < unit[j].Question })
	sort.SliceStable(lines, func(i, j int) bool {
		if lines[i].Line != lines[j].Line {
			return lines[i].Line < lines[j].Line
		}
		return lines[i].Question < lines[j].Question
	})
	var b strings.Builder
	title := u.Path
	if u.Symbol != "" {
		title += " · " + u.Symbol
	}
	b.WriteString(clipText(title, width) + "\n")
	if len(unit)+len(lines) == 0 {
		b.WriteString("no labels yet\n")
		return b.String()
	}
	row := func(prefix string, l model.Label) {
		v := l.Value
		if l.Overridden != nil {
			v = *l.Overridden + " ! (was " + l.Value + ")"
		}
		s := fmt.Sprintf("%s%-22s %-18s p %s  conf %s  %s", prefix, l.Question, v, ProbText(l.P), ProbText(l.Confidence), l.Model)
		b.WriteString(clipText(strings.TrimRight(s, " "), width) + "\n")
	}
	for _, l := range unit {
		row("unit  ", l)
	}
	for _, l := range lines {
		row(fmt.Sprintf("L%-4d ", l.Line), l)
	}
	return b.String()
}

// chip is one label chip of a unit or group header: text readable without
// colour, an optional probability, and its theme kind (warm for risk and
// breaking, red for missing tests, outlined blue for unsure, blue for
// routing).
type chip struct {
	text string
	prob string
	kind theme.ChipKind
}

// label is the chip's plain text: text, then the probability.
func (c chip) label() string {
	if c.prob == "" {
		return c.text
	}
	return c.text + " " + c.prob
}

// unitChips builds a unit's header chips in spec order: git kind (or
// "moved"), Jev kind, behaviour ✓/✗, breaking, description bullet or
// "undocumented", tests ✓/✗, risk tags, "risk N", attention verdict and
// "?" when unsure. Without labels (a nil aggregate, or labelled false)
// only facts that do not come from Jev are shown.
func unitChips(u *model.ChangeUnit, a *model.UnitAgg, labelled bool) []chip {
	cs := []chip{{text: string(u.Kind)}}
	if u.Kind == "" {
		cs[0].text = "changed"
	}
	if a == nil {
		return cs
	}
	if a.Noise && a.NoiseReason != "" && a.NoiseReason != model.NoiseMoved && (!labelled || a.NoiseReason != a.Kind) {
		cs = append(cs, chip{text: a.NoiseReason})
	}
	if !labelled {
		return cs
	}
	if a.Kind != "" && a.Kind != model.LabelUnknown {
		cs = append(cs, chip{text: a.Kind})
	}
	if a.BehaviourUnknown {
		cs = append(cs, chip{text: "behaviour ?"})
	} else {
		cs = append(cs, boolChip("behaviour", a.Behaviour, a.BehaviourP))
	}
	if a.Breaking { // an unknown breaking label makes no claim: no chip
		cs = append(cs, chip{text: "⚠ " + UnsurePrefix(a.BreakingP) + "breaking", prob: ProbText(a.BreakingP),
			kind: unsureKind(a.BreakingP, theme.ChipWarn)})
	}
	switch {
	case a.DescBullet > 0:
		cs = append(cs, chip{text: model.BulletValue(a.DescBullet)})
	case a.Undocumented:
		cs = append(cs, chip{text: "undocumented"})
	}
	tests := chip{text: "tests ✓"}
	switch {
	case a.CoveredUnknown:
		tests = chip{text: "tests ?", kind: theme.ChipUnsure}
	case !a.Covered:
		tests = chip{text: "tests ✗", kind: theme.ChipBad}
	}
	cs = append(cs, tests)
	for _, t := range a.RiskTags {
		cs = append(cs, chip{text: t, kind: theme.ChipWarn})
	}
	if a.RiskLevel > 0 {
		k := theme.ChipPlain
		if a.RiskLevel >= 4 {
			k = theme.ChipWarn
		}
		cs = append(cs, chip{text: "risk " + strconv.Itoa(a.RiskLevel), kind: k})
	}
	if a.Attention != "" {
		cs = append(cs, chip{text: string(a.Attention)})
	}
	if a.Unsure {
		cs = append(cs, chip{text: "?", kind: theme.ChipUnsure})
	}
	return cs
}

// unsureKind is ChipUnsure for a claim below UnsureBelow, else k.
func unsureKind(p float64, k theme.ChipKind) theme.ChipKind {
	if p < UnsureBelow {
		return theme.ChipUnsure
	}
	return k
}

// boolChip renders a boolean claim "name ✓" / "name ✗" with the
// probability of the claim shown (P for ✓, 1−P for ✗) and "?" below
// UnsureBelow (an outlined unsure chip with colour).
func boolChip(name string, yes bool, pYes float64) chip {
	p, mark := pYes, " ✓"
	if !yes {
		p, mark = 1-pYes, " ✗"
	}
	return chip{text: UnsurePrefix(p) + name + mark, prob: ProbText(p), kind: unsureKind(p, theme.ChipPlain)}
}

func clipText(s string, width int) string {
	if width <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}
