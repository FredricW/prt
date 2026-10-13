package codeview

import (
	"strconv"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/model"
)

// M5 contract (fixed by the M5 lead; the codeview work package implements
// it without changing these signatures).

// Layout is the diff layout of the Code page (spec Diff rendering: `u`
// toggles unified and side-by-side).
type Layout int

// Diff layouts. LayoutSplit is side-by-side; below SplitMinWidth columns it
// renders unified with a status note (Note) while still being the
// requested layout, so widening the terminal brings it back.
const (
	LayoutUnified Layout = iota
	LayoutSplit
)

// SplitMinTerminal is the spec's threshold in terminal columns ("Below
// ~140 columns side-by-side degrades to unified").
const SplitMinTerminal = 140

// SplitMinWidth is the narrowest component width that renders side-by-side:
// the Code pane of a SplitMinTerminal-column terminal (the TUI's 36-column
// sidebar plus its 2-column gap take 38).
const SplitMinWidth = SplitMinTerminal - 38

// noteSplitNarrow is the standing note while side-by-side is requested but
// the component is narrower than SplitMinWidth.
const noteSplitNarrow = "side-by-side needs a ≥140-column terminal — showing unified"

// String names the layout for notes and help.
func (l Layout) String() string {
	if l == LayoutSplit {
		return "side-by-side"
	}
	return "unified"
}

// OpenRequest is what `e` (EditFile) or `o` (OpenUnit) asked the root
// model to do: open Path at Line in $EDITOR (Browser false; Line on the
// new side, or the nearest new-side line for a deleted line), or open the
// unit on GitHub (Browser true; the root builds the URL from the item and
// Path/Line/Side).
type OpenRequest struct {
	Browser bool
	Path    string
	Line    int
	Side    model.Side
	UnitID  string
}

// PendingOpen returns and clears the open request made by the last key.
func (m *Model) PendingOpen() (OpenRequest, bool) {
	if m.pendingOpen == nil {
		return OpenRequest{}, false
	}
	r := *m.pendingOpen
	m.pendingOpen = nil
	return r, true
}

// DiffLayout returns the requested layout (LayoutUnified until `u`).
func (m Model) DiffLayout() Layout { return m.diffLayout }

// SetDiffLayout requests a layout (the snapshot and tests use it); it
// reports whether the layout changed. The cursor stays on its line.
func (m *Model) SetDiffLayout(l Layout) bool {
	if l != LayoutSplit {
		l = LayoutUnified
	}
	if l == m.diffLayout {
		return false
	}
	m.diffLayout = l
	if !m.c.plain() {
		m.rebuildKeep()
		m.keepCursorVisible()
	}
	return true
}

// splitOn reports whether side-by-side is in effect (requested and wide
// enough).
func (m *Model) splitOn() bool { return m.diffLayout == LayoutSplit && m.w >= SplitMinWidth }

// splitDegraded reports whether side-by-side is requested but too narrow.
func (m *Model) splitDegraded() bool {
	return !m.c.plain() && m.diffLayout == LayoutSplit && m.w < SplitMinWidth
}

// toggleLayout is the SideBySide key (u).
func (m *Model) toggleLayout() {
	next := LayoutSplit
	if m.diffLayout == LayoutSplit {
		next = LayoutUnified
	}
	m.SetDiffLayout(next)
	m.note = "layout: " + next.String()
	if m.splitDegraded() {
		m.note = noteSplitNarrow
	}
}

// openKey implements EditFile (browser false) and OpenUnit (true): it
// queues an OpenRequest for the cursor's line (else the cursor's unit) and
// reports whether it did; without a diff line or unit under the cursor the
// key is not consumed, so the root's global `o` (open the item) runs.
func (m *Model) openKey(browser bool) bool {
	r, ok := m.openTarget()
	if !ok {
		return false
	}
	r.Browser = browser
	m.pendingOpen = &r
	if browser {
		m.note = "opening " + r.Path + " on GitHub"
	} else {
		m.note = "opening " + r.Path + ":" + strconv.Itoa(r.Line) + " in $EDITOR"
	}
	return true
}

// openTarget is the place `e`/`o` open: the cursor's diff line (a deleted
// line maps to the nearest new-side line, Side old), else the start of the
// cursor's unit.
func (m *Model) openTarget() (OpenRequest, bool) {
	if m.p == nil || m.c.plain() || m.curRow < 0 || !m.cursorVisible() {
		return OpenRequest{}, false
	}
	u := m.c.units[m.cur.unit]
	if u == nil {
		return OpenRequest{}, false
	}
	r := OpenRequest{Path: u.Path, Side: model.SideNew, UnitID: u.ID, Line: max(1, u.Range.New.Start)}
	if pos, ok := m.posOf(m.cur); ok {
		r.Side = pos.Side
		r.Line = newSideLine(m.c.linesOf(u.ID), m.lineIndexOf(m.cur), r.Line)
	}
	return r, true
}

// lineIndexOf is the diff line index a cursor key stands for (a block: its
// anchor line; -1 when none).
func (m *Model) lineIndexOf(k curKey) int {
	if k.block == "" {
		return k.line
	}
	if bi := m.c.blockIndex(k.block); bi >= 0 {
		return m.c.blocks[bi].line
	}
	return -1
}

// newSideLine is the new-side number of line li of ls: its own for added
// and context lines; for a deleted line the nearest new-side line after it
// (where a replacement lands), else before it; def when there is none.
func newSideLine(ls []diff.Line, li, def int) int {
	if li < 0 || li >= len(ls) {
		return def
	}
	for i := li; i < len(ls); i++ {
		if ls[i].Op != diff.Del && ls[i].NewNo > 0 {
			return ls[i].NewNo
		}
	}
	for i := li - 1; i >= 0; i-- {
		if ls[i].Op != diff.Del && ls[i].NewNo > 0 {
			return ls[i].NewNo
		}
	}
	return def
}
