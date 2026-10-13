package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// groupedDetail is pr:1 with three units in two groups (no aggregate, so
// every unit shows and no filter applies).
func groupedDetail(it model.Item) proto.Detail {
	unit := func(id, path, sym string, line int) model.ChangeUnit {
		return model.ChangeUnit{ID: id, ItemID: it.ID, Path: path, Symbol: sym, Kind: model.UnitAdded, Adds: 1,
			Diff: "+func " + sym + "() {}", Range: model.Range{New: model.Span{Start: line, Count: 1}}}
	}
	return proto.Detail{
		Item:  it,
		Files: []model.File{{Path: "f.go", Adds: 2}, {Path: "g.go", Adds: 1}},
		Units: []model.ChangeUnit{unit("a1", "f.go", "A", 1), unit("a2", "f.go", "B", 3), unit("b1", "g.go", "C", 1)},
		Groups: []model.Group{
			{ID: "g1", UnitIDs: []string{"a1", "a2"}, Name: "Do the thing", NameSource: model.NameBullet, NameP: 0.9,
				Links: []model.Link{{From: "a2", To: "a1", Kind: model.LinkCalls, Weight: 1, Source: model.LinkStructural}}},
			{ID: "g2", UnitIDs: []string{"b1"}, Name: "C", NameSource: model.NamePlaceholder, Order: 1, Ungrouped: true},
		},
	}
}

// codeModel returns a model showing pr:1's grouped detail on the Code tab
// with the main pane focused.
func codeModel(t *testing.T, f *fakeClient, o Options) Model {
	t.Helper()
	it := threeItems()[0]
	f.details[it.ID] = groupedDetail(it)
	m := newTestModel(t, f, o)
	m = feed[detailMsg](t, m, m.selectionChanged())
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("enter"))
	if m.tab != TabCode || m.focus != FocusMain {
		t.Fatalf("tab %v focus %v", m.tab, m.focus)
	}
	return m
}

// press applies the keys ks in order (dropping commands).
func press(t *testing.T, m Model, ks ...string) Model {
	t.Helper()
	for _, k := range ks {
		m = update(t, m, keyMsg(k))
	}
	return m
}

func TestModeKeyRouting(t *testing.T) {
	m, p := withFakePane(newTestModel(t, newFake(threeItems()...), Options{}))
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, keyMsg("g"))
	if p.mode != codeview.ModeChange || p.offered[len(p.offered)-1] != "g" {
		t.Fatalf("g did not reach the pane: mode %v offered %v", p.mode, p.offered)
	}
	// Picker focus: g is still top.
	m = update(t, m, keyMsg("esc"))
	m = update(t, m, keyMsg("j"))
	m = update(t, m, keyMsg("g"))
	if selectedID(m) != "pr:1" || p.mode != codeview.ModeChange {
		t.Errorf("picker g: selected %s, pane mode %v", selectedID(m), p.mode)
	}
}

// Any key routed to the pane sends the pane's pending edit.
func TestPendingEditSentAfterRoutedKey(t *testing.T) {
	f := newFake(threeItems()...)
	m, p := withFakePane(newTestModel(t, f, Options{}))
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("enter"))
	p.pending = &model.GroupEdit{Op: model.EditReset}
	m, cmd := updateCmd(t, m, keyMsg("n"))
	m = feed[groupEditedMsg](t, m, cmd)
	if len(f.groupEdits) != 1 || f.groupEdits[0].ItemID != "pr:1" || f.groupEdits[0].Edit.ItemID != "pr:1" {
		t.Fatalf("group edits = %+v", f.groupEdits)
	}
	if m.note != "regrouping…" {
		t.Errorf("note %q", m.note)
	}
	if _, cmd := updateCmd(t, m, keyMsg("n")); cmd != nil {
		t.Error("an edit was sent twice")
	}
}

func TestMergeSendsEditGroups(t *testing.T) {
	f := newFake(threeItems()...)
	m := codeModel(t, f, Options{})
	m = press(t, m, "g", "M", "J")
	if m.code.Mode() != codeview.ModeChange {
		t.Fatal("not by change")
	}
	m, cmd := updateCmd(t, m, keyMsg("M"))
	if cmd == nil {
		t.Fatal("M on a second group returned no command")
	}
	m = feed[groupEditedMsg](t, m, cmd)
	if len(f.groupEdits) != 1 {
		t.Fatalf("group edits = %+v", f.groupEdits)
	}
	e := f.groupEdits[0]
	if e.ItemID != "pr:1" || e.Edit.ItemID != "pr:1" || e.Edit.Op != model.EditMerge || fmt.Sprint(e.Edit.Units) != "[a1 a2 b1]" {
		t.Errorf("edit = %+v", e)
	}
	if !strings.Contains(m.viewStatus(), "regrouping…") {
		t.Errorf("status = %q", m.viewStatus())
	}
}

func TestSplitSendsEditGroups(t *testing.T) {
	f := newFake(threeItems()...)
	m := codeModel(t, f, Options{})
	m = press(t, m, "g", "n")
	_, cmd := updateCmd(t, m, keyMsg("X"))
	run(cmd)
	if len(f.groupEdits) != 1 || f.groupEdits[0].Edit.Op != model.EditSplit || fmt.Sprint(f.groupEdits[0].Edit.Units) != "[a2]" {
		t.Fatalf("group edits = %+v", f.groupEdits)
	}
}

func TestEditGroupsError(t *testing.T) {
	f := newFake(threeItems()...)
	f.editErr = errors.New("daemon busy")
	m := codeModel(t, f, Options{})
	m = press(t, m, "g")
	m, cmd := updateCmd(t, m, keyMsg("X"))
	m = feed[groupEditedMsg](t, m, cmd)
	if m.note != "regroup failed: daemon busy" {
		t.Errorf("note %q", m.note)
	}
}

func TestByChangeHintsAndHelp(t *testing.T) {
	m := codeModel(t, newFake(threeItems()...), Options{})
	if s := m.viewStatus(); strings.Contains(s, "merge groups") {
		t.Errorf("by file status has merge: %q", s)
	}
	m = press(t, m, "g")
	if s := m.viewStatus(); !strings.Contains(s, "M merge groups") {
		t.Errorf("by change status lacks merge: %q", s)
	}
	m = press(t, m, "?")
	v := m.View()
	for _, want := range []string{"g", "by file / by change", "merge groups", "split group"} {
		if !strings.Contains(v, want) {
			t.Errorf("help lacks %q:\n%s", want, v)
		}
	}
}

func TestRebindModeMergeSplit(t *testing.T) {
	def := DefaultKeyMap()
	if got := fmt.Sprint(def.codeKeys().Top.Keys(), def.Top.Keys(), def.codeKeys().Mode.Keys()); got != "[home] [g home] [g]" {
		t.Errorf("default code top/picker top/mode = %s", got)
	}
	km, err := KeyMapFromConfig(map[string]string{"mode": "m", "merge_groups": "ctrl+g", "split_group": "S"})
	if err != nil {
		t.Fatal(err)
	}
	ck := km.codeKeys()
	if got := fmt.Sprint(ck.Mode.Keys(), ck.MergeGroups.Keys(), ck.SplitGroup.Keys(), ck.Top.Keys()); got != "[m] [ctrl+g] [S] [g home]" {
		t.Errorf("rebound code keys = %s", got)
	}
	acts := strings.Join(KeyActions(), " ")
	for _, a := range []string{"mode", "merge_groups", "split_group"} {
		if !strings.Contains(acts, a) {
			t.Errorf("KeyActions missing %s", a)
		}
	}
	f := newFake(threeItems()...)
	m := codeModel(t, f, Options{Keys: &km})
	m = press(t, m, "m")
	if m.code.Mode() != codeview.ModeChange {
		t.Fatal("rebound mode key did not switch")
	}
	_, cmd := updateCmd(t, m, keyMsg("S"))
	run(cmd)
	if len(f.groupEdits) != 1 || fmt.Sprint(f.groupEdits[0].Edit.Units) != "[a1]" {
		t.Errorf("rebound split: %+v", f.groupEdits)
	}
}

func TestSnapshotCodeByChange(t *testing.T) {
	it := threeItems()[0]
	f := newFake(it)
	f.details[it.ID] = groupedDetail(it)
	var out bytes.Buffer
	if err := SnapshotWithOptions(context.Background(), f, &out, 120, 24, 1, Options{Now: func() time.Time { return fixedNow }}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	head := "=== screen: picker + code-by-change · pr:1 ==="
	i := strings.Index(s, head)
	if i < 0 || !strings.Contains(s, "=== screen: picker + code · pr:1 ===") {
		t.Fatalf("snapshot lacks the by-change screen:\n%s", s)
	}
	screen := s[i:]
	if j := strings.Index(screen[len(head):], "=== screen:"); j >= 0 {
		screen = screen[:len(head)+j]
	}
	for _, want := range []string{"Do the thing", "why these belong together", "‹C›"} {
		if !strings.Contains(screen, want) {
			t.Errorf("by-change screen lacks %q:\n%s", want, screen)
		}
	}
}
