package tui

import (
	"context"
	"fmt"
	"sort"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// Labels on demand (M5.2, docs/m52-open-latency.md fix 2). Details are
// loaded without labels; the Overview's `L`, the Code page's `L` and `!`
// and the Code page's by-change roles ask for one unit's (or one group's)
// labels, which come from client.Labels and are cached per item, analysed
// head and unit. A run that finishes, a new analysed head, an override of
// the unit or the item's removal drops them.

// itemLabels are one item's labels fetched so far: the analysed head they
// belong to and, per unit, its labels (an empty slice: the unit has none).
type itemLabels struct {
	head  string
	units map[string][]model.Label
}

// labelState is the label cache and the units being fetched, shared by the
// copies of the root model.
type labelState struct {
	byItem   map[string]*itemLabels
	inflight map[string]map[string]bool // item id → unit ids being fetched
}

func newLabelState() *labelState {
	return &labelState{byItem: map[string]*itemLabels{}, inflight: map[string]map[string]bool{}}
}

// labelsMsg answers a Labels call: the item, the head the labels belong to,
// the units asked for and their labels.
type labelsMsg struct {
	itemID, head string
	units        []string
	labels       []model.Label
	err          error
}

// labelHead is the analysed head whose labels d's pages show: the
// snapshot's head while a run is in flight, else the item's analysed head.
func labelHead(d proto.Detail) string {
	if d.Run != nil && d.Run.Snapshot && d.Run.SnapshotSHA != "" {
		return d.Run.SnapshotSHA
	}
	return d.Item.AnalysedSHA
}

// entryFor returns the cache entry of id at head, starting a new one when
// the head moved.
func (s *labelState) entryFor(id, head string) *itemLabels {
	e := s.byItem[id]
	if e == nil || e.head != head {
		e = &itemLabels{head: head, units: map[string][]model.Label{}}
		s.byItem[id] = e
	}
	return e
}

// unitLabels returns unit's labels of the shown detail: Detail.Labels when
// it carries them, else the cache; ok is false while they are unknown.
func (m Model) unitLabels(unit string) ([]model.Label, bool) {
	if m.detail == nil {
		return nil, false
	}
	if !m.detail.LabelsOmitted {
		return m.detail.Labels, true
	}
	e := m.labels.byItem[m.detail.Item.ID]
	if e == nil || e.head != labelHead(*m.detail) {
		return nil, false
	}
	ls, ok := e.units[unit]
	return ls, ok
}

// fetchLabels starts one Labels call for the units of the shown item that
// are neither cached nor being fetched; nil when there are none.
func (m *Model) fetchLabels(units []string) tea.Cmd {
	if m.detail == nil || !m.detail.LabelsOmitted {
		return nil
	}
	id, head := m.detail.Item.ID, labelHead(*m.detail)
	busy := m.labels.inflight[id]
	if busy == nil {
		busy = map[string]bool{}
		m.labels.inflight[id] = busy
	}
	var want []string
	for _, u := range units {
		if _, cached := m.unitLabels(u); cached || busy[u] {
			continue
		}
		busy[u] = true
		want = append(want, u)
	}
	if len(want) == 0 {
		return nil
	}
	sort.Strings(want)
	c := m.client
	return func() tea.Msg {
		ls, err := c.Labels(context.Background(), proto.LabelsParams{ItemID: id, UnitIDs: want})
		return labelsMsg{itemID: id, head: head, units: want, labels: ls, err: err}
	}
}

// onLabels caches a Labels answer and hands it to the pages showing those
// units: the Code page and the Overview's raw-labels overlay.
func (m Model) onLabels(msg labelsMsg) (tea.Model, tea.Cmd) {
	if busy := m.labels.inflight[msg.itemID]; busy != nil {
		for _, u := range msg.units {
			delete(busy, u)
		}
	}
	sameItem := m.detail != nil && m.detail.Item.ID == msg.itemID
	shown := sameItem && labelHead(*m.detail) == msg.head
	if sameItem && !shown {
		// The analysis moved while the call was in flight: the answer (or
		// error) is for a head the pages no longer show, so it is dropped
		// (caching it would replace the shown head's entry). Ask again for
		// the shown head, so the Code page and the overlay do not wait for
		// good.
		m.code.LabelsStale(msg.itemID, msg.units)
		return m, batch(m.codeLabels(), m.refetchRawLabels(msg.units))
	}
	if msg.err != nil {
		if shown {
			m.code.LabelsFailed(msg.itemID, msg.units, msg.err)
			m.note = fmt.Sprintf("labels: %v", msg.err)
			m.refreshRawLabels(msg.units, msg.err)
		}
		return m, nil
	}
	m.cacheLabels(msg)
	if !shown {
		return m, nil
	}
	m.code.SetLabels(msg.itemID, msg.units, msg.labels)
	m.refreshRawLabels(msg.units, nil)
	return m, m.codeLabels()
}

// cacheLabels stores a good Labels answer under its item and head (an
// answer for another head than the cached one starts a new entry).
func (m *Model) cacheLabels(msg labelsMsg) {
	if msg.err != nil {
		return
	}
	e := m.labels.entryFor(msg.itemID, msg.head)
	byUnit := make(map[string][]model.Label, len(msg.units))
	for _, u := range msg.units {
		byUnit[u] = []model.Label{}
	}
	for _, l := range msg.labels {
		if _, asked := byUnit[l.UnitID]; asked {
			byUnit[l.UnitID] = append(byUnit[l.UnitID], l)
		}
	}
	for u, ls := range byUnit {
		e.units[u] = ls
	}
}

// refetchRawLabels asks again for the raw-labels overlay's unit when it is
// among units (their answer came for a head no longer shown).
func (m *Model) refetchRawLabels(units []string) tea.Cmd {
	o := &m.overview
	if !o.raw || o.rawUnit == "" {
		return nil
	}
	for _, u := range units {
		if u == o.rawUnit {
			return m.fetchLabels([]string{u})
		}
	}
	return nil
}

// supplyLabels hands the Code page the cached labels of d's item after a
// SetDetail without labels dropped them from the page.
func (m *Model) supplyLabels(d proto.Detail) {
	if !d.LabelsOmitted {
		return
	}
	e := m.labels.byItem[d.Item.ID]
	if e == nil || e.head != labelHead(d) || len(e.units) == 0 {
		return
	}
	units := make([]string, 0, len(e.units))
	var ls []model.Label
	for u, us := range e.units {
		units = append(units, u)
		ls = append(ls, us...)
	}
	sort.Strings(units)
	m.code.SetLabels(d.Item.ID, units, ls)
}

// codeLabels drains the Code page's label requests: cached units are
// handed over at once, the rest are fetched.
func (m *Model) codeLabels() tea.Cmd {
	id, units, ok := m.code.PendingLabels()
	if !ok || m.detail == nil || id != m.detail.Item.ID {
		return nil
	}
	var have, missing []string
	var ls []model.Label
	for _, u := range units {
		if us, ok := m.unitLabels(u); ok {
			have = append(have, u)
			ls = append(ls, us...)
			continue
		}
		missing = append(missing, u)
	}
	if len(have) > 0 {
		m.code.SetLabels(id, have, ls)
	}
	return m.fetchLabels(missing)
}

// dropLabels forgets every cached label of item id.
func (m *Model) dropLabels(id string) {
	delete(m.labels.byItem, id)
}

// dropUnitLabels forgets the cached labels of one unit of item id (its
// label was overridden).
func (m *Model) dropUnitLabels(id, unit string) {
	if e := m.labels.byItem[id]; e != nil {
		delete(e.units, unit)
	}
}
