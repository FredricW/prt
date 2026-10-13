package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

func threeItems() []model.Item {
	return []model.Item{
		{ID: "pr:1", Kind: model.KindPR, Number: 1, Title: "one", Section: model.SectionNeedsYou, Urgency: 3, State: model.StateReady,
			URL: "https://github.com/o/r/pull/1", Ref: "feat/one", BaseRef: "main", HeadSHA: "a1"},
		{ID: "pr:2", Kind: model.KindPR, Number: 2, Title: "two", Section: model.SectionNeedsYou, Urgency: 1, State: model.StateReady, HeadSHA: "a2"},
		{ID: "branch:feat/x", Kind: model.KindBranch, Ref: "feat/x", Section: model.SectionBranches, State: model.StateReady, HeadSHA: "a3"},
	}
}

// longDiff is a one-file diff with n added lines.
func longDiff(n int) string {
	var b strings.Builder
	b.WriteString("diff --git a/f.go b/f.go\n--- a/f.go\n+++ b/f.go\n")
	fmt.Fprintf(&b, "@@ -0,0 +1,%d @@\n", n)
	for i := range n {
		fmt.Fprintf(&b, "+line %d\n", i)
	}
	return b.String()
}

func selectedID(m Model) string {
	it, _ := m.picker.selected()
	return it.ID
}

func TestNavigationSkipsHeadersAndClamps(t *testing.T) {
	f := newFake(threeItems()...)
	m := newTestModel(t, f, Options{})
	steps := []struct {
		key  string
		want string
	}{
		{"k", "pr:1"}, // clamps at top
		{"j", "pr:2"},
		{"j", "branch:feat/x"}, // skips the Yours and Branches headers
		{"j", "branch:feat/x"}, // clamps at bottom
		{"g", "pr:1"},
		{"G", "branch:feat/x"},
		{"up", "pr:2"},
		{"down", "branch:feat/x"},
	}
	for i, s := range steps {
		m = update(t, m, keyMsg(s.key))
		if got := selectedID(m); got != s.want {
			t.Fatalf("step %d (%s): selected %s, want %s", i, s.key, got, s.want)
		}
	}
}

func TestSelectionLoadsLatestDetailOnly(t *testing.T) {
	f := newFake(threeItems()...)
	m := newTestModel(t, f, Options{})
	m, cmd1 := updateCmd(t, m, keyMsg("j")) // pr:2
	m, cmd2 := updateCmd(t, m, keyMsg("j")) // branch:feat/x
	stale, latest := run(cmd1), run(cmd2)
	if len(stale) != 1 || len(latest) != 1 {
		t.Fatalf("want one detail message per move, got %v / %v", stale, latest)
	}
	m = update(t, m, stale[0])
	if m.detail != nil {
		t.Fatalf("stale detail for %s was applied", m.detail.Item.ID)
	}
	m = update(t, m, latest[0])
	if m.detail == nil || m.detail.Item.ID != "branch:feat/x" {
		t.Fatalf("latest detail not applied: %+v", m.detail)
	}
}

func TestItemsKeepCursorOnSameItem(t *testing.T) {
	f := newFake(threeItems()...)
	m := newTestModel(t, f, Options{})
	m = update(t, m, keyMsg("j"))
	items := threeItems()
	items[1].Urgency = 10 // pr:2 moves to the top
	m = update(t, m, itemsMsg{items: items})
	if got := selectedID(m); got != "pr:2" || m.picker.cursor != 0 {
		t.Errorf("selected %s at %d, want pr:2 at 0", got, m.picker.cursor)
	}
}

func TestItemsStartAnalysisInOrderOnce(t *testing.T) {
	items := []model.Item{
		{ID: "pr:1", Section: model.SectionNeedsYou, Urgency: 3, HeadSHA: "s1"},
		{ID: "pr:2", Section: model.SectionNeedsYou, Urgency: 2, State: model.StateReady, HeadSHA: "s2"},
		{ID: "pr:3", Section: model.SectionNeedsYou, Urgency: 1, State: model.StateStale, HeadSHA: "s3"},
	}
	f := newFake(items...)
	m := NewWithOptions(f, plainRenderer(), Options{})
	m, cmd := updateCmd(t, m, itemsMsg{items: items})
	run(cmd)
	var got []string
	for _, p := range f.refreshes {
		if p.Force {
			t.Errorf("auto refresh of %s must not force", p.ItemID)
		}
		got = append(got, p.ItemID)
	}
	// pr:3 is stale: it waits for `r` (spec), it is not re-analysed by itself.
	if strings.Join(got, " ") != "pr:1" {
		t.Fatalf("refreshes = %v, want pr:1", got)
	}
	_, cmd = updateCmd(t, m, itemsMsg{items: items})
	run(cmd)
	if len(f.refreshes) != 1 {
		t.Errorf("same head SHA refreshed again: %v", f.refreshes)
	}
}

func TestInitShowsCachedItemsBeforeDiscovery(t *testing.T) {
	f := newFake(threeItems()...)
	m := NewWithOptions(f, plainRenderer(), Options{})
	var cached bool
	for _, msg := range run(m.Init()) {
		if im, ok := msg.(itemsMsg); ok && len(im.items) == 3 {
			cached = true
		}
	}
	if !cached {
		t.Fatal("Init must load stored items without waiting for discovery")
	}
}

func TestClosedConnectionShowsNote(t *testing.T) {
	m := newTestModel(t, newFake(threeItems()...), Options{})
	m = update(t, m, closedMsg{})
	if !strings.Contains(m.View(), "daemon connection lost") {
		t.Error("lost daemon connection must be visible")
	}
}

func TestMouse(t *testing.T) {
	f := newFake(threeItems()...)
	m := newTestModel(t, f, Options{})
	// Screen rows: 0 header line, 1 the sidebar box's top border, then the
	// list: 2 "── NEEDS YOU (2)", 3–4 pr:1, 5–6 pr:2, 7 "── YOURS (0)",
	// 8 "── BRANCHES (1)", 9–10 branch.
	click := func(y int) tea.MouseMsg {
		return tea.MouseMsg{X: 3, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	}
	m, cmd := updateCmd(t, m, click(10))
	if got := selectedID(m); got != "branch:feat/x" || cmd == nil {
		t.Fatalf("click on row 10 selected %s (cmd %v)", got, cmd != nil)
	}
	for _, y := range []int{0, 1, 2, 7, 8} { // header line, border, dividers: no change
		m = update(t, m, click(y))
		if got := selectedID(m); got != "branch:feat/x" {
			t.Fatalf("click on row %d moved the cursor to %s", y, got)
		}
	}
	m = update(t, m, click(6))
	if got := selectedID(m); got != "pr:2" {
		t.Fatalf("click on row 6 selected %s", got)
	}
	m = update(t, m, tea.MouseMsg{X: 3, Y: 4, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if got := selectedID(m); got != "pr:1" {
		t.Fatalf("wheel up in picker selected %s", got)
	}
	// Wheel over the overview scrolls it.
	d := proto.Detail{Item: threeItems()[0], Diff: longDiff(200)}
	d.Item.Body = strings.Repeat("A paragraph of description.\n\n", 80)
	m = update(t, m, detailMsg{seq: m.detailSeq, d: d})
	if m.overview.vp.TotalLineCount() <= m.overview.vp.Height {
		t.Fatalf("overview too short to scroll: %d lines", m.overview.vp.TotalLineCount())
	}
	m = update(t, m, tea.MouseMsg{X: 60, Y: 5, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.overview.vp.YOffset == 0 {
		t.Error("wheel over the overview did not scroll it")
	}
}

func TestRefreshTickDiscovers(t *testing.T) {
	f := newFake(threeItems()...)
	m := newTestModel(t, f, Options{})
	_, cmd := updateCmd(t, m, refreshTickMsg{})
	msgs := run(cmd)
	if f.discovers != 1 {
		t.Fatalf("discovers = %d, want 1", f.discovers)
	}
	var sawItems bool
	for _, msg := range msgs {
		if im, ok := msg.(itemsMsg); ok && len(im.items) == 3 {
			sawItems = true
		}
	}
	if !sawItems {
		t.Errorf("tick did not reload items: %v", msgs)
	}
}

func TestScheduleRefresh(t *testing.T) {
	m := NewWithOptions(newFake(), plainRenderer(), Options{})
	if m.scheduleRefresh() != nil {
		t.Error("RefreshEvery 0 must disable the periodic refresh")
	}
	m = NewWithOptions(newFake(), plainRenderer(), Options{RefreshEvery: 1})
	if m.scheduleRefresh() == nil {
		t.Error("RefreshEvery > 0 must schedule a refresh")
	}
}

func TestKeys(t *testing.T) {
	type check func(t *testing.T, m Model, f *fakeClient, cmd tea.Cmd, opened []string)
	focusIs := func(want Focus) check {
		return func(t *testing.T, m Model, _ *fakeClient, _ tea.Cmd, _ []string) {
			if m.focus != want {
				t.Errorf("focus = %v, want %v", m.focus, want)
			}
		}
	}
	tabIs := func(want Tab) check {
		return func(t *testing.T, m Model, _ *fakeClient, _ tea.Cmd, _ []string) {
			if m.tab != want {
				t.Errorf("tab = %v, want %v", m.tab, want)
			}
		}
	}
	noteHas := func(want string) check {
		return func(t *testing.T, m Model, _ *fakeClient, _ tea.Cmd, _ []string) {
			if !strings.Contains(m.note, want) {
				t.Errorf("note = %q, want it to contain %q", m.note, want)
			}
		}
	}
	quits := func(t *testing.T, _ Model, _ *fakeClient, cmd tea.Cmd, _ []string) {
		if cmd == nil {
			t.Fatal("no command")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Error("command is not tea.Quit")
		}
	}
	tests := []struct {
		name   string
		setup  []string // keys pressed first
		key    string
		opener bool
		cursor int
		checks []check
	}{
		{name: "enter focuses main", key: "enter", checks: []check{focusIs(FocusMain)}},
		{name: "esc returns", setup: []string{"enter"}, key: "esc", checks: []check{focusIs(FocusPicker)}},
		{name: "tab cycles", key: "tab", checks: []check{focusIs(FocusMain)}},
		{name: "tab cycles back", setup: []string{"tab"}, key: "tab", checks: []check{focusIs(FocusPicker)}},
		{name: "shift+tab cycles", key: "shift+tab", checks: []check{focusIs(FocusMain)}},
		{name: "2 code tab", key: "2", checks: []check{tabIs(TabCode)}},
		{name: "1 overview tab", setup: []string{"2"}, key: "1", checks: []check{tabIs(TabOverview)}},
		{name: "q quits", key: "q", checks: []check{quits}},
		{name: "ctrl+c quits", key: "ctrl+c", checks: []check{quits}},
		{name: "? opens help", key: "?", checks: []check{func(t *testing.T, m Model, _ *fakeClient, _ tea.Cmd, _ []string) {
			if !m.showHelp || !strings.Contains(m.View(), "Keys · picker") {
				t.Errorf("help overlay not shown:\n%s", m.View())
			}
		}}},
		{name: "? closes help", setup: []string{"?"}, key: "?", checks: []check{func(t *testing.T, m Model, _ *fakeClient, _ tea.Cmd, _ []string) {
			if m.showHelp {
				t.Error("help still shown")
			}
		}}},
		{name: "help lists code keys", setup: []string{"2", "enter"}, key: "?", checks: []check{func(t *testing.T, m Model, _ *fakeClient, _ tea.Cmd, _ []string) {
			if v := m.View(); !strings.Contains(v, "Keys · code") || !strings.Contains(v, "next file") {
				t.Errorf("code help missing:\n%s", v)
			}
		}}},
		{name: "r refreshes with force", key: "r", checks: []check{noteHas("refreshing #1 one"),
			func(t *testing.T, _ Model, f *fakeClient, cmd tea.Cmd, _ []string) {
				run(cmd)
				if len(f.refreshes) != 1 || !f.refreshes[0].Force || f.refreshes[0].ItemID != "pr:1" {
					t.Errorf("refreshes = %+v", f.refreshes)
				}
			}}},
		{name: "o opens url", key: "o", opener: true, checks: []check{func(t *testing.T, m Model, _ *fakeClient, cmd tea.Cmd, opened []string) {
			if len(opened) != 0 {
				t.Fatal("opener called inside Update")
			}
			msgs := run(cmd)
			if len(msgs) != 1 {
				t.Fatalf("msgs = %v", msgs)
			}
			m = update(t, m, msgs[0])
			if !strings.Contains(m.note, "opened https://github.com/o/r/pull/1") {
				t.Errorf("note = %q", m.note)
			}
		}}},
		{name: "o without opener", key: "o", checks: []check{noteHas("no opener")}},
		{name: "o on branch without url", key: "o", opener: true, cursor: 2, checks: []check{noteHas("no GitHub URL for feat/x")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(threeItems()...)
			var opened []string
			o := Options{}
			if tt.opener {
				o.Open = func(u string) error { opened = append(opened, u); return nil }
			}
			m := newTestModel(t, f, o)
			m.picker.moveTo(tt.cursor)
			for _, k := range tt.setup {
				m = update(t, m, keyMsg(k))
			}
			m, cmd := updateCmd(t, m, keyMsg(tt.key))
			for _, c := range tt.checks {
				c(t, m, f, cmd, opened)
			}
		})
	}
}

func TestOpenError(t *testing.T) {
	f := newFake(threeItems()...)
	m := newTestModel(t, f, Options{Open: func(string) error { return errors.New("no browser") }})
	m, cmd := updateCmd(t, m, keyMsg("o"))
	for _, msg := range run(cmd) {
		m = update(t, m, msg)
	}
	if !strings.Contains(m.note, "open failed: no browser") {
		t.Errorf("note = %q", m.note)
	}
}

func TestCheckout(t *testing.T) {
	tests := []struct {
		name      string
		dirty     bool
		answer    string // key pressed at the prompt; "" when no prompt expected
		wantCalls []bool // Force of each Checkout call
		wantNote  string
	}{
		{"clean tree", false, "", []bool{false}, "checked out feat/x"},
		{"dirty then yes", true, "y", []bool{false, true}, "checked out feat/x"},
		{"dirty then no", true, "n", []bool{false}, "checkout cancelled"},
		{"dirty then esc", true, "esc", []bool{false}, "checkout cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFake(threeItems()...)
			f.dirty = tt.dirty
			m := newTestModel(t, f, Options{})
			m, cmd := updateCmd(t, m, keyMsg("c"))
			m = feed[checkoutMsg](t, m, cmd)
			if tt.answer != "" {
				if m.confirm == nil {
					t.Fatal("no confirm prompt for a dirty tree")
				}
				if v := m.View(); !strings.Contains(v, "work tree dirty — checkout feat/x anyway? y/N") {
					t.Fatalf("prompt not in status line:\n%s", v)
				}
				m, cmd = updateCmd(t, m, keyMsg(tt.answer))
				m = feed[checkoutMsg](t, m, cmd)
			}
			if m.confirm != nil {
				t.Error("prompt still pending")
			}
			var got []bool
			for _, c := range f.checkouts {
				got = append(got, c.Force)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.wantCalls) {
				t.Errorf("checkout calls (force) = %v, want %v", got, tt.wantCalls)
			}
			if !strings.Contains(m.note, tt.wantNote) {
				t.Errorf("note = %q, want %q", m.note, tt.wantNote)
			}
		})
	}
}

// codeFocused returns a model on pr:1 with its detail loaded, the Code tab
// open and the main pane focused, and the recording pane behind it.
func codeFocused(t *testing.T) (Model, *fakePane) {
	t.Helper()
	items := threeItems()
	f := newFake(items...)
	f.details["pr:1"] = proto.Detail{Item: items[0], Diff: longDiff(200)}
	m, p := withFakePane(newTestModel(t, f, Options{}))
	m = feed[detailMsg](t, m, m.selectionChanged())
	if m.detail == nil {
		t.Fatal("detail not loaded")
	}
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("enter"))
	if m.tab != TabCode || m.focus != FocusMain {
		t.Fatalf("tab %v focus %v, want Code and main", m.tab, m.focus)
	}
	return m, p
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestCodeTabRoutesKeysToComponent(t *testing.T) {
	m, p := codeFocused(t)
	if fmt.Sprint(p.details) != "[pr:1]" || p.clears == 0 {
		t.Errorf("SetDetail calls %v, Clear calls %d", p.details, p.clears)
	}
	m = update(t, m, keyMsg("n"))
	m = update(t, m, keyMsg("j"))
	if got := strings.Join(p.offered, " "); got != "n j" {
		t.Errorf("keys offered to the code pane = %q, want \"n j\"", got)
	}
	if selectedID(m) != "pr:1" {
		t.Error("picker cursor moved while the main pane is focused")
	}

	// f opens the filters bar: 3 toggles a filter, 1 does not switch tabs,
	// q does not quit; esc closes the bar.
	m = update(t, m, keyMsg("f"))
	for _, k := range []string{"3", "1", "q"} {
		var cmd tea.Cmd
		m, cmd = updateCmd(t, m, keyMsg(k))
		if m.tab != TabCode {
			t.Fatalf("%s switched tabs while the filters bar is open", k)
		}
		if isQuit(cmd) {
			t.Fatalf("%s quit while the filters bar is open", k)
		}
	}
	m = update(t, m, keyMsg("esc"))
	if got := strings.Join(p.captured, " "); got != "3 1 q esc" {
		t.Errorf("captured keys = %q", got)
	}
	if m.focus != FocusMain {
		t.Error("esc closing the bar also left the main pane")
	}

	// Bar closed: unhandled keys fall through to the global keys.
	m = update(t, m, keyMsg("1"))
	if m.tab != TabOverview {
		t.Fatalf("1 with the bar closed: tab %v", m.tab)
	}
	m = update(t, m, keyMsg("2"))
	if m.tab != TabCode {
		t.Fatalf("2 with the bar closed: tab %v", m.tab)
	}
	if _, cmd := updateCmd(t, m, keyMsg("q")); !isQuit(cmd) {
		t.Error("q with the bar closed did not quit")
	}
	m = update(t, m, keyMsg("f"))
	if _, cmd := updateCmd(t, m, keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Error("ctrl+c must quit even while the bar captures keys")
	}
	m = update(t, m, keyMsg("esc"))
	m = update(t, m, keyMsg("esc"))
	m = update(t, m, keyMsg("j"))
	if m.focus != FocusPicker || selectedID(m) != "pr:2" {
		t.Errorf("after esc, j should move the picker (focus %v, selected %s)", m.focus, selectedID(m))
	}
}

func TestCodeTabUsesKeyMap(t *testing.T) {
	km, err := KeyMapFromConfig(map[string]string{"next_unit": "N", "filters": "F"})
	if err != nil {
		t.Fatal(err)
	}
	items := threeItems()
	m, p := withFakePane(newTestModel(t, newFake(items...), Options{Keys: &km}))
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, keyMsg("F"))
	if !p.capturing {
		t.Error("rebound filters key did not reach the pane as Filters")
	}
	if got := m.keys.codeKeys().NextUnit.Keys(); fmt.Sprint(got) != "[N]" {
		t.Errorf("codeKeys NextUnit = %v", got)
	}
}

// A burst of item events triggers one list reload at a time (plus one more
// if events arrived while it was in flight), not one per event.
func TestItemEventsCoalesceReloads(t *testing.T) {
	f := newFake(threeItems()...)
	m := newTestModel(t, f, Options{})
	it := threeItems()[2]
	var cmds []tea.Cmd
	for range 5 {
		var cmd tea.Cmd
		m, cmd = updateCmd(t, m, eventMsg{Type: proto.EventItem, ItemID: it.ID, Item: &it})
		cmds = append(cmds, cmd)
	}
	reloads := 0
	for _, c := range cmds {
		for _, msg := range run(c) {
			if _, ok := msg.(reloadedMsg); ok {
				reloads++
			}
		}
	}
	if reloads != 1 || !m.reloadPending || !m.reloadAgain {
		t.Fatalf("reloads = %d, pending %v again %v; want 1, true, true", reloads, m.reloadPending, m.reloadAgain)
	}
	items, _ := f.Items(context.Background())
	m, cmd := updateCmd(t, m, reloadedMsg{items: items})
	again := 0
	for _, msg := range run(cmd) {
		if _, ok := msg.(reloadedMsg); ok {
			again++
		}
	}
	if again != 1 || !m.reloadPending || m.reloadAgain {
		t.Fatalf("follow-up reloads = %d, pending %v again %v; want 1, true, false", again, m.reloadPending, m.reloadAgain)
	}
}

func TestOverviewContent(t *testing.T) {
	items := threeItems()
	it := items[0]
	it.Author, it.Body, it.Labels, it.Reviewers = "alice", "## Why\n\n- first point\n- second point", []string{"bug"}, []string{"bob"}
	it.UpdatedAt = ago(49 * 3600 * 1e9)
	f := newFake(it, items[1], items[2])
	f.details["pr:1"] = proto.Detail{Item: it, Diff: longDiff(3),
		Files: []model.File{{Path: "f.go", Adds: 3}, {Path: "new.go", OldPath: "old.go", Renamed: true, Adds: 1, Dels: 1}}}
	m := newTestModel(t, f, Options{})
	m = feed[detailMsg](t, m, m.selectionChanged())
	v := m.View()
	for _, want := range []string{
		"[1 Overview]", "2 Code", "3 Architecture", "@alice · 2d",
		"#1 one · alice · 2d · main ← feat/one", "Composition", "analysing…",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("overview missing %q:\n%s", want, v)
		}
	}
	m = update(t, m, keyMsg("enter"))
	m = update(t, m, keyMsg("G"))
	if v := m.View(); !strings.Contains(v, "• first point") {
		t.Errorf("description not at the bottom of the overview:\n%s", v)
	}
	// Jev unreachable: git facts (files, counts) instead of labels.
	d := f.details["pr:1"]
	d.Agg = &model.ItemAgg{LabelsMissing: true, UnitsTotal: 2}
	m = update(t, m, detailMsg{seq: m.detailSeq, d: d})
	m = update(t, m, keyMsg("g"))
	v = m.View()
	for _, want := range []string{
		"labels unavailable (Jev unreachable)", "+4 −1 · 2 files · 2 chunks", "f.go", "old.go → new.go",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("overview missing %q:\n%s", want, v)
		}
	}
}

func TestEvents(t *testing.T) {
	f := newFake(threeItems()...)
	m := newTestModel(t, f, Options{})
	m = update(t, m, eventMsg{Type: proto.EventLog, Note: "GitHub unavailable"})
	if !strings.Contains(m.viewHeader(), "GitHub unavailable") {
		t.Errorf("log note not in the header: %q", m.viewHeader())
	}
	busy := threeItems()[1]
	busy.State, busy.StateNote = model.StateAnalysing, "parse"
	m, cmd := updateCmd(t, m, eventMsg{Type: proto.EventItem, ItemID: busy.ID, Item: &busy})
	if !m.spinning || cmd == nil {
		t.Error("spinner not started for a busy item")
	}
	if !strings.Contains(m.View(), "analysing parse") {
		t.Errorf("busy state not shown:\n%s", m.View())
	}
	m = update(t, m, eventMsg{Type: proto.EventStage, ItemID: "pr:2", Stage: model.StageLink})
	if m.stages["pr:2"] != model.StageLink {
		t.Error("stage not recorded")
	}
}

func TestViewFitsTerminal(t *testing.T) {
	for _, size := range [][2]int{{120, 30}, {80, 20}, {50, 12}} {
		f := newFake(pickerItems()...)
		m := newTestModel(t, f, Options{})
		m = update(t, m, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = feed[detailMsg](t, m, m.selectionChanged())
		for _, tab := range []string{"1", "2", "?"} {
			m = update(t, m, keyMsg(tab))
			lines := strings.Split(m.View(), "\n")
			if len(lines) != size[1] {
				t.Errorf("%v tab %s: %d lines, want %d", size, tab, len(lines), size[1])
			}
			for _, l := range lines {
				if w := lipgloss.Width(l); w > size[0] {
					t.Errorf("%v tab %s: line width %d > %d: %q", size, tab, w, size[0], l)
				}
			}
		}
	}
}

func TestKeyMapFromConfig(t *testing.T) {
	km, err := KeyMapFromConfig(map[string]string{"down": "n, ctrl+n", "checkout": "C"})
	if err != nil {
		t.Fatal(err)
	}
	if got := km.Down.Keys(); fmt.Sprint(got) != "[n ctrl+n]" || km.Down.Help().Key != "n" {
		t.Errorf("down = %v (help %q)", got, km.Down.Help().Key)
	}
	f := newFake(threeItems()...)
	m := newTestModel(t, f, Options{Keys: &km})
	m = update(t, m, keyMsg("n"))
	if selectedID(m) != "pr:2" {
		t.Error("rebound down key did not move")
	}
	m = update(t, m, keyMsg("j"))
	if selectedID(m) != "pr:2" {
		t.Error("old binding still active")
	}
	for _, bad := range []map[string]string{{"fly": "f"}, {"down": " , "}} {
		if _, err := KeyMapFromConfig(bad); err == nil {
			t.Errorf("KeyMapFromConfig(%v) = nil error", bad)
		}
	}
	if len(KeyActions()) == 0 {
		t.Error("no key actions")
	}
}
