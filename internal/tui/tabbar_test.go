package tui

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// ---- (C) the tab-click bug ------------------------------------------------------

// tabModel is a model at width w (plain or colour renderer) showing
// detail_full for pr:1 with the recording Code pane, the Overview scrolled
// so the line above its top is a unit's row: before the fix a click on the
// tab bar landed there and jumped to that unit in Code.
func tabModel(t *testing.T, w int, colour bool, km *KeyMap) (Model, *fakePane) {
	t.Helper()
	items := threeItems()
	f := newFake(items...)
	d := loadDetail(t, "detail_full.json")
	d.Item = items[0]
	f.details["pr:1"] = d
	r := plainRenderer()
	if colour {
		r = lipgloss.NewRenderer(io.Discard)
		r.SetColorProfile(termenv.TrueColor)
	}
	m := NewWithOptions(f, r, Options{Now: func() time.Time { return fixedNow }, Keys: km})
	m = update(t, m, tea.WindowSizeMsg{Width: w, Height: 30})
	m = update(t, m, itemsMsg{items: items})
	m = feed[detailMsg](t, m, m.selectionChanged())
	m, p := withFakePane(m)
	m.code.SetDetail(d)
	for i, a := range m.overview.anchors {
		if a != "" && i > 0 {
			m.overview.vp.SetYOffset(i + 1)
			break
		}
	}
	return m, p
}

// tabX returns the screen column inside label on the drawn tab bar (row 1),
// found in the rendered text with the escape codes stripped.
func tabX(t *testing.T, m Model, label string) (int, bool) {
	t.Helper()
	line := ansi.Strip(strings.Split(m.View(), "\n")[1])
	sw, _, _ := m.dims()
	pane := []rune(line)[sw+2:]
	key := []rune(label)
	key = key[:min(3, len(key))]
	for i := 0; i+len(key) <= len(pane); i++ {
		if string(pane[i:i+len(key)]) == string(key) {
			return sw + 2 + i + 1, true
		}
	}
	return 0, false
}

func TestTabClicks(t *testing.T) {
	rebound, err := KeyMapFromConfig(map[string]string{"overview": "F1", "code": "F2", "architecture": "F3", "pipeline": "F4"})
	if err != nil {
		t.Fatal(err)
	}
	keymaps := map[string]*KeyMap{"default": nil, "rebound": &rebound}
	type tc struct {
		name  string
		setup func(m Model, k KeyMap) Model
		label func(k KeyMap) string
		want  Tab
	}
	cases := []tc{
		{"overview from code", func(m Model, k KeyMap) Model { m.tab = TabCode; return m },
			func(k KeyMap) string { return k.Overview.Help().Key + " Overview" }, TabOverview},
		{"code from overview", func(m Model, _ KeyMap) Model { return m },
			func(k KeyMap) string { return k.Code.Help().Key + " Code" }, TabCode},
		{"architecture from overview", func(m Model, _ KeyMap) Model { return m },
			func(k KeyMap) string { return k.Architecture.Help().Key + " Architecture" }, TabArch},
		{"pipeline tab keeps the page", func(m Model, k KeyMap) Model { return press(t, m, k.Pipeline.Help().Key) },
			func(k KeyMap) string { return k.Pipeline.Help().Key + " Pipeline" }, TabPipeline},
		{"overview closes the pipeline page", func(m Model, k KeyMap) Model { return press(t, m, k.Pipeline.Help().Key) },
			func(k KeyMap) string { return k.Overview.Help().Key + " Overview" }, TabOverview},
		{"settings ignores the mouse", func(m Model, _ KeyMap) Model { return press(t, m, ",") },
			func(k KeyMap) string { return k.Code.Help().Key + " Code" }, TabSettings},
	}
	for kmName, km := range keymaps {
		for _, colour := range []bool{false, true} {
			for _, w := range []int{80, 100, 120, 140, 200} {
				for _, c := range cases {
					name := fmt.Sprintf("%s/%s/colour=%v/w=%d", kmName, c.name, colour, w)
					t.Run(name, func(t *testing.T) {
						m, p := tabModel(t, w, colour, km)
						m = c.setup(m, m.keys)
						focus := m.focus
						x, ok := tabX(t, m, c.label(m.keys))
						if !ok {
							// The label is cut off by a narrow pane (the bar keeps its first
							// cell, "[…"), or the settings page is open: click the tab's
							// first cell.
							for _, h := range m.tabHits() {
								if strings.HasSuffix(c.label(m.keys), map[Tab]string{TabPipeline: "Pipeline",
									TabOverview: "Overview", TabCode: "Code", TabArch: "Architecture"}[h.tab]) {
									x, ok = sidebarWidth(w)+2+h.x0, true
								}
							}
						}
						if !ok {
							t.Fatalf("tab %q not drawn:\n%s", c.label(m.keys), m.View())
						}
						updates := p.updates
						m = update(t, m, mouseClick(x, 1))
						if m.tab != c.want {
							t.Errorf("tab %v, want %v", m.tab, c.want)
						}
						if len(p.focused) != 0 {
							t.Errorf("a tab click jumped to a unit: %v", p.focused)
						}
						if p.updates != updates {
							t.Error("a tab click reached the Code page")
						}
						if m.focus != focus {
							t.Errorf("focus %v → %v", focus, m.focus)
						}
					})
				}
			}
		}
	}
}

// Clicks between tabs, after them and past the pane do nothing, and never
// reach the page below.
func TestTabBarGapsDoNothing(t *testing.T) {
	for _, colour := range []bool{false, true} {
		m, p := tabModel(t, 120, colour, nil)
		sw, mw, _ := m.dims()
		hits := m.tabHits()
		gaps := []int{0, 1, hits[0].x1, hits[1].x1, hits[len(hits)-1].x1 + 1, mw - 1, mw + 5}
		for _, g := range gaps {
			m2 := update(t, m, mouseClick(sw+2+g, 1))
			if m2.tab != TabOverview || len(p.focused) != 0 {
				t.Errorf("colour=%v: click at tab-bar column %d: tab %v focused %v", colour, g, m2.tab, p.focused)
			}
		}
		// The gap column between the sidebar and the pane is no tab either.
		if m2 := update(t, m, mouseClick(sw+1, 1)); m2.tab != TabOverview {
			t.Errorf("click on the gap switched to %v", m2.tab)
		}
	}
}

// The hit-test follows the drawn bar exactly: every tab's cells lie under
// its label in the rendered text.
func TestTabHitsMatchRendering(t *testing.T) {
	for _, colour := range []bool{false, true} {
		for _, tab := range []Tab{TabOverview, TabCode, TabArch, TabPipeline} {
			m, _ := tabModel(t, 160, colour, nil)
			m.tab = tab
			line := []rune(ansi.Strip(m.viewTabs(160 - sidebarWidth(160) - 2)))
			for _, h := range m.tabHits() {
				got := strings.TrimSpace(strings.Trim(string(line[h.x0:h.x1]), "[]"))
				var want string
				for _, s := range m.tabSpecs() {
					if s.tab == h.tab {
						want = s.label
					}
				}
				if got != want {
					t.Errorf("colour=%v tab %v: cells %d–%d hold %q, want %q", colour, tab, h.x0, h.x1, got, want)
				}
			}
		}
	}
}

// A wheel over the tab bar still scrolls the Overview (only clicks are the
// tab bar's).
func TestTabBarWheelScrolls(t *testing.T) {
	m, _ := tabModel(t, 120, false, nil)
	m.overview.vp.GotoTop()
	m = update(t, m, tea.MouseMsg{X: sidebarWidth(120) + 10, Y: 1, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if m.overview.vp.YOffset == 0 {
		t.Error("wheel over the tab bar did not scroll")
	}
}
