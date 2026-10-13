// Package codeview is the Code page, mode A "by file" (spec: "Code page"):
// a left file list sorted by attention (owned first, then a `not yours`
// divider) with minimap cells, and a right pane with the selected file's
// units in line order, each under a header strip of label chips, with
// per-line gutter marks (◆ ~ ⋯ ·), noise collapsed by default (`z`
// expands), a filters bar (`f`, `1`–`9`, `0`), unit/file navigation (`n`/`p`,
// `J`/`K`) and a raw-labels overlay (`L`).
//
// It renders only what proto.Detail carries (Units, Labels, Files, Agg,
// Diff); every number comes from Detail.Agg (the TUI never does maths).
// It is a plain component like diffview: the tui root model owns it,
// forwards keys and size, and calls View.
//
// Modes, by what the detail holds:
//   - Units and an aggregate with units: chips, minimap, gutter marks,
//     filters and noise collapsing (filters off when Agg.LabelsMissing).
//   - Units without an aggregate (analysis pending): files in path order,
//     units in line order, no chips, everything expanded.
//   - No units: the plain diff viewer (diffview) over Detail.Diff.
//
// Layout: an optional one-line bar (filters, notes), then the file list
// (min(40, w/3) cells, at least 16), a one-cell gap and the right pane,
// whose first line is the selected file's title. Below 60 columns the list
// is hidden and the right pane takes the full width (J/K still move
// between files; the title shows "i/n"). Rendering is lazy: the selected
// file's rows are indexed once and only visible rows are styled.
package codeview

import (
	"strconv"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/diffview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// Filter is one toggle of the filters bar.
type Filter string

// Filters in bar order; number keys 1–9 and 0 toggle them in this order.
// The first three are on by default (spec: needs eyes, behaviour changes,
// mine). FilterRisk means risk ≥ RiskMin. Filters hide units, never files.
// Comments, findings and changed-since-last-look are placeholders until
// their data exists (M4, M3, M4).
const (
	FilterNeedsEyes    Filter = "needs eyes"
	FilterBehaviour    Filter = "behaviour"
	FilterMine         Filter = "mine"
	FilterRisk         Filter = "risk ≥ N"
	FilterUntested     Filter = "untested"
	FilterUndocumented Filter = "undocumented"
	FilterUnsure       Filter = "unsure"
	FilterComments     Filter = "has comments"
	FilterFindings     Filter = "has findings"
	FilterSince        Filter = "changed since last look"
)

// AllFilters lists the filters in bar order.
var AllFilters = []Filter{
	FilterNeedsEyes, FilterBehaviour, FilterMine, FilterRisk, FilterUntested,
	FilterUndocumented, FilterUnsure, FilterComments, FilterFindings, FilterSince,
}

// Keys are the Code page bindings the component handles. The tui KeyMap
// builds them (so they are rebindable through prt.toml [keys]).
type Keys struct {
	Down, Up, PageDown, PageUp, HalfPageDown, HalfPageUp, Top, Bottom key.Binding
	NextUnit, PrevUnit, NextFile, PrevFile                            key.Binding
	Filters, Expand, RawLabels                                        key.Binding
	// Toggle[i] toggles AllFilters[i] (default keys "1".."9", then "0");
	// they are only active while the filters bar is open (after Filters), so
	// the tab keys 1/2 keep working otherwise.
	Toggle [numFilters]key.Binding
	// Close closes the filters bar or the raw-labels overlay (default esc).
	Close key.Binding
}

// DefaultKeys returns the spec's bindings: j/k, pgdown/pgup/space,
// ctrl+d/ctrl+u, g/G, n/p, J/K, f, z, L, 1–9 and 0, esc.
func DefaultKeys() Keys {
	b := func(desc string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(keys[0], desc))
	}
	k := Keys{
		Down: b("down", "j", "down"), Up: b("up", "k", "up"),
		PageDown: b("page down", "pgdown", " "), PageUp: b("page up", "pgup"),
		HalfPageDown: b("half page down", "ctrl+d"), HalfPageUp: b("half page up", "ctrl+u"),
		Top: b("top", "g", "home"), Bottom: b("bottom", "G", "end"),
		NextUnit: b("next unit", "n"), PrevUnit: b("previous unit", "p"),
		NextFile: b("next file", "J", "]"), PrevFile: b("previous file", "K", "["),
		Filters: b("filters", "f"), Expand: b("expand noise", "z"), RawLabels: b("raw labels", "L"),
		Close: b("close", "esc"),
	}
	for i := range k.Toggle {
		s := strconv.Itoa((i + 1) % 10)
		k.Toggle[i] = b("toggle "+string(AllFilters[i]), s)
	}
	return k
}

// Model is the component. The zero value is not usable; call New.
type Model struct {
	keys Keys
	r    *lipgloss.Renderer
	st   styles
	dv   diffview.Model
	w, h int
	// ox, oy are where the root model draws the component (SetOrigin), so
	// mouse coordinates can be mapped onto the list and the pane.
	ox, oy int

	c       content
	filters [numFilters]bool
	barOpen bool
	note    string // one-shot note in the bar line; cleared on the next key

	overlay     bool
	overlayUnit string
	overlayOff  int

	sel     int // selected file index
	off     int // top pane row
	listOff int // top list row

	expandedFiles map[string]bool // `z` per file path
	expandedUnits map[string]bool // FocusUnit on a collapsed unit
	revealed      map[string]bool // FocusUnit on a unit hidden by filters

	p     *pane
	cache *renderCache
}

// New builds an empty component. r may not be nil.
func New(r *lipgloss.Renderer) Model {
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	m := Model{keys: DefaultKeys(), r: r, st: newStyles(r), dv: diffview.New(r), w: 80, h: 20, filters: defaultFilters()}
	m.resetView()
	m.rebuild()
	return m
}

// SetKeys replaces the bindings.
func (m *Model) SetKeys(k Keys) { m.keys = k }

// SetSize sets the component's width and height in cells.
func (m *Model) SetSize(w, h int) {
	oldPaneW := m.layout().paneW
	m.w, m.h = max(1, w), max(1, h)
	m.dv.SetSize(m.w, m.h)
	if m.layout().paneW != oldPaneW {
		m.rebuildKeep() // header chips wrap to the pane width
		return
	}
	m.clamp()
}

// SetOrigin tells the component where its top-left cell is on screen, so
// mouse events (absolute coordinates) map onto the list and the pane.
func (m *Model) SetOrigin(x, y int) { m.ox, m.oy = x, y }

// SetDetail shows an item. A reload of the same item (same Item.ID) keeps
// the selected file, unit, scroll, filters and expansions where possible;
// another item resets selection, scroll and expansions but keeps filters.
func (m *Model) SetDetail(d proto.Detail) {
	same := d.Item.ID != "" && d.Item.ID == m.c.itemID
	prevPath, prevOff, prevDV := m.selectedPath(), m.off, m.dv.Offset()
	m.c = buildContent(d)
	if m.c.plain() {
		m.dv.SetDiff(d.Diff)
	} else {
		m.dv.SetDiff("")
	}
	if !same {
		m.resetView()
		m.rebuild()
		return
	}
	m.dv.ScrollBy(prevDV)
	m.sel = 0
	for i, f := range m.c.files {
		if f.path == prevPath {
			m.sel = i
			break
		}
	}
	m.off = prevOff
	m.rebuild()
}

// resetView forgets selection, scroll, expansions and the overlay.
func (m *Model) resetView() {
	m.sel, m.off, m.listOff = 0, 0, 0
	m.overlay, m.overlayUnit, m.overlayOff = false, "", 0
	m.expandedFiles = map[string]bool{}
	m.expandedUnits = map[string]bool{}
	m.revealed = map[string]bool{}
	m.note = ""
}

// Clear empties the component (selection changed, detail loading). Filter
// choices are kept.
func (m *Model) Clear() {
	m.c = content{}
	m.dv.SetDiff("")
	m.resetView()
	m.rebuild()
}

// rebuild re-indexes the selected file's rows and drops rendered rows.
func (m *Model) rebuild() {
	if len(m.c.files) > 0 {
		m.sel = clamp(m.sel, 0, len(m.c.files)-1)
	} else {
		m.sel = 0
	}
	m.p = m.buildPane(m.sel)
	m.cache = newRenderCache()
	m.clamp()
}

// rebuildKeep rebuilds and keeps the current unit's header where it was
// relative to the top of the pane.
func (m *Model) rebuildKeep() {
	id, delta := "", 0
	if ui := m.p.unitAt(m.off); ui >= 0 {
		id, delta = m.p.units[ui].u.ID, m.off-m.p.heads[ui]
	}
	m.rebuild()
	if h := m.p.headOf(id); h >= 0 {
		m.off = max(0, h+delta)
		m.clamp()
	}
}

// clamp keeps the scroll offsets in range and the selection visible.
func (m *Model) clamp() {
	if m.p == nil {
		return
	}
	m.off = clamp(m.off, 0, max(m.maxOffset(), 0))
	m.ensureListVisible()
}

func (m *Model) selectedPath() string {
	if m.sel < len(m.c.files) {
		return m.c.files[m.sel].path
	}
	return ""
}

// Capturing reports whether the filters bar or the raw-labels overlay is
// open, so the root model routes every key (including 1–9 and esc) here.
func (m Model) Capturing() bool { return !m.c.plain() && (m.barOpen || m.overlay) }

// Note returns the one-shot note shown in the bar line ("" when none).
func (m Model) Note() string { return m.note }

// CurrentUnit returns the unit under the cursor: the unit whose header is
// at or above the top of the pane (the first unit when the pane is scrolled
// above every header).
func (m Model) CurrentUnit() (model.ChangeUnit, bool) {
	if m.p == nil || m.c.plain() {
		return model.ChangeUnit{}, false
	}
	ui := m.p.unitAt(m.off)
	if ui < 0 {
		return model.ChangeUnit{}, false
	}
	return *m.p.units[ui].u, true
}

// SelectedFile returns the path of the selected file ("" in the plain diff
// fallback or when empty).
func (m Model) SelectedFile() string {
	if m.c.plain() {
		return m.dv.CurrentFile()
	}
	return m.selectedPath()
}

// FocusUnit selects the file containing unitID and scrolls to that unit
// (Overview `enter` and minimap cells jump here). A collapsed unit is
// expanded and a unit hidden by filters is revealed until filters change.
// It reports success.
func (m *Model) FocusUnit(unitID string) bool {
	fi := m.c.fileOf(unitID)
	if fi < 0 {
		return false
	}
	if !m.visible(unitID) {
		m.revealed[unitID] = true
	}
	if m.collapsed(unitID) {
		m.expandedUnits[unitID] = true
	}
	m.overlay = false
	m.sel = fi
	m.rebuild()
	m.off = max(0, m.p.headOf(unitID)) // may overscroll so the header is on top
	m.ensureListVisible()
	return true
}

// HelpBindings returns the bindings for the `?` overlay, grouped in rows.
func (m Model) HelpBindings() [][]key.Binding {
	k := m.keys
	return [][]key.Binding{
		{k.Down, k.Up, k.PageDown, k.PageUp, k.HalfPageDown, k.HalfPageUp, k.Top, k.Bottom},
		{k.NextUnit, k.PrevUnit, k.NextFile, k.PrevFile},
		{k.Filters, k.Expand, k.RawLabels},
	}
}

// ShortBindings returns the status-line hints.
func (m Model) ShortBindings() []key.Binding {
	k := m.keys
	return []key.Binding{k.NextUnit, k.PrevUnit, k.NextFile, k.PrevFile, k.Filters, k.Expand, k.RawLabels}
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }
