package codeview

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
)

const (
	ellipsis = "…"
	// maxCachedRows bounds the rendered-row cache; it is cleared when full.
	maxCachedRows = 2048
)

// styles are built once per renderer. With a no-colour renderer every
// style renders plain text, so snapshots are deterministic.
type styles struct {
	bar, barOn, barDim, note                lipgloss.Style
	listSel, listItem, divider              lipgloss.Style
	title, overlayTitle                     lipgloss.Style
	header, chipWarn, prob, sig, moved      lipgloss.Style
	collapsed, hidden                       lipgloss.Style
	gutter, add, del, ctx, addEmph, delEmph lipgloss.Style
	noEOL                                   lipgloss.Style
	markHi, markMid, markLow, markValue     lipgloss.Style
	markLeft, markNone                      lipgloss.Style
	cellEyes, cellSkim, cellSkip            lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	green, red, yellow := lipgloss.Color("2"), lipgloss.Color("1"), lipgloss.Color("3")
	faint := r.NewStyle().Faint(true)
	return styles{
		bar: r.NewStyle(), barOn: r.NewStyle().Bold(true), barDim: faint, note: faint.Italic(true),
		listSel: r.NewStyle().Bold(true), listItem: r.NewStyle(), divider: faint,
		title: r.NewStyle().Bold(true).Reverse(true), overlayTitle: r.NewStyle().Bold(true).Reverse(true),
		header: r.NewStyle().Bold(true), chipWarn: r.NewStyle().Bold(true).Foreground(red),
		prob: faint, sig: faint, moved: faint.Italic(true),
		collapsed: faint, hidden: faint,
		gutter: faint, add: r.NewStyle().Foreground(green), del: r.NewStyle().Foreground(red), ctx: r.NewStyle(),
		addEmph: r.NewStyle().Foreground(green).Bold(true).Underline(true),
		delEmph: r.NewStyle().Foreground(red).Bold(true).Underline(true),
		noEOL:   faint.Italic(true),
		markHi:  r.NewStyle().Foreground(red).Bold(true), markMid: r.NewStyle().Foreground(yellow),
		markLow: faint, markValue: r.NewStyle().Foreground(lipgloss.Color("6")),
		markLeft: r.NewStyle().Foreground(yellow), markNone: faint,
		cellEyes: r.NewStyle().Foreground(red), cellSkim: r.NewStyle().Foreground(yellow), cellSkip: faint,
	}
}

// renderCache holds rendered pane rows for one width and intra-line spans;
// shared by Model copies so View (value receiver) can fill it.
type renderCache struct {
	width   int
	lines   map[int]string
	spans   map[int][]diff.Span
	renders int // rows built; lets tests check laziness
}

func newRenderCache() *renderCache {
	return &renderCache{lines: map[int]string{}, spans: map[int][]diff.Span{}}
}

// View renders the component at its size: exactly h lines, none wider
// than w cells.
func (m Model) View() string {
	if m.c.plain() {
		return m.dv.View()
	}
	lay := m.layout()
	out := make([]string, 0, m.h)
	if lay.top == 1 {
		out = append(out, m.topLine())
	}
	list := m.listLines(lay)
	pane := m.paneLines(lay)
	for i := range lay.bodyH {
		if lay.listW > 0 {
			out = append(out, list[i]+" "+pane[i])
		} else {
			out = append(out, pane[i])
		}
	}
	return strings.Join(out, "\n")
}

// topLine is the bar line: a one-shot note, the filters bar, or the
// labels-unavailable note.
func (m Model) topLine() string {
	switch {
	case m.note != "":
		return m.st.note.Render(fit(m.note, m.w))
	case m.barOpen:
		return ansi.Truncate(m.barText(), m.w, ellipsis)
	default:
		return m.st.note.Render(fit("labels unavailable — filters off", m.w))
	}
}

// barText renders the filter chips: "[1 needs eyes ✓] [4 risk ≥ 3] …".
func (m Model) barText() string {
	parts := make([]string, 0, len(AllFilters)+1)
	for i, f := range AllFilters {
		s := "[" + strconv.Itoa(i+1) + " " + filterLabel(f)
		st := m.st.bar
		switch {
		case !hasData(f):
			st = m.st.barDim
		case m.filters[i]:
			s += " ✓"
			st = m.st.barOn
		}
		parts = append(parts, st.Render(s+"]"))
	}
	if !m.filtersApply() {
		parts = append(parts, m.st.note.Render("— filters off (labels unavailable)"))
	}
	return strings.Join(parts, " ")
}

// listLines renders the file list, bodyH lines padded to listW.
func (m Model) listLines(lay layout) []string {
	out := make([]string, lay.bodyH)
	if lay.listW == 0 {
		return out
	}
	rows := m.listRows()
	for i := range out {
		s := ""
		if ri := m.listOff + i; ri < len(rows) {
			s = m.listRow(rows[ri], lay.listW)
		}
		out[i] = pad(s, lay.listW)
	}
	return out
}

// listRow renders the divider (fi < 0) or file fi: selection marker,
// left-truncated path, then minimap cells right-aligned.
func (m Model) listRow(fi, w int) string {
	if fi < 0 {
		return m.st.divider.Render(ansi.Truncate("── not yours "+strings.Repeat("─", w), w, ""))
	}
	f := m.c.files[fi]
	marker, st := "  ", m.st.listItem
	if fi == m.sel {
		marker, st = "▸ ", m.st.listSel
	}
	cells, cellsW := m.minimap(f, max(1, (w-2)/3))
	avail := w - 2
	if cellsW > 0 {
		avail -= cellsW + 1
	}
	path := pad(leftTrunc(diffview.Clean(f.path), max(1, avail)), max(1, avail))
	s := st.Render(marker + path)
	if cellsW > 0 {
		s += " " + cells
	}
	return ansi.Truncate(s, w, ellipsis)
}

// minimap renders one cell per unit (at most limit; the last becomes "+"
// when there are more) and returns the cells and their width.
func (m Model) minimap(f fileEntry, limit int) (string, int) {
	if !m.c.hasAgg || len(f.units) == 0 {
		return "", 0
	}
	n := len(f.units)
	more := n > limit
	if more {
		n = limit - 1
	}
	var b strings.Builder
	for _, id := range f.units[:n] {
		a := m.c.aggs[id]
		if a == nil {
			b.WriteString(" ")
			continue
		}
		b.WriteString(m.cellStyle(a.Attention).Render(AttentionGlyph(a.Attention, a.Unsure)))
	}
	if more {
		b.WriteString("+")
		n++
	}
	return b.String(), n
}

func (m Model) cellStyle(a model.Attention) lipgloss.Style {
	switch a {
	case model.AttnEyes:
		return m.st.cellEyes
	case model.AttnSkim:
		return m.st.cellSkim
	}
	return m.st.cellSkip
}

// paneLines renders the right pane: the file title (or overlay title),
// then the visible rows; bodyH lines.
func (m Model) paneLines(lay layout) []string {
	out := make([]string, lay.bodyH)
	if lay.bodyH == 0 || lay.paneW <= 0 {
		return out
	}
	if m.overlay {
		return m.overlayView(lay, out)
	}
	out[0] = m.st.title.Render(pad(fit(m.fileTitle(lay), lay.paneW), lay.paneW))
	if m.cache.width != lay.paneW {
		m.cache.width = lay.paneW
		m.cache.lines = map[int]string{}
	}
	for i := 1; i < lay.bodyH; i++ {
		ri := m.off + i - 1
		if ri >= len(m.p.rows) {
			break
		}
		out[i] = m.renderRow(ri, lay.paneW)
	}
	return out
}

// fileTitle is " path  +a −d", plus "(not yours)" and, when the list is
// hidden, "i/n".
func (m Model) fileTitle(lay layout) string {
	if len(m.c.files) == 0 {
		return " no files"
	}
	f := m.c.files[m.sel]
	s := " " + diffview.Clean(f.path) + "  +" + strconv.Itoa(f.adds) + " −" + strconv.Itoa(f.dels)
	if !f.owned {
		s += "  (not yours)"
	}
	if lay.listW == 0 {
		s += "  " + strconv.Itoa(m.sel+1) + "/" + strconv.Itoa(len(m.c.files))
	}
	return s
}

// overlayView draws the raw-labels overlay over the pane.
func (m Model) overlayView(lay layout, out []string) []string {
	title := " raw labels — esc/L closes"
	out[0] = m.st.overlayTitle.Render(pad(fit(title, lay.paneW), lay.paneW))
	lines := m.overlayLines(lay.paneW)
	for i := 1; i < lay.bodyH; i++ {
		li := m.overlayOff + i - 1
		if li >= len(lines) {
			break
		}
		out[i] = fit(diffview.Clean(lines[li]), lay.paneW)
	}
	return out
}

// renderRow returns pane row i, from the cache when possible.
func (m Model) renderRow(i, width int) string {
	if s, ok := m.cache.lines[i]; ok {
		return s
	}
	s := m.buildRow(i, width)
	m.cache.renders++
	if len(m.cache.lines) >= maxCachedRows {
		m.cache.lines = map[int]string{}
	}
	m.cache.lines[i] = s
	return s
}

// buildRow styles pane row i; the result is at most width cells.
func (m Model) buildRow(i, width int) string {
	r := m.p.rows[i]
	switch r.kind {
	case rowHeader:
		return ansi.Truncate(m.headerText(m.p.units[r.unit], int(r.line)), width, ellipsis)
	case rowSig:
		return m.st.sig.Render(fit("│ "+diffview.Clean(sigText(m.p.units[r.unit].u)), width))
	case rowMoved:
		return m.st.moved.Render(fit("│ "+diffview.Clean(m.movedText(m.p.units[r.unit].u)), width))
	case rowLine:
		return m.lineRow(i, r, width)
	case rowNoEOL:
		text := strings.Repeat(" ", 2*m.p.gutter+4) + `\ No newline at end of file`
		return m.st.noEOL.Render(fit(text, width))
	case rowCollapsed:
		return m.st.collapsed.Render(fit("▸ "+diffview.Clean(m.collapsedText(m.p.units[r.unit])), width))
	case rowNote:
		return m.st.hidden.Render(fit(m.p.notes[r.line], width))
	}
	return ""
}

// headerText renders row k of the unit's chip strip.
func (m Model) headerText(pu paneUnit, k int) string {
	span := pu.chipRows[k]
	cs := pu.chips[span[0]:span[1]]
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		st := m.st.header
		if c.warn {
			st = m.st.chipWarn
		}
		s := st.Render(c.text)
		if c.prob != "" {
			s += " " + m.st.prob.Render(c.prob)
		}
		parts = append(parts, s)
	}
	prefix := "┌ "
	if k > 0 {
		prefix = "│ "
	}
	return m.st.header.Render(prefix) + strings.Join(parts, m.st.sig.Render(chipSep))
}

// sigText is "L12–30 · +a −d · signature-or-symbol" (the range first so a
// long signature is what gets truncated).
func sigText(u *model.ChangeUnit) string {
	parts := []string{rangeText(u.Range), "+" + strconv.Itoa(u.Adds) + " −" + strconv.Itoa(u.Dels)}
	if s := u.Signature; s != "" {
		parts = append(parts, s)
	} else if u.Symbol != "" {
		parts = append(parts, u.Symbol)
	}
	return strings.Join(parts, " · ")
}

// rangeText is the new-side range ("L12–30"), or the old side for a pure
// deletion ("old L12–14").
func rangeText(r model.Range) string {
	span, prefix := r.New, "L"
	if span.Count == 0 && r.Old.Count > 0 {
		span, prefix = r.Old, "old L"
	}
	if span.Count <= 1 {
		return prefix + strconv.Itoa(span.Start)
	}
	return prefix + strconv.Itoa(span.Start) + "–" + strconv.Itoa(span.Start+span.Count-1)
}

// movedText is "moved from <path>:<line>" for a unit with MovedFrom.
func (m Model) movedText(u *model.ChangeUnit) string {
	o := m.c.units[u.MovedFrom]
	if o == nil {
		return "moved (origin not in this diff)"
	}
	line := o.Range.Old.Start
	if o.Range.Old.Count == 0 {
		line = o.Range.New.Start
	}
	return "moved from " + o.Path + ":" + strconv.Itoa(line)
}

// collapsedText is the one-line noise summary.
func (m Model) collapsedText(pu paneUnit) string {
	reason := "noise"
	if pu.a != nil {
		switch pu.a.NoiseReason {
		case model.NoiseFmtLines:
			reason = "formatting only"
		case "":
		default:
			reason = pu.a.NoiseReason
		}
	}
	if pu.u.MovedFrom != "" {
		reason = m.movedText(pu.u)
	}
	parts := []string{reason}
	if s := pu.u.Symbol; s != "" {
		parts = append(parts, s)
	}
	parts = append(parts, "+"+strconv.Itoa(pu.u.Adds)+" −"+strconv.Itoa(pu.u.Dels))
	return strings.Join(parts, " · ") + " (z to expand)"
}

// lineRow renders "mark old new op text" with intra-line emphasis.
func (m Model) lineRow(i int, r prow, width int) string {
	pu := m.p.units[r.unit]
	l := pu.lines[r.line]
	g := m.p.gutter
	base, emph := m.lineStyles(l.Op)
	text, cut := diffview.Clip(l.Text, 4*width+64)
	var spans []diff.Span
	if r.pair >= 0 {
		spans = m.intraSpans(i, int(r.pair))
	}
	var b strings.Builder
	b.WriteString(m.markCell(pu, l))
	b.WriteString(" ")
	b.WriteString(m.st.gutter.Render(num(l.OldNo, g) + " " + num(l.NewNo, g) + " "))
	b.WriteString(base.Render(string(rune(l.Op)) + " "))
	diffview.WriteSegments(&b, text, spans, base, emph)
	if cut {
		b.WriteString(base.Render(ellipsis))
	}
	return ansi.Truncate(b.String(), width, ellipsis)
}

func (m Model) lineStyles(op diff.Op) (base, emph lipgloss.Style) {
	switch op {
	case diff.Add:
		return m.st.add, m.st.addEmph
	case diff.Del:
		return m.st.del, m.st.delEmph
	}
	return m.st.ctx, m.st.ctx
}

// markCell is the one-cell gutter mark of a line: blank for deleted and
// context lines and before labels exist; MarkGlyph for added lines, with
// ◆ shaded by its suspicious score.
func (m Model) markCell(pu paneUnit, l diff.Line) string {
	if l.Op != diff.Add || !m.c.labelled || pu.a == nil {
		return " "
	}
	lm, ok := pu.marks[l.NewNo]
	if !ok {
		return m.st.markNone.Render(MarkGlyph(model.MarkNone))
	}
	st := m.st.markNone
	switch lm.Mark {
	case model.MarkSuspicious:
		switch {
		case lm.Score >= 4:
			st = m.st.markHi
		case lm.Score >= 2.5:
			st = m.st.markMid
		default:
			st = m.st.markLow
		}
	case model.MarkValue:
		st = m.st.markValue
	case model.MarkLeftover:
		st = m.st.markLeft
	}
	return st.Render(MarkGlyph(lm.Mark))
}

// intraSpans returns the changed spans of row i paired with row p,
// computing both sides once.
func (m Model) intraSpans(i, p int) []diff.Span {
	if s, ok := m.cache.spans[i]; ok {
		return s
	}
	ra, rb := m.p.rows[i], m.p.rows[p]
	la := m.p.units[ra.unit].lines[ra.line]
	lb := m.p.units[rb.unit].lines[rb.line]
	oldRow, newRow, oldText, newText := i, p, la.Text, lb.Text
	if la.Op == diff.Add {
		oldRow, newRow, oldText, newText = p, i, lb.Text, la.Text
	}
	oldSpans, newSpans := diffview.IntraSpans(oldText, newText)
	m.cache.spans[oldRow], m.cache.spans[newRow] = oldSpans, newSpans
	if i == oldRow {
		return oldSpans
	}
	return newSpans
}

// num right-aligns n in w cells; 0 renders as blanks.
func num(n, w int) string {
	if n == 0 {
		return strings.Repeat(" ", w)
	}
	s := strconv.Itoa(n)
	return strings.Repeat(" ", max(0, w-len(s))) + s
}

// fit truncates text to width cells, ending in "…" when cut.
func fit(s string, width int) string { return ansi.Truncate(s, width, ellipsis) }

// pad right-pads s (which may hold escape codes) with spaces to width cells.
func pad(s string, width int) string {
	if n := width - ansi.StringWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// leftTrunc shortens a path from the left to width cells, preferring a
// directory boundary ("…/api/client.ts"), else cutting runes ("…ient.ts").
func leftTrunc(p string, width int) string {
	if ansi.StringWidth(p) <= width {
		return p
	}
	if width <= 1 {
		return ellipsis
	}
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			if s := ellipsis + p[i:]; ansi.StringWidth(s) <= width {
				return s
			}
		}
	}
	r := []rune(p)
	for i := range r {
		if s := ellipsis + string(r[i:]); ansi.StringWidth(s) <= width {
			return s
		}
	}
	return ellipsis
}
