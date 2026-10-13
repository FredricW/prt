package tui

import (
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// fileStat is one row of the git-facts file list: the file's counts and,
// for the list's order and anchors, whether it is owned and its first unit.
type fileStat struct {
	path       string
	adds, dels int
	owned      bool
	unit       string
}

// markdown renders PR descriptions with glamour. Without colour (snapshots,
// pipes) it uses the "notty" style so output is plain and deterministic.
type markdown struct {
	style string
	r     *lipgloss.Renderer
	cache map[int]*glamour.TermRenderer // by wrap width
}

func newMarkdown(r *lipgloss.Renderer) *markdown {
	style := "notty"
	if hasColour(r) {
		style = "light"
		if r.HasDarkBackground() {
			style = "dark"
		}
	}
	return &markdown{style: style, r: r, cache: map[int]*glamour.TermRenderer{}}
}

// render returns body as wrapped terminal text; on a glamour error it falls
// back to the raw body.
func (md *markdown) render(body string, width int) string {
	width = max(20, width)
	tr, ok := md.cache[width]
	if !ok {
		var err error
		tr, err = glamour.NewTermRenderer(
			glamour.WithStandardStyle(md.style),
			glamour.WithColorProfile(md.r.ColorProfile()),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return body
		}
		md.cache[width] = tr
	}
	out, err := tr.Render(body)
	if err != nil {
		return body
	}
	return strings.Trim(trimRight(out), "\n")
}

// overview is the Overview tab: a scrolling page plus, per rendered line,
// the unit id behind it (anchors) so `enter` and `L` know which unit the
// block at the viewport top refers to. raw is an open overlay (`L`, `w`,
// or a block's full list).
type overview struct {
	vp      viewport.Model
	anchors []string           // per content line; "" when the line has no unit
	spans   map[int][]span     // per content line: clickable unit ranges
	cells   map[int][]cellRow  // per content line with unit cells (minimap)
	mores   map[int][]moreSpan // per content line: "+N more" ranges
	boxes   map[string]int     // box title → line of its top border
	rects   map[string]rect    // box title → its cells
	order   []string           // box titles in reading order
	focus   string             // the focused block's title ("" none; M5.2)
	list    *listOverlay       // the open full-list overlay (raw is set too)
	rawUnit string             // the unit the raw-labels overlay shows
	raw     bool               // an overlay is open (raw labels, urgency weights, a full list)
	rawVP   viewport.Model
	// weights marks the open overlay as the urgency weights (`w`), which
	// `w` closes; otherwise it is the raw labels (`L`) or a full list.
	weights bool
}

func newOverview() overview {
	return overview{vp: viewport.New(80, 20), rawVP: viewport.New(80, 20)}
}

func (o *overview) setSize(w, h int) {
	o.vp.Width, o.vp.Height = max(1, w), max(1, h)
	o.rawVP.Width, o.rawVP.Height = max(1, w), max(1, h)
}

// setContent replaces the page and its anchors and scrolls to the top.
func (o *overview) setContent(s string, anchors []string) {
	o.vp.SetContent(s)
	o.anchors, o.spans, o.cells, o.mores, o.boxes, o.rects, o.order = anchors, nil, nil, nil, nil, nil, nil
	o.vp.GotoTop()
}

// setPage is setContent for a rendered page, keeping its spans, cell runs,
// "+N more" lines and box positions. A focused block that is gone loses
// the focus.
func (o *overview) setPage(p *page) {
	o.setContent(strings.Join(p.lines, "\n"), p.anchors)
	o.spans, o.cells, o.mores, o.boxes, o.rects = p.spans, p.cells, p.mores, p.boxes, p.rects
	o.order = readingOrder(p.rects)
	if _, ok := o.rects[o.focus]; !ok {
		o.focus = ""
	}
}

// readingOrder sorts box titles top to bottom, then left to right.
func readingOrder(rects map[string]rect) []string {
	out := make([]string, 0, len(rects))
	for t := range rects {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := rects[out[i]], rects[out[j]]
		if a.y0 != b.y0 {
			return a.y0 < b.y0
		}
		if a.x0 != b.x0 {
			return a.x0 < b.x0
		}
		return out[i] < out[j]
	})
	return out
}

// scrollToBlock scrolls so the box titled title starts at the top and
// reports whether the page has it.
func (o *overview) scrollToBlock(title string) bool {
	i, ok := o.boxes[title]
	if ok {
		o.vp.SetYOffset(i)
	}
	return ok
}

// clickAt returns the unit behind a click on content line i at display
// column x: the cell's unit on a minimap row, else the unit of the range
// (box row) under x.
func (o overview) clickAt(i, x int) (string, bool) {
	for _, c := range o.cells[i] {
		if id, ok := c.at(x); ok {
			return id, true
		}
	}
	for _, s := range o.spans[i] {
		if x >= s.col && x < s.end {
			return s.unit, true
		}
	}
	return "", false
}

// anchor returns the unit behind the first anchored line at or after the
// viewport top.
func (o overview) anchor() (string, bool) {
	return o.anchorFrom(o.vp.YOffset)
}

// anchorFrom returns the first anchor at or after content line i.
func (o overview) anchorFrom(i int) (string, bool) {
	for i = max(0, i); i < len(o.anchors); i++ {
		if o.anchors[i] != "" {
			return o.anchors[i], true
		}
	}
	return "", false
}

// moreAt returns the block whose "+N more" line is at content line i,
// column x.
func (o overview) moreAt(i, x int) (string, bool) {
	for _, s := range o.mores[i] {
		if x >= s.col && x < s.end {
			return s.block, true
		}
	}
	return "", false
}

// boxAt returns the box under content line i, column x.
func (o overview) boxAt(i, x int) (string, bool) {
	for _, t := range o.order {
		if o.rects[t].contains(i, x) {
			return t, true
		}
	}
	return "", false
}

// boxAnchor returns the first unit anchored inside box title.
func (o overview) boxAnchor(title string) (string, bool) {
	r, ok := o.rects[title]
	if !ok {
		return "", false
	}
	for i := r.y0; i <= r.y1 && i < len(o.anchors); i++ {
		for _, s := range o.spans[i] {
			if s.col >= r.x0 && s.col < r.x1 {
				return s.unit, true
			}
		}
		for _, c := range o.cells[i] {
			if c.col >= r.x0 && c.col < r.x1 && len(c.ids) > 0 {
				return c.ids[0], true
			}
		}
	}
	return "", false
}

// moveFocus moves the block focus dir steps through the boxes in reading
// order. Without a focused block, forward focuses the first box starting
// at or below the viewport top, backward the last one starting above its
// bottom. The viewport scrolls so the focused box is in view.
func (o *overview) moveFocus(dir int) bool {
	if len(o.order) == 0 {
		return false
	}
	i := -1
	for j, t := range o.order {
		if t == o.focus {
			i = j
		}
	}
	switch {
	case i >= 0:
		i = max(0, min(len(o.order)-1, i+dir))
	case dir > 0:
		i = len(o.order) - 1
		for j, t := range o.order {
			if o.rects[t].y0 >= o.vp.YOffset {
				i = j
				break
			}
		}
	default:
		i = 0
		for j, t := range o.order {
			if o.rects[t].y0 < o.vp.YOffset+o.vp.Height {
				i = j
			}
		}
	}
	o.focus = o.order[i]
	o.revealFocus()
	return true
}

// revealFocus scrolls so the focused box is in view (its top first when it
// is taller than the viewport).
func (o *overview) revealFocus() {
	r, ok := o.rects[o.focus]
	if !ok {
		return
	}
	switch {
	case r.y0 < o.vp.YOffset:
		o.vp.SetYOffset(r.y0)
	case r.y1 >= o.vp.YOffset+o.vp.Height:
		o.vp.SetYOffset(min(r.y0, r.y1-o.vp.Height+1))
	}
}

// openRaw shows the raw-labels overlay with body.
func (o *overview) openRaw(body string) {
	o.raw, o.weights, o.list, o.rawUnit = true, false, nil, ""
	o.rawVP.SetContent(body)
	o.rawVP.GotoTop()
}

// openWeights shows the urgency-weights overlay with body.
func (o *overview) openWeights(body string) {
	o.openRaw(body)
	o.weights = true
}

// openList shows a block's full list with a row cursor on its first
// anchored row.
func (o *overview) openList(l *listOverlay) {
	o.openRaw("")
	o.list = l
	l.cursor = l.next(-1, 1)
	o.drawList()
	o.rawVP.GotoTop()
}

// drawList renders the open list into the overlay viewport and keeps the
// cursor row in view.
func (o *overview) drawList() {
	l := o.list
	if l == nil {
		return
	}
	o.rawVP.SetContent(strings.Join(l.render(), "\n"))
	if l.cursor < 0 {
		return
	}
	row := l.head + l.cursor
	switch {
	case row < o.rawVP.YOffset:
		o.rawVP.SetYOffset(row)
	case row >= o.rawVP.YOffset+o.rawVP.Height:
		o.rawVP.SetYOffset(row - o.rawVP.Height + 1)
	}
}

func (o *overview) closeRaw() { o.raw, o.weights, o.list, o.rawUnit = false, false, nil, "" }

// listOverlay is a block's full list (M5.2): every row of the block, the
// anchored ones selectable with a cursor; enter jumps to the cursor row's
// unit.
type listOverlay struct {
	block   string
	header  []string // title, hint and a blank line
	lines   []string // the block's rows
	anchors []string // per row; "" when the row has no unit
	cursor  int      // index into lines of the selected row; -1 none
	head    int      // len(header)
	sel     func(string) string
}

// newListOverlay builds the overlay of block title from its full page.
func newListOverlay(st styles, title, hint string, p *page) *listOverlay {
	header := []string{st.title.Render(title) + "  " + st.dim.Render(hint), ""}
	return &listOverlay{block: title, header: header, lines: p.lines, anchors: p.anchors, cursor: -1, head: len(header),
		sel: func(s string) string { return st.sel.Render(s) }}
}

// next returns the anchored row after (dir 1) or before (dir -1) row i;
// i itself (or -1) when there is none.
func (l *listOverlay) next(i, dir int) int {
	for j := i + dir; j >= 0 && j < len(l.lines); j += dir {
		if l.anchors[j] != "" {
			return j
		}
	}
	if i >= 0 && i < len(l.lines) && l.anchors[i] != "" {
		return i
	}
	return -1
}

// move moves the cursor dir anchored rows; it reports false when no row is
// selectable (the overlay then scrolls instead).
func (l *listOverlay) move(dir int) bool {
	if l.cursor < 0 {
		return false
	}
	l.cursor = l.next(l.cursor, dir)
	return true
}

// unit is the cursor row's unit.
func (l *listOverlay) unit() (string, bool) {
	if l.cursor < 0 || l.cursor >= len(l.anchors) || l.anchors[l.cursor] == "" {
		return "", false
	}
	return l.anchors[l.cursor], true
}

// rowAt returns the row at overlay content line i, when anchored.
func (l *listOverlay) rowAt(i int) (int, bool) {
	r := i - l.head
	if r < 0 || r >= len(l.lines) || l.anchors[r] == "" {
		return 0, false
	}
	return r, true
}

// render is the overlay's content: the header, then the rows, the cursor
// row marked "▸ " (and highlighted with colour).
func (l *listOverlay) render() []string {
	out := append([]string(nil), l.header...)
	for i, s := range l.lines {
		if i == l.cursor {
			out = append(out, l.sel("▸ ")+s)
			continue
		}
		out = append(out, "  "+s)
	}
	return out
}

func (o overview) view() string {
	if o.raw {
		return o.rawVP.View()
	}
	return o.vp.View()
}

// titleLine is the spec's title line: number, title, author, age, base ← head.
func titleLine(it model.Item, now time.Time) string {
	label := itemLabel(it)
	if it.Kind == model.KindBranch && it.Title != "" && it.Title != it.Ref {
		label += " — " + it.Title
	}
	parts := []string{label}
	if it.Author != "" {
		parts = append(parts, it.Author)
	}
	if a := age(it.UpdatedAt, now); a != "" {
		parts = append(parts, a)
	}
	parts = append(parts, orDash(it.BaseRef)+" ← "+orDash(it.Ref))
	return strings.Join(parts, " · ")
}

// overviewContext is the tab bar's dim context on the Overview (design:
// "@ingrid · 2d · main ← feat/jwt-session").
func overviewContext(it model.Item, now time.Time) string {
	var parts []string
	if it.Author != "" {
		parts = append(parts, "@"+it.Author)
	}
	if a := age(it.UpdatedAt, now); a != "" {
		parts = append(parts, a)
	}
	parts = append(parts, orDash(it.BaseRef)+" ← "+orDash(it.Ref))
	return strings.Join(parts, " · ")
}

// totals prefers the aggregate's counts, then the item's, then the files.
func totals(d proto.Detail, files []fileStat) (int, int) {
	if d.Agg != nil && (d.Agg.Added != 0 || d.Agg.Removed != 0) {
		return d.Agg.Added, d.Agg.Removed
	}
	if d.Item.Additions != 0 || d.Item.Deletions != 0 {
		return d.Item.Additions, d.Item.Deletions
	}
	var a, r int
	for _, f := range files {
		a, r = a+f.adds, r+f.dels
	}
	return a, r
}

// statsFromDetail returns per-file counts from Detail.Files.
func statsFromDetail(fs []model.File) []fileStat {
	out := make([]fileStat, 0, len(fs))
	for _, f := range fs {
		p := f.Path
		if f.Renamed && f.OldPath != "" {
			p = f.OldPath + " → " + f.Path
		}
		out = append(out, fileStat{path: p, adds: f.Adds, dels: f.Dels, owned: f.Owned})
	}
	return out
}

// gitFiles is the git-facts Files list of d by importance: owned files
// first, then by changed lines (most first), then by path; each row
// anchored to the file's first unit (the aggregate's line order when it has
// the file, else the first of d.Units), so enter or a click opens it in
// Code.
func gitFiles(d proto.Detail) []fileStat {
	first := map[string]string{}
	if d.Agg != nil {
		for _, fa := range d.Agg.Files {
			if len(fa.UnitIDs) > 0 {
				first[fa.Path] = fa.UnitIDs[0]
			}
		}
	}
	for _, u := range d.Units {
		if _, ok := first[u.Path]; !ok {
			first[u.Path] = u.ID
		}
	}
	out := statsFromDetail(d.Files)
	for i, f := range d.Files {
		out[i].unit = first[f.Path]
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.owned != b.owned {
			return a.owned
		}
		if a.adds+a.dels != b.adds+b.dels {
			return a.adds+a.dels > b.adds+b.dels
		}
		return a.path < b.path
	})
	return out
}

func padLeft(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return strings.Repeat(" ", n-w) + s
	}
	return s
}

func padRight(s string, n int) string {
	if w := lipgloss.Width(s); w < n {
		return s + strings.Repeat(" ", n-w)
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}
