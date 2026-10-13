package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// Overview block titles, top to bottom (spec: Overview page, Layout).
const (
	blockComposition = "Composition"
	blockHeadlines   = "Headlines"
	blockCheck       = "Check before approving"
	blockRisk        = "Risk"
	blockFidelity    = "Description fidelity"
	blockMinimap     = "Minimap"
	blockSlice       = "Your slice"
	blockSince       = "Since you looked"
	blockConfidence  = "Confidence"
)

// labelBlocks are the blocks drawn from labels (one dim line each when the
// aggregate is missing or labels are unavailable).
var labelBlocks = []string{blockHeadlines, blockCheck, blockRisk, blockFidelity, blockMinimap, blockSlice}

// page accumulates the Overview's lines and, per line, the unit behind it.
type page struct {
	st      styles
	width   int
	lines   []string
	anchors []string
	// cells maps a line index to its one-cell-per-unit run (minimap rows),
	// so a click on a cell opens that cell's unit, not the row anchor.
	cells map[int]cellRow
}

// cellRow is a run of single-cell glyphs starting at display column col,
// one per unit id.
type cellRow struct {
	col int
	ids []string
}

// at returns the unit under display column x, if any.
func (c cellRow) at(x int) (string, bool) {
	i := x - c.col
	if i < 0 || i >= len(c.ids) {
		return "", false
	}
	return c.ids[i], true
}

// add appends one row (truncated to the page width) anchored to unit ("" for
// none).
func (p *page) add(s, unit string) {
	p.lines = append(p.lines, truncate(s, p.width))
	p.anchors = append(p.anchors, unit)
}

// addFit appends left+mid+right, shortening only mid so the location and
// probability at the row's end stay visible on narrow panes.
func (p *page) addFit(left, mid, right, unit string) {
	room := p.width - lipgloss.Width(left) - lipgloss.Width(right)
	p.add(left+truncate(mid, max(1, room))+right, unit)
}

// dim appends a dim row (empty-block lines, notes).
func (p *page) dim(s string) { p.add(p.st.dim.Render(s), "") }

// block starts a section: a blank line, then the bold title.
func (p *page) block(title string) {
	p.add("", "")
	p.add(p.st.section.Render(title), "")
}

// text appends multi-line text without anchors (the description).
func (p *page) text(s string) {
	for _, l := range strings.Split(s, "\n") {
		p.lines = append(p.lines, l)
		p.anchors = append(p.anchors, "")
	}
}

// prob is a probability in dim text (".86").
func (p *page) prob(pr float64) string { return p.st.dim.Render(codeview.ProbText(pr)) }

// gutter is the two-cell row prefix: "? " for a claim below the unsure
// threshold (the row is then marked unsure), else blank.
func gutter(pr float64) string {
	if u := codeview.UnsurePrefix(pr); u != "" {
		return u + " "
	}
	return "  "
}

// overviewText renders the Overview for d at width cells and returns the
// page and its per-line anchors (unit ids). Every number comes from d.Agg;
// the page only formats and sizes bars.
func overviewText(st styles, md *markdown, d proto.Detail, now time.Time, width int) (string, []string) {
	p := overviewPage(st, md, d, now, width)
	return strings.Join(p.lines, "\n"), p.anchors
}

// overviewPage renders the Overview as a page (lines, anchors, cell runs).
func overviewPage(st styles, md *markdown, d proto.Detail, now time.Time, width int) *page {
	p := &page{st: st, width: max(10, width), cells: map[int]cellRow{}}
	p.add(st.title.Render(titleLine(d.Item, now)), "")
	tldrBlock(p, d)
	switch {
	case d.Agg == nil:
		pendingBlocks(p, d.Item)
	case d.Agg.LabelsMissing:
		gitFactsBlocks(p, d)
	default:
		labelledBlocks(p, d)
	}
	p.block("Description")
	if strings.TrimSpace(d.Item.Body) == "" {
		p.dim("no description")
	} else {
		p.text(md.render(d.Item.Body, width))
	}
	return p
}

// pendingBlocks draws every block as a dim one-liner while no aggregate
// exists (analysis running, or failed).
func pendingBlocks(p *page, it model.Item) {
	note := "analysing…"
	if it.State == model.StateError {
		note = "not analysed"
		if it.StateNote != "" {
			note += " — " + it.StateNote
		}
	}
	for _, b := range []string{blockComposition, blockHeadlines, blockCheck, blockRisk, blockFidelity,
		blockMinimap, blockSlice, blockSince, blockConfidence} {
		p.block(b)
		p.dim(note)
	}
}

// gitFactsBlocks is the page when Jev was unreachable: line counts and the
// file list; label-driven blocks are dim one-liners.
func gitFactsBlocks(p *page, d proto.Detail) {
	p.add("labels unavailable (Jev unreachable) — showing git facts only", "")
	files := statsFromDetail(d.Files)
	a, r := totals(d, files)
	p.block(blockComposition)
	p.dim("composition needs labels")
	p.add(fmt.Sprintf("+%d −%d · %s · %s", a, r, plural(len(files), "file"), plural(d.Agg.UnitsTotal, "chunk")), "")
	p.block("Files")
	if len(files) == 0 {
		p.dim("no files")
	}
	for _, f := range files {
		p.add(fmt.Sprintf("  %s  %s", padLeft(fmt.Sprintf("+%d −%d", f.adds, f.dels), 11), f.path), "")
	}
	for _, b := range labelBlocks {
		p.block(b)
		p.dim("needs labels")
	}
	sinceBlock(p, d, newUnitIndex(d))
	p.block(blockConfidence)
	p.dim("needs labels")
}

// unitIndex looks units up by id for anchors and labels.
type unitIndex struct {
	agg   map[string]model.UnitAgg
	units map[string]model.ChangeUnit
	order []model.UnitAgg
}

func newUnitIndex(d proto.Detail) unitIndex {
	ix := unitIndex{agg: map[string]model.UnitAgg{}, units: map[string]model.ChangeUnit{}, order: d.Agg.Units}
	for _, u := range d.Agg.Units {
		ix.agg[u.UnitID] = u
	}
	for _, u := range d.Units {
		ix.units[u.ID] = u
	}
	return ix
}

// first returns the id of the first unit (in aggregate order) matching ok.
func (ix unitIndex) first(ok func(model.UnitAgg) bool) string {
	for _, u := range ix.order {
		if ok(u) {
			return u.UnitID
		}
	}
	return ""
}

// where renders a unit as "path:line symbol".
func (ix unitIndex) where(id string) string {
	if u, ok := ix.units[id]; ok {
		s := fmt.Sprintf("%s:%d", u.Path, u.Range.New.Start)
		if u.Symbol != "" {
			s += " " + u.Symbol
		}
		return s
	}
	if u, ok := ix.agg[id]; ok && u.Path != "" {
		return u.Path
	}
	return id
}

// labelledBlocks draws the full label-driven page (spec blocks 2–10).
func labelledBlocks(p *page, d proto.Detail) {
	a := d.Agg
	ix := newUnitIndex(d)
	compositionBlock(p, a, ix)
	headlinesBlock(p, a)
	checkBlock(p, a)
	riskBlock(p, a, ix)
	fidelityBlock(p, a, ix)
	minimapBlock(p, a, ix)
	sliceBlock(p, a)
	sinceBlock(p, d, ix)
	confidenceBlock(p, a)
}

func compositionBlock(p *page, a *model.ItemAgg, ix unitIndex) {
	p.block(blockComposition)
	first := ""
	if len(a.Files) > 0 && len(a.Files[0].UnitIDs) > 0 {
		first = a.Files[0].UnitIDs[0]
	}
	segs := compositionSegments(a)
	if len(segs) == 0 {
		p.dim("no changed lines")
	} else {
		p.add(compositionBar(segs, min(40, p.width-2)), first)
		p.add(compositionLegend(segs), first)
	}
	p.add(fmt.Sprintf("+%d −%d → ~%d lines that matter · ~%d min · %d of %d chunks need eyes",
		a.Added, a.Removed, a.EffectiveLines, roundMinutes(a.Minutes), a.UnitsEyes, a.UnitsTotal),
		ix.first(func(u model.UnitAgg) bool { return u.Attention == model.AttnEyes }))
}

func roundMinutes(m float64) int {
	if m <= 0 {
		return 0
	}
	return max(1, int(m+0.5))
}

// segment is one part of the composition bar.
type segment struct {
	name  string
	share float64 // of all changed lines, 0..1
	glyph string
}

// noiseGlyph draws the noise segment; kinds are drawn with a letter.
const noiseGlyph = "░"

// compositionSegments scales Composition (shares of effective lines) by the
// signal share and adds a noise segment from NoiseLines, largest first,
// ties by name, each with a distinct glyph.
func compositionSegments(a *model.ItemAgg) []segment {
	total := a.EffectiveLines + a.NoiseLines
	signal := 1.0
	if total > 0 {
		signal = float64(a.EffectiveLines) / float64(total)
	}
	var segs []segment
	for k, v := range a.Composition {
		if v > 0 {
			segs = append(segs, segment{name: k, share: v * signal})
		}
	}
	if a.NoiseLines > 0 && total > 0 {
		segs = append(segs, segment{name: "noise", share: float64(a.NoiseLines) / float64(total), glyph: noiseGlyph})
	}
	sort.Slice(segs, func(i, j int) bool {
		if segs[i].share != segs[j].share {
			return segs[i].share > segs[j].share
		}
		return segs[i].name < segs[j].name
	})
	assignGlyphs(segs)
	return segs
}

// assignGlyphs gives each kind the first letter of its name not taken yet
// (then any letter of the name, then a digit), so the bar reads without
// colour.
func assignGlyphs(segs []segment) {
	used := map[string]bool{noiseGlyph: true}
	for i := range segs {
		if segs[i].glyph != "" {
			continue
		}
		for _, r := range strings.ToLower(segs[i].name) + "123456789" {
			g := string(r)
			if (unicode.IsLetter(r) || unicode.IsDigit(r)) && !used[g] {
				segs[i].glyph, used[g] = g, true
				break
			}
		}
		if segs[i].glyph == "" {
			segs[i].glyph = "?"
		}
	}
}

// compositionBar draws the segments over width cells (largest remainder,
// at least one cell per segment).
func compositionBar(segs []segment, width int) string {
	width = max(len(segs), width)
	shares := make([]float64, len(segs))
	for i, s := range segs {
		shares[i] = s.share
	}
	var b strings.Builder
	for i, n := range cellWidths(shares, width) {
		b.WriteString(strings.Repeat(segs[i].glyph, n))
	}
	return b.String()
}

// cellWidths splits total cells by share: floors, then the remainder to the
// largest fractional parts; every share gets at least one cell, taken back
// from the widest segment when that overflows.
func cellWidths(shares []float64, total int) []int {
	var sum float64
	for _, s := range shares {
		sum += s
	}
	out := make([]int, len(shares))
	if sum <= 0 || len(shares) == 0 {
		return out
	}
	used := 0
	frac := make([]float64, len(shares))
	for i, s := range shares {
		exact := s / sum * float64(total)
		out[i] = int(exact)
		frac[i] = exact - float64(out[i])
		used += out[i]
	}
	idx := make([]int, len(shares))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return frac[idx[a]] > frac[idx[b]] })
	for k := 0; used < total; k = (k + 1) % len(idx) {
		out[idx[k]]++
		used++
	}
	for i := range out {
		if out[i] > 0 {
			continue
		}
		out[i] = 1
		widest := 0
		for j := range out {
			if out[j] > out[widest] {
				widest = j
			}
		}
		if out[widest] > 1 {
			out[widest]--
		}
	}
	return out
}

func compositionLegend(segs []segment) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		parts = append(parts, fmt.Sprintf("%s %s %d%%", s.glyph, s.name, int(s.share*100+0.5)))
	}
	return strings.Join(parts, " · ")
}

func headlinesBlock(p *page, a *model.ItemAgg) {
	p.block(blockHeadlines)
	if len(a.Headlines) == 0 {
		p.dim("no contract changes")
		return
	}
	kw := 0
	for _, h := range a.Headlines {
		kw = max(kw, len([]rune(h.Kind)))
	}
	for _, h := range a.Headlines {
		// Units without a symbol or signature get Text = path, which the
		// location column already shows.
		text := h.Text
		if text == h.Path {
			text = "(top level)"
		}
		p.addFit(gutter(h.P)+padRight(h.Kind, kw)+"  ", text, fmt.Sprintf("  %s:%d  %s", h.Path, h.Line, p.prob(h.P)), h.UnitID)
	}
}

func checkBlock(p *page, a *model.ItemAgg) {
	p.block(blockCheck)
	if len(a.HotSpots) == 0 {
		p.dim("nothing flagged")
		return
	}
	for _, h := range a.HotSpots[:min(3, len(a.HotSpots))] {
		p.addFit(fmt.Sprintf("  %s:%d  ", h.Path, h.Line), h.Reason, "  "+p.st.dim.Render(strconv.FormatFloat(h.Score, 'f', 1, 64)), h.UnitID)
	}
}

// riskWord is the spec's risk headline: HIGH ≥ 4, MED 3, LOW ≤ 2.
func riskWord(r float64) string {
	switch {
	case r >= 4:
		return "HIGH"
	case r >= 3:
		return "MED"
	}
	return "LOW"
}

func riskBlock(p *page, a *model.ItemAgg, ix unitIndex) {
	p.block(blockRisk)
	if a.RiskMax <= 0 {
		p.dim("risk unknown")
	} else {
		top := ix.first(func(u model.UnitAgg) bool { return u.Risk >= a.RiskMax || float64(u.RiskLevel) >= a.RiskMax })
		p.add(fmt.Sprintf("Risk %s (%s)", riskWord(a.RiskMax), strconv.FormatFloat(a.RiskMax, 'f', -1, 64)), top)
	}
	if chips := riskChips(a.RiskTags); chips != "" {
		p.add("  "+chips, ix.first(func(u model.UnitAgg) bool { return len(u.RiskTags) > 0 }))
	}
	if a.RiskyUnits == 0 {
		p.dim("  no risky chunks")
		return
	}
	p.add(fmt.Sprintf("  %d of %d risky chunks untested", a.TestGap, a.RiskyUnits),
		ix.first(func(u model.UnitAgg) bool { return u.Untested }))
}

// riskChips renders tag counts as "auth ×2  data write ×1", by count
// descending, then name.
func riskChips(tags map[string]int) string {
	type chip struct {
		name string
		n    int
	}
	var cs []chip
	for k, n := range tags {
		if n > 0 {
			cs = append(cs, chip{strings.ReplaceAll(k, "_", " "), n})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].n != cs[j].n {
			return cs[i].n > cs[j].n
		}
		return cs[i].name < cs[j].name
	})
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, fmt.Sprintf("%s ×%d", c.name, c.n))
	}
	return strings.Join(parts, "  ")
}

func fidelityBlock(p *page, a *model.ItemAgg, ix unitIndex) {
	p.block(blockFidelity)
	if len(a.Bullets) == 0 {
		p.dim("no description bullets")
	}
	for _, b := range a.Bullets {
		unit := ""
		if len(b.UnitIDs) > 0 {
			unit = b.UnitIDs[0]
		}
		if !b.Implemented {
			p.add("  ✗ not found    "+b.Text, unit)
			continue
		}
		right := ""
		if b.P > 0 {
			right = "  " + p.prob(b.P)
		}
		p.addFit(gutter(b.P)+"✓ implemented  ", b.Text, right, unit)
	}
	for _, id := range a.Undocumented {
		p.add("  undocumented   "+ix.where(id), id)
	}
	if a.Shape != "" {
		p.add(fmt.Sprintf("%scoherence: %s  %s", gutter(a.ShapeP), a.Shape, p.prob(a.ShapeP)), "")
	}
}

func minimapBlock(p *page, a *model.ItemAgg, ix unitIndex) {
	p.block(blockMinimap)
	if len(a.Files) == 0 {
		p.dim("no units")
		return
	}
	pw := 0
	owned := false
	for _, f := range a.Files {
		pw = max(pw, len([]rune(f.Path)))
		owned = owned || f.Owned
	}
	pw = min(pw, max(8, p.width/2))
	divided := false
	for _, f := range a.Files {
		if owned && !f.Owned && !divided {
			p.dim("  ── not yours ──")
			divided = true
		}
		var cells strings.Builder
		for _, id := range f.UnitIDs {
			u := ix.agg[id]
			cells.WriteString(codeview.AttentionGlyph(u.Attention, u.Unsure))
		}
		unit := ""
		if len(f.UnitIDs) > 0 {
			unit = f.UnitIDs[0]
		}
		p.add("  "+padRight(truncate(f.Path, pw), pw)+"  "+cells.String(), unit)
		if col := 2 + pw + 2; col < p.width {
			// Cells cut off by add (the last visible cell then shows "…")
			// stay out of reach; a click there falls back to the row anchor.
			n := len(f.UnitIDs)
			if col+n > p.width {
				n = p.width - col - 1
			}
			p.cells[len(p.lines)-1] = cellRow{col: col, ids: f.UnitIDs[:max(0, n)]}
		}
	}
	p.dim("  █ eyes ▒ skim · skip □ unsure")
}

func sliceBlock(p *page, a *model.ItemAgg) {
	p.block(blockSlice)
	if len(a.Slice) == 0 {
		p.dim("no modules")
		return
	}
	for _, s := range a.Slice {
		if s.Owned {
			p.add(fmt.Sprintf("  %s %s · %s", s.Module, plural(s.Units, "chunk"), needEyes(s.Eyes)), "")
			continue
		}
		p.add(fmt.Sprintf("  %s is %s — skip unless curious", s.Module, plural(s.Units, "chunk")), "")
	}
}

// sinceMax is how many changed units "Since you looked" lists.
const sinceMax = 5

// sinceBlock draws "Since you looked" (spec block 9) from Agg.Since: the
// units changed since the base head and, per earlier comment of the
// viewer's, whether the latest push addressed it. The page only formats.
func sinceBlock(p *page, d proto.Detail, ix unitIndex) {
	p.block(blockSince)
	if d.Item.Kind == model.KindBranch {
		p.dim("no reviews to compare on a branch")
		return
	}
	s := d.Agg.Since
	if s == nil {
		p.dim("nothing to compare yet (first look)")
		return
	}
	first := ""
	if len(s.ChangedUnits) > 0 {
		first = s.ChangedUnits[0]
	}
	p.add(fmt.Sprintf("since %s (%s) · %s changed", shortSHA(s.BaseSHA), sinceSource(s.Source),
		plural(len(s.ChangedUnits), "unit")), first)
	if s.Note != "" {
		p.dim("  " + s.Note)
	}
	for _, id := range s.ChangedUnits[:min(sinceMax, len(s.ChangedUnits))] {
		p.add("  "+ix.loc(id), id)
	}
	if n := len(s.ChangedUnits) - sinceMax; n > 0 {
		p.dim(fmt.Sprintf("  … %d more", n))
	}
	if len(s.Comments) == 0 {
		p.dim("  no earlier comments of yours")
		return
	}
	for _, c := range s.Comments {
		addressedRow(p, c)
	}
}

// addressedRow is one earlier comment: "✓ addressed .91", "✗ not addressed
// .12" or "? unknown", then path:line and the excerpt. P is the probability
// that it was addressed; the row is unsure ("?" prefix) when neither answer
// reaches .60.
func addressedRow(p *page, c model.AddressedComment) {
	var verdict string
	switch {
	case !c.Known:
		verdict = "  ? unknown"
	case c.Addressed:
		verdict = gutter(max(c.P, 1-c.P)) + "✓ addressed " + p.prob(c.P)
	default:
		verdict = gutter(max(c.P, 1-c.P)) + "✗ not addressed " + p.prob(c.P)
	}
	right := ""
	if c.Resolved {
		right = "  " + p.st.dim.Render("resolved")
	}
	p.addFit(fmt.Sprintf("%s  %s:%d  ", verdict, c.Path, c.Line), c.Excerpt, right, c.UnitID)
}

// sinceSource names what the base head is.
func sinceSource(src string) string {
	if src == model.SinceReviewed {
		return "your last review"
	}
	return "you looked"
}

// shortSHA is the first seven characters of a commit id.
func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return orDash(s)
}

// loc renders a unit as "path:line" (new side), falling back to where.
func (ix unitIndex) loc(id string) string {
	if u, ok := ix.units[id]; ok {
		return fmt.Sprintf("%s:%d", u.Path, u.Range.New.Start)
	}
	return ix.where(id)
}

func confidenceBlock(p *page, a *model.ItemAgg) {
	p.block(blockConfidence)
	if a.Unsure == 0 {
		p.dim("all labels confident")
		return
	}
	unit := ""
	if len(a.UnsureUnits) > 0 {
		unit = a.UnsureUnits[0]
	}
	p.add(fmt.Sprintf("%s; those units stay expanded in Code", plural(a.Unsure, "low-confidence label")), unit)
}

// rawLabelsFor renders the `L` overlay for unit id of d.
func rawLabelsFor(d proto.Detail, id string, width int) string {
	u := model.ChangeUnit{ID: id}
	found := false
	for _, cu := range d.Units {
		if cu.ID == id {
			u, found = cu, true
			break
		}
	}
	if !found && d.Agg != nil {
		for _, ua := range d.Agg.Units {
			if ua.UnitID == id {
				u.Path = ua.Path
				break
			}
		}
	}
	return codeview.RawLabelsText(u, d.Labels, width)
}
