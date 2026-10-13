package pipeview

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/golden")

// fixedNow is 12 s after the running review row of full.json started.
var fixedNow = time.Date(2026, 10, 13, 14, 3, 17, 0, time.UTC)

func plainRenderer() *lipgloss.Renderer { return lipgloss.NewRenderer(io.Discard) }

// load reads a fixture (a fresh copy per call).
func load(t testing.TB, name string) proto.Detail {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var d proto.Detail
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return d
}

func newModel(t testing.TB, d proto.Detail, w, h int) Model {
	t.Helper()
	m := New(plainRenderer())
	m.SetSize(w, h)
	m.SetNow(fixedNow)
	m.SetDetail(d)
	return m
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	case "home":
		return tea.KeyMsg{Type: tea.KeyHome}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// press sends keys and fails when one is not consumed.
func press(t *testing.T, m *Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if !m.HandleKey(keyMsg(k)) {
			t.Fatalf("key %q not consumed", k)
		}
	}
}

func contains(t *testing.T, view, want string) {
	t.Helper()
	if !strings.Contains(view, want) {
		t.Errorf("view lacks %q:\n%s", want, view)
	}
}

func lacks(t *testing.T, view, unwanted string) {
	t.Helper()
	if strings.Contains(view, unwanted) {
		t.Errorf("view has %q:\n%s", unwanted, view)
	}
}

// sampleCalls is a call log answer for the escalated row.
func sampleCalls() []model.ReviewCall {
	at := time.Date(2026, 10, 13, 14, 3, 6, 0, time.UTC)
	return []model.ReviewCall{
		{ItemID: "pr:101", GroupID: "g1aaaaaaaaaa", Task: "review", Tier: model.TierFast, Purpose: "routed",
			Model: "anthropic:claude-haiku-5-5", System: "You review code. Report findings only.",
			Prompt: "Review group \"Cache getUser results\".\nUnits:\n+ cache.set(id, user)", Context: []string{"caller web/src/ProfilePage.tsx ProfilePage\n  const u = await getUser(id)"},
			Response: `{"findings":[],"verdict":"ok","confidence":0.42}`, InputTokens: 1840, OutputTokens: 212, Cost: 0.0029, Ms: 2100, At: at},
		{ItemID: "pr:101", GroupID: "g1aaaaaaaaaa", Task: "review", Tier: model.TierDeep, Purpose: "escalation",
			Model: "anthropic:claude-opus-5-5", System: "You review code. Report findings only.",
			Prompt: "Review group \"Cache getUser results\" (escalated).", Response: `{"findings":[{"line":42}],"verdict":"changes needed","confidence":0.81}`,
			InputTokens: 2100, OutputTokens: 480, Cost: 0.0383, Ms: 6300, Cached: true, Err: "", At: at.Add(3 * time.Second)},
	}
}

func TestGolden(t *testing.T) {
	cases := map[string]func(t *testing.T) Model{
		"full":  func(t *testing.T) Model { return newModel(t, load(t, "full"), 120, 72) },
		"prem3": func(t *testing.T) Model { return newModel(t, load(t, "prem3"), 120, 40) },
		"nokey": func(t *testing.T) Model { return newModel(t, load(t, "nokey"), 120, 50) },
		"empty": func(t *testing.T) Model {
			return newModel(t, proto.Detail{Item: model.Item{ID: "pr:104", Number: 104, Title: "Empty"}}, 120, 30)
		},
		"narrow":  func(t *testing.T) Model { return newModel(t, load(t, "full"), 70, 90) },
		"compact": func(t *testing.T) Model { return newModel(t, load(t, "full"), 44, 30) },
		"stage_log": func(t *testing.T) Model {
			m := newModel(t, load(t, "full"), 120, 30)
			press(t, &m, "K", "j", "j", "j", "j", "j", "j", "enter")
			return m
		},
		"call_log": func(t *testing.T) Model {
			m := newModel(t, load(t, "full"), 100, 40)
			press(t, &m, "j", "c")
			m.SetCallLog("g1aaaaaaaaaa", sampleCalls(), nil)
			return m
		},
		"call_log_err": func(t *testing.T) Model {
			m := newModel(t, load(t, "full"), 100, 10)
			m.SetCallLog("g1aaaaaaaaaa", nil, errors.New("daemon: no such group"))
			return m
		},
		"cap_prompt": func(t *testing.T) Model {
			m := newModel(t, load(t, "full"), 120, 24)
			press(t, &m, "$")
			return m
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			m := build(t)
			got := m.View() + "\n"
			path := filepath.Join("testdata", "golden", name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update): %v", err)
			}
			if got != string(want) {
				t.Errorf("View mismatch\n--- got\n%s\n--- want\n%s", got, want)
			}
		})
	}
}

func TestViewDimensionsAndPlain(t *testing.T) {
	for _, fx := range []string{"full", "prem3", "nokey"} {
		for _, w := range []int{30, 44, 70, 99, 120, 200} {
			for _, h := range []int{1, 5, 30} {
				m := newModel(t, load(t, fx), w, h)
				v := m.View()
				lines := strings.Split(v, "\n")
				if len(lines) != h {
					t.Errorf("%s %dx%d: %d lines", fx, w, h, len(lines))
				}
				for i, l := range lines {
					if lw := ansi.StringWidth(l); lw > w {
						t.Errorf("%s %dx%d: line %d is %d wide: %q", fx, w, h, i, lw, l)
					}
				}
				if strings.Contains(v, "\x1b") {
					t.Errorf("%s %dx%d: ANSI escape with the plain renderer", fx, w, h)
				}
			}
		}
	}
}

func TestFullContent(t *testing.T) {
	m := newModel(t, load(t, "full"), 120, 80)
	v := m.View()
	for _, want := range []string{
		"fast → deep .42", "deep (forced)", "none (skipped)", "running 12s", "↳ over cost cap: est $0.14",
		"↳ error: timeout after 30s", "ai·Retry wrapper tweaks", "‹config defaults›", "✓ agree",
		"reviews 12.4 s · all-deep sequential ~48 s", "cost $0.18 of $0.40 cap · 1 queued over cap",
		"jev 31 calls · $0.0123", "n none 2 · f fast 3 · d deep 2",
		"3 if true → fast", "4 rows", "2 rows decided outside the rules",
		"fast-tier miss rate .12 (3 of 25 samples, 30 days)", "label overrides risk 4 · behaviour 2 · kind 2",
		"structural calls 4 · tests 3 · moved 2", "kept same intent 3 · caller update 2 · test for 1",
		"review ⟳", "embed ≡", "agg ·", "run 10-13 14:03:00 · fetch ✓ 400 ms",
	} {
		contains(t, v, want)
	}
	// Rows follow Detail.Groups order, not Detail.Reviews order.
	if a, b := strings.Index(v, "Cache getUser results"), strings.Index(v, "Docs typo"); a < 0 || b < a {
		t.Errorf("rows not in group order:\n%s", v)
	}
}

func TestPreM3AndEmpty(t *testing.T) {
	v := func() string { m := newModel(t, load(t, "prem3"), 120, 60); return m.View() }()
	for _, want := range []string{"no routing decisions yet", "no review data yet", "no routing rules loaded yet",
		"no sampled double reviews yet (calibration_sample)", "embed: no embedding model", "route ·"} {
		contains(t, v, want)
	}
	m := newModel(t, proto.Detail{Item: model.Item{ID: "pr:9"}}, 80, 40)
	v = m.View()
	for _, want := range []string{"no run yet", "no grouping data yet", "not run"} {
		contains(t, v, want)
	}
	m.Clear()
	contains(t, m.View(), "no item selected")
	if m.HandleKey(keyMsg("j")) {
		t.Error("key consumed without an item")
	}
}

func TestNoKeyNote(t *testing.T) {
	m := newModel(t, load(t, "nokey"), 120, 60)
	v := m.View()
	contains(t, v, "note: reviews skipped: PRT_ANTHROPIC_API_KEY not set")
	contains(t, v, "↳ no PRT_ANTHROPIC_API_KEY")
	contains(t, v, "no sampled double reviews yet (calibration_sample .05)")
	contains(t, v, "✗ unexpected token")
	contains(t, v, "warning: rule 4 can never fire")
}

func TestKeyActions(t *testing.T) {
	type tc struct {
		name string
		keys []string
		want Action
		ok   bool
		note string
	}
	// Default selection is the routing table, row 0 (Cache getUser results).
	cases := []tc{
		{"D on escalated row", []string{"D"}, Action{Kind: ActForceDeep, ItemID: "pr:101", GroupID: "g1aaaaaaaaaa"}, true, ""},
		{"s on fast row", []string{"j", "j", "s"}, Action{Kind: ActSkip, ItemID: "pr:101", GroupID: "g3cccccccccc"}, true, ""},
		{"D refused on forced row", []string{"j", "j", "j", "D"}, Action{}, false, "already forced"},
		{"s allowed on forced row", []string{"j", "j", "j", "s"}, Action{Kind: ActSkip, ItemID: "pr:101", GroupID: "g4dddddddddd"}, true, ""},
		{"s refused on skipped row", []string{"j", "s"}, Action{}, false, "already skipped"},
		{"s refused on user-skipped row", []string{"G", "s"}, Action{}, false, "already skipped"},
		{"D on user-skipped row", []string{"G", "D"}, Action{Kind: ActForceDeep, ItemID: "pr:101", GroupID: "g7gggggggggg"}, true, ""},
		{"r in table is review", []string{"r"}, Action{Kind: ActRerun, ItemID: "pr:101", Stage: model.StageReview}, true, ""},
		{"r on strip stage", []string{"K", "j", "j", "r"}, Action{Kind: ActRerun, ItemID: "pr:101", Stage: model.StageEmbed}, true, ""},
		{"r in stage log", []string{"K", "j", "enter", "r"}, Action{Kind: ActRerun, ItemID: "pr:101", Stage: model.StageLink}, true, ""},
		{"e", []string{"e"}, Action{Kind: ActEditRules, ItemID: "pr:101"}, true, ""},
		{"c on row", []string{"j", "c"}, Action{Kind: ActCallLog, ItemID: "pr:101", GroupID: "g2bbbbbbbbbb"}, true, ""},
		{"c on agg stage", []string{"K", "G", "c"}, Action{Kind: ActCallLog, ItemID: "pr:101"}, true, ""},
		{"c on other stage", []string{"K", "c"}, Action{}, false, "call log:"},
		{"D on strip", []string{"K", "D"}, Action{}, false, "routing table"},
		{"$ enter", []string{"$", "enter"}, Action{Kind: ActRaiseCap, ItemID: "pr:101", CapUSD: 0.8}, true, ""},
		{"$ edit enter", []string{"$", "backspace", "backspace", "backspace", "backspace", "1", ".", "5", "enter"}, Action{Kind: ActRaiseCap, ItemID: "pr:101", CapUSD: 1.5}, true, ""},
		{"$ esc", []string{"$", "esc"}, Action{}, false, ""},
		{"$ invalid", []string{"$", "backspace", "backspace", "backspace", "backspace", "x", "enter"}, Action{}, false, "cap must be a number > 0"},
		{"$ zero", []string{"$", "backspace", "backspace", "backspace", "backspace", "0", "enter"}, Action{}, false, "cap must be a number > 0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newModel(t, load(t, "full"), 120, 40)
			var got Action
			var ok bool
			for _, k := range c.keys {
				press(t, &m, k)
				if a, aok := m.PendingAction(); aok {
					got, ok = a, true
				}
			}
			if ok != c.ok || got != c.want {
				t.Errorf("action = %+v (%v), want %+v (%v)", got, ok, c.want, c.ok)
			}
			if c.note != "" && !strings.Contains(m.note, c.note) {
				t.Errorf("note = %q, want %q", m.note, c.note)
			}
			if _, again := m.PendingAction(); again {
				t.Error("PendingAction returned twice")
			}
		})
	}
}

func TestCapturing(t *testing.T) {
	m := newModel(t, load(t, "full"), 120, 40)
	if m.Capturing() {
		t.Fatal("capturing at rest")
	}
	press(t, &m, "$")
	if !m.Capturing() {
		t.Error("prompt does not capture")
	}
	press(t, &m, "q") // typed into the prompt, not global
	if !strings.HasSuffix(m.ti.Value(), "q") {
		t.Errorf("prompt value %q", m.ti.Value())
	}
	press(t, &m, "esc")
	if m.Capturing() {
		t.Error("esc did not close the prompt")
	}
	m.SetCallLog("g1aaaaaaaaaa", sampleCalls(), nil)
	if !m.Capturing() {
		t.Error("call log does not capture")
	}
	press(t, &m, "j", "q", "esc")
	if m.Capturing() {
		t.Error("esc did not close the call log")
	}
	press(t, &m, "K", "enter")
	if !m.Capturing() {
		t.Error("stage log does not capture")
	}
	press(t, &m, "enter")
	if m.Capturing() {
		t.Error("enter did not close the stage log")
	}
	if m.HandleKey(keyMsg("esc")) {
		t.Error("esc consumed with nothing open")
	}
	press(t, &m, "J")
	if m.HandleKey(keyMsg("enter")) {
		t.Error("enter consumed in the routing table")
	}
}

func TestNoteClearedOnNextKey(t *testing.T) {
	m := newModel(t, load(t, "full"), 120, 20)
	m.SetNote("rules reloaded: 3 groups re-routed")
	lines := strings.Split(m.View(), "\n")
	if got := lines[len(lines)-1]; got != "rules reloaded: 3 groups re-routed" {
		t.Errorf("bar line %q", got)
	}
	press(t, &m, "j")
	lacks(t, m.View(), "rules reloaded")
}

func TestSelectionKeptAcrossSetDetail(t *testing.T) {
	m := newModel(t, load(t, "full"), 120, 40)
	press(t, &m, "j", "j", "j") // Session token checks
	if g, _ := m.SelectedGroup(); g != "g4dddddddddd" {
		t.Fatalf("selected %q", g)
	}
	d := load(t, "full")
	// A reload drops the first group and reorders the rest.
	d.Groups = d.Groups[1:]
	d.Groups[0], d.Groups[3] = d.Groups[3], d.Groups[0]
	d.Reviews = d.Reviews[2:]
	m.SetDetail(d)
	if g, _ := m.SelectedGroup(); g != "g4dddddddddd" {
		t.Errorf("after reload selected %q", g)
	}
	// The strip selection and an open stage log survive a reload too.
	press(t, &m, "K", "j", "j", "enter")
	m.SetDetail(load(t, "full"))
	if !m.stageLog || m.stage != 2 || m.panel != panelStrip {
		t.Errorf("stage state lost: log %v stage %d panel %d", m.stageLog, m.stage, m.panel)
	}
	// Another item resets.
	m.SetDetail(load(t, "nokey"))
	if m.stageLog || m.panel != panelTable || m.row != 0 {
		t.Errorf("state kept across items: log %v panel %d row %d", m.stageLog, m.panel, m.row)
	}
	// A vanished group clamps the row.
	m = newModel(t, load(t, "full"), 120, 40)
	press(t, &m, "G")
	d = load(t, "full")
	d.Reviews = d.Reviews[:2]
	m.SetDetail(d)
	if m.row != 1 {
		t.Errorf("row %d after the selected group vanished", m.row)
	}
}

func TestScrollFollowsSelection(t *testing.T) {
	m := newModel(t, load(t, "full"), 120, 12)
	press(t, &m, "G") // last row, page scrolled to the end
	v := m.View()
	contains(t, v, "label overrides")
	press(t, &m, "home")
	contains(t, m.View(), "Pipeline · #101")
	// j past the last row scrolls the page.
	m = newModel(t, load(t, "full"), 120, 30)
	for range 7 {
		press(t, &m, "j")
	}
	before := m.off
	press(t, &m, "j", "j", "j")
	if m.off <= before {
		t.Errorf("j at the last row did not scroll (off %d → %d)", before, m.off)
	}
	press(t, &m, "k")
	v = m.View()
	contains(t, v, "▸ Logger cleanup")
}

func TestMouse(t *testing.T) {
	m := newModel(t, load(t, "full"), 120, 40)
	m.SetOrigin(0, 2)
	lines := strings.Split(m.View(), "\n")
	row := -1
	for i, l := range lines {
		if strings.Contains(l, "Logger cleanup") {
			row = i
		}
	}
	if row < 0 {
		t.Fatal("row not drawn")
	}
	m.Update(tea.MouseMsg{X: 10, Y: 2 + row, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if g, _ := m.SelectedGroup(); g != "g6ffffffffff" {
		t.Errorf("click selected %q", g)
	}
	// A click on the third stage box selects embed.
	m.Update(tea.MouseMsg{X: 2*16 + 3, Y: 2 + 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.panel != panelStrip || m.stage != 2 {
		t.Errorf("click on box: panel %d stage %d", m.panel, m.stage)
	}
	m.Update(tea.MouseMsg{X: 10, Y: 10, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.off != wheelStep {
		t.Errorf("wheel off %d", m.off)
	}
	m.Update(tea.MouseMsg{X: 10, Y: 10, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.off != 0 {
		t.Errorf("wheel back off %d", m.off)
	}
}

func TestCallLogScroll(t *testing.T) {
	m := newModel(t, load(t, "full"), 100, 8)
	m.SetCallLog("g1aaaaaaaaaa", sampleCalls(), nil)
	contains(t, m.View(), "Call log · Cache getUser results · 2 calls")
	press(t, &m, "G")
	contains(t, m.View(), "changes needed")
	press(t, &m, "home")
	contains(t, m.View(), "1 · routed · fast")
	m.SetCallLog("", nil, nil)
	contains(t, m.View(), "Call log · TL;DR")
	contains(t, m.View(), "no calls recorded")
}

func TestStageLogRows(t *testing.T) {
	m := newModel(t, load(t, "full"), 120, 40)
	press(t, &m, "K")
	for range 6 {
		press(t, &m, "j")
	}
	press(t, &m, "enter")
	v := m.View()
	contains(t, v, "review log · 2 rows")
	contains(t, v, "• 10-13 14:03:04 running")
	contains(t, v, "  10-12 09:00:09 failed")
	contains(t, v, "✗ anthropic: 529 overloaded")
}

func TestStageStrip(t *testing.T) {
	full := load(t, "full").RunLogs
	nokey := load(t, "nokey").RunLogs
	failed := append([]model.RunLog(nil), nokey...)
	failed[4].Status = model.StageFailed
	cases := []struct {
		name  string
		rls   []model.RunLog
		width int
		want  string
	}{
		{"running wide", full, 0, "parse ✓ link ✓ embed ≡ jev ✓ cluster ✓ route ✓ review ⟳ agg ·"},
		{"running 61", full, 61, "parse ✓ link ✓ embed ≡ jev ✓ cluster ✓ route ✓ review ⟳ agg ·"},
		{"running 60", full, 60, "par ✓ lin ✓ emb ≡ jev ✓ clu ✓ rou ✓ rev ⟳ agg ·"},
		{"running 50", full, 50, "par ✓ lin ✓ emb ≡ jev ✓ clu ✓ rou ✓ rev ⟳ agg ·"},
		{"running 20", full, 20, "✓✓≡✓✓✓⟳·"},
		{"running 5", full, 5, "✓✓≡✓…"},
		{"complete", nokey, 80, "parse ✓ link ✓ embed ✓ jev ✓ cluster ✓ route ✓ review – agg ✓"},
		{"failed", failed, 80, "parse ✓ link ✓ embed ✓ jev ✗ cluster ✓ route ✓ review – agg ✓"},
		{"none", nil, 80, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StageStrip(c.rls, c.width)
			if got != c.want {
				t.Errorf("StageStrip = %q, want %q", got, c.want)
			}
			if c.width > 0 && ansi.StringWidth(got) > c.width {
				t.Errorf("width %d > %d", ansi.StringWidth(got), c.width)
			}
		})
	}
}

func TestCurrentRunWithoutFetch(t *testing.T) {
	rls := []model.RunLog{
		{Stage: model.StageReview, Status: model.StageFailed, StartedAt: fixedNow},
		{Stage: model.StageReview, Status: model.StageDone, StartedAt: fixedNow.Add(time.Minute)},
	}
	if got := StageStrip(rls, 0); !strings.Contains(got, "review ✓") {
		t.Errorf("latest row not used: %q", got)
	}
}

func TestNilFieldsDoNotPanic(t *testing.T) {
	details := []proto.Detail{
		{Item: model.Item{ID: "x"}},
		{Item: model.Item{ID: "x"}, Agg: &model.ItemAgg{}},
		{Item: model.Item{ID: "x"}, Agg: &model.ItemAgg{Review: &model.ReviewStats{}, Calibration: &model.CalibrationStats{Samples: 1}, Grouping: &model.GroupingStats{}}},
		{Item: model.Item{ID: "x"}, Routing: &proto.RoutingInfo{}, Reviews: []model.Review{{GroupID: "nogroup"}}},
		{Item: model.Item{ID: "x"}, RunLogs: []model.RunLog{{Stage: "weird"}}},
	}
	keys := []string{"j", "k", "J", "K", "enter", "j", "esc", "D", "s", "r", "e", "c", "$", "enter", "G", "home"}
	for _, d := range details {
		for _, w := range []int{1, 40, 120} {
			m := newModel(t, d, w, 10)
			_ = m.View()
			for _, k := range keys {
				m.HandleKey(keyMsg(k))
				_ = m.View()
			}
			m.SetCallLog("", nil, nil)
			_ = m.View()
			m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
			m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		}
	}
	zero := New(nil)
	_ = zero.View()
	zero.SetCallLog("g", nil, nil)
	zero.Update(tea.MouseMsg{})
	if zero.HandleKey(keyMsg("j")) {
		t.Error("zero model consumed a key")
	}
}

func TestFormat(t *testing.T) {
	for _, c := range []struct {
		got, want string
	}{
		{money(0), "$0"}, {money(0.18), "$0.18"}, {money(0.4), "$0.40"}, {money(0.0123), "$0.0123"},
		{money(0.05), "$0.05"}, {money(1.234), "$1.23"},
		{dur(340), "340 ms"}, {dur(12400), "12.4 s"}, {dur(125000), "2m05s"},
		{estimate(48000), "~48 s"}, {estimate(3200), "~3.2 s"}, {estimate(125000), "~2m05s"},
		{tierBar(map[model.Tier]int{model.TierNone: 2, model.TierFast: 3, model.TierDeep: 2}), "nnnnnnnffffffffffddddddd"},
		{tierBar(map[model.Tier]int{model.TierDeep: 1}), "dddddddddddddddddddddddd"},
		{tierBar(nil), "—"},
		{ctxList([]string{"callers", "code_under_test"}), "callers,cut"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	if v, ok := parseCap(" $0.80 "); !ok || v != 0.8 {
		t.Errorf("parseCap = %v %v", v, ok)
	}
	for _, bad := range []string{"", "x", "-1", "0", "Inf", "NaN"} {
		if _, ok := parseCap(bad); ok {
			t.Errorf("parseCap(%q) ok", bad)
		}
	}
}
