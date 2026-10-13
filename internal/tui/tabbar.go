package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// The tab bar (row 1, above the main pane): "1 Overview  2 Code
// 3 Architecture", plus "P Pipeline" or ", Settings" while that page is
// open, the active tab bracketed without colour, and the tab's dim context
// on the right. Its labels come from the key map's help keys, so a rebound
// tab key shows (and is clicked) as rebound.

// focusPrefix is the tab bar's first two cells: "▸ " while the main pane
// has focus.
func (m Model) focusPrefix() string {
	if m.focus == FocusMain {
		return "▸ "
	}
	return "  "
}

// tabSpec is one tab of the bar: its tab and label.
type tabSpec struct {
	tab   Tab
	label string
}

// tabSpecs are the tabs viewTabs draws, in order.
func (m Model) tabSpecs() []tabSpec {
	k := m.keys
	specs := []tabSpec{
		{TabOverview, k.Overview.Help().Key + " Overview"},
		{TabCode, k.Code.Help().Key + " Code"},
		{TabArch, k.Architecture.Help().Key + " Architecture"},
	}
	switch m.tab {
	case TabPipeline:
		specs = append(specs, tabSpec{TabPipeline, k.Pipeline.Help().Key + " Pipeline"})
	case TabSettings:
		specs = append(specs, tabSpec{TabSettings, k.Settings.Help().Key + " Settings"})
	}
	return specs
}

// tabHit is a tab's clickable cells [x0, x1) on the tab bar, counted from
// the main pane's left edge.
type tabHit struct {
	tab    Tab
	x0, x1 int
}

// tabHits lays the tab bar out the way viewTabs draws it: the focus prefix,
// each theme tab (its padding, or its brackets without colour), single
// spaces between tabs.
func (m Model) tabHits() []tabHit {
	x := ansi.StringWidth(m.focusPrefix())
	specs := m.tabSpecs()
	out := make([]tabHit, 0, len(specs))
	for _, s := range specs {
		w := ansi.StringWidth(m.st.th.Tab(s.label, m.tab == s.tab))
		out = append(out, tabHit{tab: s.tab, x0: x, x1: x + w})
		x += w + 1
	}
	return out
}

// tabAt returns the tab under column x of the tab bar (main-pane cells);
// ok is false between tabs, after them, and past the pane's width.
func (m Model) tabAt(x int) (Tab, bool) {
	_, mw, _ := m.dims()
	if x < 0 || x >= mw {
		return 0, false
	}
	for _, h := range m.tabHits() {
		if x >= h.x0 && x < h.x1 {
			return h.tab, true
		}
	}
	return 0, false
}

// viewTabs is the tab bar with the tab's dim context on the right (the
// "↻ updating review" note first while the item updates).
func (m Model) viewTabs(w int) string {
	th := m.st.th
	s := m.focusPrefix()
	for i, spec := range m.tabSpecs() {
		if i > 0 {
			s += " "
		}
		s += th.Tab(spec.label, m.tab == spec.tab)
	}
	// The tabs win: the context is cut to the room left after them.
	room := w - ansi.StringWidth(s) - 2
	if room < 4 {
		return truncate(s, w)
	}
	return spread(s, m.st.dim.Render(truncate(m.tabContext(), room)), w)
}

// tabBarMouse handles a mouse event on the tab bar (row 1 of the main
// pane). A left press on a tab selects it like its key: Overview, Code or
// Architecture (closing the Pipeline page when it is open); the open
// Pipeline tab keeps the page. Presses between or after tabs, other
// buttons and releases do nothing, and nothing falls through to the page
// below (the bug where any tab click opened the Code tab). The settings
// page ignores the mouse before this is reached; while a search result is
// previewed the tabs do not switch (the prompt has the keyboard).
func (m Model) tabBarMouse(msg tea.MouseMsg, x int) (tea.Model, tea.Cmd) {
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress || m.previewing() {
		return m, nil
	}
	// The open Pipeline tab keeps the page; any other tab replaces it.
	if t, ok := m.tabAt(x); ok && t != TabPipeline && t != TabSettings {
		m.tab = t
	}
	return m, nil
}
