package codeview

import (
	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/model"
)

// Cursor glyphs, drawn in the cell between the gutter mark and the line
// numbers so they read without colour.
const (
	glyphCursor   = "▸"
	glyphSelected = "┃"
)

// Row decorations (renderRow cache key bits).
const (
	decoNone = iota
	decoCursor
	decoSelected
	numDecos
)

// curKey names a cursor row independently of the pane layout, so the
// cursor survives rebuilds: a diff line (unit id and index into its lines)
// or the first row of a comment block (block id, line -1).
type curKey struct {
	unit  string
	line  int
	block string
}

// LinePos is a diff line the Code page cursor can be on: its file, the
// side a comment on it uses (old for a deleted line, new for added and
// context lines), its number on that side and its unit.
type LinePos struct {
	Path   string
	Side   model.Side
	Line   int
	UnitID string
}

// CursorLine returns the diff line under the line cursor. ok is false in
// the plain diff fallback, when the pane has no diff lines, or when the
// cursor is on a comment block that is not anchored to a line.
func (m Model) CursorLine() (LinePos, bool) {
	if m.c.plain() || m.p == nil || m.curRow < 0 {
		return LinePos{}, false
	}
	return m.posOf(m.cur)
}

// posOf returns the diff line a cursor key stands for (a block: its
// anchor line).
func (m *Model) posOf(k curKey) (LinePos, bool) {
	li := k.line
	if k.block != "" {
		bi := m.c.blockIndex(k.block)
		if bi < 0 || m.c.blocks[bi].line < 0 {
			return LinePos{}, false
		}
		li = m.c.blocks[bi].line
	}
	u := m.c.units[k.unit]
	ls := m.c.linesOf(k.unit)
	if u == nil || li < 0 || li >= len(ls) {
		return LinePos{}, false
	}
	side, n := lineSide(ls[li])
	return LinePos{Path: u.Path, Side: side, Line: n, UnitID: u.ID}, true
}

// isTarget reports whether pane row i can hold the cursor: a diff line,
// the first row of a comment block, or a collapsed unit's summary (so
// n/p can land on it).
func (p *pane) isTarget(i int) bool {
	switch r := p.rows[i]; r.kind {
	case rowLine, rowCollapsed:
		return true
	case rowComment:
		return p.clines[r.line].head
	}
	return false
}

// keyOfRow returns the cursor key of target row i.
func (m *Model) keyOfRow(i int) curKey {
	r := m.p.rows[i]
	k := curKey{unit: m.p.units[r.unit].u.ID, line: int(r.line)}
	switch r.kind {
	case rowComment:
		k.line, k.block = -1, m.c.blocks[m.p.clines[r.line].block].id
	case rowCollapsed:
		k.line = -1
	}
	return k
}

// rowOfKey returns the pane row of a cursor key, or -1 when it is not drawn.
func (m *Model) rowOfKey(k curKey) int {
	if m.p == nil || k.unit == "" {
		return -1
	}
	h, end := m.p.unitSpan(k.unit)
	if h < 0 {
		return -1
	}
	for i := h; i <= end; i++ {
		r := m.p.rows[i]
		switch {
		case r.kind == rowLine && k.block == "" && int(r.line) == k.line:
			return i
		case r.kind == rowCollapsed && k.block == "" && k.line < 0:
			return i
		case r.kind == rowComment && k.block != "" && m.p.clines[r.line].head &&
			m.c.blocks[m.p.clines[r.line].block].id == k.block:
			return i
		}
	}
	return -1
}

// setCursorRow puts the cursor on target row i.
func (m *Model) setCursorRow(i int) {
	if m.p == nil || i < 0 || i >= len(m.p.rows) || !m.p.isTarget(i) {
		return
	}
	m.curRow, m.cur = i, m.keyOfRow(i)
}

// resolveCursor finds the cursor's row after a rebuild; when its line is
// no longer drawn the cursor moves to the first changed line at or after
// the top of the pane (else the first target there, else the last one
// above).
func (m *Model) resolveCursor() {
	m.curRow = -1
	if m.p == nil {
		return
	}
	if r := m.rowOfKey(m.cur); r >= 0 {
		m.curRow = r
		return
	}
	r := m.firstChanged(m.off, len(m.p.rows)-1)
	if r < 0 {
		r = m.targetIn(m.off, len(m.p.rows)-1, false)
	}
	if r < 0 {
		r = m.targetIn(0, m.off-1, true)
	}
	m.setCursorRow(r)
}

// firstChanged returns the first added or deleted line row in [lo, hi],
// or -1.
func (m *Model) firstChanged(lo, hi int) int {
	for i := max(0, lo); i <= hi && i < len(m.p.rows); i++ {
		if r := m.p.rows[i]; r.kind == rowLine && m.p.units[r.unit].lines[r.line].Op != diff.Context {
			return i
		}
	}
	return -1
}

// targetIn returns the first (last when fromEnd) target row in [lo, hi],
// or -1.
func (m *Model) targetIn(lo, hi int, fromEnd bool) int {
	lo, hi = max(0, lo), min(hi, len(m.p.rows)-1)
	for j := lo; j <= hi; j++ {
		i := j
		if fromEnd {
			i = hi - (j - lo)
		}
		if m.p.isTarget(i) {
			return i
		}
	}
	return -1
}

// viewRows returns the first and last pane rows in view.
func (m *Model) viewRows() (int, int) {
	return m.off, m.off + max(1, m.layout().viewH) - 1
}

// cursorVisible reports whether the cursor row is in view.
func (m *Model) cursorVisible() bool {
	top, bot := m.viewRows()
	return m.curRow >= 0 && m.curRow >= top && m.curRow <= bot
}

// keepCursorVisible moves the cursor into view after scrolling (to the
// nearest target row in view); with no target in view it stays put.
func (m *Model) keepCursorVisible() {
	if m.p == nil || m.c.plain() || m.cursorVisible() {
		return
	}
	top, bot := m.viewRows()
	if r := m.targetIn(top, bot, m.curRow > bot); r >= 0 {
		m.setCursorRow(r)
	}
}

// ensureRowVisible scrolls the pane just enough to show row i.
func (m *Model) ensureRowVisible(i int) {
	top, bot := m.viewRows()
	switch {
	case i < top:
		m.off = i
	case i > bot:
		m.off = i - max(1, m.layout().viewH) + 1
	}
}

// cursorDown moves the cursor to the next target row, scrolling to show
// it; at the last target (or with the cursor scrolled out of view) it
// scrolls by one row instead, so trailing rows stay reachable.
func (m *Model) cursorDown() {
	next := -1
	if m.cursorVisible() {
		next = m.targetIn(m.curRow+1, len(m.p.rows)-1, false)
	}
	if next < 0 {
		m.scrollBy(1)
		return
	}
	m.setCursorRow(next)
	m.ensureRowVisible(next)
}

// cursorUp moves the cursor to the previous target row; at the first one
// (or with the cursor out of view) it scrolls up by one row.
func (m *Model) cursorUp() {
	prev := -1
	if m.cursorVisible() {
		prev = m.targetIn(0, m.curRow-1, true)
	}
	if prev < 0 {
		m.scrollBy(-1)
		return
	}
	m.setCursorRow(prev)
	m.ensureRowVisible(prev)
}

// cursorToUnit puts the cursor on unit id's first changed line (else its
// first target row).
func (m *Model) cursorToUnit(id string) {
	h, end := m.p.unitSpan(id)
	if h < 0 {
		return
	}
	r := m.firstChanged(h, end)
	if r < 0 {
		r = m.targetIn(h, end, false)
	}
	m.setCursorRow(r)
}

// cursorUnitIndex returns the pane unit the cursor is in when it is in
// view, else the unit at the top of the pane (-1 when none).
func (m *Model) cursorUnitIndex() int {
	if m.p == nil {
		return -1
	}
	if m.cursorVisible() {
		return int(m.p.rows[m.curRow].unit)
	}
	return m.p.unitAt(m.off)
}

// cursorUnitID is the id of the unit cursorUnitIndex names, or "".
func (m *Model) cursorUnitID() string {
	if ui := m.cursorUnitIndex(); ui >= 0 {
		return m.p.units[ui].u.ID
	}
	return ""
}

// rowDeco is the decoration of pane row i: the cursor, a selected line of
// a V selection, or none.
func (m Model) rowDeco(i int) int {
	switch {
	case i == m.curRow:
		return decoCursor
	case m.vis != nil && m.p.rows[i].kind == rowLine && m.inSelection(i):
		return decoSelected
	}
	return decoNone
}

// decoGlyph is the cursor cell of a row with decoration d.
func decoGlyph(d int) string {
	switch d {
	case decoCursor:
		return glyphCursor
	case decoSelected:
		return glyphSelected
	}
	return " "
}
