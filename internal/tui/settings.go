package tui

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/settingsview"
)

// settingsPane is what the root model needs from the settings page (M5.1).
// The real page is *settingsview.Model; tests substitute a scripted
// stand-in so the root's handling of its actions is checked without
// driving the Huh form.
type settingsPane interface {
	SetSize(w, h int)
	// SetInfo loads values, paths and environment status (replacing unsaved
	// edits) and returns the form's init command.
	SetInfo(info proto.SettingsInfo) tea.Cmd
	SetError(err error)
	SetNote(s string)
	// Dirty reports unsaved edits (an EventSettings then does not reload).
	Dirty() bool
	Update(msg tea.Msg) tea.Cmd
	// Capturing is true once the page is loaded: it then takes every key.
	Capturing() bool
	// PendingAction returns (once) what the user asked for.
	PendingAction() (settingsview.Action, bool)
	HelpBindings() [][]key.Binding
	View() string
}

var _ settingsPane = (*settingsview.Model)(nil)

// Settings page messages: the answers of the daemon calls behind the
// page's actions and `:auto`, and the editor exit.
type (
	settingsLoadedMsg struct {
		info proto.SettingsInfo
		err  error
	}
	settingsSavedMsg struct {
		scope proto.SettingsScope
		info  proto.SettingsInfo
		err   error
	}
	settingsEnsuredMsg struct {
		kind settingsview.ActionKind
		info proto.SettingsInfo
		err  error
	}
	settingsEditedMsg struct {
		kind settingsview.ActionKind
		err  error
	}
	settingsRoutingMsg struct {
		info proto.RoutingInfo
		err  error
	}
	autoSavedMsg struct {
		strategy model.AutoReview
		repo     string // the workspace repo saved to (M5.3; "" single repo)
		info     proto.SettingsInfo
		err      error
	}
)

// questionsNote is shown after prt.questions.toml was edited (DECISIONS
// "M5.1 settings page": a catalogue change re-asks labels, so it waits for
// the next daemon start).
const questionsNote = "question changes apply after the daemon restarts"

// openSettings shows the settings page over the main pane (remembering the
// tab to return to) and loads it; in a workspace the page is the active
// repo's (M5.3).
func (m *Model) openSettings() tea.Cmd {
	if m.tab == TabSettings {
		return nil
	}
	m.settingsPrev, m.tab, m.focus = m.tab, TabSettings, FocusMain
	m.settingsRepo = m.activeRepo()
	m.settings.SetError(nil)
	m.settings.SetNote("")
	return m.loadSettings(proto.SettingsParams{Repo: m.settingsRepo})
}

// closeSettings returns to the tab the page was opened from.
func (m *Model) closeSettings() {
	m.tab = m.settingsPrev
	if m.tab == TabSettings {
		m.tab = TabOverview
	}
}

// loadSettings reads the settings (p.Ensure creates a file first).
func (m Model) loadSettings(p proto.SettingsParams) tea.Cmd {
	c := m.client
	if p.Ensure != "" {
		kind := settingsview.ActEditRouting
		if p.Ensure == "questions" {
			kind = settingsview.ActEditQuestions
		}
		return func() tea.Msg {
			info, err := c.Settings(context.Background(), p)
			return settingsEnsuredMsg{kind: kind, info: info, err: err}
		}
	}
	return func() tea.Msg {
		info, err := c.Settings(context.Background(), p)
		return settingsLoadedMsg{info: info, err: err}
	}
}

// settingsKey hands a key to the open page. Before the page is loaded (or
// after a failed load) it does not capture: the Settings key and Back
// close it, Quit quits.
func (m Model) settingsKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if !m.settings.Capturing() {
		switch {
		case key.Matches(k, m.keys.Settings), key.Matches(k, m.keys.Back):
			m.closeSettings()
			return m, nil
		case key.Matches(k, m.keys.Quit):
			return m.quit()
		}
	}
	return m.updateSettings(k)
}

// updateSettings hands msg to the open page and runs its pending action.
func (m Model) updateSettings(msg tea.Msg) (Model, tea.Cmd) {
	cmd := m.settings.Update(msg)
	return m, batch(cmd, m.settingsAction())
}

// settingsAction turns the page's pending action into daemon calls; nil
// when there is none.
func (m *Model) settingsAction() tea.Cmd {
	a, ok := m.settings.PendingAction()
	if !ok {
		return nil
	}
	switch a.Kind {
	case settingsview.ActSave:
		m.note = "saving settings…"
		c, p := m.client, proto.SaveSettingsParams{Scope: a.Scope, Values: a.Values, Repo: m.settingsRepo}
		return func() tea.Msg {
			info, err := c.SaveSettings(context.Background(), p)
			return settingsSavedMsg{scope: p.Scope, info: info, err: err}
		}
	case settingsview.ActCancel:
		m.closeSettings()
	case settingsview.ActEditRouting:
		return m.loadSettings(proto.SettingsParams{Ensure: "routing", Repo: m.settingsRepo})
	case settingsview.ActEditQuestions:
		return m.loadSettings(proto.SettingsParams{Ensure: "questions", Repo: m.settingsRepo})
	}
	return nil
}

// onSettingsEvent re-reads the strategy and, while the page is open with
// no unsaved edits, the page (the daemon reloaded its configuration). In a
// workspace (M5.3) it re-reads the event's repo's settings (every cached
// repo's after a global change) and the workspace (roles, statuses).
func (m Model) onSettingsEvent(e proto.Event) tea.Cmd {
	cmd := m.loadStrategy()
	if m.ws.Multi {
		cmd = batch(m.loadWorkspace(), m.reloadRepoSettings(e.Repo))
	}
	if m.tab == TabSettings && !m.settings.Dirty() {
		cmd = batch(cmd, m.loadSettings(proto.SettingsParams{Repo: m.settingsRepo}))
	}
	return cmd
}

// onSettingsMsg handles the answers of the settings page's daemon calls
// and `:auto`; ok is false for any other message.
func (m Model) onSettingsMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case settingsLoadedMsg:
		return m, m.applySettings(msg.info, msg.err), true
	case settingsSavedMsg:
		cmd := m.applySettings(msg.info, msg.err)
		if msg.err == nil {
			m.note = savedNote(msg.scope, msg.info)
			m.settings.SetNote(m.note)
		} else {
			m.note = "save failed: " + msg.err.Error()
		}
		return m, cmd, true
	case settingsEnsuredMsg:
		return m, m.editSettingsFile(msg), true
	case settingsEditedMsg:
		return m, m.afterSettingsEdit(msg), true
	case settingsRoutingMsg:
		m.settings.SetNote(rulesReloadedNote(rulesReloadedMsg(msg)))
		return m, m.reloadSettings(), true
	case autoSavedMsg:
		if msg.err != nil {
			m.note = "auto-review: " + msg.err.Error()
			return m, nil, true
		}
		m.takeSettings(msg.repo, msg.info.Values)
		where := settingsFile(proto.ScopeRepo, msg.info)
		if m.ws.Multi && msg.repo != "" {
			where = m.repoLabelOf(msg.repo)
		}
		m.note = "auto-review: " + string(msg.strategy) + " (saved to " + where + ")"
		return m, nil, true
	}
	return m, nil, false
}

// applySettings loads a Settings answer into the page (or shows its error)
// and records the strategy (in a workspace, the page's repo's).
func (m *Model) applySettings(info proto.SettingsInfo, err error) tea.Cmd {
	if err != nil {
		m.settings.SetError(err)
		return nil
	}
	m.settings.SetError(nil)
	m.takeSettings(m.settingsRepo, info.Values)
	return m.settings.SetInfo(info)
}

// reloadSettings re-reads the open page unless it holds unsaved edits.
func (m Model) reloadSettings() tea.Cmd {
	if m.tab != TabSettings || m.settings.Dirty() {
		return nil
	}
	return m.loadSettings(proto.SettingsParams{Repo: m.settingsRepo})
}

// editSettingsFile opens the ensured routing or questions file in the
// editor (the hook the Pipeline page's `e` uses).
func (m *Model) editSettingsFile(msg settingsEnsuredMsg) tea.Cmd {
	if msg.err != nil {
		m.settings.SetError(msg.err)
		return nil
	}
	path := msg.info.RoutingPath
	if msg.kind == settingsview.ActEditQuestions {
		path = msg.info.QuestionsPath
	}
	if path == "" {
		m.settings.SetError(errors.New("the daemon named no file to edit"))
		return nil
	}
	edit := m.opts.Edit
	if edit == nil {
		edit = execEditor
	}
	kind := msg.kind
	return edit(path, func(err error) tea.Msg { return settingsEditedMsg{kind: kind, err: err} })
}

// afterSettingsEdit reloads the routing rules after prt.routing.toml was
// edited (then the page), or notes that question changes wait for a
// restart.
func (m *Model) afterSettingsEdit(msg settingsEditedMsg) tea.Cmd {
	if msg.err != nil {
		m.settings.SetNote("editor: " + msg.err.Error())
	}
	if msg.kind == settingsview.ActEditQuestions {
		if msg.err == nil {
			m.settings.SetNote(questionsNote)
		}
		m.note = questionsNote
		return m.reloadSettings()
	}
	c, repo := m.client, m.settingsRepo
	return func() tea.Msg {
		info, err := c.Routing(context.Background(), proto.RoutingParams{Reload: true, Repo: repo})
		return settingsRoutingMsg{info: info, err: err}
	}
}

// autoCmd is `:auto <strategy>`: it reads the settings, sets the strategy
// and saves them to the repo prt.toml (the daemon applies it live); in a
// workspace to the active repo's (M5.3).
func (m *Model) autoCmd(s model.AutoReview) tea.Cmd {
	m.note = "auto-review: " + string(s) + "…"
	c, repo := m.client, m.activeRepo()
	return func() tea.Msg {
		ctx := context.Background()
		info, err := c.Settings(ctx, proto.SettingsParams{Repo: repo})
		if err != nil {
			return autoSavedMsg{strategy: s, repo: repo, err: err}
		}
		v := info.Values
		v.AutoReview = string(s)
		saved, err := c.SaveSettings(ctx, proto.SaveSettingsParams{Scope: proto.ScopeRepo, Values: v, Repo: repo})
		return autoSavedMsg{strategy: s, repo: repo, info: saved, err: err}
	}
}

// savedNote is "saved to <dir> · <what changed live>".
func savedNote(scope proto.SettingsScope, info proto.SettingsInfo) string {
	s := "saved to " + settingsDir(scope, info)
	if info.Applied != "" {
		s += " · " + info.Applied
	}
	return s
}

// settingsDir is the scope's directory ("" scope is repo); the saved
// source file when the daemon did not name it.
func settingsDir(scope proto.SettingsScope, info proto.SettingsInfo) string {
	dir := info.RepoDir
	if scope == proto.ScopeGlobal {
		dir = info.GlobalDir
	}
	if dir == "" {
		return orDash(info.Source)
	}
	return dir
}

// settingsFile is the scope's prt.toml.
func settingsFile(scope proto.SettingsScope, info proto.SettingsInfo) string {
	dir := info.RepoDir
	if scope == proto.ScopeGlobal {
		dir = info.GlobalDir
	}
	if dir == "" {
		return orDash(info.Source)
	}
	return filepath.Join(dir, "prt.toml")
}
