package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/cmdline"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeview"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/settingsview"
)

// fakeSettings is a scripted settingsPane: once loaded (SetInfo) it
// captures every key; a key listed in acts queues that action. It records
// what the root tells it.
type fakeSettings struct {
	infos   []proto.SettingsInfo
	errs    []error
	notes   []string
	keys    []string
	loaded  bool
	dirty   bool
	acts    map[string]settingsview.Action
	pending *settingsview.Action
}

var _ settingsPane = (*fakeSettings)(nil)

func newFakeSettings() *fakeSettings { return &fakeSettings{acts: map[string]settingsview.Action{}} }

func (s *fakeSettings) SetSize(int, int) {}
func (s *fakeSettings) SetInfo(info proto.SettingsInfo) tea.Cmd {
	s.infos = append(s.infos, info)
	s.loaded = true
	return nil
}
func (s *fakeSettings) SetError(err error)            { s.errs = append(s.errs, err) }
func (s *fakeSettings) SetNote(n string)              { s.notes = append(s.notes, n) }
func (s *fakeSettings) Dirty() bool                   { return s.dirty }
func (s *fakeSettings) Capturing() bool               { return s.loaded }
func (s *fakeSettings) HelpBindings() [][]key.Binding { return nil }
func (s *fakeSettings) View() string                  { return "fake settings page" }

func (s *fakeSettings) Update(msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyMsg); ok {
		s.keys = append(s.keys, k.String())
		if a, ok := s.acts[k.String()]; ok {
			s.pending = &a
		}
	}
	return nil
}

func (s *fakeSettings) PendingAction() (settingsview.Action, bool) {
	if s.pending == nil {
		return settingsview.Action{}, false
	}
	a := *s.pending
	s.pending = nil
	return a, true
}

// lastErr is the last error the root set (nil when cleared or none).
func (s *fakeSettings) lastErr() error {
	if len(s.errs) == 0 {
		return nil
	}
	return s.errs[len(s.errs)-1]
}

// withFakeSettings installs a scripted settings page.
func withFakeSettings(m Model) (Model, *fakeSettings) {
	s := newFakeSettings()
	m.settings = s
	return m, s
}

// m51Items are a ready PR, a stopped PR and a not-analysed branch.
func m51Items() []model.Item {
	return []model.Item{
		{ID: "pr:1", Kind: model.KindPR, Number: 1, Title: "one", Section: model.SectionNeedsYou, Urgency: 3, State: model.StateReady, HeadSHA: "a1"},
		{ID: "pr:2", Kind: model.KindPR, Number: 2, Title: "two", Section: model.SectionNeedsYou, Urgency: 1, State: model.StateStopped, StateNote: "jev", HeadSHA: "a2"},
		{ID: "branch:feat/x", Kind: model.KindBranch, Ref: "feat/x", Section: model.SectionBranches, State: model.StateNotAnalysed, HeadSHA: "a3"},
	}
}

// selectID moves the picker cursor to id and loads its detail.
func selectID(t *testing.T, m Model, id string) Model {
	t.Helper()
	i := m.picker.indexOf(id)
	if i < 0 {
		t.Fatalf("no item %s", id)
	}
	m.picker.moveTo(i)
	return feed[detailMsg](t, m, m.selectionChanged())
}

// ---- automatic vs explicit runs --------------------------------------------

func TestAutoRefreshIsGated(t *testing.T) {
	items := threeItems()
	for i := range items {
		items[i].State = "" // never analysed: the first load starts them
	}
	f := newFake(items...)
	_, cmd := updateCmd(t, NewWithOptions(f, plainRenderer(), Options{}), itemsMsg{items: items})
	run(cmd)
	if len(f.refreshes) != 3 {
		t.Fatalf("refreshes = %+v", f.refreshes)
	}
	for _, p := range f.refreshes {
		if p.User || p.Force {
			t.Errorf("automatic first load sent %+v; want User and Force false", p)
		}
	}
}

func TestRefreshKeyIsExplicit(t *testing.T) {
	f := newFake(m51Items()...)
	m := newTestModel(t, f, Options{})
	pressRun(t, m, "r")
	if len(f.refreshes) != 1 || f.refreshes[0] != (proto.ItemParams{ItemID: "pr:1", Force: true, User: true}) {
		t.Errorf("r on a ready item: refreshes %+v", f.refreshes)
	}
	if len(f.ctl.resumes) != 0 {
		t.Errorf("r on a ready item resumed: %+v", f.ctl.resumes)
	}
}

func TestRefreshKeyResumesStoppedAndNotAnalysed(t *testing.T) {
	for _, tt := range []struct {
		id, note, done string
	}{
		{"pr:2", "resuming #2…", "resumed #2"},
		{"branch:feat/x", "running feat/x…", "started feat/x"},
	} {
		f := newFake(m51Items()...)
		f.ctl.resumed = proto.ResumeResult{Resumed: []string{tt.id}}
		m := selectID(t, newTestModel(t, f, Options{}), tt.id)
		m, cmd := updateCmd(t, m, keyMsg("r"))
		if m.note != tt.note {
			t.Errorf("%s: note %q, want %q", tt.id, m.note, tt.note)
		}
		for _, msg := range run(cmd) {
			m = update(t, m, msg)
		}
		if len(f.refreshes) != 0 {
			t.Errorf("%s: r refreshed %+v", tt.id, f.refreshes)
		}
		if len(f.ctl.resumes) != 1 || f.ctl.resumes[0] != (proto.ResumeParams{ItemID: tt.id}) {
			t.Errorf("%s: resumes %+v", tt.id, f.ctl.resumes)
		}
		if m.note != tt.done {
			t.Errorf("%s: note after %q, want %q", tt.id, m.note, tt.done)
		}
	}
}

func TestPipelineRerunIsExplicit(t *testing.T) {
	f := newFake(threeItems()...)
	m, p := pipeModel(t, f, Options{})
	p.actions["r"] = pipeview.Action{Kind: pipeview.ActRerun, Stage: model.StageJev}
	next, cmd := updateCmd(t, m, keyMsg("r"))
	settle(t, next, cmd)
	if len(f.refreshes) != 1 || !f.refreshes[0].User || f.refreshes[0].FromStage != model.StageJev {
		t.Errorf("pipeline re-run: refreshes %+v", f.refreshes)
	}
}

// ---- stop / resume keys -------------------------------------------------------

func TestControlKeysInEveryScope(t *testing.T) {
	scopes := []struct {
		name  string
		setup func(m Model) Model
	}{
		{"picker", func(m Model) Model { return m }},
		{"overview", func(m Model) Model { return press(t, m, "enter") }},
		{"code", func(m Model) Model { m, _ = withFakePane(m); return press(t, m, "enter", "2") }},
		{"architecture", func(m Model) Model { m, _ = withFakeArch(m); return press(t, m, "enter", "3") }},
		{"pipeline", func(m Model) Model { m, _ = withFakePipe(m); return press(t, m, "P") }},
	}
	for _, sc := range scopes {
		f := newFake(m51Items()...)
		base := newTestModel(t, f, Options{})
		base = feed[detailMsg](t, base, base.selectionChanged())
		m := sc.setup(base)
		for _, k := range []string{"ctrl+x", "ctrl+k", "ctrl+o"} {
			pressRun(t, m, k)
		}
		want := fmt.Sprint([]proto.StopParams{{ItemID: "pr:1"}, {All: true}})
		if got := fmt.Sprint(f.ctl.stops); got != want {
			t.Errorf("%s: stops %s, want %s", sc.name, got, want)
		}
		if len(f.ctl.resumes) != 1 || f.ctl.resumes[0] != (proto.ResumeParams{ItemID: "pr:1"}) {
			t.Errorf("%s: resumes %+v", sc.name, f.ctl.resumes)
		}
	}
}

func TestControlKeysNotFiredWhileCapturing(t *testing.T) {
	cases := []struct {
		name  string
		setup func(m Model) Model
	}{
		{"search prompt", func(m Model) Model { m, _ = withFakeSearch(m); return press(t, m, "/") }},
		{"command line", func(m Model) Model { m, _ = withFakeCmd(m, nil); return press(t, m, ":") }},
		{"code filters bar", func(m Model) Model {
			m, p := withFakePane(m)
			m = press(t, m, "enter", "2")
			p.capturing = true
			return m
		}},
		{"pipeline prompt", func(m Model) Model {
			m, p := withFakePipe(m)
			m = press(t, m, "P")
			p.capturing = true
			return m
		}},
		{"settings page", func(m Model) Model {
			m, _ = withFakeSettings(m)
			return pressRun(t, m, ",")
		}},
	}
	for _, c := range cases {
		f := newFake(m51Items()...)
		m := c.setup(newTestModel(t, f, Options{}))
		for _, k := range []string{"ctrl+x", "ctrl+k", "ctrl+o", "r"} {
			m = pressRun(t, m, k)
		}
		if len(f.ctl.stops) != 0 || len(f.ctl.resumes) != 0 || len(f.refreshes) != 0 {
			t.Errorf("%s: stops %+v resumes %+v refreshes %+v", c.name, f.ctl.stops, f.ctl.resumes, f.refreshes)
		}
	}
}

func TestControlNotes(t *testing.T) {
	items := m51Items()
	items[0].State, items[0].StateNote = model.StateAnalysing, "jev"
	for _, tt := range []struct {
		name    string
		key     string
		stopped []string
		resumed []string
		err     error
		want    string
	}{
		{"stop one", "ctrl+x", []string{"pr:1"}, nil, nil, "stopped #1 at jev"},
		{"nothing", "ctrl+x", nil, nil, nil, "nothing to stop"},
		{"stop all", "ctrl+k", []string{"pr:1", "pr:2", "branch:feat/x"}, nil, nil, "stopped 3 analyses"},
		{"resume", "ctrl+o", nil, []string{"pr:1"}, nil, "resumed #1"},
		{"nothing to resume", "ctrl+o", nil, nil, nil, "nothing to resume"},
		{"stop error", "ctrl+x", nil, nil, errors.New("boom"), "stop failed: boom"},
		{"resume error", "ctrl+o", nil, nil, errors.New("boom"), "resume failed: boom"},
	} {
		f := newFake(items...)
		f.ctl.stopped, f.ctl.resumed, f.ctl.err = proto.StopResult{Stopped: tt.stopped}, proto.ResumeResult{Resumed: tt.resumed}, tt.err
		m := pressRun(t, newTestModel(t, f, Options{}), tt.key)
		if m.note != tt.want {
			t.Errorf("%s: note %q, want %q", tt.name, m.note, tt.want)
		}
	}
}

// ---- item states ----------------------------------------------------------------

func TestPickerStateLines(t *testing.T) {
	items := m51Items()
	if got := secondaryLine(items[1], rowCtx{}); !strings.HasPrefix(got, "stopped at jev · r to resume") {
		t.Errorf("stopped line = %q", got)
	}
	if got := secondaryLine(items[2], rowCtx{}); !strings.HasPrefix(got, "not analysed · r to run") {
		t.Errorf("not-analysed line = %q", got)
	}
	m := newTestModel(t, newFake(items...), Options{})
	m = update(t, m, tea.WindowSizeMsg{Width: 160, Height: 30})
	v := m.View()
	// The 36-column sidebar cuts the stopped line's tail.
	for _, want := range []string{"stopped at jev · r to res", "not analysed · r to run"} {
		if !strings.Contains(v, want) {
			t.Errorf("picker lacks %q:\n%s", want, v)
		}
	}
	if m.picker.busy() {
		t.Error("stopped / not-analysed items count as busy (spinner)")
	}
}

func TestOverviewNotAnalysedBox(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settings.Values.AutoReview = "needs-you"
	m := newTestModel(t, f, Options{})
	m = update(t, m, tea.WindowSizeMsg{Width: 160, Height: 60})
	m = feed[strategyMsg](t, m, m.loadStrategy())
	if m.strategy != "needs-you" {
		t.Fatalf("strategy = %q", m.strategy)
	}
	m = press(t, selectID(t, m, "branch:feat/x"), "enter")
	v := m.View()
	for _, want := range []string{"Not analysed", "r to run",
		"auto-review: needs-you — runs PRs that need you; change with , or :auto"} {
		if !strings.Contains(v, want) {
			t.Errorf("overview lacks %q:\n%s", want, v)
		}
	}
	m = selectID(t, m, "pr:2")
	v = m.View()
	for _, want := range []string{"Stopped at jev", "r or ctrl+o to resume", "auto-review: needs-you"} {
		if !strings.Contains(v, want) {
			t.Errorf("stopped overview lacks %q:\n%s", want, v)
		}
	}
	// A ready item has no such box.
	if v := selectID(t, m, "pr:1").View(); strings.Contains(v, "Not analysed") || strings.Contains(v, "Stopped at") {
		t.Errorf("ready overview has the state box:\n%s", v)
	}
}

func TestOverviewStrategyLineOmittedWhenUnknown(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settingsErr = errors.New("no settings")
	m := newTestModel(t, f, Options{})
	m = update(t, m, tea.WindowSizeMsg{Width: 160, Height: 60})
	m = feed[strategyMsg](t, m, m.loadStrategy())
	v := selectID(t, m, "branch:feat/x").View()
	if !strings.Contains(v, "Not analysed") || strings.Contains(v, "auto-review:") {
		t.Errorf("unknown strategy: %s", v)
	}
	if strings.Contains(m.note, "no settings") {
		t.Errorf("a failed strategy read shows a note: %q", m.note)
	}
}

func TestStrategyReloadsOnSettingsEvent(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settings.Values.AutoReview = "all"
	m := newTestModel(t, f, Options{})
	m, cmd := updateCmd(t, m, eventMsg(proto.Event{Type: proto.EventSettings}))
	m = settle(t, m, cmd)
	if m.strategy != "all" {
		t.Errorf("strategy after EventSettings = %q", m.strategy)
	}
}

// A role saved on the settings page shows in the header without a restart.
func TestRoleReloadsOnSettingsEvent(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settings.Values.Role = "backend"
	m := newTestModel(t, f, Options{Role: "frontend"})
	m, cmd := updateCmd(t, m, eventMsg(proto.Event{Type: proto.EventSettings}))
	m = settle(t, m, cmd)
	if v := m.View(); !strings.Contains(v, "role: backend") {
		t.Errorf("header after a role change lacks the new role:\n%s", v)
	}
}

// The Code tab works on the plain diff of a not-analysed item.
func TestCodeTabOnNotAnalysedItem(t *testing.T) {
	items := m51Items()
	f := newFake(items...)
	f.details["branch:feat/x"] = proto.Detail{Item: items[2], Diff: longDiff(3)}
	m := selectID(t, newTestModel(t, f, Options{}), "branch:feat/x")
	m = press(t, m, "enter", "2")
	if v := m.View(); !strings.Contains(v, "line 1") {
		t.Errorf("code tab lacks the diff:\n%s", v)
	}
}

// ---- settings page ------------------------------------------------------------------

func settingsInfo() proto.SettingsInfo {
	return proto.SettingsInfo{Values: proto.SettingsValues{AutoReview: "needs-you", Role: "frontend"},
		RepoDir: ".git/prt", GlobalDir: "/home/u/.config/prt", RoutingPath: ".git/prt/prt.routing.toml",
		QuestionsPath: ".git/prt/prt.questions.toml", Applied: "re-aggregated 2 items"}
}

// The real settings page (not the fake pane) loads, renders and closes on
// esc through the root model.
func TestRealSettingsPageOpensAndCloses(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settings = settingsInfo()
	m := newTestModel(t, f, Options{})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = pressRun(t, m, ",")
	if m.tab != TabSettings {
		t.Fatalf("tab after , = %v", m.tab)
	}
	if v := m.View(); !strings.Contains(v, "Settings") || !strings.Contains(v, "Analysis") {
		t.Errorf("settings page not rendered:\n%s", v)
	}
	m = update(t, m, keyMsg("esc"))
	if m.tab == TabSettings {
		t.Errorf("esc on a clean settings page did not close it")
	}
}

func TestSettingsPageOpensSavesAndCloses(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settings = settingsInfo()
	m, s := withFakeSettings(newTestModel(t, f, Options{}))
	m = pressRun(t, m, ",")
	if m.tab != TabSettings || len(s.infos) != 1 || len(f.ctl.settingsReqs) != 1 || f.ctl.settingsReqs[0] != (proto.SettingsParams{}) {
		t.Fatalf(", : tab %v infos %d reqs %+v", m.tab, len(s.infos), f.ctl.settingsReqs)
	}
	if v := m.View(); !strings.Contains(v, "fake settings page") || !strings.Contains(v, ", Settings") {
		t.Errorf("settings page not shown:\n%s", v)
	}
	// Every key goes to the page while it is open.
	m = pressRun(t, m, "q")
	m = pressRun(t, m, "j")
	if fmt.Sprint(s.keys) != "[q j]" || m.tab != TabSettings {
		t.Errorf("keys %v tab %v", s.keys, m.tab)
	}
	// Save.
	vals := proto.SettingsValues{AutoReview: "all", Role: "backend"}
	s.acts["enter"] = settingsview.Action{Kind: settingsview.ActSave, Scope: proto.ScopeGlobal, Values: vals}
	m, cmd := updateCmd(t, m, keyMsg("enter"))
	m = settle(t, m, cmd)
	if len(f.ctl.saves) != 1 || f.ctl.saves[0].Scope != proto.ScopeGlobal || f.ctl.saves[0].Values.Role != "backend" {
		t.Fatalf("saves %+v", f.ctl.saves)
	}
	if want := "saved to /home/u/.config/prt · re-aggregated 2 items"; len(s.notes) == 0 || s.notes[len(s.notes)-1] != want {
		t.Errorf("notes %q, want %q", s.notes, want)
	}
	if len(s.infos) != 2 || s.infos[1].Values.Role != "backend" || m.strategy != "all" {
		t.Errorf("after save: infos %+v strategy %q", s.infos, m.strategy)
	}
	// Cancel closes back to the tab it was opened from.
	s.acts["esc"] = settingsview.Action{Kind: settingsview.ActCancel}
	m = pressRun(t, m, "esc")
	if m.tab != TabOverview {
		t.Errorf("esc: tab %v", m.tab)
	}
}

func TestSettingsSaveError(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settings = settingsInfo()
	f.ctl.saveErr = errors.New("disk full")
	m, s := withFakeSettings(newTestModel(t, f, Options{}))
	m = pressRun(t, m, ",")
	s.acts["enter"] = settingsview.Action{Kind: settingsview.ActSave, Values: proto.SettingsValues{AutoReview: "all"}}
	m, cmd := updateCmd(t, m, keyMsg("enter"))
	m = settle(t, m, cmd)
	if err := s.lastErr(); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("save error not on the page: %v", s.errs)
	}
	if m.tab != TabSettings {
		t.Error("a failed save closed the page")
	}
}

func TestSettingsLoadErrorAndClose(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settingsErr = errors.New("daemon too old")
	m, s := withFakeSettings(newTestModel(t, f, Options{}))
	m = pressRun(t, m, ",")
	if err := s.lastErr(); err == nil || m.tab != TabSettings {
		t.Fatalf("load error: errs %v tab %v", s.errs, m.tab)
	}
	// Not loaded: the page does not capture, so , closes it.
	m = pressRun(t, m, ",")
	if m.tab != TabOverview {
		t.Errorf(", on an unloaded page: tab %v", m.tab)
	}
}

func TestSettingsEventReloadsWhenClean(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settings = settingsInfo()
	m, s := withFakeSettings(newTestModel(t, f, Options{}))
	m = pressRun(t, m, ",")
	m, cmd := updateCmd(t, m, eventMsg(proto.Event{Type: proto.EventSettings}))
	m = settle(t, m, cmd)
	if len(s.infos) != 2 {
		t.Errorf("clean page not reloaded: %d infos", len(s.infos))
	}
	s.dirty = true
	m, cmd = updateCmd(t, m, eventMsg(proto.Event{Type: proto.EventSettings}))
	settle(t, m, cmd)
	if len(s.infos) != 2 {
		t.Errorf("dirty page reloaded: %d infos", len(s.infos))
	}
}

func TestSettingsEditRoutingAndQuestions(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settings = settingsInfo()
	f.routing = proto.RoutingInfo{Rerouted: 2}
	var edited []string
	edit := func(path string, done func(error) tea.Msg) tea.Cmd {
		edited = append(edited, path)
		return func() tea.Msg { return done(nil) }
	}
	m, s := withFakeSettings(newTestModel(t, f, Options{Edit: edit}))
	m = pressRun(t, m, ",")
	s.acts["e"] = settingsview.Action{Kind: settingsview.ActEditRouting}
	s.acts["E"] = settingsview.Action{Kind: settingsview.ActEditQuestions}
	m, cmd := updateCmd(t, m, keyMsg("e"))
	m = settle(t, m, cmd)
	if len(f.ctl.settingsReqs) < 2 || f.ctl.settingsReqs[1].Ensure != "routing" {
		t.Errorf("settings reqs %+v", f.ctl.settingsReqs)
	}
	if len(f.routingCalls) != 1 || !f.routingCalls[0].Reload {
		t.Errorf("routing reload %+v", f.routingCalls)
	}
	if !strings.Contains(strings.Join(s.notes, "|"), "rules reloaded: 2 groups re-routed") {
		t.Errorf("notes %q", s.notes)
	}
	m, cmd = updateCmd(t, m, keyMsg("E"))
	m = settle(t, m, cmd)
	want := []string{".git/prt/prt.routing.toml", ".git/prt/prt.questions.toml"}
	if fmt.Sprint(edited) != fmt.Sprint(want) {
		t.Errorf("edited %v, want %v", edited, want)
	}
	if s.notes[len(s.notes)-1] != questionsNote || m.note != questionsNote {
		t.Errorf("questions note: page %q root %q", s.notes, m.note)
	}
}

// ---- commands -------------------------------------------------------------------------

func TestM51Commands(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settings = settingsInfo()
	f.ctl.stopped = proto.StopResult{Stopped: []string{"pr:1"}}
	m, _ := withFakeSettings(newTestModel(t, f, Options{}))
	m, _ = withFakeCmd(m, map[string]cmdline.Submitted{
		"auto all":   {Cmd: cmdline.Command{Kind: cmdline.Auto, Strategy: model.AutoAll}},
		"stop all":   {Cmd: cmdline.Command{Kind: cmdline.Stop, All: true}},
		"stop":       {Cmd: cmdline.Command{Kind: cmdline.Stop}},
		"resume":     {Cmd: cmdline.Command{Kind: cmdline.Resume}},
		"resume all": {Cmd: cmdline.Command{Kind: cmdline.Resume, All: true}},
		"settings":   {Cmd: cmdline.Command{Kind: cmdline.Settings}},
	})
	runLine := func(m Model, line string) Model {
		m = press(t, m, ":")
		m = typeKeys(m, line)
		m, cmd := updateCmd(t, m, keyMsg("enter"))
		return settle(t, m, cmd)
	}
	m = runLine(m, "auto all")
	if len(f.ctl.saves) != 1 || f.ctl.saves[0].Scope != proto.ScopeRepo || f.ctl.saves[0].Values.AutoReview != "all" ||
		f.ctl.saves[0].Values.Role != "frontend" {
		t.Errorf(":auto all saves %+v", f.ctl.saves)
	}
	if m.note != "auto-review: all (saved to .git/prt/prt.toml)" || m.strategy != "all" {
		t.Errorf(":auto note %q strategy %q", m.note, m.strategy)
	}
	m = runLine(m, "stop all")
	m = runLine(m, "stop")
	if fmt.Sprint(f.ctl.stops) != fmt.Sprint([]proto.StopParams{{All: true}, {ItemID: "pr:1"}}) {
		t.Errorf("stops %+v", f.ctl.stops)
	}
	m = runLine(m, "resume")
	m = runLine(m, "resume all")
	if fmt.Sprint(f.ctl.resumes) != fmt.Sprint([]proto.ResumeParams{{ItemID: "pr:1"}, {All: true}}) {
		t.Errorf("resumes %+v", f.ctl.resumes)
	}
	m = runLine(m, "settings")
	if m.tab != TabSettings {
		t.Errorf(":settings: tab %v", m.tab)
	}
}

func TestUnknownCommandIsOneLine(t *testing.T) {
	m, _ := withFakeCmd(newTestModel(t, newFake(m51Items()...), Options{}), nil)
	m = press(t, m, ":")
	m = typeKeys(m, "bogus")
	m = update(t, m, keyMsg("enter"))
	if m.note == "" || strings.Contains(m.note, "\n") || !strings.Contains(m.viewStatus(), "bogus") {
		t.Errorf("unknown command note %q", m.note)
	}
}

func TestAutoCommandError(t *testing.T) {
	f := newFake(m51Items()...)
	f.ctl.settingsErr = errors.New("unreachable")
	m := newTestModel(t, f, Options{})
	m, cmd := m.runCommand(cmdline.Submitted{Cmd: cmdline.Command{Kind: cmdline.Auto, Strategy: model.AutoManual}})
	m = settle(t, m, cmd)
	if m.note != "auto-review: unreachable" || len(f.ctl.saves) != 0 {
		t.Errorf("note %q saves %+v", m.note, f.ctl.saves)
	}
}

// ---- keys and help ----------------------------------------------------------------------

func TestKeyMapFromConfigM51(t *testing.T) {
	km, err := KeyMapFromConfig(map[string]string{"stop": "ctrl+t", "stop_all": "ctrl+y", "resume": "ctrl+g", "settings": "comma"})
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprint(km.Stop.Keys(), km.StopAll.Keys(), km.Resume.Keys(), km.Settings.Keys())
	if got != "[ctrl+t] [ctrl+y] [ctrl+g] [,]" {
		t.Errorf("rebound keys = %s", got)
	}
	if km, err = KeyMapFromConfig(map[string]string{"settings": "comma, ctrl+e"}); err != nil || fmt.Sprint(km.Settings.Keys()) != "[, ctrl+e]" {
		t.Errorf(`settings = "comma, ctrl+e" → %v %v`, km.Settings.Keys(), err)
	}
	def := DefaultKeyMap()
	if got := fmt.Sprint(def.Stop.Keys(), def.StopAll.Keys(), def.Resume.Keys(), def.Settings.Keys()); got != "[ctrl+x] [ctrl+k] [ctrl+o] [,]" {
		t.Errorf("default keys = %s", got)
	}
}

func TestHintsNameControlKeys(t *testing.T) {
	m := newTestModel(t, newFake(m51Items()...), Options{})
	if s := m.viewStatus(); !strings.Contains(s, "ctrl+x stop") {
		t.Errorf("picker hints lack ctrl+x stop: %q", s)
	}
	m = update(t, m, tea.WindowSizeMsg{Width: 220, Height: 30})
	m, _ = withFakePipe(m)
	m = press(t, m, "P")
	if s := m.viewStatus(); !strings.Contains(s, "ctrl+x stop") || !strings.Contains(s, "ctrl+o resume") {
		t.Errorf("pipeline hints lack the control keys: %q", s)
	}
}

func TestHelpListsControlKeysAndCommands(t *testing.T) {
	m := newTestModel(t, newFake(m51Items()...), Options{})
	m = update(t, m, tea.WindowSizeMsg{Width: 200, Height: 120})
	for _, ks := range [][]string{{"?"}, {"enter", "?"}, {"enter", "2", "?"}, {"enter", "3", "?"}, {"enter", "P", "?"}} {
		v := press(t, m, ks...).View()
		for _, want := range []string{"stop analysis", "stop all analysis", "resume analysis", "settings", "Commands"} {
			if !strings.Contains(v, want) {
				t.Errorf("%v: help lacks %q", ks, want)
			}
		}
		for _, e := range cmdline.Help() {
			// A single repo's overlay leaves out the workspace forms (M5.3).
			if got := strings.Contains(v, e.Usage); got == e.Workspace {
				t.Errorf("%v: help shows command %q: %v, want %v", ks, e.Usage, got, !e.Workspace)
			}
		}
	}
}

// Narrow help wraps command descriptions inside the overlay.
func TestCommandHelpWraps(t *testing.T) {
	m := newTestModel(t, newFake(m51Items()...), Options{})
	for _, w := range []int{20, 40, 90} {
		for _, l := range strings.Split(m.commandHelp(w), "\n") {
			if n := len([]rune(l)); n > w {
				t.Errorf("width %d: line %q is %d wide", w, l, n)
			}
		}
	}
}

// helpExceptions are the keys DECISIONS "TUI shell (M5)" lets two bindings
// of one screen share (the page's binding wins): g o a r c s e space, plus
// esc and enter, which name one key in several roles (close / back, open /
// focus) on the component pages, and 1–9 (the Code page's filter toggles,
// live only while the filters bar captures keys).
var helpExceptions = map[string]bool{"g": true, "o": true, "a": true, "r": true, "c": true, "s": true, "e": true,
	" ": true, "esc": true, "enter": true, "1": true, "2": true, "3": true}

// For each focus scope, the default bindings active there bind each key
// once, except the documented exceptions; the M5.1 keys never clash.
func TestNoKeyClashPerScope(t *testing.T) {
	f := newFake(threeItems()...)
	f.details["pr:1"] = proto.Detail{Item: threeItems()[0]}
	m := newTestModel(t, f, Options{}) // the real pages
	m = feed[detailMsg](t, m, m.selectionChanged())
	for _, sc := range []struct {
		name  string
		focus Focus
		tab   Tab
	}{
		{"picker", FocusPicker, TabOverview}, {"overview", FocusMain, TabOverview}, {"code", FocusMain, TabCode},
		{"pipeline", FocusMain, TabPipeline}, {"arch", FocusMain, TabArch},
	} {
		m.focus, m.tab = sc.focus, sc.tab
		_, groups := m.helpGroups()
		owner := map[string]string{}
		for _, g := range groups {
			for _, b := range g {
				if !b.Enabled() {
					continue
				}
				for _, k := range b.Keys() {
					desc := b.Help().Desc
					if prev, ok := owner[k]; ok && prev != desc && !helpExceptions[k] {
						t.Errorf("%s: %q bound to %q and %q", sc.name, k, prev, desc)
					}
					owner[k] = desc
				}
			}
		}
		for _, k := range []string{"ctrl+x", "ctrl+k", "ctrl+o", ","} {
			if owner[k] == "" {
				t.Errorf("%s: %q not bound", sc.name, k)
			}
		}
	}
}
