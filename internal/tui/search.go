package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/searchview"
)

// searchPane is what the root model needs from the `/` prompt (spec Picker,
// "Search and add"). The real one is *searchview.Model; tests substitute a
// recording stand-in.
type searchPane interface {
	SetSearcher(s searchview.Searcher)
	SetNow(now func() time.Time)
	// SetSize sets the width and the rows available (prompt plus results).
	SetSize(w, h int)
	Open() tea.Cmd
	Close()
	// Active is true while the prompt is open: it then takes every key.
	Active() bool
	// Update handles keys while open, ResultMsg and the component's own
	// messages; mouse messages arrive in component-local cells.
	Update(msg tea.Msg) tea.Cmd
	PendingChoice() (searchview.Choice, bool)
	View() string
}

var _ searchPane = (*searchview.Model)(nil)

// newSearchPane builds the prompt with the client's Search as its source.
func newSearchPane(r *lipgloss.Renderer, c daemon.Client, now func() time.Time) *searchview.Model {
	s := searchview.New(r)
	s.SetSearcher(c.Search)
	s.SetNow(now)
	return &s
}

// Search, add and remove messages.
type (
	addedMsg struct {
		item model.Item
		err  error
	}
	removedMsg struct {
		label string
		quiet bool // a discarded preview: no note
		err   error
	}
	previewMsg struct {
		item model.Item
		err  error
	}
	previewDetailMsg struct {
		seq int
		d   proto.Detail
		err error
	}
)

// previewLine is the dim line above a previewed Overview.
const previewLine = "preview — enter adds, esc discards"

// searchRows is how many rows of the sidebar box the open prompt takes
// (results above, the prompt line at the bottom).
func searchRows(inner int) int {
	return max(2, min(12, inner/2))
}

// openSearch opens the prompt at the bottom of the sidebar box.
func (m *Model) openSearch() tea.Cmd {
	cmd := m.search.Open()
	m.sizeSidebar()
	return cmd
}

// updateSearch hands k to the open prompt and acts on its outcome: a
// chosen result, or the prompt closing (which discards a preview).
func (m Model) updateSearch(msg tea.Msg) (Model, tea.Cmd) {
	cmd := m.search.Update(msg)
	return m, batch(cmd, m.afterSearch())
}

// afterSearch acts on the prompt's pending choice and on it closing.
func (m *Model) afterSearch() tea.Cmd {
	var cmds []tea.Cmd
	if ch, ok := m.search.PendingChoice(); ok {
		cmds = append(cmds, m.onChoice(ch))
	}
	if !m.search.Active() {
		m.sizeSidebar()
		cmds = append(cmds, m.discardPreview())
	}
	return batch(cmds...)
}

// onChoice acts on a search result: enter adds it (or moves the cursor to
// it when already listed) and closes the prompt; tab previews it.
func (m *Model) onChoice(ch searchview.Choice) tea.Cmd {
	hit := ch.Hit
	switch ch.Kind {
	case searchview.ChoiceAdd:
		m.search.Close()
		if hit.ItemID != "" && hit.ItemID == m.previewID {
			m.previewID, m.preview = "", nil // Add promotes the preview item
		}
		if i := m.picker.indexOf(hit.ItemID); hit.Listed || i >= 0 {
			if i >= 0 && m.picker.moveTo(i) {
				return m.selectionChanged()
			}
			return nil
		}
		return m.addCmd(hit.Query)
	case searchview.ChoicePreview:
		if i := m.picker.indexOf(hit.ItemID); hit.Listed || i >= 0 {
			// Already listed: nothing to preview, show it in the picker.
			m.note = hitLabel(hit) + " is already in the picker"
			if i >= 0 && m.picker.moveTo(i) {
				return m.selectionChanged()
			}
			return nil
		}
		m.note = "previewing " + hitLabel(hit) + "…"
		c, q := m.client, hit.Query
		return func() tea.Msg {
			it, err := c.Preview(context.Background(), q)
			return previewMsg{item: it, err: err}
		}
	}
	return nil
}

// hitLabel names a search hit ("#482" or the ref).
func hitLabel(h proto.SearchHit) string {
	if h.Number > 0 {
		return fmt.Sprintf("#%d", h.Number)
	}
	if h.Ref != "" {
		return h.Ref
	}
	return h.Query
}

// addCmd adds query (a search hit or `:add`) to the Added section.
func (m *Model) addCmd(query string) tea.Cmd {
	m.note = "adding " + query + "…"
	c := m.client
	return func() tea.Msg {
		it, err := c.Add(context.Background(), query)
		return addedMsg{item: it, err: err}
	}
}

// removeCmd removes item it (x on an added item).
func (m *Model) removeCmd(it model.Item) tea.Cmd {
	c, id, label := m.client, it.ID, itemLabel(it)
	return func() tea.Msg {
		return removedMsg{label: label, err: c.Remove(context.Background(), id)}
	}
}

// discardPreview removes the previewed item (the prompt closed). It goes
// through RemovePreview, so an item that is listed (or was added or
// promoted meanwhile) is never deleted.
func (m *Model) discardPreview() tea.Cmd {
	if m.previewID == "" {
		return nil
	}
	id := m.previewID
	m.previewID, m.preview = "", nil
	m.previewSeq++
	return m.removePreviewCmd(id)
}

// removePreviewCmd asks the daemon to drop id if it is still a preview.
func (m *Model) removePreviewCmd(id string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		return removedMsg{label: id, quiet: true, err: c.RemovePreview(context.Background(), id)}
	}
}

// loadPreview reads the previewed item's detail.
func (m Model) loadPreview(id string, seq int) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		d, err := c.Detail(context.Background(), id)
		return previewDetailMsg{seq: seq, d: d, err: err}
	}
}

// onSearchMsg handles the answers of the search-related daemon calls; ok is
// false for any other message.
func (m Model) onSearchMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case addedMsg:
		if msg.err != nil {
			m.note = "add failed: " + msg.err.Error()
			return m, nil, true
		}
		m.note = "added " + itemLabel(msg.item) + "; analysing…"
		m.selectAfter = msg.item.ID
		if m.takeSelectAfter() {
			return m, m.selectionChanged(), true
		}
		return m, m.queueReload(), true
	case removedMsg:
		switch {
		case msg.err != nil && !msg.quiet:
			m.note = "remove failed: " + msg.err.Error()
		case msg.quiet:
		default:
			m.note = "removed " + msg.label
			return m, m.queueReload(), true
		}
	case previewMsg:
		if msg.err != nil {
			m.note = "preview failed: " + msg.err.Error()
			return m, nil, true
		}
		if msg.item.Source != model.SourcePreview {
			// Already listed (ranked, discovered or added): never track it as
			// a preview, so closing the prompt cannot delete it. Show it by
			// moving the picker cursor to it instead.
			m.note = itemLabel(msg.item) + " is already in the picker"
			m.previewID, m.preview = "", nil // the daemon dropped any older preview
			m.previewSeq++
			if i := m.picker.indexOf(msg.item.ID); i >= 0 && m.picker.moveTo(i) {
				return m, m.selectionChanged(), true
			}
			return m, nil, true
		}
		if !m.search.Active() { // closed meanwhile: drop it again
			return m, m.removePreviewCmd(msg.item.ID), true
		}
		m.note = ""
		m.previewID, m.preview = msg.item.ID, nil
		m.previewSeq++
		return m, m.loadPreview(msg.item.ID, m.previewSeq), true
	case previewDetailMsg:
		if msg.seq != m.previewSeq || m.previewID == "" {
			return m, nil, true
		}
		if msg.err != nil {
			m.note = "preview failed: " + msg.err.Error()
			return m, nil, true
		}
		m.preview = &msg.d
		m.renderPreview()
	default:
		return m, nil, false
	}
	return m, nil, true
}

// renderPreview lays out the previewed item's Overview.
func (m *Model) renderPreview() {
	if m.preview == nil {
		return
	}
	_, mw, _ := m.dims()
	y := m.previewOV.vp.YOffset
	m.previewOV.setPage(overviewPage(m.st, m.md, *m.preview, m.now(), mw))
	m.previewOV.vp.SetYOffset(y)
}

// viewPreview is the main pane while a search result is previewed.
func (m Model) viewPreview() string {
	head := m.st.dim.Render(previewLine)
	if m.preview == nil {
		return head + "\n" + m.st.dim.Render("loading preview…")
	}
	return head + "\n" + m.previewOV.view()
}

// toggleMark flips the multi-select mark of the selected item (space); the
// first use says the overlap view is M6.
func (m *Model) toggleMark() {
	it, ok := m.picker.selected()
	if !ok {
		return
	}
	if m.marked[it.ID] {
		delete(m.marked, it.ID)
	} else {
		m.marked[it.ID] = true
	}
	if !m.markNoted {
		m.markNoted = true
		m.note = "marked for overlap — the overlap view comes in M6"
	}
}

// clearMarks drops every mark (esc in the picker); ok is false when there
// were none.
func (m *Model) clearMarks() bool {
	if len(m.marked) == 0 {
		return false
	}
	m.marked = map[string]bool{}
	m.note = "selection cleared"
	return true
}

// removeSelected handles x: only items added by hand can be removed.
func (m *Model) removeSelected() tea.Cmd {
	it, ok := m.picker.selected()
	if !ok {
		return nil
	}
	if it.Source != model.SourceAdded {
		m.note = "only added items can be removed"
		return nil
	}
	m.note = "removing " + itemLabel(it) + "…"
	return m.removeCmd(it)
}

// searchLayout returns, for the open prompt, the list rows above it and
// the blank rows padding its view to the bottom of the box.
func (m Model) searchLayout() (listRows, pad int, lines []string) {
	inner := m.sidebarInner()
	rows := searchRows(inner)
	lines = strings.Split(m.search.View(), "\n")
	if len(lines) > rows {
		lines = lines[len(lines)-rows:]
	}
	return inner - rows, rows - len(lines), lines
}
