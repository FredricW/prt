package codeview

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/theme"
)

const (
	ellipsis = "…"
	// sigLead indents a unit's signature and "moved from" rows under its
	// "@@ N" chip strip.
	sigLead = "  "
	// maxCachedRows bounds the rendered-row cache; it is cleared when full.
	maxCachedRows = 2048
)

// styles are built once per renderer from the shared theme. With a
// no-colour renderer every style renders plain text, so snapshots are
// deterministic. Diff line text is styled by the diffview.Painter.
type styles struct {
	bar, barOn, barDim, note   lipgloss.Style
	listSel, listItem, divider lipgloss.Style
	title, overlayTitle        lipgloss.Style
	header, chipWarn, prob     lipgloss.Style
	sig, moved                 lipgloss.Style
	collapsed, hidden          lipgloss.Style
	gutter, ctx, noEOL         lipgloss.Style
	markNone                   lipgloss.Style
	placeholder                lipgloss.Style // placeholder group names
	// Comments layer: block headers, dim borders and bars, the ❱ gutter
	// mark, the strip line and the composer/panel titles.
	cHead, cDim, threadMark, strip, panelTitle lipgloss.Style
	// Filter chips with colour: on (filled accent), off (dim).
	fltOn, fltOff lipgloss.Style
	// bar is the findings block's left bar (red).
	findBar lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	th := theme.New(r)
	faint := r.NewStyle().Faint(true)
	return styles{
		bar: r.NewStyle(), barOn: r.NewStyle().Bold(true), barDim: faint, note: th.Dim.Italic(true),
		listSel: th.Selected, listItem: th.Text, divider: th.Dim,
		title: r.NewStyle().Bold(true).Reverse(true), overlayTitle: r.NewStyle().Bold(true).Reverse(true),
		header: th.Bold, chipWarn: r.NewStyle().Bold(true).Foreground(th.P.WarnFg),
		prob: th.Dim, sig: th.Dim, moved: th.Dim.Italic(true),
		collapsed: th.Dim, hidden: th.Dim,
		gutter: th.Dim, ctx: th.Text, noEOL: th.Dim.Italic(true),
		markNone:    r.NewStyle().Foreground(th.P.Faint),
		placeholder: th.Dim.Italic(true),
		cHead:       r.NewStyle().Bold(true), cDim: faint,
		threadMark: th.Blue.Bold(true),
		strip:      r.NewStyle().Bold(true), panelTitle: r.NewStyle().Bold(true).Reverse(true),
		fltOn:   r.NewStyle().Background(th.P.Accent).Foreground(th.P.Bg),
		fltOff:  th.Dim,
		findBar: th.Del,
	}
}

// renderCache holds rendered pane rows for one width and layout, and the
// intra-line spans (unified: by row; side-by-side: by pair); shared by
// Model copies so View (value receiver) can fill it.
type renderCache struct {
	width   int
	split   bool
	lines   map[int]string
	spans   map[int][]diff.Span
	pairs   map[int][2][]diff.Span
	renders int // rows built; lets tests check laziness
}

func newRenderCache() *renderCache {
	return &renderCache{lines: map[int]string{}, spans: map[int][]diff.Span{}, pairs: map[int][2][]diff.Span{}}
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
	body := m.bodyLines(lay)
	if m.panel != nil {
		body = m.panelOverlay(body)
	}
	out = append(out, body...)
	if lay.stripH > 0 {
		out = append(out, m.stripLines(lay.stripH)...)
	}
	return strings.Join(out, "\n")
}

// bodyLines renders the list and the pane, bodyH lines: in titled boxes
// (design), or below narrowBelow as the pane alone under a plain title row.
func (m Model) bodyLines(lay layout) []string {
	body := make([]string, lay.bodyH)
	if lay.bodyH == 0 {
		return body
	}
	var list []string
	if lay.listW > 0 {
		if m.byChange() {
			list = m.groupListLines(lay)
		} else {
			list = m.listLines(lay)
		}
	}
	title, right, pane := m.paneView(lay)
	if !lay.boxed {
		pane = append([]string{m.st.title.Render(pad(fit(" "+title, lay.paneW), lay.paneW))}, pane...)
		for i := range body {
			if lay.listW > 0 {
				body[i] = list[i] + " " + pane[i]
			} else {
				body[i] = pane[i]
			}
		}
		return body
	}
	listTitle := "Files · by attention"
	if m.byChange() {
		listTitle = "Change groups · review order"
	}
	lb := strings.Split(m.th.Box(listTitle, strings.Join(list, "\n"), lay.listW, theme.BoxOpt{Height: lay.bodyH, NoPad: true}), "\n")
	pb := strings.Split(m.th.Box(title, strings.Join(pane, "\n"), m.w-lay.listW-1,
		theme.BoxOpt{Height: lay.bodyH, NoPad: true, Right: right}), "\n")
	for i := range body {
		body[i] = lb[i] + " " + pb[i]
	}
	return body
}

// topLine is the bar line: a one-shot note, the filters bar, or the
// standing notes (side-by-side degraded, labels unavailable, embeddings
// skipped).
func (m Model) topLine() string {
	switch {
	case m.note != "":
		return m.st.note.Render(fit(m.note, m.w))
	case m.barOpen:
		return ansi.Truncate(m.barText(), m.w, ellipsis)
	default:
		return m.st.note.Render(fit(m.standingNote(), m.w))
	}
}

// barText renders the filter chips: "show: [1 needs eyes ✓] [4 risk ≥ 3]
// …" without colour; with colour active filters are filled accent chips
// and the rest dim (design v2/Code).
func (m Model) barText() string {
	parts := make([]string, 0, len(AllFilters)+2)
	parts = append(parts, m.st.sig.Render("show:"))
	for i, f := range AllFilters {
		label := strconv.Itoa(i+1) + " " + filterLabel(f)
		on, st, s := false, m.st.bar, "["+label
		switch {
		case !m.c.hasData(f):
			st = m.st.barDim
		case m.filters[i] && !m.filterActive(f):
			s += " ✓ (no owned files, ignored)"
			label += " (no owned files, ignored)"
			st = m.st.barDim
		case m.filters[i]:
			s += " ✓"
			on, st = true, m.st.barOn
		}
		switch {
		case !m.th.Colour:
			parts = append(parts, st.Render(s+"]"))
		case on:
			parts = append(parts, m.st.fltOn.Render(" "+label+" "))
		default:
			parts = append(parts, m.st.fltOff.Render("["+label+"]"))
		}
	}
	if !m.filtersApply() {
		parts = append(parts, m.st.note.Render("— filters off (labels unavailable)"))
	}
	return strings.Join(parts, " ")
}

// listLines renders the file list, listH lines padded to listIn.
func (m Model) listLines(lay layout) []string {
	out := make([]string, lay.listH)
	rows := m.listRows()
	for i := range out {
		s := ""
		if ri := m.listOff + i; ri < len(rows) {
			s = m.listRow(rows[ri], lay.listIn)
		}
		out[i] = pad(s, lay.listIn)
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
	switch {
	case fi == m.sel:
		marker, st = "▸ ", m.st.listSel
	case !f.owned:
		st = m.st.sig
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
		st := m.th.Attention(a.Attention)
		if a.Unsure {
			st = m.th.Blue
		}
		b.WriteString(st.Render(AttentionGlyph(a.Attention, a.Unsure)))
	}
	if more {
		b.WriteString("+")
		n++
	}
	return b.String(), n
}

// paneView renders the right pane: its title, the right-hand border text
// ("file 1 of 3") and the visible rows (viewH lines).
func (m Model) paneView(lay layout) (string, string, []string) {
	out := make([]string, lay.viewH)
	switch {
	case m.picker != nil:
		return m.pickerView(lay, out)
	case m.overlay:
		return m.overlayView(lay, out)
	}
	var title, right string
	if m.byChange() {
		title, right = m.groupTitle(lay)
	} else {
		title, right = m.fileTitle(lay)
	}
	if m.cache.width != lay.paneW || m.cache.split != lay.split {
		m.cache.width, m.cache.split = lay.paneW, lay.split
		m.cache.lines = map[int]string{}
	}
	copy(out, m.viewRowLines(lay))
	return title, right, out
}

// viewRowLines renders the pane rows in view (viewH lines at most), with
// the open composer spliced in under its anchor row (at the top when the
// anchor is out of view).
func (m Model) viewRowLines(lay layout) []string {
	rows := make([]string, 0, lay.viewH)
	var comp []string
	inserted := true
	if m.comp != nil {
		comp, inserted = m.composerLines(lay.paneW), false
		if m.comp.row < m.off || m.comp.row >= m.off+lay.viewH {
			rows, inserted = append(rows, comp...), true
		}
	}
	for ri := m.off; len(rows) < lay.viewH && ri < len(m.p.rows); ri++ {
		rows = append(rows, m.renderRow(ri, lay.paneW))
		if !inserted && ri == m.comp.row {
			rows, inserted = append(rows, comp...), true
		}
	}
	return rows[:min(len(rows), lay.viewH)]
}

// fileTitle is "path  +a −d", plus "(not yours)"; right is "file i of n"
// in a box, and the title ends in "i/n" when the list is hidden.
func (m Model) fileTitle(lay layout) (string, string) {
	if len(m.c.files) == 0 {
		return "no files", ""
	}
	f := m.c.files[m.sel]
	s := diffview.Clean(f.path) + "  +" + strconv.Itoa(f.adds) + " −" + strconv.Itoa(f.dels)
	if !f.owned {
		s += "  (not yours)"
	}
	pos := strconv.Itoa(m.sel+1) + "/" + strconv.Itoa(len(m.c.files))
	if lay.listW == 0 {
		s += "  " + pos
	}
	if lay.boxed {
		return s, "file " + strconv.Itoa(m.sel+1) + " of " + strconv.Itoa(len(m.c.files))
	}
	return s, ""
}

// overlayView draws the raw-labels overlay over the pane.
func (m Model) overlayView(lay layout, out []string) (string, string, []string) {
	lines := m.overlayLines(lay.paneW)
	for i := range out {
		li := m.overlayOff + i
		if li >= len(lines) {
			break
		}
		out[i] = fit(diffview.Clean(lines[li]), lay.paneW)
	}
	return "raw labels — esc/L closes", "", out
}

// renderRow returns pane row i, from the cache when possible (keyed by row
// and decoration, so moving the cursor re-renders two rows only).
func (m Model) renderRow(i, width int) string {
	deco := m.rowDeco(i)
	ck := i*numDecos + deco
	if s, ok := m.cache.lines[ck]; ok {
		return s
	}
	s := m.buildRow(i, deco, width)
	m.cache.renders++
	if len(m.cache.lines) >= maxCachedRows {
		m.cache.lines = map[int]string{}
	}
	m.cache.lines[ck] = s
	return s
}

// buildRow styles pane row i with decoration deco; the result is at most
// width cells.
func (m Model) buildRow(i, deco, width int) string {
	r := m.p.rows[i]
	switch r.kind {
	case rowHeader:
		return ansi.Truncate(m.headerText(m.p.units[r.unit], int(r.line)), width, ellipsis)
	case rowSig:
		return m.st.sig.Render(fit(sigLead+diffview.Clean(sigText(m.p.units[r.unit].u)), width))
	case rowMoved:
		return m.st.moved.Render(fit(sigLead+diffview.Clean(m.movedText(m.p.units[r.unit].u)), width))
	case rowLine:
		return m.lineRow(i, r, deco, width)
	case rowSplit:
		return m.splitRow(r, deco, width)
	case rowComment:
		return m.commentRow(m.p.clines[r.line], deco, width)
	case rowNoEOL:
		text := strings.Repeat(" ", 2*m.p.gutter+4) + `\ No newline at end of file`
		return m.st.noEOL.Render(fit(text, width))
	case rowCollapsed:
		lead := "▸ "
		if deco == decoCursor {
			lead = "▶ " // the cursor on a collapsed unit
		}
		return m.st.collapsed.Render(fit(lead+diffview.Clean(m.collapsedText(m.p.units[r.unit])), width))
	case rowNote:
		return m.st.hidden.Render(fit(m.p.notes[r.line], width))
	case rowGroupHead:
		return ansi.Truncate(m.groupHeadText(int(r.line)), width, ellipsis)
	case rowText:
		return m.textRow(m.p.texts[r.line], width)
	case rowFinding:
		return m.findingRow(m.p.flines[r.line], width)
	}
	return ""
}

// headPrefix is the first chip row's lead: "@@ 14  " (the unit's first
// line, new side; old side for a pure deletion), as in the design's
// "@@ 14  feature  behaviour ✓ …".
func headPrefix(u *model.ChangeUnit) string { return "@@ " + strconv.Itoa(unitStart(u)) + "  " }

// unitStart is a unit's first line: new side, or old for a pure deletion.
func unitStart(u *model.ChangeUnit) int {
	if u.Range.New.Count == 0 && u.Range.Old.Count > 0 {
		return u.Range.Old.Start
	}
	return u.Range.New.Start
}

// headerText renders row k of the unit's chip strip: theme chips (filled
// with colour, " · "-joined without), after "@@ N  " on the first row.
func (m Model) headerText(pu paneUnit, k int) string {
	span := pu.chipRows[k]
	prefix := headPrefix(pu.u)
	lead := m.st.sig.Render(prefix)
	if k > 0 {
		lead = strings.Repeat(" ", ansi.StringWidth(prefix))
	}
	return lead + m.chipsText(pu.chips[span[0]:span[1]])
}

// chipsText renders chips with the theme (labels cleaned).
func (m Model) chipsText(cs []chip) string {
	tc := make([]theme.Chip, len(cs))
	for i, c := range cs {
		tc[i] = theme.Chip{Kind: c.kind, Text: diffview.Clean(c.label())}
	}
	return m.th.Chips(tc...)
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
	if ge := m.c.repeatGroup(pu.u.ID); ge != nil && !m.byChange() {
		reason = "mechanical repeat of " + m.c.unitLabel(ge.example()) + " (×" + strconv.Itoa(ge.count()) + ")"
	}
	parts := []string{reason}
	if s := pu.u.Symbol; s != "" {
		parts = append(parts, s)
	}
	parts = append(parts, "+"+strconv.Itoa(pu.u.Adds)+" −"+strconv.Itoa(pu.u.Dels))
	return strings.Join(parts, " · ") + " (z to expand)"
}

// lineRow renders "mark cursor old new op text": syntax colour and
// intra-line emphasis with colour, the line's background to the pane edge
// on added and deleted lines.
func (m Model) lineRow(i int, r prow, deco, width int) string {
	pu := m.p.units[r.unit]
	l := pu.lines[r.line]
	g := m.p.gutter
	var spans []diff.Span
	if r.pair >= 0 && m.paint.Colour() {
		spans = m.intraSpans(i, int(r.pair))
	}
	t := m.paint.Prepare(pu.lang, l.Op, l.Text, spans, 4*width+64)
	base := m.paint.Base(l.Op)
	var b strings.Builder
	b.WriteString(m.gutterCell(pu, int(r.line), l))
	b.WriteString(decoGlyph(deco))
	b.WriteString(m.st.gutter.Render(num(l.OldNo, g) + " " + num(l.NewNo, g) + " "))
	b.WriteString(base.Render(string(rune(l.Op)) + " "))
	b.WriteString(t.All())
	if t.Cut {
		b.WriteString(base.Render(ellipsis))
	}
	s := ansi.Truncate(b.String(), width, ellipsis)
	if m.paint.Colour() && l.Op != diff.Context {
		s += base.Render(strings.Repeat(" ", max(0, width-ansi.StringWidth(s))))
	}
	return s
}

// gutterCell is the one-cell gutter of line li: ❱ with an open thread,
// else its mark (markCell).
func (m Model) gutterCell(pu paneUnit, li int, l diff.Line) string {
	if pu.threads[li] {
		return m.st.threadMark.Render(glyphThread)
	}
	return m.markCell(pu, l)
}

// markCell is the one-cell gutter mark of a line: blank for deleted and
// context lines and before labels exist; MarkGlyph for added lines: ◆
// shaded by its suspicious score, ~ and ⋯ in the accent, · faint (design
// gutter).
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
		st = m.th.Suspicious(lm.Score)
	case model.MarkValue, model.MarkLeftover:
		st = m.th.Accent
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
