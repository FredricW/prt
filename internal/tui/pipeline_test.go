package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeview"
	"github.com/fredricw/prt/internal/proto"
)

// settle runs cmd and every command the resulting messages produce, applying
// each message to m, until nothing is left (bounded).
func settle(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for n := 0; len(queue) > 0; n++ {
		if n > 50 {
			t.Fatal("settle: too many commands")
		}
		c := queue[0]
		queue = queue[1:]
		for _, msg := range run(c) {
			var next tea.Cmd
			m, next = updateCmd(t, m, msg)
			if next != nil {
				queue = append(queue, next)
			}
		}
	}
	return m
}

// pipeModel returns a model with recording Code and Pipeline panes, the
// Pipeline page open for pr:1 (opened from the picker).
func pipeModel(t *testing.T, f *fakeClient, o Options) (Model, *fakePipe) {
	t.Helper()
	m, p := withFakePipe(newTestModel(t, f, o))
	m, _ = withFakePane(m)
	m = feed[detailMsg](t, m, m.selectionChanged())
	m = update(t, m, keyMsg("P"))
	if m.tab != TabPipeline || m.focus != FocusMain {
		t.Fatalf("P: tab %v focus %v", m.tab, m.focus)
	}
	return m, p
}

func TestPipelinePageOpensAndCloses(t *testing.T) {
	m, p := pipeModel(t, newFake(threeItems()...), Options{})
	v := m.View()
	for _, want := range []string{"fake pipeline page", "P Pipeline"} {
		if !strings.Contains(v, want) {
			t.Errorf("pipeline view lacks %q:\n%s", want, v)
		}
	}
	if len(p.nows) == 0 || !p.nows[0].Equal(fixedNow) {
		t.Errorf("SetNow on open: %v", p.nows)
	}
	m = update(t, m, keyMsg("P"))
	if m.tab != TabOverview || strings.Contains(m.View(), "P Pipeline") {
		t.Fatalf("P again: tab %v", m.tab)
	}
	// From the Code tab, esc returns there (the page does not use esc here).
	m = press(t, m, "2", "P")
	if m.tab != TabPipeline {
		t.Fatalf("P from code: tab %v", m.tab)
	}
	m = update(t, m, keyMsg("esc"))
	if m.tab != TabCode || m.focus != FocusMain {
		t.Errorf("esc: tab %v focus %v", m.tab, m.focus)
	}
	// Without items P only says so.
	e := newTestModel(t, newFake(), Options{})
	e = update(t, e, keyMsg("P"))
	if e.tab == TabPipeline || e.note != "no item selected" {
		t.Errorf("P without items: tab %v note %q", e.tab, e.note)
	}
}

func TestPipelineKeysReachPageFirst(t *testing.T) {
	f := newFake(threeItems()...)
	m, p := pipeModel(t, f, Options{})
	m = update(t, m, keyMsg("j"))
	if got := p.offered[len(p.offered)-1]; got != "j" {
		t.Fatalf("j not offered to the page: %v", p.offered)
	}
	if selectedID(m) != "pr:1" {
		t.Error("j moved the picker while the page has focus")
	}
	// r without a page action falls through to the global refresh.
	_, cmd := updateCmd(t, m, keyMsg("r"))
	run(cmd)
	if len(f.refreshes) != 1 || f.refreshes[0].FromStage != "" || !f.refreshes[0].Force {
		t.Errorf("unused r: refreshes %+v", f.refreshes)
	}
}

func TestPipelineCapturingBlocksGlobals(t *testing.T) {
	m, p := pipeModel(t, newFake(threeItems()...), Options{})
	m = update(t, m, keyMsg("$")) // the fake opens its cap prompt
	if !p.capturing {
		t.Fatal("$ did not open the prompt")
	}
	for _, k := range []string{"q", "1", "?", "P", "tab"} {
		next, cmd := updateCmd(t, m, keyMsg(k))
		if isQuit(cmd) || next.tab != TabPipeline || next.showHelp || next.focus != FocusMain {
			t.Errorf("%q acted globally while capturing", k)
		}
		m = next
	}
	if got := strings.Join(p.captured, " "); got != "q 1 ? P tab" {
		t.Errorf("captured %q", got)
	}
	m = update(t, m, keyMsg("esc"))
	if p.capturing || m.tab != TabPipeline {
		t.Errorf("esc closed the prompt only: capturing %v tab %v", p.capturing, m.tab)
	}
}

func TestPipelineActionsCallDaemon(t *testing.T) {
	f := newFake(threeItems()...)
	f.calls = []model.ReviewCall{{GroupID: "g1", Task: "review"}, {GroupID: "g1", Task: "review", Purpose: "escalation"}}
	m, p := pipeModel(t, f, Options{})
	p.actions["D"] = pipeview.Action{Kind: pipeview.ActForceDeep, GroupID: "g1"}
	p.actions["s"] = pipeview.Action{Kind: pipeview.ActSkip, GroupID: "g2"}
	p.actions["r"] = pipeview.Action{Kind: pipeview.ActRerun, Stage: model.StageRoute}
	p.actions["c"] = pipeview.Action{Kind: pipeview.ActCallLog, GroupID: "g1"}
	p.actions["$"] = pipeview.Action{Kind: pipeview.ActRaiseCap, CapUSD: 1.5}
	for _, k := range []string{"D", "s", "r", "c", "$"} {
		next, cmd := updateCmd(t, m, keyMsg(k))
		if cmd == nil {
			t.Fatalf("%s: no command", k)
		}
		m = settle(t, next, cmd)
	}
	wantActs := []proto.ReviewActionParams{
		{ItemID: "pr:1", GroupID: "g1", Action: proto.ActionDeep},
		{ItemID: "pr:1", GroupID: "g2", Action: proto.ActionSkip},
	}
	if fmt.Sprint(f.reviewActions) != fmt.Sprint(wantActs) {
		t.Errorf("review actions = %+v", f.reviewActions)
	}
	// Re-running a later stage does not force a head re-resolve or re-fetch.
	if len(f.refreshes) != 1 || f.refreshes[0] != (proto.ItemParams{ItemID: "pr:1", FromStage: model.StageRoute, User: true}) {
		t.Errorf("refreshes = %+v", f.refreshes)
	}
	if len(f.callLogs) != 1 || f.callLogs[0] != (proto.CallLogParams{ItemID: "pr:1", GroupID: "g1"}) {
		t.Errorf("call logs = %+v", f.callLogs)
	}
	if fmt.Sprint(p.callLogs, p.callN) != "[g1] [2]" {
		t.Errorf("SetCallLog got %v %v", p.callLogs, p.callN)
	}
	if len(f.caps) != 1 || f.caps[0] != (proto.CapParams{ItemID: "pr:1", CapUSD: 1.5}) {
		t.Errorf("caps = %+v", f.caps)
	}
	if m.note != "cost cap for this item: $1.50" {
		t.Errorf("note after $: %q", m.note)
	}
	// r on fetch forces: the head is re-resolved and the diff re-fetched.
	p.actions["r"] = pipeview.Action{Kind: pipeview.ActRerun, Stage: model.StageFetch}
	next, rcmd := updateCmd(t, m, keyMsg("r"))
	m = settle(t, next, rcmd)
	if len(f.refreshes) != 2 || f.refreshes[1] != (proto.ItemParams{ItemID: "pr:1", FromStage: model.StageFetch, Force: true, User: true}) {
		t.Errorf("refreshes after r on fetch = %+v", f.refreshes)
	}
	// An action without a group is refused locally.
	p.actions["D"] = pipeview.Action{Kind: pipeview.ActForceDeep}
	m, cmd := updateCmd(t, m, keyMsg("D"))
	if cmd != nil && len(run(cmd)) > 0 || m.note != "no group selected" {
		t.Errorf("D without a group: note %q", m.note)
	}
}

func TestEditRulesEnsuresEditsAndReloads(t *testing.T) {
	f := newFake(threeItems()...)
	f.routing = proto.RoutingInfo{Path: "/repo/.git/prt/prt.routing.toml", Rerouted: 3}
	var edited []string
	edit := func(path string, done func(error) tea.Msg) tea.Cmd {
		edited = append(edited, path)
		// The editor runs right after Ensure, before any Reload.
		if n := len(f.routingCalls); n == 0 || !f.routingCalls[n-1].Ensure {
			t.Errorf("editor started before Ensure: %+v", f.routingCalls)
		}
		return func() tea.Msg { return done(nil) }
	}
	m, p := pipeModel(t, f, Options{Edit: edit})
	p.actions["e"] = pipeview.Action{Kind: pipeview.ActEditRules}
	m, cmd := updateCmd(t, m, keyMsg("e"))
	m = settle(t, m, cmd)
	if fmt.Sprint(edited) != "[/repo/.git/prt/prt.routing.toml]" {
		t.Errorf("editor opened %v", edited)
	}
	want := []proto.RoutingParams{{Ensure: true}, {Reload: true}}
	if fmt.Sprint(f.routingCalls) != fmt.Sprint(want) {
		t.Errorf("routing calls = %+v", f.routingCalls)
	}
	if m.note != "rules reloaded: 3 groups re-routed" || len(p.notes) != 1 || p.notes[0] != m.note {
		t.Errorf("note %q, page notes %v", m.note, p.notes)
	}

	// A reload error becomes the note.
	f.routing.Err = "rule 2: unknown label foo"
	m, cmd = updateCmd(t, m, keyMsg("e"))
	m = settle(t, m, cmd)
	if m.note != "rules reload failed: rule 2: unknown label foo" {
		t.Errorf("reload error note %q", m.note)
	}
}

func TestRulesNotes(t *testing.T) {
	if got := rulesReloadedNote(rulesReloadedMsg{err: errors.New("boom")}); got != "rules reload failed: boom" {
		t.Errorf("err note %q", got)
	}
	if got := rulesReloadedNote(rulesReloadedMsg{info: proto.RoutingInfo{Rerouted: 1}}); got != "rules reloaded: 1 group re-routed" {
		t.Errorf("one group %q", got)
	}
	if got := reviewActedNote(reviewActedMsg{action: proto.ActionDeep, err: errors.New("x")}); got != "review action failed: x" {
		t.Errorf("acted err %q", got)
	}
}

func TestReReviewFromCodePage(t *testing.T) {
	f := newFake(threeItems()...)
	m, _ := withFakePane(newTestModel(t, f, Options{}))
	m = press(t, m, "2", "enter")
	m, cmd := updateCmd(t, m, keyMsg("R"))
	if m.note != "re-reviewing on deep…" {
		t.Errorf("note %q", m.note)
	}
	m = settle(t, m, cmd)
	want := proto.ReviewActionParams{ItemID: "pr:1", GroupID: "g-r", Action: proto.ActionDeep}
	if len(f.reviewActions) != 1 || f.reviewActions[0] != want {
		t.Errorf("review actions = %+v", f.reviewActions)
	}
	if m.note != "re-reviewing on deep…" {
		t.Errorf("note after the answer %q", m.note)
	}
}

func TestPipelineSpinnerAndReloads(t *testing.T) {
	f := newFake(threeItems()...)
	m, p := pipeModel(t, f, Options{})
	n := len(p.nows)
	m = update(t, m, spinner.TickMsg{})
	if len(p.nows) != n+1 {
		t.Errorf("tick did not set the page clock (%d → %d)", n, len(p.nows))
	}
	// A finished-stage patch reloads the detail after the debounce (M5.2).
	before := len(p.details)
	m, cmd := updateCmd(t, m, eventMsg(proto.Event{Type: proto.EventPatch, ItemID: "pr:1", Stage: model.StageReview}))
	m = settle(t, m, cmd)
	if len(p.details) <= before || p.details[len(p.details)-1] != "pr:1" {
		t.Errorf("patch did not refresh the page: %v", p.details)
	}
	clears := p.clears
	m = press(t, m, "esc", "esc", "j") // close the page, focus the picker, move
	if p.clears <= clears {
		t.Error("selection change did not clear the page")
	}
	_ = m
}

// The page maps mouse cells from where the root draws it.
func TestPipelineOrigin(t *testing.T) {
	m, p := pipeModel(t, newFake(threeItems()...), Options{})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	if want := [2]int{sidebarWidth(120) + 2, 2}; p.origin != want {
		t.Errorf("origin %v, want %v", p.origin, want)
	}
	_ = m
}

func TestPipelineHelpAndStatus(t *testing.T) {
	m, _ := pipeModel(t, newFake(threeItems()...), Options{})
	if s := m.viewStatus(); !strings.Contains(s, "D force deep") || !strings.Contains(s, "P close pipeline") {
		t.Errorf("status %q", s)
	}
	m = press(t, m, "?")
	v := m.View()
	for _, want := range []string{"Keys · pipeline", "force deep", "close pipeline"} {
		if !strings.Contains(v, want) {
			t.Errorf("help lacks %q:\n%s", want, v)
		}
	}
}

func TestPipelineKeysFromKeyMap(t *testing.T) {
	km, err := KeyMapFromConfig(map[string]string{"pipeline": "ctrl+p", "force_deep": "F", "rerun_stage": "x",
		"call_log": "C", "raise_cap": "+", "skip_review": "S", "edit_rules": "E", "weights": "W", "rereview": "ctrl+r"})
	if err != nil {
		t.Fatal(err)
	}
	pk := km.pipeKeys()
	got := fmt.Sprint(pk.ForceDeep.Keys(), pk.Rerun.Keys(), pk.CallLog.Keys(), pk.RaiseCap.Keys(), pk.Skip.Keys(),
		pk.EditRules.Keys(), pk.NextPanel.Keys(), pk.Close.Keys(), km.codeKeys().ReReview.Keys())
	if got != "[F] [x] [C] [+] [S] [E] [J ]] [esc] [ctrl+r]" {
		t.Errorf("rebound page keys = %s", got)
	}
	acts := strings.Join(KeyActions(), " ")
	for _, a := range []string{"pipeline", "weights", "rereview", "force_deep", "skip_review", "rerun_stage", "edit_rules", "call_log", "raise_cap"} {
		if !strings.Contains(acts, a) {
			t.Errorf("KeyActions missing %s", a)
		}
	}
}

// urgencyDetail is pr:1 with stored urgency terms.
func urgencyDetail(it model.Item) proto.Detail {
	return proto.Detail{Item: it, Agg: &model.ItemAgg{ItemID: it.ID, Urgency: 4.37, Why: "asked you · release · risk",
		UrgencyTerms: []model.UrgencyTerm{
			{Name: "w_requested", Words: "asked you", Value: 1, Weight: 3, Contrib: 3},
			{Name: "w_release", Words: "release", Value: 1, Weight: 2, Contrib: 2},
			{Name: "w_risk", Words: "risk", Value: 4, Weight: 0.8, Contrib: 3.2},
			{Name: "w_minutes", Words: "long read", Value: 191.5, Weight: 0.02, Contrib: -3.83},
		}}}
}

func TestWeightsOverlay(t *testing.T) {
	it := threeItems()[0]
	f := newFake(threeItems()...)
	f.details[it.ID] = urgencyDetail(it)
	m := newTestModel(t, f, Options{})
	m = feed[detailMsg](t, m, m.selectionChanged())
	m = press(t, m, "enter", "w")
	if !m.overview.raw || !m.overview.weights {
		t.Fatal("w did not open the weights overlay")
	}
	v := m.View()
	for _, want := range []string{
		"Urgency weights",
		"term         words        value   weight   contrib",
		"w_requested  asked you     1.00     3.00     +3.00",
		"w_minutes    long read   191.50     0.02     -3.83",
		"total" + strings.Repeat(" ", 40) + "+4.37",
		"why: asked you · release · risk",
	} {
		if !strings.Contains(v, want) {
			t.Errorf("overlay lacks %q:\n%s", want, v)
		}
	}
	if s := m.viewStatus(); !strings.Contains(s, "urgency weights · esc or w closes") {
		t.Errorf("status %q", s)
	}
	m = update(t, m, keyMsg("L")) // captured: L does not switch overlays
	if !m.overview.weights {
		t.Error("L closed the weights overlay")
	}
	m = update(t, m, keyMsg("w"))
	if m.overview.raw {
		t.Error("w did not close the overlay")
	}
	if got := weightsText(proto.Detail{Item: it}, 80); !strings.Contains(got, "no urgency terms yet") {
		t.Errorf("no terms: %q", got)
	}
}

func TestTLDRLine(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	d.Agg.TLDR = "Refreshes tokens before expiry; one auth path is untested."
	got, anchors := renderOverviewText(d)
	lines := strings.Split(got, "\n")
	if len(lines) < 2 || lines[1] != d.Agg.TLDR+"  ai·from labels" || anchors[1] != "" {
		t.Errorf("TL;DR line = %q", lines[1])
	}

	tests := []struct {
		name   string
		mutate func(d *proto.Detail)
		reason string
	}{
		{"pending agg", func(d *proto.Detail) { d.Agg = nil }, "pending"},
		{"labels missing", func(d *proto.Detail) { d.Agg.LabelsMissing = true }, "labels unavailable"},
		{"no model", func(d *proto.Detail) {
			d.Agg.Review = &model.ReviewStats{Note: "reviews skipped: PRT_ANTHROPIC_API_KEY not set"}
		}, "no review model"},
		{"not yet", func(d *proto.Detail) {}, "pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := loadDetail(t, "detail_full.json")
			d.Agg.TLDR = ""
			tt.mutate(&d)
			got, _ := renderOverviewText(d)
			if l := strings.Split(got, "\n")[1]; l != "TL;DR: not generated — "+tt.reason {
				t.Errorf("line 1 = %q", l)
			}
		})
	}
}

func TestTLDRWrapsAndKeepsMark(t *testing.T) {
	d := loadDetail(t, "detail_full.json")
	d.Agg.TLDR = strings.Repeat("word ", 20) + "end."
	r := plainRenderer()
	got, _ := overviewText(newStyles(r), newMarkdown(r), d, fixedNow, 40)
	lines := strings.Split(got, "\n")
	for _, l := range lines[1:4] {
		if len([]rune(l)) > 40 {
			t.Errorf("line too wide: %q", l)
		}
	}
	if !strings.Contains(got, "end.  ai·from labels") && !strings.Contains(got, "end.\nai·from labels") {
		t.Errorf("mark missing after the sentence:\n%s", got)
	}
}

func TestPickerStageStrip(t *testing.T) {
	items := threeItems()
	items[0].State, items[0].StateNote = model.StateAnalysing, "jev"
	f := newFake(items...)
	m := newTestModel(t, f, Options{})
	var seen []string
	m.strip = func(rls []model.RunLog, width int) string {
		seen = seen[:0]
		for _, r := range rls {
			seen = append(seen, string(r.Stage)+"="+string(r.Status))
		}
		return fmt.Sprintf("STRIP%d", len(rls))
	}
	if strings.Contains(m.View(), "STRIP") {
		t.Fatal("strip shown before any stage row")
	}
	for _, rl := range []model.RunLog{
		{Stage: model.StageParse, Status: model.StageDone},
		{Stage: model.StageJev, Status: model.StageRunning},
	} {
		m = update(t, m, eventMsg(proto.Event{Type: proto.EventStage, ItemID: "pr:1", Stage: rl.Stage, RunLog: &rl}))
	}
	v := m.View()
	if !strings.Contains(v, "STRIP2") || strings.Join(seen, " ") != "parse=done jev=running" {
		t.Errorf("picker strip: rows %v\n%s", seen, v)
	}
	if strings.Contains(v, "analysing jev") {
		t.Error("the strip did not replace the secondary line")
	}
	// A ready item shows its usual line, whatever rows it has.
	rl := model.RunLog{Stage: model.StageParse, Status: model.StageDone}
	m = update(t, m, eventMsg(proto.Event{Type: proto.EventStage, ItemID: "pr:2", Stage: rl.Stage, RunLog: &rl}))
	if strings.Count(m.View(), "STRIP") != 1 {
		t.Errorf("strip drawn for a ready item:\n%s", m.View())
	}
}

func TestUpsertRunLog(t *testing.T) {
	var rows []model.RunLog
	add := func(s model.Stage, st model.StageStatus) {
		rows = upsertRunLog(rows, model.RunLog{Stage: s, Status: st})
	}
	add(model.StageJev, model.StageDone)
	add(model.StageParse, model.StageDone)
	add(model.StageReview, model.StageDone)
	add(model.StageJev, model.StageRunning) // a re-run from jev drops review
	var got []string
	for _, r := range rows {
		got = append(got, string(r.Stage)+"="+string(r.Status))
	}
	if strings.Join(got, " ") != "parse=done jev=running" {
		t.Errorf("rows = %v", got)
	}
}

func TestSnapshotHasPipelineScreen(t *testing.T) {
	it := threeItems()[0]
	f := newFake(it)
	f.details[it.ID] = groupedDetail(it)
	var out bytes.Buffer
	if err := SnapshotWithOptions(context.Background(), f, &out, 120, 24, 1, Options{Now: func() time.Time { return fixedNow }}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	byChange := strings.Index(s, "=== screen: picker + code-by-change · pr:1 ===")
	pipe := strings.Index(s, "=== screen: picker + pipeline · pr:1 ===")
	help := strings.Index(s, "=== screen: help · picker ===")
	if byChange < 0 || pipe < byChange || help < pipe {
		t.Fatalf("screen order: by change %d, pipeline %d, help %d:\n%s", byChange, pipe, help, s)
	}
	if !strings.Contains(s[pipe:help], "P Pipeline") {
		t.Errorf("pipeline screen lacks its tab:\n%s", s[pipe:help])
	}
	if strings.Contains(s[help:], "P Pipeline") {
		t.Error("the help screen still shows the Pipeline page")
	}
}
