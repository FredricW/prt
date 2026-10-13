package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/codeview"
)

// Overview blocks (M5.2, user request D): every list block shows a capped
// summary; its full list opens in an overlay.
//
//   - A block cursor: NextBlock/PrevBlock (J/K, ]/[) move it through the
//     boxes in reading order and a click on a box focuses it; the box is
//     drawn focused (accent border, heavy line without colour). esc clears
//     it before it returns to the picker.
//   - With a block focused, OpenBlock (enter or space) opens its full list
//     in an overlay (a block without a list jumps to its first unit); L
//     shows the raw labels of its first unit. Without one, enter keeps
//     jumping to the unit behind the viewport top and space pages down.
//   - A click on a "+N more" line opens that block's list.
//   - In the list overlay j/k move a row cursor over the rows with a unit,
//     enter (or a click) jumps to it in Code, J/K switch to the next or
//     previous block's list, esc closes.

// renderOverview redraws the shown item's Overview (scrolled to the top;
// redrawOverview keeps the scroll position).
func (m *Model) renderOverview() {
	if m.detail == nil {
		return
	}
	_, mw, _ := m.dims()
	o := m.ovOpts()
	o.focus = m.overview.focus
	m.overview.setPage(overviewPageWith(m.st, m.md, *m.detail, m.now(), mw, o))
}

// redrawOverview redraws the Overview keeping the scroll position.
func (m *Model) redrawOverview() {
	y := m.overview.vp.YOffset
	m.renderOverview()
	m.overview.vp.SetYOffset(y)
}

// overviewBlockKey handles the block keys of the Overview: ok is false
// when k is not one of them.
func (m *Model) overviewBlockKey(k tea.KeyMsg) (tea.Cmd, bool) {
	km, o := m.keys, &m.overview
	switch {
	case key.Matches(k, km.NextBlock), key.Matches(k, km.PrevBlock):
		dir := 1
		if key.Matches(k, km.PrevBlock) {
			dir = -1
		}
		if !o.moveFocus(dir) {
			m.note = "no blocks here"
			return nil, true
		}
		m.redrawOverview()
		return nil, true
	case o.focus != "" && key.Matches(k, km.OpenBlock):
		m.openBlock(o.focus)
		return nil, true
	case o.focus != "" && key.Matches(k, km.RawLabels):
		id, ok := o.boxAnchor(o.focus)
		if !ok {
			m.note = "no unit here"
			return nil, true
		}
		return m.openRawLabels(id), true
	}
	return nil, false
}

// openBlock opens block title's full list, or, for a block without one,
// jumps to its first unit.
func (m *Model) openBlock(title string) {
	if m.detail == nil {
		return
	}
	_, mw, _ := m.dims()
	o := m.ovOpts()
	p, ok := fullBlock(m.st, *m.detail, o, title, max(10, mw-2))
	if !ok {
		if id, ok := m.overview.boxAnchor(title); ok {
			m.jumpToUnit(id)
			return
		}
		m.note = "no list here"
		return
	}
	km := m.keys
	hint := pairHint(km.Down, km.Up, "move").Key + " move · " + km.Focus.Help().Key + " jump to unit · " +
		pairHint(km.NextBlock, km.PrevBlock, "").Key + " other blocks · " + km.Back.Help().Key + " closes"
	m.overview.openList(newListOverlay(m.st, title, hint, p))
}

// handleRawKey drives the Overview's overlays: L (raw labels) or w
// (urgency weights) or esc closes the open one, scroll keys scroll it,
// anything else is ignored; in a block's full list j/k move the row
// cursor, enter jumps to its unit and J/K switch blocks.
func (m *Model) handleRawKey(k tea.KeyMsg) {
	if m.overview.list != nil {
		m.listKey(k)
		return
	}
	km, vp := m.keys, &m.overview.rawVP
	closer := km.RawLabels
	if m.overview.weights {
		closer = km.Weights
	}
	switch {
	case key.Matches(k, closer), key.Matches(k, km.Back):
		m.overview.closeRaw()
	case key.Matches(k, km.Down):
		vp.ScrollDown(1)
	case key.Matches(k, km.Up):
		vp.ScrollUp(1)
	case key.Matches(k, km.PageDown):
		vp.PageDown()
	case key.Matches(k, km.PageUp):
		vp.PageUp()
	case key.Matches(k, km.Top):
		vp.GotoTop()
	case key.Matches(k, km.Bottom):
		vp.GotoBottom()
	}
}

// listKey handles a key in a block's full-list overlay.
func (m *Model) listKey(k tea.KeyMsg) {
	km, o := m.keys, &m.overview
	l, vp := o.list, &o.rawVP
	switch {
	case key.Matches(k, km.Back):
		o.closeRaw()
	case key.Matches(k, km.Focus):
		if id, ok := l.unit(); ok {
			o.closeRaw()
			m.jumpToUnit(id)
			return
		}
		m.note = "no unit here"
	case key.Matches(k, km.NextBlock), key.Matches(k, km.PrevBlock):
		dir := 1
		if key.Matches(k, km.PrevBlock) {
			dir = -1
		}
		m.nextList(dir)
	case key.Matches(k, km.Down):
		if !l.move(1) {
			vp.ScrollDown(1)
		}
		o.drawList()
	case key.Matches(k, km.Up):
		if !l.move(-1) {
			vp.ScrollUp(1)
		}
		o.drawList()
	case key.Matches(k, km.PageDown):
		vp.PageDown()
	case key.Matches(k, km.PageUp):
		vp.PageUp()
	case key.Matches(k, km.Top):
		l.cursor = l.next(-1, 1)
		vp.GotoTop()
		o.drawList()
	case key.Matches(k, km.Bottom):
		l.cursor = l.next(len(l.lines), -1)
		vp.GotoBottom()
		o.drawList()
	}
}

// nextList moves the block focus dir boxes at a time from the open list's
// block, skipping boxes without a list (Description, Not analysed or
// Stopped, the one-line note boxes), and opens the first list it reaches.
// With none left in that direction the open list and the focus stay, with
// a note, so the overlay never shows a block other than the focused one.
func (m *Model) nextList(dir int) {
	o := &m.overview
	start, y := o.focus, o.vp.YOffset
	for {
		prev := o.focus
		o.moveFocus(dir)
		if o.focus == prev {
			o.focus = start
			o.vp.SetYOffset(y)
			m.note = "no other list"
			return
		}
		if m.hasList(o.focus) {
			m.redrawOverview()
			m.openBlock(o.focus)
			return
		}
	}
}

// hasList reports whether the shown detail's block title has a full list.
func (m *Model) hasList(title string) bool {
	if m.detail == nil {
		return false
	}
	b, ok := findBlock(*m.detail, m.ovOpts(), title)
	return ok && b.list
}

// overviewClick handles a left press on the Overview at main-pane cell
// (x, y): in a full list, a row with a unit jumps to it; on the page, a
// "+N more" line opens its block's list, an anchored row (or minimap cell)
// opens its unit in Code, and anywhere else in a box focuses that box.
func (m Model) overviewClick(x, y int) (Model, tea.Cmd) {
	o := &m.overview
	if m.detail == nil {
		return m, nil
	}
	if o.raw {
		if o.list == nil {
			return m, nil
		}
		if r, ok := o.list.rowAt(o.rawVP.YOffset + y); ok {
			m.focus = FocusMain
			id := o.list.anchors[r]
			o.closeRaw()
			m.jumpToUnit(id)
		}
		return m, nil
	}
	line := o.vp.YOffset + y
	if block, ok := o.moreAt(line, x); ok {
		m.focus = FocusMain
		o.focus = block
		m.redrawOverview()
		m.openBlock(block)
		return m, nil
	}
	if id, ok := o.clickAt(line, x); ok {
		m.focus = FocusMain
		m.jumpToUnit(id)
		return m, nil
	}
	if block, ok := o.boxAt(line, x); ok {
		m.focus = FocusMain
		o.focus = block
		m.redrawOverview()
	}
	return m, nil
}

// openRawLabels opens the raw-labels overlay on unit id: the labels at once
// when they are known, else "loading labels…" while they are fetched.
func (m *Model) openRawLabels(id string) tea.Cmd {
	_, mw, _ := m.dims()
	m.overview.openRaw(m.rawLabelsBody(id, mw-2, nil))
	m.overview.rawUnit = id
	if _, ok := m.unitLabels(id); ok {
		return nil
	}
	return m.fetchLabels([]string{id})
}

// rawLabelsBody is the raw-labels overlay of unit id: the labels, or the
// unit's title and "loading labels…" (the error when the fetch failed).
func (m Model) rawLabelsBody(id string, w int, err error) string {
	head := m.st.title.Render("Raw labels") + "  " + m.st.dim.Render("esc or "+m.keys.RawLabels.Help().Key+" closes") + "\n\n"
	if m.detail == nil {
		return head
	}
	if ls, ok := m.unitLabels(id); ok {
		return head + rawLabelsFor(*m.detail, ls, id, w)
	}
	title, _, _ := strings.Cut(codeview.RawLabelsText(unitOf(*m.detail, id), nil, w), "\n")
	note := "loading labels…"
	if err != nil {
		note = "labels unavailable: " + err.Error()
	}
	return head + title + "\n" + note
}

// refreshRawLabels redraws the open raw-labels overlay when its unit is
// among units (their labels arrived, or failed with err).
func (m *Model) refreshRawLabels(units []string, err error) {
	o := &m.overview
	if !o.raw || o.rawUnit == "" {
		return
	}
	for _, u := range units {
		if u == o.rawUnit {
			_, mw, _ := m.dims()
			y := o.rawVP.YOffset
			o.rawVP.SetContent(m.rawLabelsBody(u, mw-2, err))
			o.rawVP.SetYOffset(y)
			return
		}
	}
}
