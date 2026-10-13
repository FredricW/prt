package codeview

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
)

// maxWrapBytes caps a side-by-side line before wrapping (minified files
// would otherwise wrap onto thousands of rows); a cut line ends in "…".
const maxWrapBytes = 8192

// Side-by-side layout (spec Diff rendering): the pane is two columns, old
// on the left and new on the right, separated by a dim "│". Inside a hunk a
// run of deletions pairs row by row with the additions that follow it;
// context lines show on both sides; an unpaired line leaves the other side
// blank. Long lines wrap onto continuation rows and both columns of a pair
// grow to the taller one. Gutter marks (and ❱) sit on the new side only;
// each column has its own line numbers and cursor cell. Comment blocks,
// findings and "no newline" notes span the pane under the pair's last row.

// addSplitLines appends unit ui's lines as side-by-side pairs, with their
// findings (atLine, by new-side line) and comment blocks, and returns the
// unit's largest line number.
func (m *Model) addSplitLines(p *pane, ui int32, atLine map[int][]model.Finding, width int) int {
	pu := &p.units[ui]
	ls := pu.lines
	maxNo := 0
	for _, l := range ls {
		maxNo = max(maxNo, l.OldNo, l.NewNo)
	}
	for _, s := range splitPairs(ls) {
		m.addPair(p, ui, s, atLine, width)
	}
	return maxNo
}

// splitPairs aligns a unit's lines: context lines alone (both sides), each
// run of deletions paired in order with the run of additions after it.
func splitPairs(ls []diff.Line) []srow {
	var out []srow
	for i := 0; i < len(ls); {
		if ls[i].Op == diff.Context {
			out = append(out, srow{left: i, right: i})
			i++
			continue
		}
		var dels, adds []int
		for i < len(ls) && ls[i].Op == diff.Del {
			dels = append(dels, i)
			i++
		}
		for i < len(ls) && ls[i].Op == diff.Add {
			adds = append(adds, i)
			i++
		}
		for k := range max(len(dels), len(adds)) {
			s := srow{left: -1, right: -1}
			if k < len(dels) {
				s.left = dels[k]
			}
			if k < len(adds) {
				s.right = adds[k]
			}
			out = append(out, s)
		}
	}
	return out
}

// addPair appends one pair's visual rows, then what hangs under its lines.
func (m *Model) addPair(p *pane, ui int32, s srow, atLine map[int][]model.Finding, width int) {
	pu := &p.units[ui]
	ls := pu.lines
	s.parts = max(wrapCount(ls, s.left, p.geom.lt), wrapCount(ls, s.right, p.geom.rt))
	si := int32(len(p.splits))
	p.splits = append(p.splits, s)
	for k := range s.parts {
		p.rows = append(p.rows, prow{kind: rowSplit, unit: ui, line: si, pair: int32(k)})
	}
	sides := []int{s.left}
	if s.right != s.left {
		sides = append(sides, s.right)
	}
	for _, li := range sides {
		if li >= 0 && ls[li].NoEOL {
			p.rows = append(p.rows, prow{kind: rowNoEOL, unit: ui, line: int32(li), pair: -1})
		}
	}
	if s.right >= 0 && atLine[ls[s.right].NewNo] != nil {
		m.addFindingRows(p, ui, atLine[ls[s.right].NewNo], width)
	}
	for _, li := range sides {
		if li >= 0 {
			m.addBlockRows(p, ui, m.c.blocksAt(pu.u.ID, li), width)
		}
	}
}

// wrapCount is the number of rows line li takes at text width w (1 for a
// blank side).
func wrapCount(ls []diff.Line, li, w int) int {
	if li < 0 {
		return 1
	}
	text, _ := diffview.Clip(ls[li].Text, maxWrapBytes)
	return len(diffview.WrapBounds(diffview.Clean(text), w))
}

// splitKeyLine is the line a cursor on pair s stands for: the new side,
// except on a pair whose old side the cursor is already on (sticky side:
// a cursor coming from a deleted line stays on deleted lines), or a pair
// with only an old side.
func (m *Model) splitKeyLine(s srow) int {
	switch {
	case s.right < 0:
		return s.left
	case !s.paired():
		return s.right
	}
	if m.cur.block == "" && m.cur.line >= 0 {
		if ls := m.c.linesOf(m.cur.unit); m.cur.line < len(ls) && ls[m.cur.line].Op == diff.Del {
			return s.left
		}
	}
	return s.right
}

// splitRow renders visual row part of pair s: "old column │ new column".
func (m Model) splitRow(r prow, deco, width int) string {
	pu := m.p.units[r.unit]
	s := m.p.splits[r.line]
	part := int(r.pair)
	g := m.p.geom
	var oldSp, newSp []diff.Span
	if s.paired() && m.paint.Colour() {
		oldSp, newSp = m.pairSpans(int(r.line), pu.lines[s.left].Text, pu.lines[s.right].Text)
	}
	leftDeco, rightDeco := decoNone, deco
	switch deco {
	case decoCursorOld:
		leftDeco, rightDeco = decoCursor, decoNone
	case decoSelected:
		leftDeco = decoSelected
	}
	mark := " "
	if part == 0 && s.right >= 0 {
		mark = m.gutterCell(pu, s.right, pu.lines[s.right])
	}
	if part == 0 && s.left >= 0 && pu.threads[s.left] {
		mark = m.st.threadMark.Render(glyphThread) // ❱ of a deleted line, on the new side too
	}
	left := m.splitCell(pu, s.left, part, " ", leftDeco, oldSp, g.lw, g.lt, true)
	right := m.splitCell(pu, s.right, part, mark, rightDeco, newSp, g.rw, g.rt, false)
	return ansi.Truncate(left+m.st.divider.Render("│")+right, width, ellipsis)
}

// splitCell renders one column of a split row: the mark cell (new side
// only), the cursor cell, the line number, the op and the text piece of
// visual row part, padded to cw cells (on the line's background with
// colour). A blank side is spaces.
func (m Model) splitCell(pu paneUnit, li, part int, mark string, deco int, spans []diff.Span, cw, tw int, old bool) string {
	if li < 0 {
		return strings.Repeat(" ", cw)
	}
	l := pu.lines[li]
	n := l.NewNo
	if old {
		n = l.OldNo
	}
	t := m.paint.Prepare(pu.lang, l.Op, l.Text, spans, maxWrapBytes)
	bounds := diffview.WrapBounds(t.Clean, tw)
	base := m.paint.Base(l.Op)
	var b strings.Builder
	if part == 0 {
		b.WriteString(mark + decoGlyph(deco) + m.st.gutter.Render(num(n, m.p.gutter)+" ") + base.Render(string(rune(l.Op))+" "))
	} else {
		b.WriteString(strings.Repeat(" ", splitGutter(m.p.gutter)))
	}
	if part < len(bounds) {
		end := len(t.Clean)
		if part+1 < len(bounds) {
			end = bounds[part+1]
		}
		b.WriteString(t.Render(bounds[part], end))
		if t.Cut && part == len(bounds)-1 {
			b.WriteString(base.Render(ellipsis))
		}
	}
	s := ansi.Truncate(b.String(), cw, ellipsis)
	fill := strings.Repeat(" ", max(0, cw-ansi.StringWidth(s)))
	if m.paint.Colour() && l.Op != diff.Context {
		return s + base.Render(fill)
	}
	return s + fill
}

// pairSpans returns the intra-line spans of split pair si, computed once.
func (m Model) pairSpans(si int, oldText, newText string) ([]diff.Span, []diff.Span) {
	if s, ok := m.cache.pairs[si]; ok {
		return s[0], s[1]
	}
	o, n := diffview.IntraSpans(oldText, newText)
	m.cache.pairs[si] = [2][]diff.Span{o, n}
	return o, n
}
