package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// ---- (B) instant open, stale-while-revalidate ---------------------------------

// fullFake is a fake over threeItems where pr:1 holds detail_full (with
// labels) and pr:2 a small detail.
func fullFake(t *testing.T) *fakeClient {
	t.Helper()
	items := threeItems()
	f := newFake(items...)
	d := loadDetail(t, "detail_full.json")
	d.Item = items[0]
	d.Item.AnalysedSHA = "a1"
	f.details["pr:1"] = d
	f.details["pr:2"] = proto.Detail{Item: items[1]}
	return f
}

// loaded returns a model on pr:1 with its detail loaded.
func loaded(t *testing.T, f *fakeClient) Model {
	t.Helper()
	m := newTestModel(t, f, Options{})
	m = feed[detailMsg](t, m, m.selectionChanged())
	if m.detail == nil || m.detail.Item.ID != "pr:1" {
		t.Fatalf("pr:1 not loaded: %+v", m.detail)
	}
	return m
}

func TestCachedDetailShowsAtOnce(t *testing.T) {
	f := fullFake(t)
	m := loaded(t, f)
	// pr:2 was never loaded: "loading…" until it lands.
	m, cmd := updateCmd(t, m, keyMsg("j"))
	if m.detail != nil || !strings.Contains(m.View(), "loading…") {
		t.Fatalf("an item never loaded must show loading…:\n%s", m.View())
	}
	m = feedAll[detailMsg](t, m, cmd)
	if m.detail == nil || m.detail.Item.ID != "pr:2" {
		t.Fatalf("pr:2 not shown: %+v", m.detail)
	}
	// Back on pr:1: its cached detail at once, no placeholder, and a
	// revalidation in the background.
	calls := len(f.detailParams)
	m, cmd = updateCmd(t, m, keyMsg("k"))
	if m.detail == nil || m.detail.Item.ID != "pr:1" || strings.Contains(m.View(), "loading…") {
		t.Fatalf("cached pr:1 not shown at once:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "Headlines") {
		t.Errorf("cached Overview not drawn:\n%s", m.View())
	}
	m = feedAll[detailMsg](t, m, cmd)
	if len(f.detailParams) != calls+1 {
		t.Fatalf("no revalidation: %d DetailOf calls, want %d", len(f.detailParams), calls+1)
	}
	for _, p := range f.detailParams {
		if !p.NoLabels {
			t.Errorf("a Detail was loaded with labels: %+v", p)
		}
	}
	if !m.detail.LabelsOmitted || len(m.detail.Labels) != 0 {
		t.Error("the shown detail carries labels")
	}
}

func TestCacheIsBoundedAndDropsRemoved(t *testing.T) {
	c := newDetailCache(2)
	for _, id := range []string{"a", "b", "c"} {
		c.put(proto.Detail{Item: model.Item{ID: id}})
	}
	if _, ok := c.get("a"); ok {
		t.Error("the least recently used entry was not evicted")
	}
	c.get("b")
	c.put(proto.Detail{Item: model.Item{ID: "d"}})
	if _, ok := c.get("c"); ok {
		t.Error("c should have been evicted after b was used")
	}
	f := fullFake(t)
	m := loaded(t, f)
	m = update(t, m, eventMsg{Type: proto.EventRemove, ItemID: "pr:1"})
	if _, ok := m.cache.peek("pr:1"); ok {
		t.Error("a removed item stays cached")
	}
}

func TestSelectionChangeCancelsLoad(t *testing.T) {
	f := fullFake(t)
	m := newTestModel(t, f, Options{})
	m, cmd := updateCmd(t, m, keyMsg("j")) // pr:2
	msgs := run(cmd)                       // the call ran; its answer is not applied yet
	if len(f.detailCtxs) == 0 {
		t.Fatal("no DetailOf call")
	}
	ctx := f.detailCtxs[len(f.detailCtxs)-1]
	if ctx.Err() != nil {
		t.Fatal("context cancelled too early")
	}
	m = update(t, m, keyMsg("j")) // branch: the pr:2 load is cancelled
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("the previous load's context was not cancelled: %v", ctx.Err())
	}
	for _, msg := range msgs {
		m = update(t, m, msg)
	}
	if m.detail != nil {
		t.Errorf("the stale pr:2 answer was applied: %s", m.detail.Item.ID)
	}
	if m.loads.dropped == 0 {
		t.Error("the stale answer was not counted as dropped")
	}
}

// One `r` of the open item sends a storm of events; with the debounce they
// cause one load, never more than one in flight.
func TestReloadStormIsCoalesced(t *testing.T) {
	f := fullFake(t)
	m := loaded(t, f)
	started := m.loads.started
	it := f.details["pr:1"].Item
	var ticks []reloadTickMsg
	for i := range 70 {
		var e proto.Event
		switch i % 5 {
		case 0:
			it.State, it.StateNote = model.StateAnalysing, string(model.Stages[i%len(model.Stages)])
			cp := it
			e = proto.Event{Type: proto.EventItem, ItemID: "pr:1", Item: &cp}
		case 1:
			rl := model.RunLog{ItemID: "pr:1", Stage: model.StageJev, Status: model.StageRunning}
			e = proto.Event{Type: proto.EventStage, ItemID: "pr:1", Stage: model.StageJev, RunLog: &rl}
		case 2:
			e = proto.Event{Type: proto.EventPatch, ItemID: "pr:1", Stage: model.StageJev}
		case 3:
			rl := model.RunLog{ItemID: "pr:1", Stage: model.StageReview, Status: model.StageRunning, Note: "1/3 reviewed"}
			e = proto.Event{Type: proto.EventPatch, ItemID: "pr:1", Stage: model.StageReview, RunLog: &rl}
		default:
			e = proto.Event{Type: proto.EventPatch, ItemID: "pr:1", Stage: model.StageParse}
		}
		// The commands (listener, list reload, debounce tick) are dropped:
		// the ticks are fed below.
		m = update(t, m, eventMsg(e))
		ticks = append(ticks, reloadTickMsg{gen: m.loads.gen, at: time.Now()})
		if len(m.loads.live) > 1 {
			t.Fatalf("event %d: %d loads in flight", i, len(m.loads.live))
		}
	}
	if m.loads.started != started {
		t.Fatalf("events started %d loads before the debounce", m.loads.started-started)
	}
	// Every tick but the last is superseded.
	var loads []tea.Cmd
	for _, tk := range ticks {
		var cmd tea.Cmd
		m, cmd = updateCmd(t, m, tk)
		if cmd != nil {
			loads = append(loads, cmd)
		}
	}
	if len(loads) != 1 || m.loads.started != started+1 || len(m.loads.live) != 1 {
		t.Fatalf("after the debounce: %d load commands, %d started, %d live; want 1, 1, 1",
			len(loads), m.loads.started-started, len(m.loads.live))
	}
	// More events while it is in flight: one follow-up when it lands.
	e := proto.Event{Type: proto.EventPatch, ItemID: "pr:1", Stage: model.StageAggregate}
	m = update(t, m, eventMsg(e))
	m, cmd := updateCmd(t, m, reloadTickMsg{gen: m.loads.gen, at: time.Now()})
	if cmd != nil || !m.loads.dirty["pr:1"] {
		t.Fatalf("a reload while one is in flight must only mark it dirty (cmd %v)", cmd != nil)
	}
	var follow tea.Cmd
	for _, msg := range run(loads[0]) {
		m, follow = updateCmd(t, m, msg)
	}
	if follow == nil || m.loads.started != started+2 || len(m.loads.live) != 1 {
		t.Fatalf("no single follow-up: started %d live %d", m.loads.started-started, len(m.loads.live))
	}
	m = feedAll[detailMsg](t, m, follow)
	if len(m.loads.live) != 0 || m.loads.started != started+2 {
		t.Errorf("settled with %d live, %d started", len(m.loads.live), m.loads.started-started)
	}
}

// A steady stream of events cannot hold a reload back past reloadMaxWait.
func TestReloadMaxWait(t *testing.T) {
	m := loaded(t, fullFake(t))
	m = update(t, m, eventMsg{Type: proto.EventPatch, ItemID: "pr:1"})
	old := m.loads.gen
	m = update(t, m, eventMsg{Type: proto.EventPatch, ItemID: "pr:1"})
	if _, cmd := updateCmd(t, m, reloadTickMsg{gen: old, at: m.loads.since.Add(10 * time.Millisecond)}); cmd != nil {
		t.Error("a superseded tick fired early")
	}
	if _, cmd := updateCmd(t, m, reloadTickMsg{gen: old, at: m.loads.since.Add(reloadMaxWait)}); cmd == nil {
		t.Error("a superseded tick past the max wait did not fire")
	}
}

// Stage rows (start, finish, progress on the Overview) load nothing; they
// feed the picker's strip and the stage map.
func TestStageRowsCauseNoReload(t *testing.T) {
	m := loaded(t, fullFake(t))
	gen := m.loads.gen
	for _, st := range []model.StageStatus{model.StageRunning, model.StageDone} {
		rl := model.RunLog{ItemID: "pr:1", Stage: model.StageJev, Status: st}
		m = update(t, m, eventMsg{Type: proto.EventStage, ItemID: "pr:1", Stage: model.StageJev, RunLog: &rl})
	}
	prog := model.RunLog{ItemID: "pr:1", Stage: model.StageReview, Status: model.StageRunning}
	m = update(t, m, eventMsg{Type: proto.EventPatch, ItemID: "pr:1", Stage: model.StageReview, RunLog: &prog})
	if m.loads.gen != gen || len(m.loads.due) != 0 {
		t.Errorf("stage or progress rows scheduled a reload (gen %d → %d)", gen, m.loads.gen)
	}
	if m.stages["pr:1"] != model.StageReview && m.stages["pr:1"] != model.StageJev {
		t.Errorf("stage map not updated: %v", m.stages)
	}
	if len(m.runLogs["pr:1"]) == 0 {
		t.Error("the picker's stage strip got no rows")
	}
	// On the Pipeline page a progress row reloads the shown item.
	m = press(t, m, "P")
	m = update(t, m, eventMsg{Type: proto.EventPatch, ItemID: "pr:1", Stage: model.StageReview, RunLog: &prog})
	if m.loads.gen == gen {
		t.Error("a progress row on the Pipeline page did not schedule a reload")
	}
}

func TestItemEventReloadsOnlyOnAnalysisChange(t *testing.T) {
	m := loaded(t, fullFake(t))
	it := m.detail.Item
	gen := m.loads.gen
	// Urgency and why move every discovery tick: no reload, but the shown
	// item follows.
	it.Urgency, it.Why = 42, "age"
	m = update(t, m, eventMsg{Type: proto.EventItem, ItemID: it.ID, Item: &it})
	if m.loads.gen != gen || m.detail.Item.Urgency != 42 {
		t.Errorf("urgency-only change: gen %d → %d, item urgency %v", gen, m.loads.gen, m.detail.Item.Urgency)
	}
	// Starting a run: no reload (the note follows the item).
	it.State, it.StateNote = model.StateAnalysing, "jev"
	m = update(t, m, eventMsg{Type: proto.EventItem, ItemID: it.ID, Item: &it})
	if m.loads.gen != gen {
		t.Error("a run start scheduled a reload")
	}
	if !strings.Contains(m.View(), "↻ updating review · jev") {
		t.Errorf("updating note missing:\n%s", m.View())
	}
	// The run finished: reload.
	it.State, it.StateNote, it.AnalysedSHA = model.StateReady, "", "a2"
	m = update(t, m, eventMsg{Type: proto.EventItem, ItemID: it.ID, Item: &it})
	if m.loads.gen == gen {
		t.Error("a finished run did not schedule a reload")
	}
}

func TestUpdatingNote(t *testing.T) {
	ready := model.Item{ID: "pr:1", State: model.StateReady}
	tests := []struct {
		name string
		d    proto.Detail
		want string
	}{
		{"ready", proto.Detail{Item: ready}, ""},
		{"analysing", proto.Detail{Item: model.Item{State: model.StateAnalysing, StateNote: "jev"}}, "↻ updating review · jev"},
		{"fetching", proto.Detail{Item: model.Item{State: model.StateFetching}}, "↻ updating review · fetch"},
		{"run without stage", proto.Detail{Item: ready, Run: &proto.RunStatus{}}, "↻ updating review"},
		{"snapshot", proto.Detail{Item: model.Item{State: model.StateAnalysing, StateNote: "jev"},
			Run: &proto.RunStatus{Stage: model.StageReview, Snapshot: true, SnapshotSHA: "abcdef1234"}},
			"↻ updating review · review · showing abcdef1"},
	}
	for _, tt := range tests {
		if got := updatingNote(tt.d); got != tt.want {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	// On the Overview's title line and in the tab bar's context, colour-free.
	f := fullFake(t)
	d := f.details["pr:1"]
	d.Item.State, d.Item.StateNote = model.StateAnalysing, "review"
	d.Run = &proto.RunStatus{Stage: model.StageReview, Snapshot: true, SnapshotSHA: "abcdef1234"}
	f.details["pr:1"] = d
	m := newTestModel(t, f, Options{})
	m = update(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})
	m = feed[detailMsg](t, m, m.selectionChanged())
	_, mw, _ := m.dims()
	if bar := m.viewTabs(mw); !strings.Contains(bar, "↻ updating review · review · showing abcdef1") {
		t.Errorf("tab bar %q", bar)
	}
	title := strings.Split(m.overview.view(), "\n")[0]
	if !strings.HasSuffix(title, "↻ updating review · review · showing abcdef1") || strings.Contains(title, "\x1b[") {
		t.Errorf("title line %q", title)
	}
}

// While a run is in flight a detail without an aggregate keeps the boxes of
// the previous analysis; an item never analysed shows the pending boxes.
func TestBoxesKeepPreviousAnalysis(t *testing.T) {
	f := fullFake(t)
	m := loaded(t, f)
	d := f.details["pr:1"]
	d.Agg, d.Units = nil, nil
	d.Item.State, d.Item.StateNote = model.StateAnalysing, "parse"
	f.details["pr:1"] = d
	m = update(t, m, eventMsg{Type: proto.EventPatch, ItemID: "pr:1"})
	m, cmd := updateCmd(t, m, reloadTickMsg{gen: m.loads.gen, at: time.Now()})
	m = feedAll[detailMsg](t, m, cmd)
	v := pageText(m)
	if strings.Contains(v, "analysing…") || !strings.Contains(v, "Risk HIGH") {
		t.Errorf("previous analysis not kept:\n%s", v)
	}
	if !strings.Contains(v, "↻ updating review · parse · showing a1") {
		t.Errorf("no updating note:\n%s", v)
	}
	// Never analysed: the pending boxes.
	f2 := newFake(threeItems()...)
	busy := threeItems()[0]
	busy.State, busy.StateNote = model.StateAnalysing, "parse"
	f2.details["pr:1"] = proto.Detail{Item: busy}
	m2 := newTestModel(t, f2, Options{})
	m2 = feed[detailMsg](t, m2, m2.selectionChanged())
	if !strings.Contains(m2.View(), "analysing…") {
		t.Errorf("an item without an aggregate must show the pending boxes:\n%s", m2.View())
	}
}

// pageText is the shown item's whole Overview page (not only the part in
// view).
func pageText(m Model) string {
	_, mw, _ := m.dims()
	o := m.ovOpts()
	o.focus = m.overview.focus
	return strings.Join(overviewPageWith(m.st, m.md, *m.detail, m.now(), mw, o).lines, "\n")
}

func TestReloadOnTick(t *testing.T) {
	f := newFake(threeItems()...)
	m := newTestModel(t, f, Options{ReloadOnTick: true})
	_, cmd := updateCmd(t, m, refreshTickMsg{})
	sawItems := false
	for _, msg := range run(cmd) {
		if _, ok := msg.(itemsMsg); ok {
			sawItems = true
		}
	}
	if f.discovers != 0 || !sawItems {
		t.Errorf("ReloadOnTick: discovers %d, items reloaded %v", f.discovers, sawItems)
	}
}

// ---- labels on demand ------------------------------------------------------------

func TestOverviewRawLabelsFetchedOnDemand(t *testing.T) {
	f := fullFake(t)
	m := loaded(t, f)
	m = press(t, m, "enter")
	m.overview.vp.SetYOffset(lineOf(t, m, "Headlines"))
	m, cmd := updateCmd(t, m, keyMsg("L"))
	if !m.overview.raw || !strings.Contains(m.View(), "loading labels…") || strings.Contains(m.View(), "no labels") {
		t.Fatalf("L before labels:\n%s", m.View())
	}
	m = feedAll[labelsMsg](t, m, cmd)
	if len(f.labelCalls) != 1 || fmt.Sprint(f.labelCalls[0].UnitIDs) != "[u-refresh]" {
		t.Fatalf("Labels calls %+v", f.labelCalls)
	}
	v := m.View()
	for _, want := range []string{"Raw labels", "risk_tags.auth", "breaking"} {
		if !strings.Contains(v, want) {
			t.Errorf("overlay lacks %q:\n%s", want, v)
		}
	}
	// Cached: L again calls nothing.
	m = press(t, m, "esc")
	_, cmd = updateCmd(t, m, keyMsg("L"))
	if cmd != nil || len(f.labelCalls) != 1 {
		t.Errorf("cached labels fetched again: %d calls", len(f.labelCalls))
	}
}

func TestOverviewRawLabelsFailure(t *testing.T) {
	f := fullFake(t)
	f.labelsErr = errors.New("daemon down")
	m := loaded(t, f)
	m = press(t, m, "enter")
	m.overview.vp.SetYOffset(lineOf(t, m, "Headlines"))
	m, cmd := updateCmd(t, m, keyMsg("L"))
	m = feedAll[labelsMsg](t, m, cmd)
	if !strings.Contains(m.View(), "labels unavailable: daemon down") {
		t.Errorf("failure not shown:\n%s", m.View())
	}
}

// The Code page's L and ! ask through PendingLabels; the root fetches the
// unit's labels and hands them over (real codeview page).
func TestCodeLabelsFetchedOnDemand(t *testing.T) {
	f := fullFake(t)
	d := f.details["pr:1"]
	d.Units[0].Diff = "+a\n+b\n"
	d.Agg.Units[0].Behaviour, d.Agg.Units[0].BehaviourP = true, 0.9 // shown under the default filters
	f.details["pr:1"] = d
	m := loaded(t, f)
	m = press(t, m, "2", "enter")
	m, cmd := updateCmd(t, m, keyMsg("L"))
	if !strings.Contains(m.View(), "loading labels…") {
		t.Fatalf("Code L before labels:\n%s", m.View())
	}
	m = feedAll[labelsMsg](t, m, cmd)
	if len(f.labelCalls) != 1 {
		t.Fatalf("Labels calls %+v", f.labelCalls)
	}
	if v := m.View(); !strings.Contains(v, "breaking") || strings.Contains(v, "loading labels…") {
		t.Errorf("labels not shown:\n%s", v)
	}
	// ! on the same unit: labels are cached, the picker opens at once.
	m = press(t, m, "esc")
	_, cmd = updateCmd(t, m, keyMsg("!"))
	if len(f.labelCalls) != 1 {
		t.Errorf("! fetched cached labels again: %+v", f.labelCalls)
	}
	_ = cmd
}

// With the recording pane: what the page asks for is fetched once and
// handed over; a failure is reported to it.
func TestCodeLabelsWithRecordingPane(t *testing.T) {
	f := fullFake(t)
	m, p := withFakePane(loaded(t, f))
	m = update(t, m, keyMsg("2"))
	m = update(t, m, keyMsg("enter"))
	p.itemID = "pr:1"
	p.wantLabels = []string{"u-devices"}
	m, cmd := updateCmd(t, m, keyMsg("n")) // any key the page takes
	m = feedAll[labelsMsg](t, m, cmd)
	if len(p.setLabels) != 1 || fmt.Sprint(p.setLabels[0]) != "[u-devices]" || len(p.gotLabels) != 1 {
		t.Fatalf("handed over %v (%d labels)", p.setLabels, len(p.gotLabels))
	}
	f.labelsErr = errors.New("nope")
	p.wantLabels = []string{"u-server"}
	m, cmd = updateCmd(t, m, keyMsg("n"))
	_ = feedAll[labelsMsg](t, m, cmd)
	if len(p.failedLabels) != 1 {
		t.Errorf("failure not reported: %v", p.failedLabels)
	}
}

// An override drops the unit's cached labels, so the next L fetches them.
func TestOverrideDropsCachedLabels(t *testing.T) {
	f := fullFake(t)
	m := loaded(t, f)
	m.labels.entryFor("pr:1", "a1").units["u-refresh"] = nil
	m.dropUnitLabels("pr:1", "u-refresh")
	if _, ok := m.unitLabels("u-refresh"); ok {
		t.Error("labels still cached after the override")
	}
}

// feedAll runs cmd and every command its messages produce (bounded),
// applying every message, and stops after the first message of type T.
func feedAll[T any](t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0 && n < 20; n++ {
		c := queue[0]
		queue = queue[1:]
		for _, msg := range run(c) {
			var next tea.Cmd
			m, next = updateCmd(t, m, msg)
			if _, ok := msg.(T); ok {
				return m
			}
			if next != nil {
				queue = append(queue, next)
			}
		}
	}
	return m
}
