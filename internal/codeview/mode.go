package codeview

import "github.com/fredricw/prt/internal/model"

// Notes shown by the mode and grouping keys.
const (
	noteNoGroups    = "no groups yet"
	noteMergeStart  = "merge: select another group and press M (esc cancels)"
	noteMergeCancel = "merge cancelled"
	noteByChange    = "by change only (g switches)"
)

// byChange reports whether mode B is in effect: requested and the detail
// has groups.
func (m *Model) byChange() bool { return m.mode == ModeChange && len(m.c.groups) > 0 }

// Mode returns the reading mode in effect (ModeFile while the detail has no
// groups, whatever was requested).
func (m Model) Mode() Mode {
	if m.byChange() {
		return ModeChange
	}
	return ModeFile
}

// SetMode switches the reading mode, keeping the cursor on the same unit.
// ModeChange without groups (analysis pending, a pre-M2 cache) keeps by
// file, sets the note "no groups yet" and reports false.
func (m *Model) SetMode(md Mode) bool {
	if md == ModeChange && len(m.c.groups) == 0 {
		m.mode = ModeFile
		if !m.c.plain() {
			m.note = noteNoGroups
		}
		return false
	}
	if md != m.Mode() {
		m.switchMode(md)
		m.keepCursorVisible()
	}
	return true
}

// toggleMode is the Mode key.
func (m *Model) toggleMode() {
	if m.byChange() {
		m.SetMode(ModeFile)
		return
	}
	m.SetMode(ModeChange)
}

// switchMode changes mode and puts the unit under the cursor at the same
// place in the new layout (unit headers render the same in both modes).
func (m *Model) switchMode(md Mode) {
	id, delta := m.cursor()
	m.mode = md
	m.mergeMark = ""
	m.sel, m.off = 0, 0
	if id == "" || !m.place(id, delta) {
		m.rebuild()
	}
}

// entries is the number of list entries: files by file, groups by change.
func (m *Model) entries() int {
	if m.byChange() {
		return len(m.c.groups)
	}
	return len(m.c.files)
}

// entryUnits are the units entry i draws, in order.
func (m *Model) entryUnits(i int) []string {
	if m.byChange() {
		return m.c.groups[i].paneUnits()
	}
	return m.c.files[i].units
}

// entryOf returns the index of the entry holding unitID, or -1.
func (m *Model) entryOf(unitID string) int {
	if m.byChange() {
		if gi, ok := m.c.groupOf[unitID]; ok {
			return gi
		}
		return -1
	}
	return m.c.fileOf(unitID)
}

// PendingEdit returns the grouping edit made by the merge or split keys
// and clears it; ok is false when there is none. The component cannot run
// commands, so the root model sends it to the daemon.
func (m *Model) PendingEdit() (model.GroupEdit, bool) {
	if m.pending == nil {
		return model.GroupEdit{}, false
	}
	e := *m.pending
	m.pending = nil
	return e, true
}

// mergeKey implements MergeGroups: the first press marks the selected
// group, a press on another group requests the merge of both, a press on
// the marked group cancels.
func (m *Model) mergeKey() {
	if !m.byChange() {
		m.note = "merge: " + noteByChange
		return
	}
	cur := m.c.groups[m.sel].g.ID
	switch m.mergeMark {
	case "":
		m.mergeMark = cur
		m.note = noteMergeStart
	case cur:
		m.mergeMark = ""
		m.note = noteMergeCancel
	default:
		a, b := m.c.groupIndex(m.mergeMark), m.sel
		m.mergeMark = ""
		if a < 0 {
			m.note = noteMergeCancel
			return
		}
		m.pending = &model.GroupEdit{ItemID: m.c.itemID, Op: model.EditMerge, Units: m.union(min(a, b), max(a, b))}
	}
}

// union lists group a's units, then group b's, without duplicates.
func (m *Model) union(a, b int) []string {
	seen := map[string]bool{}
	var out []string
	for _, gi := range []int{a, b} {
		for _, id := range m.c.groups[gi].units {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

// splitKey implements SplitGroup: the unit under the cursor through the end
// of the group's review order split off into a new group; on the first unit
// only that unit splits off. Refused (with a note) by file, and on
// ungrouped, mechanical and one-unit groups.
func (m *Model) splitKey() {
	if !m.byChange() {
		m.note = "split: " + noteByChange
		return
	}
	ge := &m.c.groups[m.sel]
	switch {
	case ge.g.Ungrouped:
		m.note = "split: an ungrouped unit is already on its own"
		return
	case ge.mechanical():
		m.note = "split: mechanical groups are not split (units that differ are promoted out)"
		return
	case len(ge.units) < 2:
		m.note = "split: a one-unit group cannot be split"
		return
	}
	id := m.cursorUnitID()
	at := -1
	for i, u := range ge.units {
		if u == id {
			at = i
		}
	}
	var units []string
	switch {
	case at < 0:
		m.note = "split: no unit under the cursor (f: filters)"
		return
	case at == 0:
		units = []string{ge.units[0]}
	default:
		units = append(units, ge.units[at:]...)
	}
	m.pending = &model.GroupEdit{ItemID: m.c.itemID, Op: model.EditSplit, Units: units}
}

// cancelMerge clears the merge mark; it reports whether one was set.
func (m *Model) cancelMerge() bool {
	if m.mergeMark == "" {
		return false
	}
	m.mergeMark = ""
	m.note = noteMergeCancel
	return true
}
