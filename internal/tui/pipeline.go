package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeview"
	"github.com/fredricw/prt/internal/proto"
)

// pipePane is what the root model needs from the Pipeline page. The real
// page is *pipeview.Model; tests substitute a recording fake, so key routing
// and action execution are checked without pipeview's rendering.
type pipePane interface {
	SetKeys(k pipeview.Keys)
	SetSize(w, h int)
	SetDetail(d proto.Detail)
	Clear()
	// SetNow sets the clock for a running review's elapsed seconds.
	SetNow(t time.Time)
	// SetCallLog answers an ActCallLog action.
	SetCallLog(groupID string, calls []model.ReviewCall, err error)
	// SetNote shows a one-shot note on the page.
	SetNote(s string)
	// HandleKey reports whether the key was consumed.
	HandleKey(k tea.KeyMsg) bool
	// Update handles non-key messages (mouse).
	Update(msg tea.Msg) tea.Cmd
	// Capturing is true while the page wants every key (cap prompt, call
	// log, stage log).
	Capturing() bool
	// PendingAction returns (once) the daemon action the last key asked for.
	PendingAction() (pipeview.Action, bool)
	// SelectedGroup is the routing table's selected group (M5: `:tier`).
	SelectedGroup() (string, bool)
	HelpBindings() [][]key.Binding
	ShortBindings() []key.Binding
	View() string
}

var _ pipePane = (*pipeview.Model)(nil)

// EditFunc opens path in an editor and reports back through done when the
// editor exits; the default runs $EDITOR (fallback vi) with tea.ExecProcess.
type EditFunc func(path string, done func(error) tea.Msg) tea.Cmd

// execEditor is the default EditFunc: $EDITOR (which may carry arguments,
// "code -w"), else vi, run in the foreground terminal.
func execEditor(path string, done func(error) tea.Msg) tea.Cmd {
	args := strings.Fields(os.Getenv("EDITOR"))
	if len(args) == 0 {
		args = []string{"vi"}
	}
	c := exec.CommandContext(context.Background(), args[0], append(args[1:], path)...)
	return tea.ExecProcess(c, func(err error) tea.Msg { return done(err) })
}

// Pipeline page messages: the answers of the daemon calls behind the page's
// actions, and the editor exit.
type (
	reviewActedMsg struct {
		action proto.ReviewAction
		err    error
	}
	rerunMsg struct {
		stage model.Stage
		err   error
	}
	rulesEnsuredMsg struct {
		info proto.RoutingInfo
		err  error
	}
	editorDoneMsg    struct{ err error }
	rulesReloadedMsg struct {
		info proto.RoutingInfo
		err  error
	}
	callLogMsg struct {
		groupID string
		calls   []model.ReviewCall
		err     error
	}
	capSetMsg struct {
		capUSD float64
		err    error
	}
)

// shownItemID is the item the main pane shows: the loaded detail's, else
// the picker selection ("" when none).
func (m Model) shownItemID() string {
	if m.detail != nil {
		return m.detail.Item.ID
	}
	if it, ok := m.picker.selected(); ok {
		return it.ID
	}
	return ""
}

// togglePipeline opens the Pipeline page for the selected item (remembering
// the tab to return to) or closes it.
func (m *Model) togglePipeline() {
	if m.tab == TabPipeline {
		m.closePipeline()
		return
	}
	if _, ok := m.picker.selected(); !ok {
		m.note = "no item selected"
		return
	}
	m.prevTab, m.tab, m.focus = m.tab, TabPipeline, FocusMain
	m.pipe.SetNow(m.now())
}

// closePipeline returns to the tab the page was opened from.
func (m *Model) closePipeline() {
	m.tab = m.prevTab
	if m.tab == TabPipeline {
		m.tab = TabOverview
	}
}

// pipeActionCmd turns the page's pending action into a daemon call; nil
// when there is none. It sets the in-progress note.
func (m *Model) pipeActionCmd() tea.Cmd {
	a, ok := m.pipe.PendingAction()
	if !ok || a.Kind == pipeview.ActNone {
		return nil
	}
	if a.ItemID == "" {
		a.ItemID = m.shownItemID()
	}
	if a.ItemID == "" {
		m.note = "no item selected"
		return nil
	}
	c := m.client
	switch a.Kind {
	case pipeview.ActForceDeep, pipeview.ActSkip:
		return m.reviewActionCmd(a)
	case pipeview.ActRerun:
		m.note = "re-running from " + orDash(string(a.Stage)) + "…"
		// Force (re-resolve the head, re-fetch) only for fetch or a whole
		// re-run: re-running a later stage re-runs it on the current
		// analysis, and FromStage already makes it and the later stages
		// skip their caches.
		force := a.Stage == "" || a.Stage == model.StageFetch
		return func() tea.Msg {
			err := c.Refresh(context.Background(), proto.ItemParams{ItemID: a.ItemID, FromStage: a.Stage, Force: force})
			return rerunMsg{stage: a.Stage, err: err}
		}
	case pipeview.ActEditRules:
		m.note = "opening routing rules…"
		return func() tea.Msg {
			info, err := c.Routing(context.Background(), proto.RoutingParams{Ensure: true})
			return rulesEnsuredMsg{info: info, err: err}
		}
	case pipeview.ActCallLog:
		return func() tea.Msg {
			calls, err := c.CallLog(context.Background(), proto.CallLogParams{ItemID: a.ItemID, GroupID: a.GroupID})
			return callLogMsg{groupID: a.GroupID, calls: calls, err: err}
		}
	case pipeview.ActRaiseCap:
		m.note = fmt.Sprintf("setting the cost cap to $%.2f…", a.CapUSD)
		return func() tea.Msg {
			err := c.SetCap(context.Background(), proto.CapParams{ItemID: a.ItemID, CapUSD: a.CapUSD})
			return capSetMsg{capUSD: a.CapUSD, err: err}
		}
	}
	return nil
}

// reviewActionCmd sends D (deep) or s (skip) for a group.
func (m *Model) reviewActionCmd(a pipeview.Action) tea.Cmd {
	if a.GroupID == "" {
		m.note = "no group selected"
		return nil
	}
	act := proto.ActionDeep
	m.note = "forcing deep…"
	if a.Kind == pipeview.ActSkip {
		act = proto.ActionSkip
		m.note = "skipping review…"
	}
	return sendReviewAction(m.client, proto.ReviewActionParams{ItemID: a.ItemID, GroupID: a.GroupID, Action: act})
}

// sendReviewAction returns the command calling client.ReviewAction.
func sendReviewAction(c daemon.Client, p proto.ReviewActionParams) tea.Cmd {
	return func() tea.Msg {
		return reviewActedMsg{action: p.Action, err: c.ReviewAction(context.Background(), p)}
	}
}

// onPipeMsg handles the answers of the Pipeline page's daemon calls; ok is
// false for any other message.
func (m Model) onPipeMsg(msg tea.Msg) (Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case reviewActedMsg:
		m.note = reviewActedNote(msg)
	case rerunMsg:
		if msg.err != nil {
			m.note = "re-run failed: " + msg.err.Error()
		}
	case rulesEnsuredMsg:
		if msg.err != nil {
			m.note = "routing rules: " + msg.err.Error()
			return m, nil, true
		}
		edit := m.opts.Edit
		if edit == nil {
			edit = execEditor
		}
		return m, edit(msg.info.Path, func(err error) tea.Msg { return editorDoneMsg{err: err} }), true
	case editorDoneMsg:
		if msg.err != nil {
			m.note = "editor: " + msg.err.Error()
		}
		c := m.client
		return m, func() tea.Msg {
			info, err := c.Routing(context.Background(), proto.RoutingParams{Reload: true})
			return rulesReloadedMsg{info: info, err: err}
		}, true
	case rulesReloadedMsg:
		m.note = rulesReloadedNote(msg)
		m.pipe.SetNote(m.note)
	case callLogMsg:
		m.pipe.SetCallLog(msg.groupID, msg.calls, msg.err)
		if msg.err != nil {
			m.note = "call log: " + msg.err.Error()
		}
	case capSetMsg:
		switch {
		case msg.err != nil:
			m.note = "cost cap: " + msg.err.Error()
		case msg.capUSD <= 0:
			m.note = "cost cap for this item: the routing file's default"
		default:
			m.note = fmt.Sprintf("cost cap for this item: $%.2f", msg.capUSD)
		}
	default:
		return m, nil, false
	}
	return m, nil, true
}

func reviewActedNote(msg reviewActedMsg) string {
	if msg.err != nil {
		return "review action failed: " + msg.err.Error()
	}
	switch msg.action {
	case proto.ActionSkip:
		return "review skipped; re-routing…"
	case proto.ActionReset:
		return "back to the routing rules; re-routing…"
	}
	return "re-reviewing on deep…"
}

func rulesReloadedNote(msg rulesReloadedMsg) string {
	switch {
	case msg.err != nil:
		return "rules reload failed: " + msg.err.Error()
	case msg.info.Err != "":
		return "rules reload failed: " + msg.info.Err
	}
	return fmt.Sprintf("rules reloaded: %s re-routed", plural(msg.info.Rerouted, "group"))
}
