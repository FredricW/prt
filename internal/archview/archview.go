// Package archview is the Architecture tab (tab 3): the module graph of
// model.ItemAgg.Arch drawn as boxes and edges in the terminal, coloured by
// attention as in docs/design/v2/Architecture.dc.html, with a legend, a
// per-module table and a net-effect block. Keys: hjkl move the focus
// between modules, enter lists the focused module's files (enter on a file
// asks the root to open it in Code, see PendingJump), +/- change the depth
// (hops shown), d toggles diff-only (changed, new and removed modules).
//
// Layout: a "Module graph · depth N" box on the left and a right column
// (Per module, Contracts crossing edges, Net effect, Legend) from
// SideMinWidth columns; below that the right column follows the graph.
// The graph is layered (importers above what they import, see layoutGraph)
// with module boxes of boxW × boxH cells; when it does not fit the box it
// falls back to a list grouped by hop ("── hop 0 ──") with the edges as
// "A → B (new)" lines.
//
// Like codeview and pipeview it is a component the root model
// (internal/tui) owns. The API below was fixed by the M5 lead (DECISIONS
// "M5 shared contracts"); the arch work package implements it without
// changing these signatures.
package archview

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/theme"
)

// Keys are the bindings the component handles (the tui KeyMap builds them,
// so they are rebindable).
type Keys struct {
	Left, Down, Up, Right key.Binding // h j k l (and arrows)
	Enter, Close          key.Binding // enter: files of the module / open file; esc: close the file list
	DepthUp, DepthDown    key.Binding // + / -
	DiffOnly              key.Binding // d
}

// DefaultKeys returns the design's bindings.
func DefaultKeys() Keys {
	b := func(desc string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(keys[0], desc))
	}
	return Keys{
		Left: b("focus left", "h", "left"), Down: b("focus down", "j", "down"),
		Up: b("focus up", "k", "up"), Right: b("focus right", "l", "right"),
		Enter: b("files in module", "enter"), Close: b("close", "esc"),
		DepthUp: b("more depth", "+", "="), DepthDown: b("less depth", "-"),
		DiffOnly: b("diff-only", "d"),
	}
}

// SideMinWidth is the width from which the right column sits beside the
// graph; below it the right column follows the graph (or list).
const SideMinWidth = 80

// sideW is the right column's width.
const sideW = 33

// wheelStep is how many lines one mouse wheel notch scrolls.
const wheelStep = 3

// Model is the component. The zero value is not usable; call New.
type Model struct {
	keys   Keys
	r      *lipgloss.Renderer
	th     theme.Theme
	w, h   int
	ox, oy int
	d      *proto.Detail

	focus    string // focused module id
	depth    int    // hops shown
	diffOnly bool
	off      int // first body line shown

	files    bool // the focused module's file list is open
	fileRows []fileRow
	fileSel  int

	jump    string
	hasJump bool
}

// fileRow is one row of a module's file list.
type fileRow struct {
	path       string
	adds, dels int
	status     string // "new", "deleted", "renamed" or ""
}

// New builds an empty component. r may be nil (default renderer).
func New(r *lipgloss.Renderer) Model {
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	return Model{keys: DefaultKeys(), r: r, th: theme.New(r), w: 80, h: 20, depth: 1}
}

// SetKeys replaces the bindings.
func (m *Model) SetKeys(k Keys) { m.keys = k }

// SetSize sets the component's width and height in cells.
func (m *Model) SetSize(w, h int) {
	m.w, m.h = max(1, w), max(1, h)
	m.clampOff()
}

// SetOrigin tells the component where the root draws it, so mouse cells
// map onto modules.
func (m *Model) SetOrigin(x, y int) { m.ox, m.oy = x, y }

// SetDetail shows an item; a reload of the same item keeps focus, depth
// and diff-only (and an open file list, when its module is still there).
func (m *Model) SetDetail(d proto.Detail) {
	same := m.d != nil && d.Item.ID != "" && d.Item.ID == m.d.Item.ID
	m.d = &d
	if !same {
		m.reset()
	}
	m.depth = clamp(m.depth, 0, m.maxDepth())
	m.ensureFocus()
	if m.files {
		m.openFiles()
	}
	m.clampOff()
}

// reset forgets focus, depth, diff-only, scroll, the file list and any
// pending jump.
func (m *Model) reset() {
	m.focus, m.depth, m.diffOnly, m.off = "", 1, false, 0
	m.files, m.fileRows, m.fileSel = false, nil, 0
	m.jump, m.hasJump = "", false
}

// Clear drops the item (selection changed).
func (m *Model) Clear() {
	m.d = nil
	m.reset()
}

// graph is the item's module graph (nil before stage 8 ran).
func (m *Model) graph() *model.ArchGraph {
	if m.d == nil || m.d.Agg == nil {
		return nil
	}
	return m.d.Agg.Arch
}

// maxDepth is the deepest hop the graph has (ArchGraph.MaxHop, or the
// largest module hop when that is bigger).
func (m *Model) maxDepth() int {
	g := m.graph()
	if g == nil {
		return 0
	}
	n := g.MaxHop
	for _, mod := range g.Modules {
		n = max(n, mod.Hop)
	}
	return n
}

// shown is the visible part of the graph.
func (m *Model) shown() shown { return visible(m.graph(), m.depth, m.diffOnly) }

// ensureFocus keeps the focus on a visible module (the first one when the
// focused module is hidden or gone).
func (m *Model) ensureFocus() {
	s := m.shown()
	if _, ok := s.idx[m.focus]; ok {
		return
	}
	m.focus = ""
	if len(s.mods) > 0 {
		m.focus = s.mods[0].ID
	}
}

// focused returns the focused module.
func (m *Model) focused() (model.ArchModule, bool) {
	g := m.graph()
	if g == nil {
		return model.ArchModule{}, false
	}
	for _, mod := range g.Modules {
		if mod.ID == m.focus {
			return mod, true
		}
	}
	return model.ArchModule{}, false
}

// HandleKey handles k and reports whether it was consumed. While the file
// list is open every key is consumed.
func (m *Model) HandleKey(k tea.KeyMsg) bool {
	g := m.graph()
	if g == nil || len(g.Modules) == 0 {
		return false
	}
	if m.files {
		m.fileKey(k)
		return true
	}
	km := m.keys
	switch {
	case key.Matches(k, km.Left):
		m.move(dLeft)
	case key.Matches(k, km.Right):
		m.move(dRight)
	case key.Matches(k, km.Up):
		m.move(dUp)
	case key.Matches(k, km.Down):
		m.move(dDown)
	case key.Matches(k, km.Enter):
		if m.focus == "" {
			return false
		}
		m.fileSel = 0
		m.openFiles()
		m.off = 0
	case key.Matches(k, km.DepthUp):
		m.setDepth(m.depth + 1)
	case key.Matches(k, km.DepthDown):
		m.setDepth(m.depth - 1)
	case key.Matches(k, km.DiffOnly):
		m.diffOnly = !m.diffOnly
		m.ensureFocus()
		m.follow()
	default:
		return false
	}
	return true
}

// setDepth changes the hops shown (0..maxDepth).
func (m *Model) setDepth(d int) {
	m.depth = clamp(d, 0, m.maxDepth())
	m.ensureFocus()
	m.follow()
}

// fileKey drives the open file list.
func (m *Model) fileKey(k tea.KeyMsg) {
	km := m.keys
	switch {
	case key.Matches(k, km.Close):
		m.files = false
		m.clampOff()
	case key.Matches(k, km.Down):
		m.fileSel = clamp(m.fileSel+1, 0, max(0, len(m.fileRows)-1))
	case key.Matches(k, km.Up):
		m.fileSel = clamp(m.fileSel-1, 0, max(0, len(m.fileRows)-1))
	case key.Matches(k, km.Enter):
		if len(m.fileRows) == 0 {
			return
		}
		m.jump, m.hasJump = m.fileRows[m.fileSel].path, true
		m.files = false
		m.clampOff()
	}
}

// openFiles opens (or refreshes) the focused module's file list: its
// changed files with +adds −dels from Detail.Files.
func (m *Model) openFiles() {
	mod, ok := m.focused()
	if !ok {
		m.files = false
		return
	}
	m.files = true
	m.fileRows = m.fileRows[:0]
	for _, p := range mod.Files {
		m.fileRows = append(m.fileRows, m.fileRow(p))
	}
	m.fileSel = clamp(m.fileSel, 0, max(0, len(m.fileRows)-1))
}

// fileRow looks p up in Detail.Files (by path, then old path).
func (m *Model) fileRow(p string) fileRow {
	row := fileRow{path: p}
	for _, f := range m.d.Files {
		if f.Path != p && f.OldPath != p {
			continue
		}
		row.adds, row.dels = f.Adds, f.Dels
		switch {
		case f.Added:
			row.status = "new"
		case f.Deleted:
			row.status = "deleted"
		case f.Renamed || f.OldPath != "" && f.OldPath != f.Path:
			row.status = "renamed"
		}
		break
	}
	return row
}

// move focuses the nearest module in direction d: among modules whose
// centre lies that way, the one with the smallest distance along d plus
// twice the distance across it (ties: graph order).
func (m *Model) move(d uint8) {
	f := m.render()
	cur, ok := f.centers[m.focus]
	if !ok {
		return
	}
	best, bestScore := "", -1
	for _, id := range f.order {
		c := f.centers[id]
		along, across := 0, 0
		switch d {
		case dDown:
			along, across = c.y-cur.y, abs(c.x-cur.x)
		case dUp:
			along, across = cur.y-c.y, abs(c.x-cur.x)
		case dRight:
			along, across = c.x-cur.x, abs(c.y-cur.y)
		case dLeft:
			along, across = cur.x-c.x, abs(c.y-cur.y)
		}
		if id == m.focus || along <= 0 {
			continue
		}
		if score := along + 2*across; bestScore < 0 || score < bestScore {
			best, bestScore = id, score
		}
	}
	if best != "" {
		m.focus = best
		m.follow()
	}
}

// Update handles mouse messages (wheel scrolls, a click focuses a module
// or selects a file of the open list).
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	mm, ok := msg.(tea.MouseMsg)
	if !ok || m.graph() == nil {
		return nil
	}
	switch mm.Button {
	case tea.MouseButtonWheelUp:
		m.off = clamp(m.off-wheelStep, 0, m.maxOff())
	case tea.MouseButtonWheelDown:
		m.off = clamp(m.off+wheelStep, 0, m.maxOff())
	case tea.MouseButtonLeft:
		if mm.Action == tea.MouseActionPress {
			m.click(mm.X-m.ox, mm.Y-m.oy)
		}
	}
	return nil
}

// click acts on component cell (x, y): row 0 is the header.
func (m *Model) click(x, y int) {
	if y < 1 {
		return
	}
	f := m.render()
	by := y - 1 + clamp(m.off, 0, max(0, len(f.lines)-m.bodyHeight()))
	for _, h := range f.hits {
		if !h.r.contains(x, by) {
			continue
		}
		if h.file >= 0 {
			m.fileSel = h.file
		} else if !m.files {
			m.focus = h.id
		}
		return
	}
}

// Capturing reports whether the component takes every key (the module's
// file list is open).
func (m *Model) Capturing() bool { return m.files }

// PendingJump returns and clears the file the user chose in a module's file
// list (the root switches to Code and focuses it).
func (m *Model) PendingJump() (path string, ok bool) {
	path, ok = m.jump, m.hasJump
	m.jump, m.hasJump = "", false
	return path, ok
}

// HelpBindings are the component's groups for the `?` overlay.
func (m *Model) HelpBindings() [][]key.Binding {
	k := m.keys
	return [][]key.Binding{{k.Left, k.Down, k.Up, k.Right}, {k.Enter, k.DepthUp, k.DepthDown, k.DiffOnly}}
}

// ShortBindings are the component's hints for the bottom key bar.
func (m *Model) ShortBindings() []key.Binding {
	k := m.keys
	return []key.Binding{k.Right, k.Enter, k.DepthUp, k.DiffOnly}
}

// bodyHeight is the height below the header line.
func (m *Model) bodyHeight() int { return max(1, m.h-1) }

// maxOff is the largest useful scroll offset.
func (m *Model) maxOff() int { return max(0, len(m.render().lines)-m.bodyHeight()) }

func (m *Model) clampOff() { m.off = clamp(m.off, 0, m.maxOff()) }

// follow scrolls so the focused module is visible.
func (m *Model) follow() {
	f := m.render()
	bh := m.bodyHeight()
	switch {
	case f.focusEnd-f.focusTop > bh, f.focusTop < m.off:
		m.off = f.focusTop
	case f.focusEnd > m.off+bh:
		m.off = f.focusEnd - bh
	}
	m.off = clamp(m.off, 0, max(0, len(f.lines)-bh))
}

// View renders the tab body (no tab bar) in exactly the set size.
func (m *Model) View() string {
	g := m.graph()
	if g == nil || len(g.Modules) == 0 {
		msg := "module graph not computed yet"
		if g != nil && g.Note != "" {
			msg = "module graph: " + g.Note
		}
		return m.fit([]string{m.th.Dim.Render(msg)})
	}
	f := m.render()
	bh := m.bodyHeight()
	off := clamp(m.off, 0, max(0, len(f.lines)-bh))
	out := []string{m.header()}
	out = append(out, f.lines[off:min(len(f.lines), off+bh)]...)
	return m.fit(out)
}

// fit pads or cuts lines to exactly h, each at most w cells.
func (m *Model) fit(lines []string) string {
	if len(lines) > m.h {
		lines = lines[:m.h]
	}
	for len(lines) < m.h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		lines[i] = theme.Truncate(strings.TrimRight(l, " "), m.w)
	}
	return strings.Join(lines, "\n")
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }
