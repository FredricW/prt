package codeview

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	// narrowBelow is the width under which the file list is hidden and the
	// boxes collapse to a plain title row.
	narrowBelow = 60
	// minBoxH is the least body height drawn in titled boxes (top and
	// bottom borders plus one row).
	minBoxH = 3
	// listMax and listMin bound the file list width.
	listMax, listMin = 40, 16
	// wheelStep is how many pane rows one mouse wheel notch scrolls.
	wheelStep = 3
)

// layout is the geometry of one frame. From narrowBelow columns (and
// minBoxH body rows) the list and the pane are titled boxes (design v2/Code
// and v3/Groups); below that the list is hidden and the pane's title is a
// plain reverse-video row.
type layout struct {
	top    int  // 1 when the bar line is shown
	boxed  bool // list and pane drawn in titled boxes
	listW  int  // outer width of the list column; 0 when hidden
	listIn int  // list content width
	listY  int  // first list content row within the body (1 when boxed)
	listH  int  // list content rows
	paneX  int  // first column of the pane content
	paneW  int  // pane content width
	bodyH  int  // rows below the bar and above the Comments strip
	viewH  int  // pane content rows (below the title row or border)
	stripH int  // Comments strip rows at the bottom (0 when hidden)
	split  bool // side-by-side in effect
}

func (m *Model) layout() layout {
	l := layout{split: m.splitOn()}
	if m.barVisible() {
		l.top = 1
	}
	l.bodyH = max(0, m.h-l.top)
	// The strip is drawn only when at least two body rows remain.
	if s := m.stripWant(); s > 0 && l.bodyH-s >= 2 {
		l.stripH = s
		l.bodyH -= s
	}
	if m.w < narrowBelow {
		l.paneW, l.viewH = m.w, max(0, l.bodyH-1)
		return l
	}
	l.listW = max(listMin, min(listMax, m.w/3))
	if l.bodyH >= minBoxH {
		l.boxed = true
		l.listIn, l.listY, l.listH = l.listW-2, 1, l.bodyH-2
		l.paneX, l.paneW, l.viewH = l.listW+2, m.w-l.listW-3, l.bodyH-2
		return l
	}
	l.listIn, l.listH = l.listW, l.bodyH
	l.paneX, l.paneW, l.viewH = l.listW+1, m.w-l.listW-1, max(0, l.bodyH-1)
	return l
}

// barVisible reports whether the top line is drawn.
func (m *Model) barVisible() bool {
	return !m.c.plain() && (m.barOpen || m.note != "" || m.standingNote() != "")
}

// standingNote is the bar line when no one-shot note or filters bar shows:
// side-by-side degraded to unified, labels unavailable, and by change why
// grouping ran without embeddings.
func (m *Model) standingNote() string {
	var parts []string
	if m.splitDegraded() {
		parts = append(parts, noteSplitNarrow)
	}
	if m.c.labelsMissing {
		parts = append(parts, "labels unavailable — filters off")
	}
	if m.mode == ModeChange && m.c.embedNote != "" {
		parts = append(parts, m.c.embedNote)
	}
	return strings.Join(parts, " · ")
}

func (m *Model) maxOffset() int {
	if m.p == nil {
		return 0
	}
	return max(0, len(m.p.rows)-m.layout().viewH)
}

// scrollBy moves the pane by n rows. Scrolling down never pulls back a
// header placed past the normal bottom by n/p or FocusUnit.
func (m *Model) scrollBy(n int) {
	m.off = clamp(m.off+n, 0, max(m.maxOffset(), m.off))
}

// listRows returns the list layout: file indices, -1 for the divider.
func (m *Model) listRows() []int {
	div := m.c.divider()
	rows := make([]int, 0, len(m.c.files)+1)
	for i := range m.c.files {
		if i == div {
			rows = append(rows, -1)
		}
		rows = append(rows, i)
	}
	return rows
}

// ensureListVisible scrolls the list so the selected file (and the divider
// right above it) is visible.
func (m *Model) ensureListVisible() {
	if m.byChange() {
		m.ensureGroupVisible()
		return
	}
	h := m.layout().listH
	rows := m.listRows()
	if h <= 0 || len(rows) == 0 {
		m.listOff = 0
		return
	}
	sr := 0
	for i, fi := range rows {
		if fi == m.sel {
			sr = i
			break
		}
	}
	top := sr
	if sr > 0 && rows[sr-1] == -1 {
		top = sr - 1
	}
	if top < m.listOff {
		m.listOff = top
	}
	if sr >= m.listOff+h {
		m.listOff = sr - h + 1
	}
	m.listOff = clamp(m.listOff, 0, max(0, len(rows)-h))
}

// selectEntry selects file (by change: group) fi, clamped, with the pane
// at its top.
func (m *Model) selectEntry(fi int) {
	if m.entries() == 0 {
		return
	}
	fi = clamp(fi, 0, m.entries()-1)
	if fi == m.sel {
		return
	}
	m.sel, m.off = fi, 0
	m.rebuild()
}

// HandleKey handles a key; it reports whether the key was consumed (an
// unconsumed key falls through to the root model's global keys).
func (m *Model) HandleKey(k tea.KeyMsg) bool {
	m.note = ""
	used := m.handleKey(k)
	if !m.c.plain() {
		m.ensureListVisible() // the bar line may have appeared or gone
		m.keepCursorVisible()
	}
	return used
}

func (m *Model) handleKey(k tea.KeyMsg) bool {
	switch {
	case m.c.plain():
		return m.plainKey(k)
	case m.comp != nil:
		return m.composerKey(k)
	case m.panel != nil:
		return m.panelKey(k)
	case m.picker != nil:
		return m.pickerKey(k)
	case m.overlay:
		return m.overlayKey(k)
	case m.barOpen && m.barKey(k):
		return true
	case m.stripFocus && m.stripKey(k):
		return true
	case m.vis != nil && m.visualKey(k):
		return true
	}
	kk := m.keys
	switch {
	case key.Matches(k, kk.Mode):
		m.toggleMode()
		return true
	case key.Matches(k, kk.Close) && m.cancelMerge():
		return true
	case m.scrollKey(k):
		return true
	case key.Matches(k, kk.Accept):
		return m.acceptKey()
	case m.commentKey(k):
		return true
	case key.Matches(k, kk.EditFile):
		return m.openKey(false)
	case key.Matches(k, kk.OpenUnit):
		return m.openKey(true)
	case key.Matches(k, kk.SideBySide):
		m.toggleLayout()
		return true
	}
	switch {
	case key.Matches(k, kk.NextUnit):
		m.nextUnit()
	case key.Matches(k, kk.PrevUnit):
		m.prevUnit()
	case key.Matches(k, kk.NextFile):
		m.selectEntry(m.sel + 1)
	case key.Matches(k, kk.PrevFile):
		m.selectEntry(m.sel - 1)
	case key.Matches(k, kk.MergeGroups):
		m.mergeKey()
	case key.Matches(k, kk.SplitGroup):
		m.splitKey()
	case key.Matches(k, kk.ReReview):
		m.reReviewKey()
	case key.Matches(k, kk.Filters):
		m.barOpen = !m.barOpen
		m.clamp()
	case key.Matches(k, kk.Expand):
		m.toggleExpand()
	case key.Matches(k, kk.RawLabels):
		m.openOverlay()
	default:
		return false
	}
	return true
}

// barKey handles the filter toggles and closing while the bar is open.
func (m *Model) barKey(k tea.KeyMsg) bool {
	if key.Matches(k, m.keys.Close) || key.Matches(k, m.keys.Filters) {
		m.barOpen = false
		m.clamp()
		return true
	}
	for i, b := range m.keys.Toggle {
		if key.Matches(k, b) {
			m.toggleFilter(i)
			return true
		}
	}
	return false
}

// toggleFilter flips AllFilters[i]; filters without data only show a note.
func (m *Model) toggleFilter(i int) {
	f := AllFilters[i]
	if !m.c.hasData(f) {
		m.note = filterLabel(f) + ": no data yet (no last look recorded for this item)"
		return
	}
	m.filters[i] = !m.filters[i]
	m.revealed = map[string]bool{}
	m.rebuildKeep()
}

// scrollKey handles the pane scrolling keys.
func (m *Model) scrollKey(k tea.KeyMsg) bool {
	kk, page := m.keys, max(1, m.layout().viewH)
	switch {
	case key.Matches(k, kk.Down):
		m.cursorDown()
	case key.Matches(k, kk.Up):
		m.cursorUp()
	case key.Matches(k, kk.PageDown):
		m.scrollBy(page)
	case key.Matches(k, kk.PageUp):
		m.scrollBy(-page)
	case key.Matches(k, kk.HalfPageDown):
		m.scrollBy(max(1, page/2))
	case key.Matches(k, kk.HalfPageUp):
		m.scrollBy(-max(1, page/2))
	case key.Matches(k, kk.Top):
		m.off = 0
		m.setCursorRow(m.targetIn(0, len(m.p.rows)-1, false))
	case key.Matches(k, kk.Bottom):
		m.off = m.maxOffset()
		m.setCursorRow(m.targetIn(0, len(m.p.rows)-1, true))
	default:
		return false
	}
	return true
}

// plainKey drives the plain diff viewer fallback.
func (m *Model) plainKey(k tea.KeyMsg) bool {
	kk, page := m.keys, max(1, m.h)
	switch {
	case key.Matches(k, kk.Down):
		m.dv.ScrollBy(1)
	case key.Matches(k, kk.Up):
		m.dv.ScrollBy(-1)
	case key.Matches(k, kk.PageDown):
		m.dv.ScrollBy(page)
	case key.Matches(k, kk.PageUp):
		m.dv.ScrollBy(-page)
	case key.Matches(k, kk.HalfPageDown):
		m.dv.ScrollBy(max(1, page/2))
	case key.Matches(k, kk.HalfPageUp):
		m.dv.ScrollBy(-max(1, page/2))
	case key.Matches(k, kk.Top):
		m.dv.GotoTop()
	case key.Matches(k, kk.Bottom):
		m.dv.GotoBottom()
	case key.Matches(k, kk.NextFile):
		m.dv.NextFile()
	case key.Matches(k, kk.PrevFile):
		m.dv.PrevFile()
	default:
		return false
	}
	return true
}

// overlayKey handles keys while the raw-labels overlay is open: it closes
// on Close or RawLabels, scrolls on the movement keys and swallows the rest
// (except ctrl+c) so nothing acts on a unit behind the overlay.
func (m *Model) overlayKey(k tea.KeyMsg) bool {
	kk := m.keys
	n := len(m.overlayLines(m.layout().paneW))
	maxOff := max(0, n-m.layout().viewH)
	switch {
	case key.Matches(k, kk.Close), key.Matches(k, kk.RawLabels):
		m.overlay = false
	case key.Matches(k, kk.Down):
		m.overlayOff = min(m.overlayOff+1, maxOff)
	case key.Matches(k, kk.Up):
		m.overlayOff = max(m.overlayOff-1, 0)
	case key.Matches(k, kk.Top):
		m.overlayOff = 0
	case key.Matches(k, kk.Bottom):
		m.overlayOff = maxOff
	case k.String() == "ctrl+c":
		return false
	}
	return true
}

// openOverlay shows the raw labels of the current unit.
func (m *Model) openOverlay() {
	u, ok := m.CurrentUnit()
	if !ok {
		m.note = "no unit here"
		return
	}
	m.overlay, m.overlayUnit, m.overlayOff = true, u.ID, 0
}

// overlayLines is the overlay body for the pane width.
func (m *Model) overlayLines(width int) []string {
	u := m.c.units[m.overlayUnit]
	if u == nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(RawLabelsText(*u, m.c.labels, width), "\n"), "\n")
}

// toggleExpand flips `z` for the selected file (by change: group);
// collapsing again also drops units FocusUnit expanded there.
func (m *Model) toggleExpand() {
	name, set, units := m.selectedPath(), m.expandedFiles, []string(nil)
	if m.byChange() {
		name, set, units = m.c.groups[m.sel].g.ID, m.expandedGroups, m.c.groups[m.sel].units
	} else if m.sel < len(m.c.files) {
		units = m.c.files[m.sel].units
	}
	if name == "" {
		return
	}
	if set[name] {
		delete(set, name)
		for _, id := range units {
			delete(m.expandedUnits, id)
		}
	} else {
		set[name] = true
	}
	m.rebuildKeep()
}

// nextUnit moves to the next visible unit, crossing into later files (by
// change: groups).
func (m *Model) nextUnit() {
	// The unit after the cursor's (the top unit when the cursor is out of
	// view): by change the pane may be scrolled above the first header
	// (group header rows), which already counts as being on the first unit.
	if ui := m.cursorUnitIndex(); ui+1 < len(m.p.heads) {
		m.off = m.p.heads[ui+1]
		m.cursorToUnit(m.p.units[ui+1].u.ID)
		return
	}
	for fi := m.sel + 1; fi < m.entries(); fi++ {
		if id := m.edgeVisible(fi, true); id != "" {
			m.focusRow(fi, id)
			return
		}
	}
	m.note = "no next unit (f: filters)"
}

// prevUnit moves to the previous visible unit, crossing into earlier files.
// The cursor's unit counts as previous when its header is scrolled out of
// view.
func (m *Model) prevUnit() {
	ui := m.cursorUnitIndex()
	if ui >= 0 && m.p.heads[ui] >= m.off {
		ui--
	}
	if ui >= 0 {
		m.off = m.p.heads[ui]
		m.cursorToUnit(m.p.units[ui].u.ID)
		return
	}
	for fi := m.sel - 1; fi >= 0; fi-- {
		if id := m.edgeVisible(fi, false); id != "" {
			m.focusRow(fi, id)
			return
		}
	}
	m.note = "no previous unit (f: filters)"
}

// edgeVisible returns the first (or last) visible unit of entry fi, or "".
func (m *Model) edgeVisible(fi int, first bool) string {
	ids := m.entryUnits(fi)
	for i := range ids {
		j := i
		if !first {
			j = len(ids) - 1 - i
		}
		if m.visible(ids[j]) {
			return ids[j]
		}
	}
	return ""
}

// focusRow selects file fi and puts unit id's header at the top.
func (m *Model) focusRow(fi int, id string) {
	m.sel = fi
	m.rebuild()
	m.off = max(0, m.p.headOf(id))
	m.cursorToUnit(id)
}

// Update handles non-key messages: the mouse wheel scrolls the pane (or
// moves the selection over the list) and a left click selects a list row.
func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	mm, ok := msg.(tea.MouseMsg)
	if !ok {
		return m, nil
	}
	if m.c.plain() {
		var cmd tea.Cmd
		m.dv, cmd = m.dv.Update(msg)
		return m, cmd
	}
	if m.comp != nil || m.panel != nil || m.picker != nil {
		return m, nil // the composer, panel and picker take the keyboard only
	}
	lay := m.layout()
	x, y := mm.X-m.ox, mm.Y-m.oy
	inList := lay.listW > 0 && x >= 0 && x < lay.listW && y >= lay.top && y < lay.top+lay.bodyH
	switch mm.Button {
	case tea.MouseButtonWheelUp:
		if inList {
			m.selectEntry(m.sel - 1)
		} else if !m.overlay && m.picker == nil {
			m.scrollBy(-wheelStep)
		}
	case tea.MouseButtonWheelDown:
		if inList {
			m.selectEntry(m.sel + 1)
		} else if !m.overlay && m.picker == nil {
			m.scrollBy(wheelStep)
		}
	case tea.MouseButtonLeft:
		if r := y - lay.top - lay.listY; inList && mm.Action == tea.MouseActionPress && r >= 0 && r < lay.listH {
			m.clickList(r)
		}
	}
	m.keepCursorVisible()
	return m, nil
}

// clickList selects the file on list row r (relative to the list top); by
// change, the group on that row, or a split-suggestion row's group.
func (m *Model) clickList(r int) {
	if m.byChange() {
		m.clickGroupList(m.listOff + r)
		return
	}
	rows := m.listRows()
	if i := m.listOff + r; i >= 0 && i < len(rows) && rows[i] >= 0 {
		m.selectEntry(rows[i])
	}
}
