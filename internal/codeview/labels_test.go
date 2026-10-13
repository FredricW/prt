package codeview

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// omitted is d's fixture loaded without labels (DetailParams.NoLabels),
// and the labels it would have carried.
func omitted(t *testing.T) (Model, []model.Label) {
	t.Helper()
	d := loadDetail(t)
	all := d.Labels
	d.Labels, d.LabelsOmitted = nil, true
	return newModel(t, d, 120, 30), all
}

func labelsOf(all []model.Label, id string) []model.Label {
	var out []model.Label
	for _, l := range all {
		if l.UnitID == id {
			out = append(out, l)
		}
	}
	return out
}

func TestLabelsOnDemandOverlay(t *testing.T) {
	m, all := omitted(t)
	if _, ids, ok := m.PendingLabels(); ok {
		t.Fatalf("labels asked for before any key: %v", ids)
	}
	press(t, &m, "L")
	v := m.View()
	contains(t, v, "web/src/api/client.ts · fetchJSON")
	contains(t, v, loadingLabels)
	lacks(t, v, "no labels yet")
	item, ids, ok := m.PendingLabels()
	if !ok || item != "pr:7" || fmt.Sprint(ids) != "[u1]" {
		t.Fatalf("PendingLabels = %q %v %v", item, ids, ok)
	}
	if _, ids, ok := m.PendingLabels(); ok {
		t.Errorf("asked twice: %v", ids)
	}
	m.SetLabels("pr:7", []string{"u1"}, labelsOf(all, "u1"))
	v = m.View()
	lacks(t, v, loadingLabels)
	contains(t, v, "unit  behaviour              yes                p .91  conf .91  typesafe")
	// Another item's answer changes nothing.
	m.SetLabels("pr:8", []string{"u2"}, labelsOf(all, "u2"))
	if m.c.labelsKnown("u2") {
		t.Error("labels of another item accepted")
	}
}

func TestLabelsOnDemandReloadAsksAgain(t *testing.T) {
	m, all := omitted(t)
	press(t, &m, "L")
	m.PendingLabels()
	m.SetLabels("pr:7", []string{"u1"}, labelsOf(all, "u1"))
	// A reload without labels drops them: the open overlay asks again.
	d := loadDetail(t)
	d.Labels, d.LabelsOmitted = nil, true
	m.SetDetail(d)
	if _, ids, ok := m.PendingLabels(); !ok || fmt.Sprint(ids) != "[u1]" {
		t.Fatalf("reload did not ask again: %v %v", ids, ok)
	}
	contains(t, m.View(), loadingLabels)
}

func TestLabelsOnDemandFailure(t *testing.T) {
	m, _ := omitted(t)
	press(t, &m, "L")
	_, ids, _ := m.PendingLabels()
	m.LabelsFailed("pr:7", ids, errors.New("daemon gone"))
	contains(t, m.View(), "labels unavailable: daemon gone")
	// Reopening retries.
	press(t, &m, "esc", "L")
	if _, ids, ok := m.PendingLabels(); !ok || fmt.Sprint(ids) != "[u1]" {
		t.Errorf("reopening did not retry: %v %v", ids, ok)
	}
}

func TestLabelsOnDemandPicker(t *testing.T) {
	m, all := omitted(t)
	press(t, &m, "!")
	if m.picker == nil || !m.picker.loading {
		t.Fatalf("picker %+v, want it waiting for labels", m.picker)
	}
	contains(t, m.View(), loadingLabels)
	lacks(t, m.View(), "no labels on this unit")
	// While loading, enter and j do nothing; the picker stays open.
	press(t, &m, "enter", "j")
	if _, ok := m.PendingComment(); ok || m.picker == nil {
		t.Fatal("a key acted on a loading picker")
	}
	_, ids, ok := m.PendingLabels()
	if !ok || fmt.Sprint(ids) != "[u1]" {
		t.Fatalf("PendingLabels = %v %v", ids, ok)
	}
	m.SetLabels("pr:7", ids, labelsOf(all, "u1"))
	if m.picker == nil || m.picker.loading || len(m.picker.labels) == 0 {
		t.Fatalf("picker not filled: %+v", m.picker)
	}
	press(t, &m, "enter", "enter")
	if op, ok := m.PendingComment(); !ok || op.Kind != CommentOpOverride || op.Override.UnitID != "u1" {
		t.Errorf("op %+v %v", op, ok)
	}
}

func TestLabelsOnDemandPickerFailsAndCancels(t *testing.T) {
	m, _ := omitted(t)
	press(t, &m, "!")
	_, ids, _ := m.PendingLabels()
	m.LabelsFailed("pr:7", ids, errors.New("boom"))
	if m.picker != nil || !strings.Contains(m.Note(), "labels unavailable (boom)") {
		t.Errorf("picker %v note %q", m.picker, m.Note())
	}
	press(t, &m, "!", "esc")
	if m.picker != nil {
		t.Error("esc did not cancel the waiting picker")
	}
	// No labels for the unit at all: the picker closes with the old note.
	press(t, &m, "!")
	_, ids, _ = m.PendingLabels()
	m.SetLabels("pr:7", ids, nil)
	if m.picker != nil || !strings.Contains(m.Note(), "no labels on this unit to correct") {
		t.Errorf("picker %v note %q", m.picker, m.Note())
	}
}

func TestLabelsOnDemandByChangeRoles(t *testing.T) {
	d := loadGroups(t)
	full := append(d.Labels, model.Label{UnitID: "c1", Question: model.QInterface, Value: model.LabelYes, P: 0.9, Confidence: 0.9})
	d.Labels, d.LabelsOmitted = nil, true
	m := byChangeModel(t, d, 120, 30, true)
	if strings.Contains(m.View(), "interface · eyes") || strings.Contains(m.View(), "caller · eyes") {
		t.Errorf("roles claimed before labels:\n%s", m.View())
	}
	item, ids, ok := m.PendingLabels()
	if !ok || item != "pr:11" || fmt.Sprint(ids) != "[c1 k1 t1]" {
		t.Fatalf("PendingLabels = %q %v %v", item, ids, ok)
	}
	var got []model.Label
	for _, id := range ids {
		got = append(got, labelsOf(full, id)...)
	}
	m.SetLabels(item, ids, got)
	v := m.View()
	contains(t, v, "interface · eyes")
	contains(t, v, "caller · eyes")
}

func TestLabelsCarriedNeverAsked(t *testing.T) {
	m := newModel(t, loadDetail(t), 120, 30)
	press(t, &m, "L", "esc", "!", "esc")
	if _, ids, ok := m.PendingLabels(); ok {
		t.Errorf("a detail with labels asked for %v", ids)
	}
}
