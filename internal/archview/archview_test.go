package archview

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/golden")

func plain() *lipgloss.Renderer { return lipgloss.NewRenderer(io.Discard) }

// sample is the v2 design's graph: web/app and web/components import
// web/auth (whose expiresAt contract changed), web/auth imports web/api,
// web/api now imports the new server/middleware and no longer the removed
// server/session.
func sample() *model.ArchGraph {
	return &model.ArchGraph{
		MaxHop: 2,
		Modules: []model.ArchModule{
			{ID: "web/auth", Status: model.ArchChanged, Owned: true, Files: []string{"web/auth/session.ts", "web/auth/token.ts"}, FileCount: 5,
				Units: 3, Eyes: 2, Skim: 1, Untested: 1, Breaking: 1, RiskMax: 5, Attention: model.AttnEyes},
			{ID: "web/components", Status: model.ArchChanged, Owned: true, Files: []string{"web/components/Header.tsx"}, FileCount: 12,
				Units: 1, Eyes: 1, Untested: 1, RiskMax: 3, Attention: model.AttnEyes},
			{ID: "web/api", Status: model.ArchChanged, Owned: true, Files: []string{"web/api/client.ts"}, FileCount: 4,
				Units: 2, Eyes: 1, Skim: 1, Unsure: 1, RiskMax: 3, Attention: model.AttnEyes},
			{ID: "server/auth", Status: model.ArchChanged, Files: []string{"server/auth/verify.go"}, FileCount: 3,
				Units: 3, Skim: 3, Undocumented: 1, RiskMax: 4, Attention: model.AttnSkim},
			{ID: "server/middleware", Status: model.ArchNew, Files: []string{"server/middleware/session.go"}, FileCount: 1,
				Units: 4, Skim: 4, RiskMax: 3, Attention: model.AttnSkim},
			{ID: "server/session", Status: model.ArchRemoved, Files: []string{"server/session/store.go"}, FileCount: 2,
				Units: 2, Skip: 2, RiskMax: 1, Attention: model.AttnSkip},
			{ID: "web/app", Status: model.ArchContext, Hop: 1, FileCount: 4},
			{ID: "server/db", Status: model.ArchContext, Hop: 2, FileCount: 6},
		},
		Edges: []model.ArchEdge{
			{From: "server/auth", To: "server/db", Status: model.ArchSame, Imports: 2},
			{From: "server/auth", To: "server/session", Status: model.ArchRemoved, Imports: 1},
			{From: "server/middleware", To: "server/auth", Status: model.ArchSame, Imports: 1},
			{From: "web/api", To: "server/middleware", Status: model.ArchNew, Imports: 1},
			{From: "web/api", To: "server/session", Status: model.ArchRemoved, Imports: 1},
			{From: "web/app", To: "web/auth", Status: model.ArchSame, Imports: 2, Contracts: []string{"expiresAt ISO → ms · breaking"}},
			{From: "web/auth", To: "web/api", Status: model.ArchSame, Imports: 3},
			{From: "web/components", To: "web/auth", Status: model.ArchSame, Imports: 1, Contracts: []string{"expiresAt ISO → ms · breaking"}},
		},
		NetEffect: []string{"new module server/middleware", "server/session removed", "web/api now imports server/middleware", "and 2 more import changes"},
	}
}

func detail(g *model.ArchGraph) proto.Detail {
	return proto.Detail{
		Item: model.Item{ID: "pr:482", Number: 482, Title: "Migrate session to JWT"},
		Files: []model.File{
			{Path: "web/auth/session.ts", Adds: 40, Dels: 12},
			{Path: "web/auth/token.ts", Added: true, Adds: 31},
			{Path: "web/components/Header.tsx", Adds: 3, Dels: 1},
			{Path: "server/session/store.go", Deleted: true, Dels: 54},
		},
		Agg: &model.ItemAgg{ItemID: "pr:482", Arch: g},
	}
}

func newModel(t *testing.T, g *model.ArchGraph, w, h int) Model {
	t.Helper()
	m := New(plain())
	m.SetSize(w, h)
	m.SetDetail(detail(g))
	return m
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "left":
		return tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		return tea.KeyMsg{Type: tea.KeyRight}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(t *testing.T, m *Model, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if !m.HandleKey(keyMsg(k)) {
			t.Fatalf("key %q not consumed", k)
		}
	}
}

func degraded() *model.ArchGraph {
	g := sample()
	g.Note = "head not readable locally: graph from base imports plus the diff's files"
	g.NetEffect = nil
	for i := range g.Edges {
		g.Edges[i].Status = model.ArchSame
	}
	return g
}

func TestGolden(t *testing.T) {
	cases := map[string]func(t *testing.T) Model{
		"wide":   func(t *testing.T) Model { return newModel(t, sample(), 160, 40) },
		"list":   func(t *testing.T) Model { return newModel(t, sample(), 90, 30) },
		"narrow": func(t *testing.T) Model { return newModel(t, sample(), 60, 40) },
		"diff_only": func(t *testing.T) Model {
			m := newModel(t, sample(), 160, 40)
			press(t, &m, "d")
			return m
		},
		"depth0": func(t *testing.T) Model {
			m := newModel(t, sample(), 160, 40)
			press(t, &m, "-")
			return m
		},
		"depth2": func(t *testing.T) Model {
			m := newModel(t, sample(), 160, 40)
			press(t, &m, "+")
			return m
		},
		"files": func(t *testing.T) Model {
			m := newModel(t, sample(), 160, 40)
			press(t, &m, "enter", "j")
			return m
		},
		"nil_arch": func(t *testing.T) Model { return newModel(t, nil, 80, 6) },
		"note":     func(t *testing.T) Model { return newModel(t, degraded(), 160, 40) },
		"no_modules": func(t *testing.T) Model {
			return newModel(t, &model.ArchGraph{Note: "no source modules touched"}, 80, 4)
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

// cyclic is a graph whose edges form a cycle (a → b → c → a) plus a long
// edge, so one edge is drawn upwards.
func cyclic() *model.ArchGraph {
	return &model.ArchGraph{
		MaxHop: 1,
		Modules: []model.ArchModule{
			{ID: "a", Status: model.ArchChanged, Eyes: 1, Units: 1, Attention: model.AttnEyes},
			{ID: "b", Status: model.ArchChanged},
			{ID: "c", Status: model.ArchContext, Hop: 1},
			{ID: "d", Status: model.ArchContext, Hop: 1},
		},
		Edges: []model.ArchEdge{
			{From: "a", To: "b", Status: model.ArchSame},
			{From: "a", To: "d", Status: model.ArchNew},
			{From: "b", To: "c", Status: model.ArchSame},
			{From: "c", To: "a", Status: model.ArchRemoved},
			{From: "c", To: "d", Status: model.ArchSame},
		},
	}
}

// fan is a graph whose top layer is wider than any canvas (list fallback).
func fan() *model.ArchGraph {
	g := &model.ArchGraph{MaxHop: 1, Modules: []model.ArchModule{{ID: "core", Status: model.ArchChanged}}}
	for i := range 12 {
		id := "pkg/importer" + string(rune('a'+i))
		g.Modules = append(g.Modules, model.ArchModule{ID: id, Status: model.ArchContext, Hop: 1})
		g.Edges = append(g.Edges, model.ArchEdge{From: id, To: "core", Status: model.ArchSame, Imports: 1})
	}
	return g
}

func TestCycleAndFanLayouts(t *testing.T) {
	m := newModel(t, cyclic(), 160, 40)
	v := m.View()
	if !strings.Contains(v, "▲") {
		t.Errorf("cycle edge not drawn upwards:\n%s", v)
	}
	l, ok := layoutGraph(visible(cyclic(), 1, false), 120, 37)
	if !ok || len(l.back) != 1 {
		t.Errorf("cycle layout ok %v back %v", ok, l.back)
	}
	m = newModel(t, fan(), 160, 40)
	if !strings.Contains(m.View(), "── hop 1 ──") {
		t.Errorf("fan did not fall back to the list:\n%s", m.View())
	}
}

func TestViewSizeAndWidth(t *testing.T) {
	graphs := map[string]*model.ArchGraph{"sample": sample(), "degraded": degraded(), "nil": nil, "cyclic": cyclic(), "fan": fan()}
	for name, g := range graphs {
		for _, w := range []int{1, 20, 45, 60, 79, 80, 90, 120, 160, 220} {
			for _, h := range []int{1, 3, 12, 30, 45} {
				for _, keys := range [][]string{nil, {"enter"}, {"d"}, {"+"}} {
					m := newModel(t, g, w, h)
					for _, k := range keys {
						m.HandleKey(keyMsg(k))
					}
					v := m.View()
					lines := strings.Split(v, "\n")
					if len(lines) != h {
						t.Errorf("%s %dx%d %v: %d lines", name, w, h, keys, len(lines))
					}
					for i, l := range lines {
						if lw := ansi.StringWidth(l); lw > w {
							t.Errorf("%s %dx%d %v: line %d is %d wide: %q", name, w, h, keys, i, lw, l)
						}
					}
					if strings.Contains(v, "\x1b") {
						t.Errorf("%s %dx%d: escape codes without colour", name, w, h)
					}
				}
			}
		}
	}
}

func TestFocusMovement(t *testing.T) {
	// The wide layout at depth 1 (see testdata/golden/wide.golden):
	//
	//	layer 0:  web/components   web/app
	//	layer 1:          web/auth
	//	layer 2:          web/api
	//	layer 3:      server/middleware  server/session(?) …
	cases := []struct {
		name  string
		keys  []string
		focus string
	}{
		{"starts on the first module", nil, "web/auth"},
		{"j goes down to the import", []string{"j"}, "web/api"},
		{"k goes up to an importer", []string{"k"}, "web/components"},
		{"k then l moves along the layer", []string{"k", "l"}, "web/app"},
		{"l then h comes back", []string{"k", "l", "h"}, "web/components"},
		{"arrows work too", []string{"down", "down"}, "server/middleware"},
		{"nothing above the top layer", []string{"k", "k"}, "web/components"},
		{"right of the last box stays", []string{"k", "l", "l"}, "web/app"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newModel(t, sample(), 160, 40)
			press(t, &m, c.keys...)
			if m.focus != c.focus {
				t.Errorf("focus %q, want %q", m.focus, c.focus)
			}
		})
	}
	// In the list fallback j/k walk the list.
	m := newModel(t, sample(), 90, 30)
	press(t, &m, "j", "j")
	if m.focus != "web/api" {
		t.Errorf("list j j: focus %q", m.focus)
	}
	press(t, &m, "k")
	if m.focus != "web/components" {
		t.Errorf("list k: focus %q", m.focus)
	}
}

func TestDepthAndDiffOnly(t *testing.T) {
	m := newModel(t, sample(), 160, 40)
	if m.depth != 1 {
		t.Fatalf("default depth %d", m.depth)
	}
	press(t, &m, "+", "+", "+")
	if m.depth != 2 {
		t.Errorf("depth past MaxHop: %d", m.depth)
	}
	press(t, &m, "-", "-", "-")
	if m.depth != 0 {
		t.Errorf("depth below 0: %d", m.depth)
	}
	// Focus a context module, then hide it: the focus moves to a shown one.
	m = newModel(t, sample(), 160, 40)
	m.focus = "web/app"
	press(t, &m, "d")
	if m.focus != "web/auth" {
		t.Errorf("focus after diff-only %q", m.focus)
	}
	s := m.shown()
	for _, mod := range s.mods {
		if mod.Status == model.ArchContext {
			t.Errorf("context module %s shown in diff-only", mod.ID)
		}
	}
	// A reload of the same item keeps focus, depth and diff-only; another
	// item resets them.
	m.focus = "web/api"
	press(t, &m, "+")
	m.SetDetail(detail(sample()))
	if m.focus != "web/api" || m.depth != 2 || !m.diffOnly {
		t.Errorf("reload lost state: focus %q depth %d diff-only %v", m.focus, m.depth, m.diffOnly)
	}
	other := detail(sample())
	other.Item.ID = "pr:1"
	m.SetDetail(other)
	if m.focus != "web/auth" || m.depth != 1 || m.diffOnly {
		t.Errorf("other item kept state: focus %q depth %d diff-only %v", m.focus, m.depth, m.diffOnly)
	}
}

func TestFileListAndPendingJump(t *testing.T) {
	m := newModel(t, sample(), 160, 40)
	if m.Capturing() {
		t.Fatal("capturing at rest")
	}
	if _, ok := m.PendingJump(); ok {
		t.Fatal("jump at rest")
	}
	press(t, &m, "enter")
	if !m.Capturing() {
		t.Fatal("file list does not capture")
	}
	v := m.View()
	for _, want := range []string{"Files in web/auth", "web/auth/session.ts", "+40 −12", "web/auth/token.ts", "+31 −0 new"} {
		if !strings.Contains(v, want) {
			t.Errorf("file list lacks %q:\n%s", want, v)
		}
	}
	press(t, &m, "q", "j") // swallowed while open
	press(t, &m, "enter")
	if m.Capturing() {
		t.Error("enter on a file did not close the list")
	}
	if p, ok := m.PendingJump(); !ok || p != "web/auth/token.ts" {
		t.Errorf("PendingJump = %q %v", p, ok)
	}
	if _, ok := m.PendingJump(); ok {
		t.Error("PendingJump returned twice")
	}
	press(t, &m, "enter", "esc")
	if m.Capturing() {
		t.Error("esc did not close")
	}
	if _, ok := m.PendingJump(); ok {
		t.Error("esc set a jump")
	}
	if m.HandleKey(keyMsg("esc")) {
		t.Error("esc consumed with nothing open")
	}
	// A context module has no files: enter opens an empty list.
	m.focus = "web/app"
	press(t, &m, "enter")
	if !strings.Contains(m.View(), "no changed files") {
		t.Errorf("context file list:\n%s", m.View())
	}
	press(t, &m, "enter")
	if _, ok := m.PendingJump(); ok || !m.Capturing() {
		t.Error("enter on an empty list jumped or closed")
	}
}

func TestNoGraphConsumesNothing(t *testing.T) {
	m := newModel(t, nil, 80, 10)
	for _, k := range []string{"j", "enter", "d", "+"} {
		if m.HandleKey(keyMsg(k)) {
			t.Errorf("%q consumed without a graph", k)
		}
	}
	m.Clear()
	if !strings.Contains(m.View(), "module graph not computed yet") {
		t.Errorf("cleared view %q", m.View())
	}
	m.Update(tea.MouseMsg{Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
}

// rowOf returns the screen row and column of the first line containing s.
func rowOf(t *testing.T, view, s string) (int, int) {
	t.Helper()
	for i, l := range strings.Split(view, "\n") {
		if j := strings.Index(l, s); j >= 0 {
			return i, ansi.StringWidth(l[:j])
		}
	}
	t.Fatalf("%q not drawn:\n%s", s, view)
	return 0, 0
}

func TestMouse(t *testing.T) {
	m := newModel(t, sample(), 160, 40)
	m.SetOrigin(30, 2)
	y, x := rowOf(t, m.View(), "+ …rver/middleware")
	m.Update(tea.MouseMsg{X: 30 + x, Y: 2 + y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.focus != "server/middleware" {
		t.Errorf("click focused %q", m.focus)
	}
	// A click on the header or empty space changes nothing.
	m.Update(tea.MouseMsg{X: 30, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.focus != "server/middleware" {
		t.Errorf("header click focused %q", m.focus)
	}
	// The list scrolls with the wheel.
	m = newModel(t, sample(), 60, 12)
	m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.off != wheelStep {
		t.Errorf("wheel off %d", m.off)
	}
	m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if m.off != 0 {
		t.Errorf("wheel back off %d", m.off)
	}
	// In the list a click on a module's second line focuses it.
	m = newModel(t, sample(), 90, 30)
	y, _ = rowOf(t, m.View(), "server/auth")
	m.Update(tea.MouseMsg{X: 10, Y: y + 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if m.focus != "server/auth" {
		t.Errorf("list click focused %q", m.focus)
	}
}

func TestColour(t *testing.T) {
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.TrueColor)
	r.SetHasDarkBackground(true)
	m := New(r)
	m.SetSize(160, 40)
	m.SetDetail(detail(sample()))
	v := m.View()
	if !strings.Contains(v, "\x1b[") {
		t.Fatal("no escape codes with a colour renderer")
	}
	for i, l := range strings.Split(v, "\n") {
		if lw := ansi.StringWidth(l); lw > 160 {
			t.Errorf("line %d is %d wide", i, lw)
		}
	}
	// Accent (#f0a63a) borders the eyes modules; the new edge is green.
	if !strings.Contains(v, "240;166;58") || !strings.Contains(v, "126;226;168") {
		t.Errorf("accent or add colour missing")
	}
	if plainView := ansi.Strip(v); !strings.Contains(plainView, "web/auth") {
		t.Errorf("stripped view lacks modules")
	}
}

func TestHelpBindings(t *testing.T) {
	m := New(nil)
	if len(m.HelpBindings()) != 2 || len(m.ShortBindings()) != 4 {
		t.Error("bindings")
	}
	k := DefaultKeys()
	k.DiffOnly.SetKeys("D")
	m.SetKeys(k)
	m.SetSize(160, 40)
	m.SetDetail(detail(sample()))
	if !m.HandleKey(keyMsg("D")) || !m.diffOnly {
		t.Error("rebound key not used")
	}
}
