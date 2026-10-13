package codeview

import (
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Labels on demand (M5.2, docs/m52-open-latency.md fix 2). A Detail loaded
// with DetailParams.NoLabels (LabelsOmitted) carries no labels. The page
// asks for the units whose labels it needs (the raw-labels overlay's unit,
// the label picker's unit and, by change, the shown group's units for their
// interface role) through PendingLabels; the root fetches them and hands
// them over with SetLabels, or LabelsFailed. Until then the overlay and the
// picker say "loading labels…", never "no labels". A Detail with labels
// behaves as before: every unit's labels are on the page.

// loadingLabels is shown while a unit's labels are being fetched.
const loadingLabels = "loading labels…"

// labelsKnown reports whether unit id's labels are on the page.
func (c *content) labelsKnown(id string) bool {
	return !c.labelsOmitted || c.known[id]
}

// wantLabels asks for the labels of the ids that are not on the page and
// reports whether every one was. A unit whose fetch failed is asked for
// again only with retry (an explicit L or !), so a failing daemon is not
// asked on every rebuild; retry also asks again for a unit still waiting
// (the root does not start a second call while one is in flight).
func (m *Model) wantLabels(retry bool, ids ...string) bool {
	all := true
	for _, id := range ids {
		if id == "" || m.c.labelsKnown(id) {
			continue
		}
		all = false
		if m.labelWait[id] && !retry {
			continue
		}
		if _, failed := m.labelErr[id]; failed && !retry {
			continue
		}
		delete(m.labelWait, id)
		delete(m.labelErr, id)
		m.labelReq[id] = true
	}
	return all
}

// rewantLabels asks again for what the open overlay, label picker and
// by-change group need after a reload dropped the page's labels.
func (m *Model) rewantLabels() {
	if !m.c.labelsOmitted {
		return
	}
	if m.overlay {
		m.wantLabels(false, m.overlayUnit)
	}
	if m.picker != nil && m.picker.loading {
		m.wantLabels(false, m.picker.unit)
	}
	m.wantGroupLabels()
}

// wantGroupLabels asks, by change, for the labels of the shown group's
// units (their "interface" role in the Units rows).
func (m *Model) wantGroupLabels() {
	if !m.c.labelsOmitted || !m.byChange() || m.sel < 0 || m.sel >= len(m.c.groups) {
		return
	}
	m.wantLabels(false, m.c.groups[m.sel].units...)
}

// PendingLabels returns (once) the item and the units whose labels the page
// asked for since the last call, sorted; ok is false when there are none.
// The units then count as waiting until SetLabels or LabelsFailed answers.
func (m *Model) PendingLabels() (itemID string, unitIDs []string, ok bool) {
	for id := range m.labelReq {
		if !m.c.labelsKnown(id) && !m.labelWait[id] {
			unitIDs = append(unitIDs, id)
			m.labelWait[id] = true
		}
	}
	clear(m.labelReq)
	if len(unitIDs) == 0 {
		return "", nil, false
	}
	sort.Strings(unitIDs)
	return m.c.itemID, unitIDs, true
}

// SetLabels hands over every label of units (item itemID): they replace
// whatever the page held for those units, which then count as known (a
// unit without labels too). It does nothing for another item or a detail
// that carried its labels. A waiting label picker opens, the overlay
// redraws, and by change the Units rows show their roles.
func (m *Model) SetLabels(itemID string, units []string, labels []model.Label) {
	if itemID != m.c.itemID || !m.c.labelsOmitted || len(units) == 0 {
		return
	}
	set := make(map[string]bool, len(units))
	for _, id := range units {
		set[id] = true
		m.c.known[id] = true
		delete(m.labelWait, id)
		delete(m.labelErr, id)
	}
	keep := make([]model.Label, 0, len(m.c.labels)+len(labels))
	for _, l := range m.c.labels {
		if !set[l.UnitID] {
			keep = append(keep, l)
		}
	}
	for _, l := range labels {
		if set[l.UnitID] {
			keep = append(keep, l)
		}
	}
	m.c.labels = keep
	if m.picker != nil && m.picker.loading && set[m.picker.unit] {
		m.fillPicker()
	}
	if m.byChange() && m.sel < len(m.c.groups) && anyIn(set, m.c.groups[m.sel].units) {
		m.rebuildKeep()
		m.keepCursorVisible()
	}
}

// LabelsFailed reports that fetching the labels of units failed: the
// overlay says so, and a label picker waiting for them closes with a note.
func (m *Model) LabelsFailed(itemID string, units []string, err error) {
	if itemID != m.c.itemID || !m.c.labelsOmitted {
		return
	}
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	for _, id := range units {
		delete(m.labelWait, id)
		m.labelErr[id] = msg
		if m.picker != nil && m.picker.loading && m.picker.unit == id {
			m.picker = nil
			m.note = "mark a label wrong: labels unavailable (" + msg + ")"
		}
	}
}

// LabelsStale reports that the answer for units came for an analysis the
// page no longer shows (the item's head moved while the call was in
// flight): they stop waiting, and the page asks again for those the open
// overlay, a waiting label picker or the shown group still need.
func (m *Model) LabelsStale(itemID string, units []string) {
	if itemID != m.c.itemID || !m.c.labelsOmitted {
		return
	}
	for _, id := range units {
		delete(m.labelWait, id)
	}
	m.rewantLabels()
}

// labelsNote is the overlay's stand-in for the rows of unit id while its
// labels are not on the page: loading, or why the fetch failed.
func (m *Model) labelsNote(id string) string {
	if e, ok := m.labelErr[id]; ok {
		return "labels unavailable: " + e
	}
	return loadingLabels
}

// overlayTitle is the first line of RawLabelsText for u (path · symbol).
func overlayTitle(u model.ChangeUnit, width int) string {
	t, _, _ := strings.Cut(RawLabelsText(u, nil, width), "\n")
	return t
}

// anyIn reports whether any of ids is in set.
func anyIn(set map[string]bool, ids []string) bool {
	for _, id := range ids {
		if set[id] {
			return true
		}
	}
	return false
}
