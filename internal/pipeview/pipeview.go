// Package pipeview is the Pipeline page component (spec "Pipeline page"):
// stage strip, routing table, totals, rules panel, grouping panel and
// calibration panel for one item, rendered from proto.Detail only (the page
// never does maths: totals come from ItemAgg.Review / ItemAgg.Calibration,
// the grouping panel from ItemAgg.Grouping, rows from Detail.Reviews,
// stages from Detail.RunLogs, rules from Detail.Routing).
//
// Like codeview, it is a component the root model (internal/tui) owns: the
// root routes keys to HandleKey, and actions that need the daemon (D, s, r,
// e, c, $) come back through PendingAction for the root to execute.
//
// The look follows docs/design/v3/Pipeline.dc.html through internal/theme:
// a "prt · #N title · pipeline" header with the run state on the right,
// stage boxes joined by "→", the routing table in a titled box with the
// totals under it, and the Rules, Grouping, Calibration and Urgency boxes
// in a right column from 120 columns (stacked below the totals on
// narrower pages).
//
// The selection lives in the stage strip or in the routing table
// (NextPanel/PrevPanel switch); the page is one document that scrolls so
// the selection stays visible. A stage log (enter on a stage), the call log
// (SetCallLog) and the cap prompt ($) take every key while open (Capturing).
package pipeview

import (
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// Keys are the Pipeline page bindings (rebindable via the tui KeyMap).
type Keys struct {
	Down, Up, Top, Bottom key.Binding
	// NextPanel/PrevPanel move the selection between the stage strip and
	// the routing table (default tab is taken by the root; use ]/[ or J/K).
	NextPanel, PrevPanel key.Binding
	// Enter opens the selected stage's log below the strip.
	Enter key.Binding
	// ForceDeep (D), Skip (s), Rerun (r: selected stage and everything
	// after it, ignoring cache), EditRules (e), CallLog (c), RaiseCap ($).
	ForceDeep, Skip, Rerun, EditRules, CallLog, RaiseCap key.Binding
	// Close closes the call log, a stage log or the cap prompt (esc).
	Close key.Binding
}

// DefaultKeys returns the spec's bindings: j/k, home/G, J/K, enter,
// D s r e c $, esc.
func DefaultKeys() Keys {
	b := func(desc string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(keys[0], desc))
	}
	return Keys{
		Down: b("down", "j", "down"), Up: b("up", "k", "up"), Top: b("top", "home"), Bottom: b("bottom", "G", "end"),
		NextPanel: b("next panel", "J", "]"), PrevPanel: b("previous panel", "K", "["),
		Enter:     b("stage log", "enter"),
		ForceDeep: b("force deep", "D"), Skip: b("skip review", "s"), Rerun: b("re-run stage", "r"),
		EditRules: b("edit rules", "e"), CallLog: b("call log", "c"), RaiseCap: b("raise cost cap", "$"),
		Close: b("close", "esc"),
	}
}

// ActionKind is a daemon action the page asks the root model to run.
type ActionKind int

// Actions.
const (
	ActNone      ActionKind = iota
	ActForceDeep            // client.ReviewAction{Action: ActionDeep}
	ActSkip                 // client.ReviewAction{Action: ActionSkip}
	ActRerun                // client.Refresh{FromStage: Stage, Force: true}
	ActEditRules            // client.Routing{Ensure}, $EDITOR on Path, then client.Routing{Reload}
	ActCallLog              // client.CallLog, then SetCallLog
	ActRaiseCap             // client.SetCap{CapUSD}
)

// Action is one pending daemon action.
type Action struct {
	Kind    ActionKind
	ItemID  string
	GroupID string      // ForceDeep, Skip, CallLog ("" for the TL;DR calls)
	Stage   model.Stage // Rerun
	CapUSD  float64     // RaiseCap
}

// panel is where the selection lives.
type panel int

const (
	panelStrip panel = iota
	panelTable
)

// Model is the component. The zero value is not usable; call New.
type Model struct {
	keys Keys
	r    *lipgloss.Renderer
	st   styles
	w, h int
	// ox, oy are where the root model draws the component (SetOrigin), so
	// mouse coordinates map onto the page.
	ox, oy int

	d    proto.Detail
	rows []row
	run  runView
	now  time.Time

	panel panel
	stage int // selected strip stage (index into stripStages)
	row   int // selected routing row
	off   int // top document line

	stageLog bool
	logOff   int

	callLog   bool
	callGroup string
	calls     []model.ReviewCall
	callErr   error
	vp        viewport.Model

	prompt bool
	ti     textinput.Model

	note    string
	pending *Action
}

// New builds an empty page. r may be nil (default renderer).
func New(r *lipgloss.Renderer) Model {
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	return Model{keys: DefaultKeys(), r: r, st: newStyles(r), w: 80, h: 20, run: currentRun(nil)}
}

// SetKeys replaces the bindings.
func (m *Model) SetKeys(k Keys) { m.keys = k }

// SetSize sets the component's width and height in cells.
func (m *Model) SetSize(w, h int) {
	m.w, m.h = max(1, w), max(1, h)
	if m.callLog {
		y := m.vp.YOffset
		m.openCallLog()
		m.vp.SetYOffset(y)
	}
	m.clampOff()
}

// SetOrigin tells the component where its top-left cell is on screen, so
// mouse events (absolute coordinates) map onto the page.
func (m *Model) SetOrigin(x, y int) { m.ox, m.oy = x, y }

// SetDetail shows an item. A reload of the same item (same Item.ID) keeps
// the panel, the selected stage, the selected routing row (by group id),
// the scroll position and any open log; another item resets the page.
func (m *Model) SetDetail(d proto.Detail) {
	same := d.Item.ID != "" && d.Item.ID == m.d.Item.ID
	prevGroup, hadGroup := m.SelectedGroup()
	m.d, m.rows, m.run = d, buildRows(d), currentRun(d.RunLogs)
	if !same {
		m.reset()
		return
	}
	m.row = clampInt(m.row, 0, max(0, len(m.rows)-1))
	if hadGroup {
		for i, rw := range m.rows {
			if rw.rev.GroupID == prevGroup {
				m.row = i
				break
			}
		}
	}
	if m.panel == panelTable && len(m.rows) == 0 {
		m.panel = panelStrip
	}
	if m.callLog {
		y := m.vp.YOffset
		m.openCallLog()
		m.vp.SetYOffset(y)
	}
	m.clampOff()
}

// reset forgets selection, scroll, logs, prompt and note; the selection
// starts in the routing table when it has rows.
func (m *Model) reset() {
	m.panel, m.stage, m.row, m.off = panelStrip, 0, 0, 0
	if len(m.rows) > 0 {
		m.panel = panelTable
	}
	m.stageLog, m.logOff = false, 0
	m.callLog, m.callGroup, m.calls, m.callErr = false, "", nil, nil
	m.prompt, m.note, m.pending = false, "", nil
}

// Clear empties the page (no item selected).
func (m *Model) Clear() {
	m.d, m.rows, m.run = proto.Detail{}, nil, currentRun(nil)
	m.reset()
}

// SetNow sets the clock used for a running review's elapsed seconds (the
// root calls it on its spinner tick while anything is running).
func (m *Model) SetNow(t time.Time) { m.now = t }

// elapsed is the whole seconds since t ("12s"); "" without a clock.
func (m *Model) elapsed(t time.Time) string {
	if m.now.IsZero() || t.IsZero() || m.now.Before(t) {
		return ""
	}
	return strconv.Itoa(int(m.now.Sub(t).Seconds())) + "s"
}

// SetCallLog shows the raw call log of a routing-table row (answer to
// ActCallLog); err is shown instead when the call failed. groupID "" shows
// the item's TL;DR calls.
func (m *Model) SetCallLog(groupID string, calls []model.ReviewCall, err error) {
	if m.d.Item.ID == "" {
		return
	}
	m.callGroup, m.calls, m.callErr = groupID, calls, err
	m.prompt = false
	m.openCallLog()
}

// SetNote shows a one-shot note (e.g. "rules reloaded: 3 groups re-routed"
// or a daemon error); cleared on the next key.
func (m *Model) SetNote(s string) { m.note = s }

// Capturing is true while the page wants every key (cap prompt, call log
// or stage log open), so the root does not treat q, 1, 2 … as global.
func (m *Model) Capturing() bool { return m.prompt || m.callLog || m.stageLog }

// PendingAction returns (once) the action the last key asked for.
func (m *Model) PendingAction() (Action, bool) {
	if m.pending == nil {
		return Action{}, false
	}
	a := *m.pending
	m.pending = nil
	return a, true
}

// SelectedGroup returns the group id of the selected routing-table row.
func (m *Model) SelectedGroup() (string, bool) {
	if m.row < 0 || m.row >= len(m.rows) {
		return "", false
	}
	return m.rows[m.row].rev.GroupID, true
}

// HelpBindings lists the page's bindings for the ? overlay.
func (m *Model) HelpBindings() [][]key.Binding {
	k := m.keys
	return [][]key.Binding{{k.Down, k.Up, k.Top, k.Bottom, k.NextPanel, k.PrevPanel, k.Enter, k.Close}, {k.ForceDeep, k.Skip, k.Rerun, k.EditRules, k.CallLog, k.RaiseCap}}
}

// ShortBindings lists the bindings for the status bar.
func (m *Model) ShortBindings() []key.Binding {
	k := m.keys
	return []key.Binding{k.ForceDeep, k.Skip, k.Rerun, k.EditRules, k.CallLog, k.RaiseCap}
}

// bodyHeight is the number of document lines shown: the height minus the
// bar line (prompt or note) when there is one.
func (m *Model) bodyHeight() int {
	if m.prompt || m.note != "" {
		return max(1, m.h-1)
	}
	return m.h
}

// View renders the page at the size set by SetSize: exactly h lines, none
// wider than w cells.
func (m *Model) View() string {
	if m.d.Item.ID == "" {
		return m.frame([]string{m.st.dim.Render("no item selected")})
	}
	if m.callLog {
		return m.frame(strings.Split(m.callLogView(), "\n"))
	}
	lines := m.build().lines
	bh := m.bodyHeight()
	off := clampInt(m.off, 0, max(0, len(lines)-bh))
	out := append([]string(nil), lines[off:min(len(lines), off+bh)]...)
	for len(out) < bh {
		out = append(out, "")
	}
	switch {
	case m.prompt:
		out = append(out, m.promptLine())
	case m.note != "":
		out = append(out, m.st.note.Render(m.note))
	}
	return m.frame(out)
}

// frame pads or cuts lines to exactly h and truncates each to w cells.
func (m *Model) frame(lines []string) string {
	if len(lines) > m.h {
		lines = lines[:m.h]
	}
	for len(lines) < m.h {
		lines = append(lines, "")
	}
	for i, l := range lines {
		l = strings.TrimRight(l, " ")
		if ansi.StringWidth(l) > m.w {
			l = ansi.Truncate(l, m.w, ellipsis)
		}
		lines[i] = l
	}
	return strings.Join(lines, "\n")
}

// clampInt bounds v to [lo, hi].
func clampInt(v, lo, hi int) int {
	return max(lo, min(v, hi))
}
