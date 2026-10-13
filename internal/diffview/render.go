package diffview

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/theme"
)

const (
	// tabSpaces replaces every tab in diff text.
	tabSpaces = "    "
	// maxCachedRows bounds the rendered-row cache; it is cleared when full.
	maxCachedRows = 2048
	// maxIntraBytes skips intra-line diffs for very long pairs (minified
	// files), where a character diff is slow and the highlight is noise.
	maxIntraBytes = 4096
	// ellipsis marks truncated rows.
	ellipsis = "…"
)

// styles are built once per renderer (diff line text is styled by the
// Painter).
type styles struct {
	file, hunk, gutter, noEOL, empty, raw lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	th := theme.New(r)
	return styles{
		file:   r.NewStyle().Bold(true).Reverse(true),
		hunk:   th.Blue,
		gutter: th.Dim,
		noEOL:  th.Dim.Italic(true),
		empty:  th.Dim,
		raw:    r.NewStyle(),
	}
}

// renderCache holds rendered rows (valid for one width) and intra-line spans
// (valid for one diff). It is shared by Model copies so View, which has a
// value receiver, can fill it.
type renderCache struct {
	lines   map[int]string
	spans   map[int][]diff.Span // keyed by row index
	renders int                 // rows built; lets tests check laziness
}

func newRenderCache() *renderCache {
	return &renderCache{lines: map[int]string{}, spans: map[int][]diff.Span{}}
}

func (c *renderCache) resetLines() { c.lines = map[int]string{} }

// renderRow returns row i rendered for the current width, from the cache when
// possible.
func (m Model) renderRow(i int) string {
	if m.cache == nil {
		return m.buildRow(i)
	}
	if s, ok := m.cache.lines[i]; ok {
		return s
	}
	s := m.buildRow(i)
	m.cache.renders++
	if len(m.cache.lines) >= maxCachedRows {
		m.cache.resetLines()
	}
	m.cache.lines[i] = s
	return s
}

// buildRow styles row i; every result is at most m.width cells.
func (m Model) buildRow(i int) string {
	rw := m.rows[i]
	switch rw.kind {
	case kindRaw:
		return m.st.raw.Render(fit(clean(m.raw[rw.line]), m.width))
	case kindFile:
		return m.fileRow(rw)
	case kindHunk:
		h := m.files[rw.file].Hunks[rw.hunk]
		return m.st.hunk.Render(fit(clean(h.Header()), m.width))
	case kindNoEOL:
		text := strings.Repeat(" ", 2*m.gutter+4) + `\ No newline at end of file`
		return m.st.noEOL.Render(fit(text, m.width))
	default:
		return m.lineRow(i, rw)
	}
}

// fileRow renders a file header as a full-width reverse bar.
func (m Model) fileRow(rw row) string {
	text := fit(clean(fileTitle(m.files[rw.file], m.meta[rw.file])), m.width)
	if pad := m.width - ansi.StringWidth(text); pad > 0 {
		text += strings.Repeat(" ", pad)
	}
	return m.st.file.Render(text)
}

// fileTitle is the header text: path (or "old → new"), markers and counts.
func fileTitle(f diff.File, fm fileMeta) string {
	var b strings.Builder
	b.WriteString(" ")
	if f.Renamed && f.OldPath != f.NewPath {
		b.WriteString(f.OldPath + " → " + f.NewPath)
	} else {
		b.WriteString(f.Path())
	}
	switch {
	case f.Added:
		b.WriteString(" (added)")
	case f.Deleted:
		b.WriteString(" (deleted)")
	case f.Renamed && f.Similarity > 0:
		b.WriteString(" (renamed " + strconv.Itoa(f.Similarity) + "%)")
	case f.Renamed:
		b.WriteString(" (renamed)")
	}
	if f.OldMode != "" && f.NewMode != "" && f.OldMode != f.NewMode {
		b.WriteString(" (mode " + f.OldMode + " → " + f.NewMode + ")")
	}
	if f.Binary {
		b.WriteString(" (binary)")
		return b.String()
	}
	b.WriteString("  +" + strconv.Itoa(fm.adds) + " −" + strconv.Itoa(fm.dels))
	return b.String()
}

// lineRow renders "old new op text" with syntax colour and intra-line
// emphasis on paired lines (colour only; plain text otherwise).
func (m Model) lineRow(i int, rw row) string {
	f := m.files[rw.file]
	l := f.Hunks[rw.hunk].Lines[rw.line]
	gutter := num(l.OldNo, m.gutter) + " " + num(l.NewNo, m.gutter) + " "
	var spans []diff.Span
	if rw.pair >= 0 {
		spans = m.intraSpans(i, int(rw.pair))
	}
	t := m.paint.Prepare(Lang("", f.Path()), l.Op, l.Text, spans, 4*m.width+64)
	base := m.paint.Base(l.Op)
	var b strings.Builder
	b.WriteString(m.st.gutter.Render(gutter))
	b.WriteString(base.Render(string(rune(l.Op)) + " "))
	b.WriteString(t.All())
	if t.Cut {
		b.WriteString(base.Render(ellipsis))
	}
	s := ansi.Truncate(b.String(), m.width, ellipsis)
	if m.paint.Colour() && l.Op != diff.Context {
		s += base.Render(strings.Repeat(" ", max(0, m.width-ansi.StringWidth(s))))
	}
	return s
}

// intraSpans returns the changed byte spans of row i, whose paired row is p,
// computing both sides once and caching them.
func (m Model) intraSpans(i, p int) []diff.Span {
	if m.cache != nil {
		if s, ok := m.cache.spans[i]; ok {
			return s
		}
	}
	a, b := m.rows[i], m.rows[p]
	la := m.files[a.file].Hunks[a.hunk].Lines[a.line]
	lb := m.files[b.file].Hunks[b.hunk].Lines[b.line]
	oldRow, newRow, oldText, newText := i, p, la.Text, lb.Text
	if la.Op == diff.Add {
		oldRow, newRow, oldText, newText = p, i, lb.Text, la.Text
	}
	var oldSpans, newSpans []diff.Span
	if len(oldText)+len(newText) <= maxIntraBytes {
		oldSpans, newSpans = diff.IntraLine(oldText, newText)
	}
	if m.cache != nil {
		m.cache.spans[oldRow], m.cache.spans[newRow] = oldSpans, newSpans
	}
	if i == oldRow {
		return oldSpans
	}
	return newSpans
}

// writeSegments writes text split at spans: unchanged parts in base, changed
// parts in emph. Spans past the end of text (after clip) are ignored.
func writeSegments(b *strings.Builder, text string, spans []diff.Span, base, emph lipgloss.Style) {
	pos := 0
	for _, s := range spans {
		start, end := min(s.Start, len(text)), min(s.End, len(text))
		if start < pos || start >= end {
			continue
		}
		writeStyled(b, text[pos:start], base)
		writeStyled(b, text[start:end], emph)
		pos = end
	}
	writeStyled(b, text[pos:], base)
}

func writeStyled(b *strings.Builder, s string, st lipgloss.Style) {
	if s != "" {
		b.WriteString(st.Render(clean(s)))
	}
}

// num right-aligns n in w cells; 0 renders as blanks.
func num(n, w int) string {
	if n == 0 {
		return strings.Repeat(" ", w)
	}
	s := strconv.Itoa(n)
	return strings.Repeat(" ", max(0, w-len(s))) + s
}

// fit truncates plain text to width cells, ending in "…" when cut.
func fit(s string, width int) string {
	return ansi.Truncate(s, width, ellipsis)
}

// clip cuts s to at most limit bytes on a rune boundary, reporting whether it
// cut. It keeps rendering cost independent of line length.
func clip(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit], true
}

// clean makes diff text safe and predictable to draw: tabs become four
// spaces and control characters (which could move the cursor or change
// terminal state) become '?'.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return '?'
		}
		return r
	}, s)
	return strings.ReplaceAll(s, "\t", tabSpaces)
}
