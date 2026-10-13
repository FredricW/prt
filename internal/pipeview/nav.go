package pipeview

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/model"
)

// wheelStep is how many lines one mouse wheel notch scrolls.
const wheelStep = 3

// HandleKey handles a key; false when the page does not use it (the root
// then applies global keys). While Capturing, every key is consumed. Any
// key clears the one-shot note.
func (m *Model) HandleKey(k tea.KeyMsg) bool {
	if m.d.Item.ID == "" {
		return false
	}
	m.note = ""
	switch {
	case m.prompt:
		m.promptKey(k)
		return true
	case m.callLog:
		m.callLogKey(k)
		return true
	case m.stageLog:
		m.stageLogKey(k)
		return true
	}
	return m.pageKey(k)
}

// pageKey handles a key on the page itself (nothing open).
func (m *Model) pageKey(k tea.KeyMsg) bool {
	km := m.keys
	switch {
	case key.Matches(k, km.Down):
		m.move(1)
	case key.Matches(k, km.Up):
		m.move(-1)
	case key.Matches(k, km.Top):
		m.moveTo(0)
		m.off = 0
	case key.Matches(k, km.Bottom):
		m.moveTo(m.count() - 1)
		if m.panel == panelTable {
			m.off = m.maxOff() // show the panels under the table too
		}
	case key.Matches(k, km.NextPanel), key.Matches(k, km.PrevPanel):
		m.switchPanel()
	case key.Matches(k, km.Enter):
		if m.panel != panelStrip {
			return false
		}
		m.stageLog, m.logOff = true, 0
		m.follow()
	case key.Matches(k, km.ForceDeep):
		m.reviewAction(ActForceDeep)
	case key.Matches(k, km.Skip):
		m.reviewAction(ActSkip)
	case key.Matches(k, km.Rerun):
		m.pending = &Action{Kind: ActRerun, ItemID: m.d.Item.ID, Stage: m.selectedStage()}
	case key.Matches(k, km.EditRules):
		m.pending = &Action{Kind: ActEditRules, ItemID: m.d.Item.ID}
	case key.Matches(k, km.CallLog):
		m.askCallLog()
	case key.Matches(k, km.RaiseCap):
		m.openPrompt()
	default:
		return false
	}
	return true
}

// stageLogKey drives an open stage log: scroll keys scroll it, Enter or
// Close close it, Rerun re-runs the stage; other keys are swallowed.
func (m *Model) stageLogKey(k tea.KeyMsg) {
	km := m.keys
	last := max(0, len(m.run.stageRows(m.selectedStage()))-maxLogRows)
	switch {
	case key.Matches(k, km.Close), key.Matches(k, km.Enter):
		m.stageLog, m.logOff = false, 0
		m.clampOff()
	case key.Matches(k, km.Down):
		m.logOff = min(m.logOff+1, last)
	case key.Matches(k, km.Up):
		m.logOff = max(m.logOff-1, 0)
	case key.Matches(k, km.Top):
		m.logOff = 0
	case key.Matches(k, km.Bottom):
		m.logOff = last
	case key.Matches(k, km.Rerun):
		m.pending = &Action{Kind: ActRerun, ItemID: m.d.Item.ID, Stage: m.selectedStage()}
	}
}

// selectedStage is the stage r re-runs: the selected strip stage, or
// review in the routing table.
func (m *Model) selectedStage() model.Stage {
	if m.panel == panelTable {
		return model.StageReview
	}
	return stripStages[m.stage]
}

// reviewAction asks for D or s on the selected routing row, refusing a
// forced row for D and a skipped row for s.
func (m *Model) reviewAction(kind ActionKind) {
	gid, ok := m.SelectedGroup()
	if m.panel != panelTable || !ok {
		m.note = "select a group in the routing table first (J)"
		return
	}
	r := m.rows[m.row].rev
	switch {
	case kind == ActForceDeep && r.Forced:
		m.note = "already forced to the deep tier"
		return
	case kind == ActSkip && (r.UserSkipped || r.Status == model.ReviewSkipped):
		m.note = "review already skipped"
		return
	}
	m.pending = &Action{Kind: kind, ItemID: m.d.Item.ID, GroupID: gid}
}

// askCallLog asks for the selected row's call log; on the strip's agg
// stage, for the item's TL;DR calls.
func (m *Model) askCallLog() {
	switch {
	case m.panel == panelTable && len(m.rows) > 0:
		m.pending = &Action{Kind: ActCallLog, ItemID: m.d.Item.ID, GroupID: m.rows[m.row].rev.GroupID}
	case m.panel == panelStrip && stripStages[m.stage] == model.StageAggregate:
		m.pending = &Action{Kind: ActCallLog, ItemID: m.d.Item.ID}
	default:
		m.note = "call log: select a routing row (J), or the agg stage for the TL;DR calls"
	}
}

// count is the number of selectable entries in the current panel.
func (m *Model) count() int {
	if m.panel == panelTable {
		return len(m.rows)
	}
	return len(stripStages)
}

// move moves the selection by delta inside the current panel. At the end
// of the routing table it scrolls the page instead, so the panels below can
// be read with j.
func (m *Model) move(delta int) {
	cur := m.stage
	if m.panel == panelTable {
		cur = m.row
	}
	next := clampInt(cur+delta, 0, max(0, m.count()-1))
	if next == cur {
		m.off = clampInt(m.off+delta, 0, m.maxOff())
		return
	}
	m.moveTo(next)
}

// moveTo selects entry i of the current panel and keeps it visible.
func (m *Model) moveTo(i int) {
	i = clampInt(i, 0, max(0, m.count()-1))
	if m.panel == panelTable {
		m.row = i
	} else {
		m.stage = i
	}
	m.follow()
}

// switchPanel moves the selection between the strip and the table.
func (m *Model) switchPanel() {
	if m.panel == panelStrip {
		if len(m.rows) == 0 {
			m.note = "no routing rows yet"
			return
		}
		m.panel = panelTable
	} else {
		m.panel = panelStrip
	}
	m.follow()
}

// maxOff is the largest useful scroll offset.
func (m *Model) maxOff() int {
	return max(0, len(m.build().lines)-m.bodyHeight())
}

// clampOff keeps the scroll offset in range.
func (m *Model) clampOff() { m.off = clampInt(m.off, 0, m.maxOff()) }

// follow scrolls so the selection (the selected row, or the strip and an
// open stage log) is visible; when it is taller than the page, its top is.
func (m *Model) follow() {
	d := m.build()
	var top, end int
	switch {
	case m.panel == panelTable && m.row < len(d.rowTop):
		top, end = d.rowTop[m.row], d.rowEnd[m.row]
	default:
		top, end = d.stripTop-1, d.stripEnd // with the "Stages" heading
		if m.stageLog {
			end = d.logEnd
		}
	}
	bh := m.bodyHeight()
	switch {
	case end-top > bh, top < m.off:
		m.off = top
	case end > m.off+bh:
		m.off = end - bh
	}
	m.off = clampInt(m.off, 0, max(0, len(d.lines)-bh))
}

// Update handles non-key messages: the mouse wheel scrolls the page (or the
// call log) and a left click selects a stage box or a routing row.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	mm, ok := msg.(tea.MouseMsg)
	if !ok || m.d.Item.ID == "" {
		return nil
	}
	if m.callLog {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(msg)
		return cmd
	}
	switch mm.Button {
	case tea.MouseButtonWheelUp:
		m.off = clampInt(m.off-wheelStep, 0, m.maxOff())
	case tea.MouseButtonWheelDown:
		m.off = clampInt(m.off+wheelStep, 0, m.maxOff())
	case tea.MouseButtonLeft:
		if mm.Action == tea.MouseActionPress && !m.prompt {
			m.click(mm.X-m.ox, mm.Y-m.oy)
		}
	}
	return nil
}

// click selects what is under page cell (x, y).
func (m *Model) click(x, y int) {
	if y < 0 || y >= m.bodyHeight() {
		return
	}
	d := m.build()
	line := y + clampInt(m.off, 0, max(0, len(d.lines)-m.bodyHeight()))
	for _, h := range d.hits {
		if !h.contains(x, line) {
			continue
		}
		if h.table {
			m.panel, m.row = panelTable, h.idx
		} else {
			if m.stage != h.idx {
				m.stageLog, m.logOff = false, 0
			}
			m.panel, m.stage = panelStrip, h.idx
		}
		return
	}
}
