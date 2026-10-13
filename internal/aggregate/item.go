package aggregate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/fredricw/prt/internal/model"
)

// Item-level limits.
const (
	maxHotSpots      = 3
	maxReasonWords   = 5
	maxHeadlineRunes = 60
	unknownKind      = "unknown"
	rootModule       = "(root)"
)

// units computes every unit's aggregate in unit order (ChangeUnit.Order,
// ties by input position).
func units(in Input, idx labelIndex, unsureBelow float64) []unitFacts {
	sorted := append([]model.ChangeUnit(nil), in.Units...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Order < sorted[j].Order })
	owned := ownedPaths(in.Files)
	skipped := mechanicalRepeats(in.Groups)
	out := make([]unitFacts, 0, len(sorted))
	for _, u := range sorted {
		ul := idx.units[u.ID]
		a := unitAgg(u, ul, owned[u.Path], !skipped[u.ID], unsureBelow)
		out = append(out, unitFacts{unit: u, agg: a, labels: ul, line: firstLine(u)})
	}
	return out
}

func ownedPaths(files []model.File) map[string]bool {
	m := make(map[string]bool, len(files))
	for _, f := range files {
		m[f.Path] = f.Owned
	}
	return m
}

// mechanicalRepeats returns the members of mechanical groups other than the
// example (the first member when ExampleUnit is unset).
func mechanicalRepeats(groups []model.Group) map[string]bool {
	m := map[string]bool{}
	for _, g := range groups {
		if !g.Mechanical || len(g.UnitIDs) == 0 {
			continue
		}
		example := g.ExampleUnit
		if example == "" {
			example = g.UnitIDs[0]
		}
		for _, id := range g.UnitIDs {
			if id != example {
				m[id] = true
			}
		}
	}
	return m
}

// summary is the arithmetic shared by items and groups.
type summary struct {
	composition map[string]float64
	effective   int
	minutes     float64
	riskMax     int
	riskTags    map[string]int
	testGap     int
	risky       int
	breaking    []string
	unsure      int
}

func summarise(us []unitFacts, unsureBelow float64) summary {
	s := summary{composition: map[string]float64{}, riskTags: map[string]int{}, breaking: []string{}}
	byKind := map[string]int{}
	for _, f := range us {
		a := f.agg
		s.effective += a.EffectiveLines
		if a.EffectiveLines > 0 {
			byKind[compositionKind(a.Kind)] += a.EffectiveLines
		}
		s.minutes += a.Minutes
		s.riskMax = max(s.riskMax, a.RiskLevel)
		for _, t := range a.RiskTags {
			s.riskTags[t]++
		}
		if a.Untested {
			s.testGap++
		}
		if a.RiskLevel >= 3 {
			s.risky++
		}
		if a.Breaking {
			s.breaking = append(s.breaking, a.UnitID)
		}
		s.unsure += f.labels.unsureCount(unsureBelow)
	}
	if s.effective > 0 {
		for k, n := range byKind {
			s.composition[k] = float64(n) / float64(s.effective)
		}
	}
	return s
}

// compositionKind is the composition key of a kind label value.
func compositionKind(kind string) string {
	if kind == "" {
		return unknownKind
	}
	return kind
}

// riskAny is 1 − ∏(1 − p_i) over units with risk rung >= 4, where p_i is
// P(risk >= 4) from the score distribution (the label's P without one).
func riskAny(us []unitFacts) float64 {
	none := 1.0
	for _, f := range us {
		if f.agg.RiskLevel < 4 {
			continue
		}
		l, _ := f.labels.known(model.QRisk)
		p := l.P
		if len(l.Probs) > 0 {
			p = l.Probs["4"] + l.Probs["5"]
		}
		none *= 1 - clamp01(p)
	}
	return 1 - none
}

func clamp01(p float64) float64 { return min(max(p, 0), 1) }

// hotSpots ranks units by risk × relevance × (1 − covered) × min confidence
// and keeps the top three with a positive score (ties keep unit order).
func hotSpots(us []unitFacts) []model.HotSpot {
	out := []model.HotSpot{}
	for _, f := range us {
		a := f.agg
		score := a.Risk * a.Relevance * (1 - a.CoveredP) * a.MinConfidence
		if score <= 0 {
			continue
		}
		out = append(out, model.HotSpot{UnitID: a.UnitID, Path: a.Path, Line: f.line, Score: score, Reason: hotSpotReason(a)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > maxHotSpots {
		out = out[:maxHotSpots]
	}
	return out
}

// hotSpotReason names what fired: the first risk tag, the fail mode and
// "untested", falling back to the risk rung; at most five words.
func hotSpotReason(a model.UnitAgg) string {
	var parts []string
	if len(a.RiskTags) > 0 {
		parts = append(parts, tagWord(a.RiskTags[0]))
	}
	if a.FailMode != "" {
		parts = append(parts, a.FailMode)
	}
	if a.Untested {
		parts = append(parts, "untested")
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("risk %d of 5", a.RiskLevel))
	}
	words := strings.Fields(strings.Join(parts, ", "))
	if len(words) > maxReasonWords {
		words = words[:maxReasonWords]
	}
	return strings.TrimSuffix(strings.Join(words, " "), ",")
}

// tagWord renders a risk tag sub-id as one word.
func tagWord(sub string) string {
	if sub == "date_tz" {
		return "date/tz"
	}
	return strings.ReplaceAll(sub, "_", "-")
}

// Headline kinds that are not contract values.
const (
	headlineBreaking     = "breaking"
	headlineTestWeakened = "test weakened"
	headlinePII          = "pii or secret"
)

// headlineRank orders headline kinds: breaking, contract kinds, test
// weakened, pii.
func headlineRank(kind string) int {
	switch kind {
	case headlineBreaking:
		return 0
	case headlineTestWeakened:
		return 2
	case headlinePII:
		return 3
	}
	return 1
}

// headlines lists breaking units, contract changes, weakened tests and pii
// lines, sorted by kind, then P descending, then unit order.
func headlines(us []unitFacts) []model.Headline {
	out := []model.Headline{}
	for _, f := range us {
		out = append(out, unitHeadlines(f)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := headlineRank(out[i].Kind), headlineRank(out[j].Kind)
		if ri != rj {
			return ri < rj
		}
		return out[i].P > out[j].P
	})
	return out
}

func unitHeadlines(f unitFacts) []model.Headline {
	var out []model.Headline
	h := model.Headline{UnitID: f.agg.UnitID, Path: f.unit.Path, Line: f.line, Text: headlineText(f.unit)}
	add := func(kind string, l model.Label) {
		h.Kind, h.P, h.Confidence = kind, l.P, l.Confidence
		out = append(out, h)
	}
	if l, ok := f.labels.known(model.QBreaking); ok && l.P >= 0.5 {
		add(headlineBreaking, l)
	}
	if l, ok := f.labels.known(model.QContract); ok && l.Value != model.ContractNone {
		add(l.Value, l)
	}
	if l, ok := f.labels.known(model.QTestWeakened); ok && l.P >= 0.5 {
		add(headlineTestWeakened, l)
	}
	if l, ok := strongest(f.labels.linesYes(model.QPIIOrSecret)); ok {
		h.Line = l.Line
		add(headlinePII, l)
	}
	return out
}

// strongest returns the label with the highest P (earliest on ties).
func strongest(ls []model.Label) (model.Label, bool) {
	if len(ls) == 0 {
		return model.Label{}, false
	}
	best := ls[0]
	for _, l := range ls[1:] {
		if l.P > best.P {
			best = l
		}
	}
	return best, true
}

// headlineText is the unit's symbol, else signature, else path, shortened.
func headlineText(u model.ChangeUnit) string {
	t := u.Symbol
	if t == "" {
		t = u.Signature
	}
	if t == "" {
		t = u.Path
	}
	r := []rune(strings.TrimSpace(t))
	if len(r) > maxHeadlineRunes {
		return string(r[:maxHeadlineRunes-1]) + "…"
	}
	return string(r)
}

// bullets matches description bullets to the units that implement them and
// returns them with the fidelity ratio (0 without bullets).
func bullets(body string, us []unitFacts) ([]model.BulletAgg, float64) {
	texts := model.DescBullets(body)
	if len(texts) == 0 {
		return nil, 0
	}
	out := make([]model.BulletAgg, len(texts))
	implemented := 0
	for i, t := range texts {
		b := model.BulletAgg{Index: i + 1, Text: t}
		for _, f := range us {
			if f.agg.DescBullet != b.Index {
				continue
			}
			b.UnitIDs = append(b.UnitIDs, f.agg.UnitID)
			if l, ok := f.labels.known(model.QDescBullet); ok {
				b.P = max(b.P, l.P)
			}
		}
		b.Implemented = len(b.UnitIDs) > 0
		if b.Implemented {
			implemented++
		}
		out[i] = b
	}
	return out, float64(implemented) / float64(len(texts))
}

// files builds the Code page file list: every changed file (plus any path
// that only appears on a unit), units in line order, owned files first,
// then by attention descending, then by path.
func files(in []model.File, us []unitFacts) []model.FileAgg {
	byPath := map[string]*model.FileAgg{}
	var order []*model.FileAgg
	get := func(path string, owned bool) *model.FileAgg {
		if fa, ok := byPath[path]; ok {
			return fa
		}
		fa := &model.FileAgg{Path: path, Owned: owned, UnitIDs: []string{}}
		byPath[path] = fa
		order = append(order, fa)
		return fa
	}
	for _, f := range in {
		get(f.Path, f.Owned)
	}
	for _, f := range lineOrder(us) {
		fa := get(f.unit.Path, f.agg.Owned)
		fa.UnitIDs = append(fa.UnitIDs, f.agg.UnitID)
		switch f.agg.Attention {
		case model.AttnEyes:
			fa.Eyes++
		case model.AttnSkim:
			fa.Skim++
		default:
			fa.Skip++
		}
		fa.Attention = fa.Eyes
	}
	out := make([]model.FileAgg, 0, len(order))
	for _, fa := range order {
		out = append(out, *fa)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Owned != b.Owned {
			return a.Owned
		}
		if a.Attention != b.Attention {
			return a.Attention > b.Attention
		}
		return a.Path < b.Path
	})
	return out
}

// lineOrder sorts units by new-side start, then old-side start, then Order.
func lineOrder(us []unitFacts) []unitFacts {
	out := append([]unitFacts(nil), us...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].unit, out[j].unit
		if a.Range.New.Start != b.Range.New.Start {
			return a.Range.New.Start < b.Range.New.Start
		}
		if a.Range.Old.Start != b.Range.Old.Start {
			return a.Range.Old.Start < b.Range.Old.Start
		}
		return a.Order < b.Order
	})
	return out
}

// slice builds "Your slice": owned units by the first matching owned glob
// (in Owns order), owned units no glob matches and the rest by top-level
// directory ("dir/*", "(root)"), each of those by unit count descending.
func slice(owns []string, us []unitFacts) []model.SliceRow {
	rows := map[string]*model.SliceRow{}
	for _, f := range us {
		mod := moduleOf(f.agg, owns)
		key := fmt.Sprintf("%t\x00%s", f.agg.Owned, mod)
		r := rows[key]
		if r == nil {
			r = &model.SliceRow{Module: mod, Owned: f.agg.Owned}
			rows[key] = r
		}
		r.Units++
		if f.agg.Attention == model.AttnEyes {
			r.Eyes++
		}
	}
	globRank := make(map[string]int, len(owns))
	for i, g := range owns {
		if _, dup := globRank[g]; !dup {
			globRank[g] = i
		}
	}
	out := make([]model.SliceRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return sliceLess(out[i], out[j], globRank) })
	return out
}

func sliceLess(a, b model.SliceRow, globRank map[string]int) bool {
	if a.Owned != b.Owned {
		return a.Owned
	}
	ra, aGlob := globRank[a.Module]
	rb, bGlob := globRank[b.Module]
	if a.Owned && aGlob != bGlob {
		return aGlob
	}
	if a.Owned && aGlob && ra != rb {
		return ra < rb
	}
	if a.Units != b.Units {
		return a.Units > b.Units
	}
	return a.Module < b.Module
}

// moduleOf is an owned unit's first matching glob, else its top-level dir.
func moduleOf(a model.UnitAgg, owns []string) string {
	if a.Owned {
		for _, g := range owns {
			if ok, err := doublestar.Match(g, a.Path); err == nil && ok {
				return g
			}
		}
	}
	dir, _, found := strings.Cut(a.Path, "/")
	if !found {
		return rootModule
	}
	return dir + "/*"
}

// groups computes a GroupAgg per input group; unknown unit ids are ignored.
func groups(gs []model.Group, us []unitFacts, unsureBelow float64) []model.GroupAgg {
	byID := make(map[string]unitFacts, len(us))
	for _, f := range us {
		byID[f.agg.UnitID] = f
	}
	out := make([]model.GroupAgg, 0, len(gs))
	for _, g := range gs {
		members := make([]unitFacts, 0, len(g.UnitIDs))
		for _, id := range g.UnitIDs {
			if f, ok := byID[id]; ok {
				members = append(members, f)
			}
		}
		s := summarise(members, unsureBelow)
		out = append(out, model.GroupAgg{
			GroupID: g.ID, Composition: s.composition, EffectiveLines: s.effective,
			Minutes: s.minutes, RiskMax: float64(s.riskMax), RiskTags: s.riskTags,
			TestGap: s.testGap, Breaking: s.breaking, Unsure: s.unsure,
		})
	}
	return out
}
