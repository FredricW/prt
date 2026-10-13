package settingsview

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/proto"
)

func plain() *lipgloss.Renderer { return lipgloss.NewRenderer(io.Discard) }

func sampleInfo() proto.SettingsInfo {
	return proto.SettingsInfo{
		Values: proto.SettingsValues{
			AutoReview: "needs-you",
			Role:       "backend",
			Owns:       []string{"internal/**", "cmd/*"},
			CostCapUSD: 0.75,
			DeepSlots:  2,
			Fast:       "anthropic:claude-haiku",
			Deep:       "anthropic:claude-opus",
			Embed:      "ollama:embeddinggemma-2",
			Rank: config.Rank{
				WRequested: 3, WBreaking: 2.5, WRisk: 1.5, WTestGap: 1, WRelevance: 0.1 + 0.2,
				WRelease: 1, WCIRed: 0.5, WAge: 0.25, WMinutes: 0.01, WUnpushed: 0.2, WReadyStale: 0.3,
			},
			UnsureBelow:    0.6,
			KeepPairP:      0.55,
			EmbedMinCosine: 0.35,
			MaxGroupUnits:  8,
			Concurrency:    4,
		},
		Source:        ".git/prt/prt.toml",
		RoutingSource: "builtin",
		RepoDir:       ".git/prt",
		GlobalDir:     "/home/u/.config/prt",
		RoutingPath:   ".git/prt/prt.routing.toml",
		QuestionsPath: ".git/prt/prt.questions.toml",
		Env: proto.EnvStatus{
			JevKey:       proto.Probe{OK: true, Note: "set"},
			AnthropicKey: proto.Probe{OK: false, Note: "not set"},
			Ollama:       proto.Probe{OK: false, Note: "unreachable at 127.0.0.1:11434"},
			GitHub:       proto.Probe{OK: true},
		},
		Warnings: []string{"global prt.toml is shadowed by .git/prt/prt.toml"},
	}
}

// runCmd runs one command, giving up on commands that block (cursor
// blinks wait half a second; nothing the form needs does).
func runCmd(cmd tea.Cmd) tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(30 * time.Millisecond):
		return nil
	}
}

// asCmds unpacks tea.BatchMsg and Bubble Tea's unexported sequence message
// (both are []tea.Cmd).
func asCmds(msg tea.Msg) ([]tea.Cmd, bool) {
	v := reflect.ValueOf(msg)
	if v.Kind() != reflect.Slice || v.Type().Elem() != reflect.TypeFor[tea.Cmd]() {
		return nil, false
	}
	out := make([]tea.Cmd, v.Len())
	for i := range out {
		out[i], _ = v.Index(i).Interface().(tea.Cmd)
	}
	return out, true
}

// pump plays the Bubble Tea runtime: it runs cmd and feeds the resulting
// messages back into m (Huh moves between fields and sections through
// messages), skipping cursor blinks and spinner ticks.
func pump(m *Model, cmd tea.Cmd, depth int) {
	if cmd == nil || depth > 40 {
		return
	}
	msg := runCmd(cmd)
	if msg == nil {
		return
	}
	if cmds, ok := asCmds(msg); ok {
		for _, c := range cmds {
			pump(m, c, depth+1)
		}
		return
	}
	pkg := reflect.TypeOf(msg).PkgPath()
	if strings.Contains(pkg, "bubbles/cursor") || strings.Contains(pkg, "bubbles/spinner") || strings.HasSuffix(pkg, "bubbletea") {
		return
	}
	pump(m, m.Update(msg), depth+1)
}

func send(m *Model, msgs ...tea.Msg) {
	for _, msg := range msgs {
		pump(m, m.Update(msg), 0)
	}
}

func kp(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

func typeText(m *Model, s string) {
	for _, r := range s {
		send(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func loaded(t *testing.T, info proto.SettingsInfo, w, h int) *Model {
	t.Helper()
	m := New(plain())
	m.SetSize(w, h)
	pump(&m, m.SetInfo(info), 0)
	return &m
}

func view(m *Model) string { return ansi.Strip(m.View()) }

// toSection presses pgdown until section i is shown.
func toSection(t *testing.T, m *Model, i int) {
	t.Helper()
	for range 10 {
		if m.section() == i {
			return
		}
		send(m, kp(tea.KeyPgDown))
	}
	t.Fatalf("section %d not reached (at %d)", i, m.section())
}

func TestLoadingView(t *testing.T) {
	m := New(plain())
	if !strings.Contains(m.View(), "loading settings…") || m.Capturing() {
		t.Errorf("unloaded view %q, capturing %v", m.View(), m.Capturing())
	}
	if m.Update(kp(tea.KeyEnter)) != nil {
		t.Error("unloaded page returned a command")
	}
}

func TestSetInfoShowsValues(t *testing.T) {
	m := loaded(t, sampleInfo(), 100, 40)
	if !m.Capturing() || m.Dirty() {
		t.Fatalf("capturing %v dirty %v", m.Capturing(), m.Dirty())
	}
	want := map[int][]string{
		0: {"Settings", "Analysis · 1/7", "auto_review", "needs-you", "PRs that need you", "repo    .git/prt/prt.toml",
			"global  /home/u/.config/prt/prt.toml", "values from .git/prt/prt.toml · routing from builtin",
			"! global prt.toml is shadowed", "esc cancel"},
		1: {"role", "backend", "owns", "internal/**", "cmd/*"},
		2: {"cost cap ($)", "0.75", "deep_slots", "2"},
		3: {"anthropic:claude-haiku", "anthropic:claude-opus", "ollama:embeddinggemma-2"},
		4: {"w_requested", "w_ready_stale", "0.3"},
		5: {"unsure_below", "0.6", "keep_pair_p", "0.55", "embed_min_cosine", "0.35", "max_group_units", "8", "concurrency", "4"},
		6: {"Environment · 7/7", "Save", "Edit routing rules (prt.routing.toml)", "Cancel"},
	}
	for i := range 7 {
		toSection(t, m, i)
		v := view(m)
		for _, s := range want[i] {
			if !strings.Contains(v, s) {
				t.Errorf("section %d view lacks %q:\n%s", i, s, v)
			}
		}
	}
}

func TestSaveEditedStrategyAndScope(t *testing.T) {
	info := sampleInfo()
	m := loaded(t, info, 100, 40)
	send(m, kp(tea.KeyDown), kp(tea.KeyDown)) // needs-you → needs-you+yours → all
	if !m.Dirty() {
		t.Error("not dirty after choosing a strategy")
	}
	send(m, kp(tea.KeyEnter)) // to scope
	send(m, kp(tea.KeyDown))  // global
	send(m, kp(tea.KeyEnter)) // next section
	if m.section() != 1 {
		t.Fatalf("enter on the last field of Analysis left section %d", m.section())
	}
	toSection(t, m, 6)
	send(m, kp(tea.KeyEnter)) // Save
	a, ok := m.PendingAction()
	if !ok || a.Kind != ActSave || a.Scope != proto.ScopeGlobal {
		t.Fatalf("action = %+v, %v", a, ok)
	}
	want := info.Values
	want.AutoReview = "all"
	if !reflect.DeepEqual(a.Values, want) {
		t.Errorf("values = %+v\nwant %+v", a.Values, want)
	}
	if !strings.Contains(view(m), "saving…") {
		t.Errorf("view while saving:\n%s", view(m))
	}
	// The answer rebuilds the form.
	info.Values.AutoReview = "all"
	pump(m, m.SetInfo(info), 0)
	m.SetNote("saved to .git/prt/prt.toml · re-aggregated 3 items")
	v := view(m)
	if m.Dirty() || strings.Contains(v, "saving…") || !strings.Contains(v, "saved to .git/prt/prt.toml · re-aggregated 3 items") {
		t.Errorf("after SetInfo dirty %v view:\n%s", m.Dirty(), v)
	}
}

func TestSaveTypedValues(t *testing.T) {
	info := sampleInfo()
	m := loaded(t, info, 100, 40)
	toSection(t, m, 1)
	send(m, kp(tea.KeyEnd))
	typeText(m, "-lead")
	toSection(t, m, 5)
	send(m, kp(tea.KeyTab), kp(tea.KeyTab), kp(tea.KeyTab), kp(tea.KeyTab)) // concurrency
	send(m, kp(tea.KeyBackspace))
	typeText(m, "6")
	toSection(t, m, 6)
	send(m, kp(tea.KeyEnter))
	a, ok := m.PendingAction()
	if !ok || a.Kind != ActSave || a.Scope != proto.ScopeRepo {
		t.Fatalf("action = %+v, %v", a, ok)
	}
	want := info.Values
	want.Role, want.Concurrency = "backend-lead", 6
	if !reflect.DeepEqual(a.Values, want) {
		t.Errorf("values = %+v\nwant %+v", a.Values, want)
	}
}

func TestEditActions(t *testing.T) {
	for _, tc := range []struct {
		downs int
		want  ActionKind
		shown string
	}{
		{1, ActEditRouting, "opening .git/prt/prt.routing.toml in $EDITOR…"},
		{2, ActEditQuestions, "opening .git/prt/prt.questions.toml in $EDITOR…"},
	} {
		m := loaded(t, sampleInfo(), 100, 40)
		toSection(t, m, 6)
		for range tc.downs {
			send(m, kp(tea.KeyDown))
		}
		send(m, kp(tea.KeyEnter))
		a, ok := m.PendingAction()
		if !ok || a.Kind != tc.want {
			t.Errorf("action = %+v, %v; want %v", a, ok, tc.want)
		}
		if !strings.Contains(view(m), tc.shown) {
			t.Errorf("view lacks %q", tc.shown)
		}
	}
}

func TestCancelAction(t *testing.T) {
	m := loaded(t, sampleInfo(), 100, 40)
	toSection(t, m, 6)
	send(m, kp(tea.KeyDown), kp(tea.KeyDown), kp(tea.KeyDown), kp(tea.KeyEnter))
	if a, ok := m.PendingAction(); !ok || a.Kind != ActCancel {
		t.Errorf("action = %+v, %v", a, ok)
	}
}

func TestEscCancels(t *testing.T) {
	m := loaded(t, sampleInfo(), 100, 40)
	send(m, kp(tea.KeyEsc))
	if a, ok := m.PendingAction(); !ok || a.Kind != ActCancel {
		t.Fatalf("esc on a clean form: %+v, %v", a, ok)
	}

	// A dirty form asks once.
	m = loaded(t, sampleInfo(), 100, 40)
	toSection(t, m, 1)
	typeText(m, "x")
	if !m.Dirty() {
		t.Fatal("typing did not make the form dirty")
	}
	send(m, kp(tea.KeyEsc))
	if _, ok := m.PendingAction(); ok {
		t.Fatal("first esc on a dirty form cancelled")
	}
	if !strings.Contains(view(m), "esc again to discard") {
		t.Errorf("no discard prompt:\n%s", view(m))
	}
	// Any other key disarms.
	typeText(m, "y")
	send(m, kp(tea.KeyEsc))
	if _, ok := m.PendingAction(); ok {
		t.Fatal("esc after typing cancelled without asking again")
	}
	send(m, kp(tea.KeyEsc))
	if a, ok := m.PendingAction(); !ok || a.Kind != ActCancel {
		t.Fatalf("second esc: %+v, %v", a, ok)
	}
	if m.Dirty() {
		t.Error("edits survived the cancel")
	}

	// esc while waiting for the save answer cancels too.
	m = loaded(t, sampleInfo(), 100, 40)
	toSection(t, m, 6)
	send(m, kp(tea.KeyEnter))
	m.PendingAction()
	send(m, kp(tea.KeyEsc))
	if a, ok := m.PendingAction(); !ok || a.Kind != ActCancel {
		t.Errorf("esc while saving: %+v, %v", a, ok)
	}
}

func TestInlineValidation(t *testing.T) {
	m := loaded(t, sampleInfo(), 100, 40)
	toSection(t, m, 2)
	for range 4 {
		send(m, kp(tea.KeyBackspace))
	}
	typeText(m, "0")
	send(m, kp(tea.KeyEnter))
	if v := view(m); !strings.Contains(v, "✗ cost cap must be an amount in dollars > 0") {
		t.Errorf("no validation error:\n%s", v)
	}
	if m.section() != 2 {
		t.Error("left the field with an invalid value")
	}
	send(m, kp(tea.KeyPgDown))
	if m.section() != 2 {
		t.Error("pgdown left a section with an invalid value")
	}
}

func TestValidators(t *testing.T) {
	cases := []struct {
		key, val string
		ok       bool
	}{
		{keyUnsure, "0.5", true}, {keyUnsure, "1", true}, {keyUnsure, "1.2", false}, {keyUnsure, "-0.1", false}, {keyUnsure, "x", false},
		{keyCosine, "NaN", false},
		{keyConc, "1", true}, {keyConc, "0", false}, {keyConc, "1.5", false},
		{keyDeepSlots, "3", true}, {keyDeepSlots, "", false},
		{keyCostCap, "$1.50", true}, {keyCostCap, "0", false}, {keyCostCap, "Inf", false},
		{keyFast, "openai:gpt", true}, {keyFast, "  ", false},
		{"w_risk", "0", true}, {"w_risk", "-1", false},
	}
	checks := validators()
	for _, tc := range cases {
		if err := checks[tc.key](tc.val); (err == nil) != tc.ok {
			t.Errorf("%s(%q) = %v", tc.key, tc.val, err)
		}
	}
	if n := reflect.TypeFor[config.Rank]().NumField(); len(rankFields()) != n {
		t.Errorf("rankFields has %d entries, config.Rank %d fields", len(rankFields()), n)
	}
	for _, f := range rankFields() {
		found := false
		rt := reflect.TypeFor[config.Rank]()
		for i := range rt.NumField() {
			found = found || rt.Field(i).Tag.Get("toml") == f.key
		}
		if !found {
			t.Errorf("rank key %q is not a config.Rank toml key", f.key)
		}
	}
}

func TestEnvironmentLines(t *testing.T) {
	t.Setenv("JEV_API_KEY", "sk-very-secret-value")
	t.Setenv("PRT_ANTHROPIC_API_KEY", "sk-ant-secret")
	m := loaded(t, sampleInfo(), 100, 40)
	toSection(t, m, 6)
	v := view(m)
	for _, s := range []string{
		"JEV_API_KEY  ✓ set",
		"PRT_ANTHROPIC_API_KEY  ✗ not set",
		"Ollama  ✗ unreachable at 127.0.0.1:11434",
		"GitHub  ✓ connected",
	} {
		if !strings.Contains(v, s) {
			t.Errorf("view lacks %q:\n%s", s, v)
		}
	}
	if strings.Contains(v, "secret") {
		t.Errorf("an environment value leaked:\n%s", v)
	}
	info := sampleInfo()
	info.Env.Ollama = proto.Probe{OK: true, Note: "reachable at 127.0.0.1:11434"}
	info.Env.GitHub = proto.Probe{OK: false}
	if got := strings.Join(envLines(info.Env), "\n"); !strings.Contains(got, "Ollama  ✓ reachable at 127.0.0.1:11434") ||
		!strings.Contains(got, "GitHub  ✗ unavailable") {
		t.Errorf("env lines:\n%s", got)
	}
}

func TestNoGlobalScope(t *testing.T) {
	info := sampleInfo()
	info.GlobalDir = ""
	m := loaded(t, info, 100, 40)
	if v := view(m); strings.Contains(v, "save to") || strings.Contains(v, "global  ") {
		t.Errorf("global scope offered:\n%s", v)
	}
	if _, ok := m.bf.section[keyScope]; ok {
		t.Error("scope field built without a global dir")
	}
	send(m, kp(tea.KeyEnter)) // auto_review was the only field
	if m.section() != 1 {
		t.Errorf("section %d after the only Analysis field", m.section())
	}
}

func TestErrorAndNoteShown(t *testing.T) {
	m := loaded(t, sampleInfo(), 80, 30)
	m.SetError(errors.New("save failed:\nkeep_pair_p must be between 0 and 1"))
	m.SetNote("re-aggregated 3 items")
	v := view(m)
	if !strings.Contains(v, "✗ save failed: keep_pair_p must be between 0 and 1") || !strings.Contains(v, "re-aggregated 3 items") {
		t.Errorf("view:\n%s", v)
	}
	m.SetError(nil)
	if strings.Contains(view(m), "save failed") {
		t.Error("SetError(nil) kept the error")
	}

	// A failed save gives the form back with the edits.
	m = loaded(t, sampleInfo(), 80, 30)
	send(m, kp(tea.KeyDown))
	toSection(t, m, 6)
	send(m, kp(tea.KeyEnter))
	m.PendingAction()
	m.SetError(errors.New("disk full"))
	v = view(m)
	if !m.Dirty() || strings.Contains(v, "saving…") || !strings.Contains(v, "✗ disk full") || !strings.Contains(v, "auto_review") {
		t.Errorf("after a failed save dirty %v:\n%s", m.Dirty(), v)
	}
}

func TestViewFitsWidth(t *testing.T) {
	for _, size := range [][2]int{{40, 20}, {60, 30}, {120, 40}} {
		w, h := size[0], size[1]
		m := loaded(t, sampleInfo(), w, h)
		m.SetNote(strings.Repeat("long note ", 20))
		for i := range 7 {
			toSection(t, m, i)
			lines := strings.Split(m.View(), "\n")
			if len(lines) != h {
				t.Errorf("%dx%d section %d: %d lines", w, h, i, len(lines))
			}
			for _, l := range lines {
				if lw := ansi.StringWidth(l); lw > w {
					t.Errorf("%dx%d section %d: line %d wide: %q", w, h, i, lw, l)
				}
			}
		}
	}
}

func TestHelpBindings(t *testing.T) {
	m := loaded(t, sampleInfo(), 100, 40)
	rows := m.HelpBindings()
	if len(rows) == 0 {
		t.Fatal("no help bindings")
	}
	var keys []string
	for _, row := range rows {
		for _, b := range row {
			keys = append(keys, b.Help().Key)
		}
	}
	got := strings.Join(keys, " ")
	for _, k := range []string{"tab/shift+tab", "enter", "pgdn", "pgup", "esc"} {
		if !strings.Contains(got, k) {
			t.Errorf("help keys %q lack %q", got, k)
		}
	}
}

func TestApplyKeepsUntouchedValues(t *testing.T) {
	v := sampleInfo().Values
	v.AutoReview = "Needs-You " // loads as needs-you
	base := fromValues(v)
	cur := base.clone()
	got := apply(v, base, cur)
	if !reflect.DeepEqual(got, v) {
		t.Errorf("untouched apply = %+v, want %+v", got, v)
	}
	cur.Owns = "a/**\n\n  b/*  \n"
	cur.Rank[0] = "4.5"
	got = apply(v, base, cur)
	if !reflect.DeepEqual(got.Owns, []string{"a/**", "b/*"}) || got.Rank.WRequested != 4.5 || got.AutoReview != v.AutoReview {
		t.Errorf("edited apply = %+v", got)
	}
}
