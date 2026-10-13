package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/surface"
	"github.com/fredricw/prt/internal/theme"
)

// Overview box titles (spec: Overview page, Layout; design v2/Overview;
// M5.2 structured change blocks).
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
	blockAPI         = "API endpoints"
	blockDB          = "Database"
	blockFlags       = "Feature flags & config"
)

// Row caps of the Overview's list blocks (M5.2: the Overview is an
// overview; the rest is a "+N more" line away, in the full-list overlay).
const (
	capHeadlines = 4
	capHotSpots  = 3 // spec: "the top three hot spots"
	capFidelity  = 3 // after the "✓ N of M bullets implemented" summary
	capRiskChips = 4
	capMinimap   = 6 // files (design: six rows, then "+8 files")
	capSlice     = 4
	capSince     = 4 // changed units and earlier comments together
	capFiles     = 5 // the git-facts Files list
	capSurface   = 4 // each structured change block
	capCommits   = 5 // the branch state's unpushed commits
)

// twoColumnMin is the narrowest page that lays the boxes out in two
// columns (design: a 2-column grid); narrower pages stack them.
const twoColumnMin = 100

// colGap is the gap between the two columns.
const colGap = 2

// span is a column range [col, end) of one line that belongs to a unit, so
// a click opens the unit of the box it lands in.
type span struct {
	col, end int
	unit     string
}

// moreSpan is the column range of a "+N more" line and the title of the
// block whose full list a click on it opens.
type moreSpan struct {
	col, end int
	block    string
}

// rect is a box's cells: lines y0..y1 (inclusive), columns [x0, x1).
type rect struct{ y0, y1, x0, x1 int }

// contains reports whether line y, column x is inside r.
func (r rect) contains(y, x int) bool { return y >= r.y0 && y <= r.y1 && x >= r.x0 && x < r.x1 }

// page accumulates the Overview's lines and, per line, the units behind
// it. A page is also a fragment: boxes are rendered as pages and placed
// into the parent with their spans, cell runs and box titles shifted.
type page struct {
	st    styles
	width int
	// focus is the focused block's title: its box is drawn focused.
	focus string
	lines []string
	// anchors holds, per line, the first unit on it (left column first):
	// enter and L take the first anchored line at or after the top.
	anchors []string
	// spans are the clickable unit ranges per line; cells the one-cell-per-
	// unit runs (minimap rows), so a click on a cell opens that cell's unit;
	// mores the "+N more" lines.
	spans map[int][]span
	cells map[int][]cellRow
	mores map[int][]moreSpan
	// boxes maps a box title to the line its top border is on, rects to
	// its cells.
	boxes map[string]int
	rects map[string]rect
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
	return &page{st: st, width: max(10, width), spans: map[int][]span{}, cells: map[int][]cellRow{},
		mores: map[int][]moreSpan{}, boxes: map[string]int{}, rects: map[string]rect{}}
}

// sub is a fragment page of width w with p's styles and focus.
func (p *page) sub(w int) *page {
	q := newPage(p.st, w)
	q.width, q.focus = w, p.focus
	return q
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

// more appends the dim "+N <noun>" line of a capped list; a click on it
// opens the block's full list. Nothing when n is 0.
func (p *page) more(n int, noun string) {
	if n <= 0 {
		return
	}
	p.dim(fmt.Sprintf("  +%d %s", n, noun))
	i := len(p.lines) - 1
	p.mores[i] = append(p.mores[i], moreSpan{col: 0, end: p.width})
}

// text appends multi-line text without anchors (the description).
func (p *page) text(s string) {
	for _, l := range strings.Split(s, "\n") {
		p.add(l, "")
	}
}

// prob is a probability in dim text (".86").
func (p *page) prob(pr float64) string { return p.st.dim.Render(codeview.ProbText(pr)) }

// place copies src's spans, cell runs, "+N more" lines and boxes into p,
// shifted by dy lines and dx columns (src's lines are already in p).
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
	for i, ms := range src.mores {
		for _, s := range ms {
			p.mores[i+dy] = append(p.mores[i+dy], moreSpan{col: s.col + dx, end: s.end + dx, block: s.block})
		}
	}
	for t, i := range src.boxes {
		if _, ok := p.boxes[t]; !ok {
			p.boxes[t] = i + dy
		}
	}
	for t, r := range src.rects {
		if _, ok := p.rects[t]; !ok {
			p.rects[t] = rect{y0: r.y0 + dy, y1: r.y1 + dy, x0: r.x0 + dx, x1: r.x1 + dx}
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

// box renders fill inside a titled box of width w (theme.Box), drawn
// focused when it is the page's focused block; h > 0 stretches it to h
// lines. Inner lines start at column 2 (border, pad).
func (p *page) box(title string, w, h int, fill func(*page)) *page {
	in := p.sub(w - 4)
	fill(in)
	for i := range in.mores {
		for j := range in.mores[i] {
			in.mores[i][j].block = title
		}
	}
	out := p.sub(w)
	text := p.st.th.Box(title, strings.Join(in.lines, "\n"), w, theme.BoxOpt{Height: h, Focused: title == p.focus})
	out.lines = strings.Split(text, "\n")
	out.anchors = make([]string, len(out.lines))
	for i := 0; i < len(in.lines) && i+1 < len(out.lines)-1; i++ {
		out.anchors[i+1] = in.anchors[i]
	}
	inner := len(out.lines) - 2
	for i := range in.mores {
		if i >= inner {
			delete(in.mores, i) // cut off by a fixed height
		}
	}
	out.place(in, 1, 2)
	out.boxes[title] = 0
	out.rects[title] = rect{y0: 0, y1: len(out.lines) - 1, x0: 0, x1: w}
	return out
}

// blockSpec is one box of the grid: its title and what fills it. fill
// draws the capped summary, or every row when full (the full-list
// overlay); list is set when the block has rows to list.
type blockSpec struct {
	title string
	fill  func(q *page, full bool)
	list  bool
}

// summary draws the block's capped summary into q.
func (b blockSpec) summary(q *page) { b.fill(q, false) }

// cell is one grid cell: boxes stacked top to bottom.
type cell []blockSpec

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
	out := p.sub(w)
	boxes := make([]*page, len(c))
	total := 0
	for i, b := range c {
		boxes[i] = p.box(b.title, w, 0, b.summary)
		total += len(boxes[i].lines)
	}
	if last := len(c) - 1; last >= 0 && h > total {
		boxes[last] = p.box(c[last].title, w, len(boxes[last].lines)+h-total, c[last].summary)
	}
	for _, b := range boxes {
		out.appendPage(b)
	}
	return out
}

// hjoin puts a and b side by side, colGap apart; a line's anchor is a's,
// else b's.
func (p *page) hjoin(a, b *page) *page {
	out := p.sub(a.width + colGap + b.width)
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
	return overviewPageWith(st, md, d, now, width, defaultOvOpts())
}

// defaultOvOpts are the Overview options with the default keys and no
// known strategy.
func defaultOvOpts() ovOpts {
	k := DefaultKeyMap()
	return ovOpts{runKey: k.Refresh.Help().Key, resumeKey: k.Resume.Help().Key, settingsKey: k.Settings.Help().Key}
}

// overviewPageWith is overviewPage with the strategy and keys a
// not-analysed or stopped item's box names (M5.1) and the focused block
// (M5.2).
func overviewPageWith(st styles, md *markdown, d proto.Detail, now time.Time, width int, o ovOpts) *page {
	p := newPage(st, width)
	p.focus = o.focus
	p.add(titleRow(st, titleLine(d.Item, now), updatingNote(d), p.width), "")
	tldrBlock(p, d)
	lead, cells := overviewCells(d, o)
	switch {
	case d.Agg == nil:
		analysisStateBlock(p, d.Item, o)
		pendingLines(p, d, func() { p.grid(cells) })
	case d.Agg.LabelsMissing:
		gitFactsLines(p, d, func() {
			for _, b := range lead {
				p.appendPage(p.box(b.title, p.width, 0, b.summary))
			}
			p.grid(cells)
		})
	default:
		compositionBlock(p, d.Agg, newUnitIndex(d))
		p.add("", "")
		p.grid(cells)
		confidenceBlock(p, d.Agg)
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

// titleRow is the title line, with the dim "↻ updating review" note at its
// right end while the item updates (the title is cut first).
func titleRow(st styles, title, note string, w int) string {
	if note == "" {
		return st.title.Render(title)
	}
	room := w - ansi.StringWidth(note) - 2
	if room < 8 {
		return st.title.Render(title)
	}
	return theme.Spread(st.title.Render(truncate(title, room)), st.dim.Render(note), w)
}

// overviewCells returns d's Overview grid and, on the labels-missing page,
// the full-width boxes before it (the git-facts Files list). The page and
// the full-list overlay build from the same specs.
func overviewCells(d proto.Detail, o ovOpts) (lead []blockSpec, cells []cell) {
	branch := d.Item.Kind == model.KindBranch
	switch {
	case d.Agg == nil:
		return nil, noteCells(branch, pendingNote(d.Item))
	case d.Agg.LabelsMissing:
		cells = noteCells(branch, "needs labels")
		surfaceCell := surfaceCellOf(d.Agg, o)
		if branch {
			cells[0] = cell{{blockBranch, func(q *page, full bool) { branchBlock(q, d, full) }, true}}
			cells[2] = surfaceCell
		} else {
			cells[2] = surfaceCell
			cells[5] = cell{{blockSlice, noteFill("needs labels"), false}, sinceSpec(d)}
		}
		files := statsFromDetail(d.Files)
		return []blockSpec{{blockFiles, func(q *page, full bool) { filesBlock(q, files, full) }, true}}, cells
	}
	return nil, labelledCells(d, o)
}

// labelledCells is the full label-driven grid (spec blocks 3–9 and the
// M5.2 structured change blocks): Headlines · Check, then the three change
// blocks stacked beside Risk and Description fidelity, then Minimap · Your
// slice and Since you looked. For a branch the description-derived boxes
// (Description fidelity, Since you looked) give way to the branch state
// box; the label-driven Headlines stay (spec Overview, "Branches";
// DECISIONS "Branch Overview keeps Headlines").
func labelledCells(d proto.Detail, o ovOpts) []cell {
	a := d.Agg
	ix := newUnitIndex(d)
	headlines := blockSpec{blockHeadlines, func(q *page, full bool) { headlinesBlock(q, a, full) }, true}
	check := blockSpec{blockCheck, func(q *page, full bool) { checkBlock(q, a, full) }, true}
	risk := blockSpec{blockRisk, func(q *page, full bool) { riskBlock(q, a, ix, full) }, true}
	minimap := blockSpec{blockMinimap, func(q *page, full bool) { minimapBlock(q, a, ix, full) }, true}
	slice := blockSpec{blockSlice, func(q *page, full bool) { sliceBlock(q, a, full) }, true}
	if d.Item.Kind == model.KindBranch {
		return []cell{
			{{blockBranch, func(q *page, full bool) { branchBlock(q, d, full) }, true}}, {headlines},
			surfaceCellOf(a, o), {check, risk},
			{minimap}, {slice},
		}
	}
	return []cell{
		{headlines}, {check},
		surfaceCellOf(a, o), {risk, {blockFidelity, func(q *page, full bool) { fidelityBlock(q, a, ix, full) }, true}},
		{minimap}, {slice, sinceSpec(d)},
	}
}

// sinceSpec is the "Since you looked" block of d.
func sinceSpec(d proto.Detail) blockSpec {
	ix := newUnitIndex(d)
	return blockSpec{blockSince, func(q *page, full bool) { sinceBlock(q, d, ix, full) }, true}
}

// noteFill fills a box with one dim note.
func noteFill(note string) func(*page, bool) {
	return func(q *page, _ bool) { q.dim(note) }
}

// noteCells is the grid with every box showing one dim note (analysis
// pending or failed; labels missing), laid out like the labelled grid.
func noteCells(branch bool, note string) []cell {
	one := func(title string) blockSpec { return blockSpec{title, noteFill(note), false} }
	surfaceCell := cell{one(blockAPI), one(blockDB), one(blockFlags)}
	if branch {
		return []cell{
			{one(blockBranch)}, {one(blockHeadlines)},
			surfaceCell, {one(blockCheck), one(blockRisk)},
			{one(blockMinimap)}, {one(blockSlice)},
		}
	}
	return []cell{
		{one(blockHeadlines)}, {one(blockCheck)},
		surfaceCell, {one(blockRisk), one(blockFidelity)},
		{one(blockMinimap)}, {one(blockSlice), one(blockSince)},
	}
}

// pendingNote is the boxes' note while no aggregate exists.
func pendingNote(it model.Item) string {
	switch it.State {
	case model.StateError:
		if it.StateNote != "" {
			return "not analysed — " + it.StateNote
		}
		return "not analysed"
	case model.StateNotAnalysed:
		return "not analysed"
	case model.StateStopped:
		return "stopped"
	}
	return "analysing…"
}

// pendingLines draws the composition and confidence lines and, between
// them, the grid of dim one-liners while no aggregate exists (analysis
// running, or failed).
func pendingLines(p *page, d proto.Detail, grid func()) {
	note := pendingNote(d.Item)
	p.dim("Composition: " + note)
	p.add("", "")
	grid()
	p.dim("Confidence: " + note)
}

// analysisStateBlock is the full-width box of an item without an aggregate
// that waits for the user (M5.1): "Not analysed" (outside the auto-review
// strategy) with "r to run", or "Stopped at <stage>" with "r or ctrl+o to
// resume", then the strategy line when it is known. Other items get no box.
func analysisStateBlock(p *page, it model.Item, o ovOpts) {
	var title, action string
	switch it.State {
	case model.StateNotAnalysed:
		title, action = "Not analysed", o.runKey+" to run"
	case model.StateStopped:
		title = "Stopped"
		if it.StateNote != "" {
			title += " at " + it.StateNote
		}
		action = o.runKey + " or " + o.resumeKey + " to resume"
	default:
		return
	}
	line := o.strategyLine()
	p.appendPage(p.box(title, p.width, 0, func(q *page) {
		q.add(action, "")
		if line != "" {
			for _, l := range strings.Split(ansi.Wordwrap(line, q.width, ""), "\n") {
				q.dim(l)
			}
		}
	}))
	p.add("", "")
}

// gitFactsLines is the page when Jev was unreachable: line counts, then the
// boxes (the Files list, the change blocks, which need no labels, and the
// label-driven boxes as dim one-liners; Since you looked or the branch
// state still render).
func gitFactsLines(p *page, d proto.Detail, boxes func()) {
	p.add("labels unavailable (Jev unreachable) — showing git facts only", "")
	files := statsFromDetail(d.Files)
	a, r := totals(d, files)
	p.dim("Composition: composition needs labels")
	p.add(fmt.Sprintf("+%d −%d · %s · %s", a, r, plural(len(files), "file"), plural(d.Agg.UnitsTotal, "chunk")), "")
	p.add("", "")
	boxes()
	p.dim("Confidence: needs labels")
}

// filesBlock is the git-facts Files list: "+a −d  path" per file, capped.
func filesBlock(p *page, files []fileStat, full bool) {
	if len(files) == 0 {
		p.dim("no files")
		return
	}
	shown := capped(len(files), capFiles, full)
	for _, f := range files[:shown] {
		p.add(fmt.Sprintf("%s  %s", padLeft(fmt.Sprintf("+%d −%d", f.adds, f.dels), 11), f.path), "")
	}
	p.more(len(files)-shown, "more")
}

// capped is how many of n rows a block shows: all when full, else at most
// limit.
func capped(n, limit int, full bool) int {
	if full {
		return n
	}
	return min(n, limit)
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
		p.add(compositionBar(p.st.th, segs, min(40, p.width-2)), first)
		p.add(compositionLegend(p.st.th, segs), first)
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

// noiseGlyph draws the noise segment without colour; kinds are drawn with
// a letter.
const noiseGlyph = "░"

// Composition glyphs with colour (design v2/Overview): bar cells are full
// blocks in the kind's colour, legend keys squares.
const (
	barCell   = "█"
	legendKey = "■"
)

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
		segs = append(segs, segment{name: theme.KindNameNoise, share: float64(a.NoiseLines) / float64(total), glyph: noiseGlyph})
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
// at least one cell per segment): with colour, full blocks in each kind's
// composition colour (noise in the noise colour); without, each segment's
// letter (░ for noise), so snapshots stay colour-free and readable.
func compositionBar(th theme.Theme, segs []segment, width int) string {
	width = max(len(segs), width)
	shares := make([]float64, len(segs))
	for i, s := range segs {
		shares[i] = s.share
	}
	var b strings.Builder
	for i, n := range cellWidths(shares, width) {
		if th.Colour {
			b.WriteString(th.Kind(segs[i].name).Render(strings.Repeat(barCell, n)))
			continue
		}
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

// compositionLegend names the segments with their shares, largest first:
// with colour "■ refactor 62%  ■ feature 20%" (coloured squares, design
// v2), without "r refactor 62% · f feature 20%" (the bar's letters).
func compositionLegend(th theme.Theme, segs []segment) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		pct := fmt.Sprintf("%s %d%%", s.name, int(s.share*100+0.5))
		if th.Colour {
			parts = append(parts, th.Kind(s.name).Render(legendKey)+" "+pct)
			continue
		}
		parts = append(parts, s.glyph+" "+pct)
	}
	if th.Colour {
		return strings.Join(parts, "  ")
	}
	return strings.Join(parts, " · ")
}

// headlineBreaking is the Headline.Kind of a breaking change.
const headlineBreaking = "breaking"

// sortedHeadlines orders headlines by importance: breaking first, then by
// probability (highest first); ties keep the aggregate's order.
func sortedHeadlines(hs []model.Headline) []model.Headline {
	out := append([]model.Headline(nil), hs...)
	sort.SliceStable(out, func(i, j int) bool {
		bi, bj := out[i].Kind == headlineBreaking, out[j].Kind == headlineBreaking
		if bi != bj {
			return bi
		}
		return out[i].P > out[j].P
	})
	return out
}

// headlinesBlock lists the headlines (capped), then a dim line for the
// candidates left out below .60 (Agg.HeadlinesUnsure).
func headlinesBlock(p *page, a *model.ItemAgg, full bool) {
	hs := sortedHeadlines(a.Headlines)
	if len(hs) == 0 {
		p.dim("no contract changes")
	}
	shown := capped(len(hs), capHeadlines, full)
	kw := 0
	for _, h := range hs[:shown] {
		kw = max(kw, len([]rune(h.Kind)))
	}
	for _, h := range hs[:shown] {
		// Units without a symbol or signature get Text = path, which the
		// location column already shows.
		text := h.Text
		if text == h.Path {
			text = "(top level)"
		}
		p.fitRow(gutter(h.P)+p.st.accent.Render("⚠")+" "+padRight(h.Kind, kw)+"  ", text,
			fmt.Sprintf("%s:%d", h.Path, h.Line), p.prob(h.P), h.UnitID)
	}
	p.more(len(hs)-shown, "more")
	if a.HeadlinesUnsure > 0 {
		p.dim(fmt.Sprintf("  +%d below .60 (unsure)", a.HeadlinesUnsure))
	}
}

func checkBlock(p *page, a *model.ItemAgg, full bool) {
	if len(a.HotSpots) == 0 {
		p.dim("nothing flagged")
		return
	}
	shown := capped(len(a.HotSpots), capHotSpots, full)
	for i, h := range a.HotSpots[:shown] {
		p.addFit(fmt.Sprintf("%s %s:%d  ", p.st.accent.Render(strconv.Itoa(i+1)), h.Path, h.Line), h.Reason,
			"  "+p.st.dim.Render(strconv.FormatFloat(h.Score, 'f', 1, 64)), h.UnitID)
	}
	p.more(len(a.HotSpots)-shown, "more")
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

// riskBlock is the risk headline, the risk-tag chips (capped; the full list
// has one row per tag, anchored to the first unit with it) and the test
// gap.
func riskBlock(p *page, a *model.ItemAgg, ix unitIndex, full bool) {
	if a.RiskMax <= 0 {
		p.dim("risk unknown")
	} else {
		top := ix.first(func(u model.UnitAgg) bool { return u.Risk >= a.RiskMax || float64(u.RiskLevel) >= a.RiskMax })
		p.add("Risk "+p.st.accent.Bold(true).Render(riskWord(a.RiskMax))+
			" ("+strconv.FormatFloat(a.RiskMax, 'f', -1, 64)+")", top)
	}
	tags := riskTagCounts(a.RiskTags)
	switch {
	case full:
		for i, tc := range tags {
			kind := theme.ChipPlain
			if i == 0 {
				kind = theme.ChipWarn
			}
			p.add("  "+p.st.th.Chips(tc.chip(kind)), ix.first(func(u model.UnitAgg) bool { return hasTag(u, tc.tag) }))
		}
	case len(tags) > 0:
		shown := min(len(tags), capRiskChips)
		p.add("  "+p.st.th.Chips(riskChips(a.RiskTags)[:shown]...), ix.first(func(u model.UnitAgg) bool { return len(u.RiskTags) > 0 }))
		p.more(len(tags)-shown, "more")
	}
	if a.RiskyUnits == 0 {
		p.dim("  no risky chunks")
		return
	}
	p.add(fmt.Sprintf("  %s %d of %d risky chunks untested", p.st.del.Render("test gap:"), a.TestGap, a.RiskyUnits),
		ix.first(func(u model.UnitAgg) bool { return u.Untested }))
}

// tagCount is one risk tag and how many units carry it.
type tagCount struct {
	tag, name string
	n         int
}

// chip is the tag's chip ("auth ×2").
func (tc tagCount) chip(kind theme.ChipKind) theme.Chip {
	return theme.Chip{Kind: kind, Text: fmt.Sprintf("%s ×%d", tc.name, tc.n)}
}

// riskTagCounts are the tags with a count, by count descending, then name.
func riskTagCounts(tags map[string]int) []tagCount {
	var cs []tagCount
	for k, n := range tags {
		if n > 0 {
			cs = append(cs, tagCount{tag: k, name: strings.ReplaceAll(k, "_", " "), n: n})
		}
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].n != cs[j].n {
			return cs[i].n > cs[j].n
		}
		return cs[i].name < cs[j].name
	})
	return cs
}

// hasTag reports whether u carries risk tag tag.
func hasTag(u model.UnitAgg, tag string) bool {
	for _, t := range u.RiskTags {
		if t == tag {
			return true
		}
	}
	return false
}

// riskChips turns tag counts into chips ("auth ×2", "data write ×1"), by
// count descending, then name; the top tag is the warm chip.
func riskChips(tags map[string]int) []theme.Chip {
	cs := riskTagCounts(tags)
	out := make([]theme.Chip, 0, len(cs))
	for i, c := range cs {
		kind := theme.ChipPlain
		if i == 0 {
			kind = theme.ChipWarn
		}
		out = append(out, c.chip(kind))
	}
	return out
}

// fidelityRow is one row of Description fidelity, with its sort rank.
type fidelityRow struct {
	rank             int // 0 not found, 1 undocumented, 2 unsure implemented, 3 implemented
	left, mid, right string
	unit             string
	plain            bool // add (no fit) rather than addFit
}

// fidelityBlock is design v2's summary: "✓ N of M bullets implemented",
// then the bullets not found and the undocumented changes first, the
// implemented bullets (unsure ones first) after them, capped; then the
// coherence verdict.
func fidelityBlock(p *page, a *model.ItemAgg, ix unitIndex, full bool) {
	st := p.st
	if len(a.Bullets) == 0 {
		p.dim("no description bullets")
	} else {
		done := 0
		for _, b := range a.Bullets {
			if b.Implemented {
				done++
			}
		}
		p.add(st.add.Render("✓")+fmt.Sprintf(" %d of %d bullets implemented", done, len(a.Bullets)), "")
	}
	rows := fidelityRows(p, a, ix)
	shown := capped(len(rows), capFidelity, full)
	for _, r := range rows[:shown] {
		if r.plain {
			p.add(r.left+r.mid, r.unit)
			continue
		}
		p.addFit(r.left, r.mid, r.right, r.unit)
	}
	p.more(len(rows)-shown, "more")
	if a.Shape != "" {
		p.add(fmt.Sprintf("%scoherence: %s  %s", gutter(a.ShapeP), a.Shape, p.prob(a.ShapeP)), "")
	}
}

// fidelityRows are the bullet and undocumented rows in importance order.
func fidelityRows(p *page, a *model.ItemAgg, ix unitIndex) []fidelityRow {
	st := p.st
	var rows []fidelityRow
	for _, b := range a.Bullets {
		unit := ""
		if len(b.UnitIDs) > 0 {
			unit = b.UnitIDs[0]
		}
		if !b.Implemented {
			rows = append(rows, fidelityRow{rank: 0, left: "  " + st.del.Render("✗") + " not found    ", mid: b.Text,
				unit: unit, plain: true})
			continue
		}
		right := ""
		if b.P > 0 {
			right = "  " + p.prob(b.P)
		}
		rank := 3
		if codeview.UnsurePrefix(b.P) != "" {
			rank = 2
		}
		rows = append(rows, fidelityRow{rank: rank, left: gutter(b.P) + st.add.Render("✓") + " implemented  ", mid: b.Text,
			right: right, unit: unit})
	}
	for _, id := range a.Undocumented {
		rows = append(rows, fidelityRow{rank: 1, left: "  " + st.accent.Render("+") + " undocumented ", mid: ix.where(id),
			unit: id, plain: true})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].rank < rows[j].rank })
	return rows
}

// minimapBlock is one row per file (capped; "+N files" for the rest), one
// cell per unit shaded by attention, and its legend. With colour cells are
// squares in the attention colours (outlined blue for unsure, design v2);
// without colour they keep their glyphs (█ ▒ · □).
func minimapBlock(p *page, a *model.ItemAgg, ix unitIndex, full bool) {
	if len(a.Files) == 0 {
		p.dim("no units")
		return
	}
	th := p.st.th
	shown := capped(len(a.Files), capMinimap, full)
	files := a.Files[:shown]
	pw := 0
	owned, other := false, false
	for _, f := range files {
		pw = max(pw, len([]rune(f.Path)))
		owned = owned || f.Owned
		other = other || !f.Owned
	}
	pw = min(pw, max(8, p.width/2))
	divided := false
	for _, f := range files {
		if owned && other && !f.Owned && !divided {
			p.dim("── not yours ──")
			divided = true
		}
		var cells strings.Builder
		for _, id := range f.UnitIDs {
			cells.WriteString(minimapCell(th, ix.agg[id]))
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
	p.more(len(a.Files)-shown, "files")
	p.add(minimapLegend(th), "")
}

// minimapCell draws one unit's cell.
func minimapCell(th theme.Theme, u model.UnitAgg) string {
	if !th.Colour {
		style := th.Attention(u.Attention)
		if u.Unsure {
			style = th.Blue
		}
		return style.Render(codeview.AttentionGlyph(u.Attention, u.Unsure))
	}
	if u.Unsure {
		return th.Blue.Render("□")
	}
	return th.Attention(u.Attention).Render(legendKey)
}

// minimapLegend is "█ eyes ▒ skim · skip □ unsure" without colour, and
// "■ eyes ■ skim ■ skip □ unsure" in the attention colours with colour.
func minimapLegend(th theme.Theme) string {
	if !th.Colour {
		return "█ eyes ▒ skim · skip □ unsure"
	}
	return th.Attention(model.AttnEyes).Render(legendKey) + " eyes " + th.Attention(model.AttnSkim).Render(legendKey) + " skim " +
		th.Attention(model.AttnSkip).Render(legendKey) + " skip " + th.Blue.Render("□") + " unsure"
}

func sliceBlock(p *page, a *model.ItemAgg, full bool) {
	if len(a.Slice) == 0 {
		p.dim("no modules")
		return
	}
	shown := capped(len(a.Slice), capSlice, full)
	for _, s := range a.Slice[:shown] {
		if s.Owned {
			p.add(fmt.Sprintf("%s %s · %s", p.st.bold.Render(s.Module), plural(s.Units, "chunk"), needEyes(s.Eyes)), "")
			continue
		}
		p.dim(fmt.Sprintf("%s is %s — skip unless curious", s.Module, plural(s.Units, "chunk")))
	}
	p.more(len(a.Slice)-shown, "more")
}

// sinceBlock draws "Since you looked" (spec block 9) from Agg.Since: a
// summary line (the base, the units changed, the comments addressed), then
// one capped list: earlier comments not (or not known to be) addressed,
// the changed units, then the addressed comments. The page only formats.
func sinceBlock(p *page, d proto.Detail, ix unitIndex, full bool) {
	s := d.Agg.Since
	if s == nil {
		p.dim("nothing to compare yet (first look)")
		return
	}
	first := ""
	if len(s.ChangedUnits) > 0 {
		first = s.ChangedUnits[0]
	}
	head := fmt.Sprintf("since %s (%s) · %s changed", shortSHA(s.BaseSHA), sinceSource(s.Source),
		plural(len(s.ChangedUnits), "unit"))
	if n := len(s.Comments); n > 0 {
		done := 0
		for _, c := range s.Comments {
			if c.Known && c.Addressed {
				done++
			}
		}
		head += fmt.Sprintf(" · %d of %d addressed", done, n)
	}
	p.add(head, first)
	if s.Note != "" {
		p.dim("  " + s.Note)
	}
	var rows []func()
	open, closed := []model.AddressedComment{}, []model.AddressedComment{}
	for _, c := range s.Comments {
		if c.Known && c.Addressed {
			closed = append(closed, c)
		} else {
			open = append(open, c)
		}
	}
	for _, c := range open {
		rows = append(rows, func() { addressedRow(p, c) })
	}
	for _, id := range s.ChangedUnits {
		rows = append(rows, func() { p.add("  "+ix.loc(id), id) })
	}
	for _, c := range closed {
		rows = append(rows, func() { addressedRow(p, c) })
	}
	shown := capped(len(rows), capSince, full)
	for _, row := range rows[:shown] {
		row()
	}
	p.more(len(rows)-shown, "more")
	if len(s.Comments) == 0 {
		p.dim("  no earlier comments of yours")
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

// surfaceCellOf is the three structured change blocks (M5.2): API
// endpoints, Database, Feature flags & config, stacked in one grid cell.
func surfaceCellOf(a *model.ItemAgg, o ovOpts) cell {
	var s *model.ChangeSurface
	if a != nil {
		s = a.Surface
	}
	spec := func(title, area string, entries func(*model.ChangeSurface) []model.SurfaceEntry) blockSpec {
		return blockSpec{title, func(q *page, full bool) { surfaceBlock(q, s, area, entries, o.runKey, full) }, true}
	}
	return cell{
		spec(blockAPI, model.SurfaceAPI, func(s *model.ChangeSurface) []model.SurfaceEntry { return s.API }),
		spec(blockDB, model.SurfaceDB, func(s *model.ChangeSurface) []model.SurfaceEntry { return s.DB }),
		spec(blockFlags, model.SurfaceFlag, func(s *model.ChangeSurface) []model.SurfaceEntry { return s.Flags }),
	}
}

// surfaceBlock lists one area's structured changes (surface.Rows, in the
// detector's importance order, capped): "+ GET /api/v1/x   path:line  .97",
// the glyph green for added, red for removed, accent for changed, the
// probability dim with the "?" gutter below .60. An empty list is the
// area's explicit empty line; with no surface computed yet (an analysis
// from before M5.2) a dim "not computed — r refreshes". The API block also
// carries the surface's degradation note.
func surfaceBlock(p *page, s *model.ChangeSurface, area string, entries func(*model.ChangeSurface) []model.SurfaceEntry,
	runKey string, full bool,
) {
	if s == nil {
		p.dim("not computed — " + runKey + " refreshes")
		return
	}
	rows := surface.Rows(entries(s))
	if len(rows) == 0 {
		p.dim(surface.Empty(area))
	}
	shown := capped(len(rows), capSurface, full)
	for _, r := range rows[:shown] {
		p.surfaceRow(r)
	}
	p.more(len(rows)-shown, "more")
	if area == model.SurfaceAPI && s.Note != "" {
		p.dim(s.Note)
	}
}

// surfaceRow appends one change-block row: gutter, glyph, name, path:line,
// probability (fitRow).
func (p *page) surfaceRow(r surface.Row) {
	p.fitRow(gutter(r.Confidence)+surfaceGlyph(p.st, r.Glyph)+" ", r.Text, r.Loc, p.prob(r.Confidence), r.UnitID)
}

// fitRow appends left, text, then "  loc  tail" at the row's end. When it
// does not fit, the text keeps at least three fifths of the room and the
// location is cut from the left (its file name and line stay); left and
// tail (the probability) always show.
func (p *page) fitRow(left, text, loc, tail, unit string) {
	right := "  " + tail
	avail := p.width - lipgloss.Width(left) - lipgloss.Width(right)
	if loc != "" {
		avail -= 2
	}
	if tw, lw := ansi.StringWidth(text), ansi.StringWidth(loc); tw+lw > avail {
		tb := min(tw, max(avail-lw, avail*3/5))
		text, loc = truncate(text, max(1, tb)), leftTruncate(loc, avail-tb)
	}
	if loc != "" {
		right = "  " + loc + right
	}
	p.add(left+text+right, unit)
}

// leftTruncate keeps the last n cells of s, marking the cut with "…".
func leftTruncate(s string, n int) string {
	if ansi.StringWidth(s) <= n {
		return s
	}
	if n <= 1 {
		return strings.Repeat("…", max(0, n))
	}
	rs := []rune(s)
	return "…" + string(rs[len(rs)-(n-1):])
}

// surfaceGlyph colours a surface row's change glyph.
func surfaceGlyph(st styles, g string) string {
	switch g {
	case "+":
		return st.add.Render(g)
	case "−":
		return st.del.Render(g)
	}
	return st.accent.Render(g)
}

// branchStateOrder is the catalogue order of the branch states (ties in the
// options line).
var branchStateOrder = []string{model.BranchReady, model.BranchWIP, model.BranchSpike, model.BranchAbandoned, model.BranchSuperseded}

// branchBlock draws the branch state box (spec Overview, "Branches"): the
// state with its probability, the rule that set it, every option with its
// probability, the unpushed commits (capped) and the suggested PR title.
func branchBlock(p *page, d proto.Detail, full bool) {
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
	shown := capped(len(b.Commits), capCommits, full)
	for _, c := range b.Commits[:shown] {
		p.add("  "+p.st.dim.Render(shortSHA(c.SHA))+" "+c.Subject, "")
	}
	p.more(len(b.Commits)-shown, "more")
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

// findBlock returns d's block titled title (grid boxes and lead boxes).
func findBlock(d proto.Detail, o ovOpts, title string) (blockSpec, bool) {
	lead, cells := overviewCells(d, o)
	for _, b := range lead {
		if b.title == title {
			return b, true
		}
	}
	for _, c := range cells {
		for _, b := range c {
			if b.title == title {
				return b, true
			}
		}
	}
	return blockSpec{}, false
}

// fullBlock renders block title of d with every row (the full-list
// overlay) at width; ok is false for a block without a list.
func fullBlock(st styles, d proto.Detail, o ovOpts, title string, width int) (*page, bool) {
	b, ok := findBlock(d, o, title)
	if !ok || !b.list {
		return nil, false
	}
	q := newPage(st, width)
	b.fill(q, true)
	return q, true
}

// rawLabelsFor renders the `L` overlay for unit id of d with labels (the
// detail's, or those fetched on demand).
func rawLabelsFor(d proto.Detail, labels []model.Label, id string, width int) string {
	return codeview.RawLabelsText(unitOf(d, id), labels, width)
}

// unitOf is unit id of d (its path from the aggregate when the detail has
// no units).
func unitOf(d proto.Detail, id string) model.ChangeUnit {
	for _, cu := range d.Units {
		if cu.ID == id {
			return cu
		}
	}
	u := model.ChangeUnit{ID: id}
	if d.Agg != nil {
		for _, ua := range d.Agg.Units {
			if ua.UnitID == id {
				u.Path = ua.Path
				break
			}
		}
	}
	return u
}
