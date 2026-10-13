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
	"github.com/fredricw/prt/internal/theme"
)

// Overview box titles (spec: Overview page, Layout; design v2/Overview).
const (
	blockHeadlines   = "Headlines · contract surface"
	blockCheck       = "Check before approving · hot spots"
	blockRisk        = "Risk"
	blockFidelity    = "Description fidelity"
	blockMinimap     = "Minimap · attention per chunk"
	blockSlice       = "Your slice"
	blockSince       = "Since you looked"
	blockBranch      = "Branch state"
	blockFiles       = "Files"
	blockDescription = "Description"
)

// twoColumnMin is the narrowest page that lays the boxes out in two
// columns (design: a 2-column grid); narrower pages stack them.
const twoColumnMin = 100

// colGap is the gap between the two columns.
const colGap = 2

// branchCommitsMax is how many unpushed commit subjects the branch block
// lists.
const branchCommitsMax = 5

// span is a column range [col, end) of one line that belongs to a unit, so
// a click opens the unit of the box it lands in.
type span struct {
	col, end int
	unit     string
}

// page accumulates the Overview's lines and, per line, the units behind
// it. A page is also a fragment: boxes are rendered as pages and placed
// into the parent with their spans, cell runs and box titles shifted.
type page struct {
	st    styles
	width int
	lines []string
	// anchors holds, per line, the first unit on it (left column first):
	// enter and L take the first anchored line at or after the top.
	anchors []string
	// spans are the clickable unit ranges per line; cells the one-cell-per-
	// unit runs (minimap rows), so a click on a cell opens that cell's unit.
	spans map[int][]span
	cells map[int][]cellRow
	// boxes maps a box title to the line its top border is on.
	boxes map[string]int
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

func newPage(st styles, width int) *page {
	return &page{st: st, width: max(10, width), spans: map[int][]span{}, cells: map[int][]cellRow{}, boxes: map[string]int{}}
}

// add appends one row (truncated to the page width) anchored to unit ("" for
// none).
func (p *page) add(s, unit string) {
	p.lines = append(p.lines, truncate(s, p.width))
	p.anchors = append(p.anchors, unit)
	if unit != "" {
		i := len(p.lines) - 1
		p.spans[i] = append(p.spans[i], span{col: 0, end: p.width, unit: unit})
	}
}

// addFit appends left+mid+right, shortening only mid so the location and
// probability at the row's end stay visible on narrow panes.
func (p *page) addFit(left, mid, right, unit string) {
	room := p.width - lipgloss.Width(left) - lipgloss.Width(right)
	p.add(left+truncate(mid, max(1, room))+right, unit)
}

// dim appends a dim row (empty-block lines, notes).
func (p *page) dim(s string) { p.add(p.st.dim.Render(s), "") }

// text appends multi-line text without anchors (the description).
func (p *page) text(s string) {
	for _, l := range strings.Split(s, "\n") {
		p.add(l, "")
	}
}

// prob is a probability in dim text (".86").
func (p *page) prob(pr float64) string { return p.st.dim.Render(codeview.ProbText(pr)) }

// place copies src's spans, cell runs and box titles into p, shifted by dy
// lines and dx columns (src's lines are already in p).
func (p *page) place(src *page, dy, dx int) {
	for i, ss := range src.spans {
		for _, s := range ss {
			p.spans[i+dy] = append(p.spans[i+dy], span{col: s.col + dx, end: s.end + dx, unit: s.unit})
		}
	}
	for i, cs := range src.cells {
		for _, c := range cs {
			p.cells[i+dy] = append(p.cells[i+dy], cellRow{col: c.col + dx, ids: c.ids})
		}
	}
	for t, i := range src.boxes {
		if _, ok := p.boxes[t]; !ok {
			p.boxes[t] = i + dy
		}
	}
}

// appendPage appends src below p's lines.
func (p *page) appendPage(src *page) {
	dy := len(p.lines)
	p.lines = append(p.lines, src.lines...)
	p.anchors = append(p.anchors, src.anchors...)
	p.place(src, dy, 0)
}

// box renders fill inside a titled box of width w (theme.Box); h > 0
// stretches it to h lines. Inner lines start at column 2 (border, pad).
func (p *page) box(title string, w, h int, fill func(*page)) *page {
	in := newPage(p.st, w-4)
	fill(in)
	out := newPage(p.st, w)
	out.width = w
	text := p.st.th.Box(title, strings.Join(in.lines, "\n"), w, theme.BoxOpt{Height: h})
	out.lines = strings.Split(text, "\n")
	out.anchors = make([]string, len(out.lines))
	for i := 0; i < len(in.lines) && i+1 < len(out.lines)-1; i++ {
		out.anchors[i+1] = in.anchors[i]
	}
	out.place(in, 1, 2)
	out.boxes[title] = 0
	return out
}

// boxSpec is one box of the grid: its title and what fills it.
type boxSpec struct {
	title string
	fill  func(*page)
}

// cell is one grid cell: boxes stacked top to bottom.
type cell []boxSpec

// grid lays cells out two per row (left, right) from twoColumnMin columns,
// the boxes of a row stretched to the same height; narrower pages stack
// every box in one column. A last odd cell spans the full width.
func (p *page) grid(cells []cell) {
	if p.width < twoColumnMin {
		for _, c := range cells {
			p.appendPage(p.stack(c, p.width, 0))
		}
		return
	}
	lw := (p.width - colGap) / 2
	rw := p.width - colGap - lw
	for i := 0; i < len(cells); i += 2 {
		if i+1 >= len(cells) {
			p.appendPage(p.stack(cells[i], p.width, 0))
			continue
		}
		left, right := p.stack(cells[i], lw, 0), p.stack(cells[i+1], rw, 0)
		h := max(len(left.lines), len(right.lines))
		p.appendPage(p.hjoin(p.stack(cells[i], lw, h), p.stack(cells[i+1], rw, h)))
	}
}

// stack renders a cell's boxes at width w; with h above their natural
// height the last box stretches to fill it.
func (p *page) stack(c cell, w, h int) *page {
	out := newPage(p.st, w)
	out.width = w
	boxes := make([]*page, len(c))
	total := 0
	for i, b := range c {
		boxes[i] = p.box(b.title, w, 0, b.fill)
		total += len(boxes[i].lines)
	}
	if last := len(c) - 1; last >= 0 && h > total {
		boxes[last] = p.box(c[last].title, w, len(boxes[last].lines)+h-total, c[last].fill)
	}
	for _, b := range boxes {
		out.appendPage(b)
	}
	return out
}

// hjoin puts a and b side by side, colGap apart; a line's anchor is a's,
// else b's.
func (p *page) hjoin(a, b *page) *page {
	out := newPage(p.st, a.width+colGap+b.width)
	n := max(len(a.lines), len(b.lines))
	for i := range n {
		l, r := lineAt(a.lines, i), lineAt(b.lines, i)
		line := l
		if r != "" {
			line = padRight(l, a.width) + strings.Repeat(" ", colGap) + r
		}
		an := lineAt(a.anchors, i)
		if an == "" {
			an = lineAt(b.anchors, i)
		}
		out.lines = append(out.lines, line)
		out.anchors = append(out.anchors, an)
	}
	out.place(a, 0, 0)
	out.place(b, 0, a.width+colGap)
	return out
}

func lineAt(ls []string, i int) string {
	if i < len(ls) {
		return ls[i]
	}
	return ""
}

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

// overviewPage renders the Overview as a page (lines, anchors, spans, cell
// runs and box positions): the title and TL;DR, the composition bar and
// summary line, the blocks as titled boxes (two columns from twoColumnMin),
// the confidence footer, then the description.
func overviewPage(st styles, md *markdown, d proto.Detail, now time.Time, width int) *page {
	p := newPage(st, width)
	p.add(st.title.Render(titleLine(d.Item, now)), "")
	tldrBlock(p, d)
	switch {
	case d.Agg == nil:
		pendingBlocks(p, d)
	case d.Agg.LabelsMissing:
		gitFactsBlocks(p, d)
	default:
		labelledBlocks(p, d)
	}
	p.appendPage(p.box(blockDescription, p.width, 0, func(q *page) {
		if strings.TrimSpace(d.Item.Body) == "" {
			q.dim("no description")
			return
		}
		q.text(md.render(d.Item.Body, q.width))
	}))
	return p
}

// noteCells is the grid with every box showing one dim note (analysis
// pending or failed; labels missing).
func noteCells(d proto.Detail, note string) []cell {
	one := func(title string) cell { return cell{{title, func(q *page) { q.dim(note) }}} }
	if d.Item.Kind == model.KindBranch {
		return []cell{one(blockBranch), one(blockHeadlines), one(blockCheck), one(blockRisk), one(blockMinimap), one(blockSlice)}
	}
	last := cell{{blockSlice, func(q *page) { q.dim(note) }}, {blockSince, func(q *page) { q.dim(note) }}}
	return []cell{one(blockHeadlines), one(blockCheck), one(blockRisk), one(blockFidelity), one(blockMinimap), last}
}

// pendingBlocks draws every block as a dim one-liner while no aggregate
// exists (analysis running, or failed).
func pendingBlocks(p *page, d proto.Detail) {
	it := d.Item
	note := "analysing…"
	if it.State == model.StateError {
		note = "not analysed"
		if it.StateNote != "" {
			note += " — " + it.StateNote
		}
	}
	p.dim("Composition: " + note)
	p.add("", "")
	p.grid(noteCells(d, note))
	p.dim("Confidence: " + note)
}

// gitFactsBlocks is the page when Jev was unreachable: line counts and the
// file list; label-driven boxes are dim one-liners, Since you looked (or
// the branch state) still renders.
func gitFactsBlocks(p *page, d proto.Detail) {
	p.add("labels unavailable (Jev unreachable) — showing git facts only", "")
	files := statsFromDetail(d.Files)
	a, r := totals(d, files)
	p.dim("Composition: composition needs labels")
	p.add(fmt.Sprintf("+%d −%d · %s · %s", a, r, plural(len(files), "file"), plural(d.Agg.UnitsTotal, "chunk")), "")
	p.add("", "")
	p.appendPage(p.box(blockFiles, p.width, 0, func(q *page) {
		if len(files) == 0 {
			q.dim("no files")
		}
		for _, f := range files {
			q.add(fmt.Sprintf("%s  %s", padLeft(fmt.Sprintf("+%d −%d", f.adds, f.dels), 11), f.path), "")
		}
	}))
	cells := noteCells(d, "needs labels")
	ix := newUnitIndex(d)
	if d.Item.Kind == model.KindBranch {
		cells[0] = cell{{blockBranch, func(q *page) { branchBlock(q, d) }}}
	} else {
		cells[5] = cell{{blockSlice, func(q *page) { q.dim("needs labels") }}, {blockSince, func(q *page) { sinceBlock(q, d, ix) }}}
	}
	p.grid(cells)
	p.dim("Confidence: needs labels")
}

// unitIndex looks units up by id for anchors and labels.
type unitIndex struct {
	agg   map[string]model.UnitAgg
	units map[string]model.ChangeUnit
	order []model.UnitAgg
}

func newUnitIndex(d proto.Detail) unitIndex {
	ix := unitIndex{agg: map[string]model.UnitAgg{}, units: map[string]model.ChangeUnit{}}
	if d.Agg != nil {
		ix.order = d.Agg.Units
	}
	for _, u := range ix.order {
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

// labelledBlocks draws the full label-driven page (spec blocks 2–10). For
// a branch the description-derived boxes (Description fidelity, Since you
// looked) give way to the branch state box; the label-driven Headlines stay
// (spec Overview, "Branches"; DECISIONS "Branch Overview keeps Headlines").
func labelledBlocks(p *page, d proto.Detail) {
	a := d.Agg
	ix := newUnitIndex(d)
	compositionBlock(p, a, ix)
	p.add("", "")
	headlines := cell{{blockHeadlines, func(q *page) { headlinesBlock(q, a) }}}
	check := cell{{blockCheck, func(q *page) { checkBlock(q, a) }}}
	risk := cell{{blockRisk, func(q *page) { riskBlock(q, a, ix) }}}
	minimap := cell{{blockMinimap, func(q *page) { minimapBlock(q, a, ix) }}}
	if d.Item.Kind == model.KindBranch {
		p.grid([]cell{
			{{blockBranch, func(q *page) { branchBlock(q, d) }}},
			headlines, check, risk, minimap,
			{{blockSlice, func(q *page) { sliceBlock(q, a) }}},
		})
	} else {
		p.grid([]cell{
			headlines, check, risk,
			{{blockFidelity, func(q *page) { fidelityBlock(q, a, ix) }}},
			minimap,
			{{blockSlice, func(q *page) { sliceBlock(q, a) }}, {blockSince, func(q *page) { sinceBlock(q, d, ix) }}},
		})
	}
	confidenceBlock(p, a)
}

// compositionBlock is the composition bar, its legend and the summary line
// (no box, design v2).
func compositionBlock(p *page, a *model.ItemAgg, ix unitIndex) {
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
	st := p.st
	p.add(st.add.Render(fmt.Sprintf("+%d", a.Added))+" "+st.del.Render(fmt.Sprintf("−%d", a.Removed))+
		fmt.Sprintf(" → ~%d lines that matter · ~%d min · ", a.EffectiveLines, roundMinutes(a.Minutes))+
		st.accent.Render(fmt.Sprintf("%d of %d chunks need eyes", a.UnitsEyes, a.UnitsTotal)),
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
		p.addFit(gutter(h.P)+p.st.accent.Render("⚠")+" "+padRight(h.Kind, kw)+"  ", text,
			fmt.Sprintf("  %s:%d  %s", h.Path, h.Line, p.prob(h.P)), h.UnitID)
	}
}

func checkBlock(p *page, a *model.ItemAgg) {
	if len(a.HotSpots) == 0 {
		p.dim("nothing flagged")
		return
	}
	for i, h := range a.HotSpots[:min(3, len(a.HotSpots))] {
		p.addFit(fmt.Sprintf("%s %s:%d  ", p.st.accent.Render(strconv.Itoa(i+1)), h.Path, h.Line), h.Reason,
			"  "+p.st.dim.Render(strconv.FormatFloat(h.Score, 'f', 1, 64)), h.UnitID)
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
	if a.RiskMax <= 0 {
		p.dim("risk unknown")
	} else {
		top := ix.first(func(u model.UnitAgg) bool { return u.Risk >= a.RiskMax || float64(u.RiskLevel) >= a.RiskMax })
		p.add("Risk "+p.st.accent.Bold(true).Render(riskWord(a.RiskMax))+
			" ("+strconv.FormatFloat(a.RiskMax, 'f', -1, 64)+")", top)
	}
	if chips := riskChips(a.RiskTags); len(chips) > 0 {
		p.add("  "+p.st.th.Chips(chips...), ix.first(func(u model.UnitAgg) bool { return len(u.RiskTags) > 0 }))
	}
	if a.RiskyUnits == 0 {
		p.dim("  no risky chunks")
		return
	}
	p.add(fmt.Sprintf("  %s %d of %d risky chunks untested", p.st.del.Render("test gap:"), a.TestGap, a.RiskyUnits),
		ix.first(func(u model.UnitAgg) bool { return u.Untested }))
}

// riskChips turns tag counts into chips ("auth ×2", "data write ×1"), by
// count descending, then name; the top tag is the warm chip.
func riskChips(tags map[string]int) []theme.Chip {
	type tc struct {
		name string
		n    int
	}
	var cs []tc
	for k, n := range tags {
		if n > 0 {
			cs = append(cs, tc{strings.ReplaceAll(k, "_", " "), n})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].n != cs[j].n {
			return cs[i].n > cs[j].n
		}
		return cs[i].name < cs[j].name
	})
	out := make([]theme.Chip, 0, len(cs))
	for i, c := range cs {
		kind := theme.ChipPlain
		if i == 0 {
			kind = theme.ChipWarn
		}
		out = append(out, theme.Chip{Kind: kind, Text: fmt.Sprintf("%s ×%d", c.name, c.n)})
	}
	return out
}

func fidelityBlock(p *page, a *model.ItemAgg, ix unitIndex) {
	st := p.st
	if len(a.Bullets) == 0 {
		p.dim("no description bullets")
	}
	for _, b := range a.Bullets {
		unit := ""
		if len(b.UnitIDs) > 0 {
			unit = b.UnitIDs[0]
		}
		if !b.Implemented {
			p.add("  "+st.del.Render("✗")+" not found    "+b.Text, unit)
			continue
		}
		right := ""
		if b.P > 0 {
			right = "  " + p.prob(b.P)
		}
		p.addFit(gutter(b.P)+st.add.Render("✓")+" implemented  ", b.Text, right, unit)
	}
	for _, id := range a.Undocumented {
		p.add("  "+st.accent.Render("+")+" undocumented "+ix.where(id), id)
	}
	if a.Shape != "" {
		p.add(fmt.Sprintf("%scoherence: %s  %s", gutter(a.ShapeP), a.Shape, p.prob(a.ShapeP)), "")
	}
}

func minimapBlock(p *page, a *model.ItemAgg, ix unitIndex) {
	if len(a.Files) == 0 {
		p.dim("no units")
		return
	}
	th := p.st.th
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
			p.dim("── not yours ──")
			divided = true
		}
		var cells strings.Builder
		for _, id := range f.UnitIDs {
			u := ix.agg[id]
			glyph := codeview.AttentionGlyph(u.Attention, u.Unsure)
			style := th.Attention(u.Attention)
			if u.Unsure {
				style = th.Blue
			}
			cells.WriteString(style.Render(glyph))
		}
		unit := ""
		if len(f.UnitIDs) > 0 {
			unit = f.UnitIDs[0]
		}
		p.add(padRight(truncate(f.Path, pw), pw)+"  "+cells.String(), unit)
		if col := pw + 2; col < p.width {
			// Cells cut off by add (the last visible cell then shows "…")
			// stay out of reach; a click there falls back to the row's unit.
			n := len(f.UnitIDs)
			if col+n > p.width {
				n = p.width - col - 1
			}
			i := len(p.lines) - 1
			p.cells[i] = append(p.cells[i], cellRow{col: col, ids: f.UnitIDs[:max(0, n)]})
		}
	}
	p.add(th.Attention(model.AttnEyes).Render("█")+" eyes "+th.Attention(model.AttnSkim).Render("▒")+" skim "+
		th.Attention(model.AttnSkip).Render("·")+" skip "+th.Blue.Render("□")+" unsure", "")
}

func sliceBlock(p *page, a *model.ItemAgg) {
	if len(a.Slice) == 0 {
		p.dim("no modules")
		return
	}
	for _, s := range a.Slice {
		if s.Owned {
			p.add(fmt.Sprintf("%s %s · %s", p.st.bold.Render(s.Module), plural(s.Units, "chunk"), needEyes(s.Eyes)), "")
			continue
		}
		p.dim(fmt.Sprintf("%s is %s — skip unless curious", s.Module, plural(s.Units, "chunk")))
	}
}

// sinceMax is how many changed units "Since you looked" lists.
const sinceMax = 5

// sinceBlock draws "Since you looked" (spec block 9) from Agg.Since: the
// units changed since the base head and, per earlier comment of the
// viewer's, whether the latest push addressed it. The page only formats.
func sinceBlock(p *page, d proto.Detail, ix unitIndex) {
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
		verdict = gutter(max(c.P, 1-c.P)) + p.st.add.Render("✓") + " addressed " + p.prob(c.P)
	default:
		verdict = gutter(max(c.P, 1-c.P)) + p.st.del.Render("✗") + " not addressed " + p.prob(c.P)
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

// branchStateOrder is the catalogue order of the branch states (ties in the
// options line).
var branchStateOrder = []string{model.BranchReady, model.BranchWIP, model.BranchSpike, model.BranchAbandoned, model.BranchSuperseded}

// branchBlock draws the branch state box (spec Overview, "Branches"): the
// state with its probability, the rule that set it, every option with its
// probability, the unpushed commits and the suggested PR title.
func branchBlock(p *page, d proto.Detail) {
	var b *model.BranchInfo
	if d.Agg != nil {
		b = d.Agg.Branch
	}
	if b == nil {
		p.dim("branch state not computed yet")
		return
	}
	if b.State == "" || (!b.Known && b.Rule == "") {
		p.dim("state unknown — labels unavailable")
	} else {
		p.add(p.st.bold.Render(branchStateWords(b.State, b.SupersededBy))+" "+p.prob(b.P), "")
	}
	if b.Rule != "" {
		p.dim("rule: " + b.Rule)
	}
	if len(b.Probs) > 0 {
		p.dim(branchOptions(b.Probs))
	}
	if b.Unpushed > 0 || len(b.Commits) > 0 {
		p.add(plural(b.Unpushed, "unpushed commit"), "")
	}
	for _, c := range b.Commits[:min(branchCommitsMax, len(b.Commits))] {
		p.add("  "+p.st.dim.Render(shortSHA(c.SHA))+" "+c.Subject, "")
	}
	if n := len(b.Commits) - branchCommitsMax; n > 0 {
		p.dim(fmt.Sprintf("  … %d more", n))
	}
	switch {
	case b.SuggestedTitle != "":
		p.add("Suggested PR title: "+p.st.bold.Render(b.SuggestedTitle)+"  "+p.st.blue.Render("ai·from group names"), "")
	case b.TitleNote != "":
		p.dim("Suggested PR title: none — " + b.TitleNote)
	}
}

// branchOptions lists every state with its probability, most likely first
// (ties in catalogue order, unknown options last by name).
func branchOptions(probs map[string]float64) string {
	rank := func(s string) int {
		for i, o := range branchStateOrder {
			if o == s {
				return i
			}
		}
		return len(branchStateOrder)
	}
	keys := make([]string, 0, len(probs))
	for k := range probs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if probs[a] != probs[b] {
			return probs[a] > probs[b]
		}
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		return a < b
	})
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+" "+codeview.ProbText(probs[k]))
	}
	return strings.Join(parts, " · ")
}

// confidenceBlock is the confidence footer line (spec block 10).
func confidenceBlock(p *page, a *model.ItemAgg) {
	if a.Unsure == 0 {
		p.dim("Confidence: all labels confident")
		return
	}
	unit := ""
	if len(a.UnsureUnits) > 0 {
		unit = a.UnsureUnits[0]
	}
	p.add(p.st.dim.Render("Confidence: ")+p.st.blue.Render("□")+" "+
		plural(a.Unsure, "low-confidence label")+"; those units stay expanded in Code", unit)
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
