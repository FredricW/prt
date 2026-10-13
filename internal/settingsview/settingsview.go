// Package settingsview is the settings page component (M5.1): a Huh form
// over proto.SettingsValues (auto-review strategy, role and owned globs,
// cost cap and deep slots, fast/deep models, embedder, urgency weights,
// thresholds), a save-scope choice (repo, the default, or global), the
// environment presence lines (JEV_API_KEY, PRT_ANTHROPIC_API_KEY, Ollama,
// GitHub: never values) and actions to open prt.routing.toml and
// prt.questions.toml in $EDITOR. The root model (internal/tui) hosts it,
// feeds it SettingsInfo and executes its PendingAction through
// daemon.Client.
//
// The form has one section per page (Analysis, You, Cost, Models, Urgency
// weights, Thresholds, Environment); tab/enter move field by field,
// pgdown/pgup jump a section, and the last field chooses the action.
// Huh's own messages travel through tea.Cmds, so the root must hand this
// page every message (not only keys) while it is open.
//
// The API below was fixed by the M5.1 lead (DECISIONS "M5.1 shared
// contracts"); the settingsview work package implements it without
// changing these signatures.
package settingsview

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/theme"
)

// ActionKind is what the user asked the page to do.
type ActionKind int

// Actions.
const (
	ActNone ActionKind = iota
	// ActSave: save Values to Scope (the root calls SaveSettings and hands
	// the answer back with SetInfo, or the error with SetError).
	ActSave
	// ActCancel: leave the page without saving (esc on an unchanged form,
	// or after confirming that edits are discarded).
	ActCancel
	// ActEditRouting / ActEditQuestions: open the file in $EDITOR (the root
	// calls Settings with Ensure "routing"/"questions", then opens
	// RoutingPath/QuestionsPath; after the editor exits it reloads routing
	// for ActEditRouting).
	ActEditRouting
	ActEditQuestions
)

// Action is one pending request (see PendingAction).
type Action struct {
	Kind   ActionKind
	Scope  proto.SettingsScope  // ActSave
	Values proto.SettingsValues // ActSave
}

// Model is the page. The zero value is not usable; call New.
type Model struct {
	r       *lipgloss.Renderer
	w, h    int
	info    proto.SettingsInfo
	loaded  bool
	note    string
	err     error
	pending *Action

	th   theme.Theme
	keys pageKeys
	// base is the loaded values as field text; fv the form's live copy
	// (the Huh fields write into it).
	base formValues
	fv   *formValues
	bf   builtForm
	// armed: esc was pressed once on a dirty form (the next esc discards).
	armed bool
	// waiting is the status shown after the form completed (save or edit)
	// until the root answers with SetInfo or SetError.
	waiting string
}

// pageKeys are the keys the page handles around the form.
type pageKeys struct {
	nextSection, prevSection, cancel key.Binding
}

func defaultPageKeys() pageKeys {
	return pageKeys{
		nextSection: key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "next section")),
		prevSection: key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "previous section")),
		cancel:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
}

// New builds an empty page (it shows "loading settings…" until SetInfo).
// r may be nil.
func New(r *lipgloss.Renderer) Model {
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	return Model{r: r, w: 80, h: 24, th: theme.New(r), keys: defaultPageKeys()}
}

// SetSize sets the page size.
func (m *Model) SetSize(w, h int) {
	m.w, m.h = max(1, w), max(1, h)
	m.resize()
}

// SetInfo loads values, paths and environment status into the form,
// replacing unsaved edits (called when the page opens, after a save, and
// on proto.EventSettings while the form is unchanged). It returns the
// form's init command.
func (m *Model) SetInfo(info proto.SettingsInfo) tea.Cmd {
	m.info, m.loaded, m.err = info, true, nil
	m.base = fromValues(info.Values)
	return m.reset()
}

// SetError shows a failed load or save on the page (nil clears it). After
// a failed save the form comes back with the user's edits.
func (m *Model) SetError(err error) {
	m.err = err
	if err != nil && m.waiting != "" && m.fv != nil {
		m.rebuild()
	}
}

// SetNote shows a one-line status ("saved to .git/prt/prt.toml · re-aggregated 3 items").
func (m *Model) SetNote(s string) {
	m.note = s
	m.resize()
}

// Dirty reports whether the form holds unsaved edits.
func (m Model) Dirty() bool { return m.fv != nil && !sameEdits(*m.fv, m.base) }

// Update handles messages while the page is open; every key goes to the
// form (the page always captures keys, see Capturing). esc cancels; on a
// dirty form the first esc asks for a second one (DECISIONS "M5.1
// settings page").
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	if m.bf.form == nil {
		return nil
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		if cmd, done := m.onKey(k); done {
			return cmd
		}
	}
	if m.waiting != "" {
		return nil // the form completed; the root answers with SetInfo
	}
	_, cmd := m.bf.form.Update(msg)
	after := m.afterForm()
	m.resize()
	return tea.Batch(cmd, after)
}

// onKey handles the page's own keys; done is false when the key goes on
// to the form.
func (m *Model) onKey(k tea.KeyMsg) (tea.Cmd, bool) {
	esc := key.Matches(k, m.keys.cancel)
	armed := m.armed
	m.armed = false
	switch {
	case esc && m.waiting != "":
		m.pending = &Action{Kind: ActCancel}
		return m.reset(), true
	case esc && m.Dirty() && !armed:
		m.armed = true
		return nil, true
	case m.waiting != "":
		return nil, true
	case key.Matches(k, m.keys.nextSection):
		return m.jump(1), true
	case key.Matches(k, m.keys.prevSection):
		return m.jump(-1), true
	}
	return nil, false // esc on a clean (or confirmed) form: Huh aborts
}

// jump moves to the next (d 1) or previous (d -1) section after checking
// the focused field, like tab would.
func (m *Model) jump(d int) tea.Cmd {
	to := m.section() + d
	if to < 0 || to >= len(sectionNames()) {
		return nil
	}
	m.bf.form.GetFocusedField().Blur() // runs the field's validation
	if len(m.bf.form.Errors()) > 0 {
		m.resize()
		return nil
	}
	var cmd tea.Cmd
	if d > 0 {
		cmd = m.bf.form.NextGroup()
	} else {
		cmd = m.bf.form.PrevGroup()
	}
	m.resize()
	return cmd
}

// afterForm turns a finished form into the pending action.
func (m *Model) afterForm() tea.Cmd {
	switch m.bf.form.State {
	case huh.StateAborted:
		m.pending = &Action{Kind: ActCancel}
		return m.reset()
	case huh.StateCompleted:
		return m.complete()
	}
	return nil
}

// complete records the chosen action. Save and the edit actions keep the
// page open until the root's next SetInfo (or SetError); Cancel resets.
func (m *Model) complete() tea.Cmd {
	a := Action{Kind: m.fv.Action}
	switch a.Kind {
	case ActSave:
		if err := validateAll(m.fv); err != nil {
			m.err = err
			return m.rebuild()
		}
		a.Scope = m.fv.Scope
		a.Values = apply(m.info.Values, m.base, *m.fv)
		m.waiting = "saving…"
	case ActEditRouting:
		m.waiting = "opening " + m.info.RoutingPath + " in $EDITOR…"
	case ActEditQuestions:
		m.waiting = "opening " + m.info.QuestionsPath + " in $EDITOR…"
	default:
		m.pending = &Action{Kind: ActCancel}
		return m.reset()
	}
	m.pending = &a
	return nil
}

// reset drops the edits and rebuilds the form from the loaded values.
func (m *Model) reset() tea.Cmd {
	fv := m.base.clone()
	m.fv = &fv
	return m.rebuild()
}

// rebuild builds a fresh form over the current field values (edits kept),
// focused on its first field.
func (m *Model) rebuild() tea.Cmd {
	m.fv.Action = ActSave
	m.bf = buildForm(m.info, m.fv, m.th)
	m.armed, m.waiting = false, ""
	m.resize()
	return m.bf.form.Init()
}

// inner is the width inside the page's box.
func (m Model) inner() int { return max(1, m.w-4) }

// formHeight is the height left for the form under the status lines and
// above the blank line and the hint line.
func (m Model) formHeight() int { return max(3, m.h-2-len(m.statusLines())-2) }

// resize fits the form to the page.
func (m *Model) resize() {
	if m.bf.form == nil {
		return
	}
	m.bf.form.WithWidth(max(1, m.inner()-1)).WithHeight(m.formHeight())
}

// section is the index of the focused field's section.
func (m Model) section() int {
	if m.bf.form == nil {
		return 0
	}
	f := m.bf.form.GetFocusedField()
	if f == nil {
		return 0
	}
	return m.bf.section[f.GetKey()]
}

// Capturing reports whether the page takes every key (true once loaded:
// the root's single-key bindings must not fire while typing).
func (m Model) Capturing() bool { return m.loaded }

// PendingAction returns and clears the user's last request.
func (m *Model) PendingAction() (Action, bool) {
	if m.pending == nil {
		return Action{}, false
	}
	a := *m.pending
	m.pending = nil
	return a, true
}

// navBindings are the page's navigation keys (help overlay and hint line).
func (m Model) navBindings() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab/shift+tab", "fields")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "next")),
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑/↓", "choose")),
		m.keys.nextSection, m.keys.prevSection, m.keys.cancel,
	}
}

// hints is the short key line under the form.
func hints() []theme.Hint {
	return []theme.Hint{
		{Key: "tab/shift+tab", Desc: "fields"},
		{Key: "enter", Desc: "next"},
		{Key: "esc", Desc: "cancel"},
		{Key: "pgdn/pgup", Desc: "section"},
	}
}

// HelpBindings are the page's keys for the `?` overlay: navigation, then
// the focused field's own keys.
func (m Model) HelpBindings() [][]key.Binding {
	rows := [][]key.Binding{m.navBindings()}
	if m.bf.form != nil && m.waiting == "" {
		if fk := m.bf.form.KeyBinds(); len(fk) > 0 {
			rows = append(rows, fk)
		}
	}
	return rows
}

// statusLines are the lines above the form: the error (red), a field's
// validation error, the discard prompt, the waiting or note line, config
// warnings and where the values came from (dim).
func (m Model) statusLines() []string {
	var out []string
	oneLine := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if m.err != nil {
		out = append(out, m.th.Del.Render("✗ "+oneLine(m.err.Error())))
	}
	if m.bf.form != nil && m.waiting == "" {
		for _, e := range m.bf.form.Errors() {
			out = append(out, m.th.Del.Render("✗ "+oneLine(e.Error())))
		}
	}
	if m.armed {
		out = append(out, m.th.Accent.Render("unsaved edits: esc again to discard them, any other key keeps editing"))
	}
	if m.waiting != "" {
		out = append(out, m.th.Dim.Render(m.waiting))
	} else if m.note != "" {
		out = append(out, m.th.Dim.Render(oneLine(m.note)))
	}
	for _, w := range m.info.Warnings {
		out = append(out, m.th.Dim.Render("! "+oneLine(w)))
	}
	if m.info.Source != "" {
		src := "values from " + m.info.Source
		if m.info.RoutingSource != "" {
			src += " · routing from " + m.info.RoutingSource
		}
		out = append(out, m.th.Dim.Render(src))
	}
	return out
}

// View renders the page at its size.
func (m Model) View() string {
	opt := theme.BoxOpt{Height: m.h, Focused: true}
	if !m.loaded || m.bf.form == nil {
		return m.th.Box("Settings", m.th.Dim.Render("loading settings…"), m.w, opt)
	}
	names := sectionNames()
	sec := m.section()
	opt.Right = fmt.Sprintf("%s · %d/%d", names[sec], sec+1, len(names))
	lines := m.statusLines()
	form := ""
	if m.waiting == "" {
		form = m.bf.form.View()
	}
	lines = append(lines, fitLines(form, m.formHeight())...)
	lines = append(lines, "", m.th.Hints(hints(), m.inner()))
	return m.th.Box("Settings", strings.Join(lines, "\n"), m.w, opt)
}

// fitLines splits s into exactly n lines (cut or padded with blanks).
func fitLines(s string, n int) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	for len(lines) < n {
		lines = append(lines, "")
	}
	return lines
}
