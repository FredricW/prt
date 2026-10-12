// Package tui is the Bubble Tea front end. The root Model holds the picker
// (sidebar) and the active item; the main pane will be one of four child
// models (overview, code, architecture, pipeline) fed by daemon events.
// Long operations never run in Update; they are tea.Cmds returning messages.
//
// Current state (skeleton): picker grouped by section, plain unified diff of
// the selected item. TODO(M0–M5): child screens per the spec.
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

const sidebarWidth = 34

// Focus is which pane has keyboard focus.
type Focus int

// Focus targets.
const (
	FocusPicker Focus = iota
	FocusMain
)

// Messages.
type (
	itemsMsg  []model.Item
	detailMsg proto.Detail
	eventMsg  proto.Event
	errMsg    struct{ err error }
	closedMsg struct{}
)

// styles are built from a renderer so snapshots render without colour.
type styles struct {
	title, dim, sel, section, add, del, hunk, border lipgloss.Style
}

func newStyles(r *lipgloss.Renderer) styles {
	return styles{
		title:   r.NewStyle().Bold(true),
		dim:     r.NewStyle().Faint(true),
		sel:     r.NewStyle().Reverse(true),
		section: r.NewStyle().Bold(true).Underline(true),
		add:     r.NewStyle().Foreground(lipgloss.Color("2")),
		del:     r.NewStyle().Foreground(lipgloss.Color("1")),
		hunk:    r.NewStyle().Foreground(lipgloss.Color("6")),
		border:  r.NewStyle().Border(lipgloss.NormalBorder(), false, true, false, false),
	}
}

// Model is the root model.
type Model struct {
	client daemon.Client
	st     styles

	items  []model.Item
	cursor int
	focus  Focus
	detail *proto.Detail
	vp     viewport.Model
	err    error

	width, height int
}

// New builds the root model. r may be nil (default renderer).
func New(c daemon.Client, r *lipgloss.Renderer) Model {
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	return Model{client: c, st: newStyles(r), vp: viewport.New(80, 20), width: 120, height: 40}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.discover(), m.listen())
}

func (m Model) discover() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if err := m.client.Discover(ctx); err != nil {
			return errMsg{err}
		}
		items, err := m.client.Items(ctx)
		if err != nil {
			return errMsg{err}
		}
		return itemsMsg(items)
	}
}

func (m Model) loadItems() tea.Cmd {
	return func() tea.Msg {
		items, err := m.client.Items(context.Background())
		if err != nil {
			return errMsg{err}
		}
		return itemsMsg(items)
	}
}

func (m Model) loadDetail(id string) tea.Cmd {
	return func() tea.Msg {
		d, err := m.client.Detail(context.Background(), id)
		if err != nil {
			return errMsg{err}
		}
		return detailMsg(d)
	}
}

func (m Model) listen() tea.Cmd {
	ch := m.client.Events()
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return closedMsg{}
		}
		return eventMsg(e)
	}
}

func (m Model) selected() (model.Item, bool) {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return model.Item{}, false
	}
	return m.items[m.cursor], true
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.vp.Width, m.vp.Height = max(10, m.width-sidebarWidth-2), max(3, m.height-3)
		return m, nil
	case itemsMsg:
		m.items = sortItems(msg)
		if m.cursor >= len(m.items) {
			m.cursor = max(0, len(m.items)-1)
		}
		var cmds []tea.Cmd
		for _, it := range m.items {
			if it.State == "" || it.State == model.StateStale {
				cmds = append(cmds, m.refresh(it.ID))
			}
		}
		if it, ok := m.selected(); ok {
			cmds = append(cmds, m.loadDetail(it.ID))
		}
		return m, tea.Batch(cmds...)
	case detailMsg:
		d := proto.Detail(msg)
		if it, ok := m.selected(); ok && it.ID == d.Item.ID {
			m.detail = &d
			m.vp.SetContent(m.renderDiff(d.Diff))
		}
		return m, nil
	case eventMsg:
		cmds := []tea.Cmd{m.listen()}
		if msg.Type == proto.EventItem || msg.Type == proto.EventRemove {
			cmds = append(cmds, m.loadItems())
		}
		if it, ok := m.selected(); ok && it.ID == msg.ItemID {
			cmds = append(cmds, m.loadDetail(it.ID))
		}
		return m, tea.Batch(cmds...)
	case errMsg:
		m.err = msg.err
		return m, nil
	case closedMsg:
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

func (m Model) refresh(id string) tea.Cmd {
	return func() tea.Msg {
		if err := m.client.Refresh(context.Background(), proto.ItemParams{ItemID: id}); err != nil {
			return errMsg{err}
		}
		return nil
	}
}

func (m Model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "tab", "enter":
		if m.focus == FocusPicker {
			m.focus = FocusMain
		} else {
			m.focus = FocusPicker
		}
		return m, nil
	case "esc":
		m.focus = FocusPicker
		return m, nil
	case "r":
		if it, ok := m.selected(); ok {
			return m, m.refresh(it.ID)
		}
	}
	if m.focus == FocusMain {
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		return m, cmd
	}
	prev := m.cursor
	switch k.String() {
	case "j", "down":
		m.cursor = min(m.cursor+1, max(0, len(m.items)-1))
	case "k", "up":
		m.cursor = max(m.cursor-1, 0)
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(0, len(m.items)-1)
	}
	if m.cursor != prev {
		m.detail = nil
		m.vp.SetContent("")
		if it, ok := m.selected(); ok {
			return m, m.loadDetail(it.ID)
		}
	}
	return m, nil
}

var sectionOrder = map[model.Section]int{
	model.SectionNeedsYou: 0, model.SectionYours: 1, model.SectionBranches: 2, model.SectionAdded: 3,
}

func sortItems(items []model.Item) []model.Item {
	out := append([]model.Item(nil), items...)
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := sectionOrder[out[i].Section], sectionOrder[out[j].Section]
		if si != sj {
			return si < sj
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func (m Model) renderDiff(d string) string {
	if d == "" {
		return m.st.dim.Render("(no diff yet)")
	}
	lines := strings.Split(strings.TrimRight(d, "\n"), "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "@@"):
			lines[i] = m.st.hunk.Render(l)
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "diff --git"):
			lines[i] = m.st.title.Render(l)
		case strings.HasPrefix(l, "+"):
			lines[i] = m.st.add.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[i] = m.st.del.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// View implements tea.Model.
func (m Model) View() string {
	side := m.viewPicker()
	main := m.viewMain()
	body := lipgloss.JoinHorizontal(lipgloss.Top, m.st.border.Width(sidebarWidth).Height(max(1, m.height-2)).Render(side), " ", main)
	status := m.st.dim.Render("j/k move · enter focus · r refresh · q quit")
	if m.err != nil {
		status = "error: " + m.err.Error()
	}
	return body + "\n" + status
}

func (m Model) viewPicker() string {
	var b strings.Builder
	b.WriteString(m.st.title.Render("prt") + "\n")
	if len(m.items) == 0 {
		b.WriteString(m.st.dim.Render("no items yet"))
		return b.String()
	}
	var cur model.Section = "-"
	for i, it := range m.items {
		if it.Section != cur {
			cur = it.Section
			b.WriteString("\n" + m.st.section.Render(sectionTitle(cur)) + "\n")
		}
		label := truncate(itemLabel(it), sidebarWidth-2)
		if i == m.cursor {
			label = m.st.sel.Render(label)
		}
		b.WriteString(label + "\n")
		b.WriteString(m.st.dim.Render(truncate("  "+secondary(it), sidebarWidth-2)) + "\n")
	}
	return b.String()
}

func (m Model) viewMain() string {
	it, ok := m.selected()
	if !ok {
		return m.st.dim.Render("Select an item.")
	}
	head := m.st.title.Render(truncate(itemLabel(it), max(10, m.width-sidebarWidth-4)))
	if m.detail == nil {
		return head + "\n" + m.st.dim.Render("loading…")
	}
	sub := fmt.Sprintf("%s ← %s · %d files", orDash(it.BaseRef), orDash(it.Ref), len(m.detail.Files))
	if a := m.detail.Agg; a != nil {
		sub += fmt.Sprintf(" · +%d −%d", a.Added, a.Removed)
	}
	return head + "\n" + m.st.dim.Render(sub) + "\n" + m.vp.View()
}

func sectionTitle(s model.Section) string {
	switch s {
	case model.SectionNeedsYou:
		return "Needs you"
	case model.SectionYours:
		return "Yours"
	case model.SectionBranches:
		return "Branches"
	case model.SectionAdded:
		return "Added"
	}
	return "Other"
}

func itemLabel(it model.Item) string {
	if it.Kind == model.KindPR {
		return fmt.Sprintf("#%d %s", it.Number, it.Title)
	}
	return it.Ref
}

func secondary(it model.Item) string {
	parts := []string{}
	if it.State != "" {
		s := string(it.State)
		if it.StateNote != "" {
			s += " " + it.StateNote
		}
		parts = append(parts, s)
	}
	if it.Kind == model.KindBranch {
		parts = append(parts, fmt.Sprintf("%d ahead", it.Ahead))
		if it.Title != "" && it.Title != it.Ref {
			parts = append(parts, it.Title)
		}
	}
	return strings.Join(parts, " · ")
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if n <= 1 || len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
