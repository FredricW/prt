package tui

import (
	"context"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/cmdline"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// cmdPane is what the root model needs from the `:` command line. The real
// one is *cmdline.Model; tests substitute a scripted stand-in.
type cmdPane interface {
	SetWidth(w int)
	Open() tea.Cmd
	Close()
	// Active is true while the line is open: it then takes every key.
	Active() bool
	Update(msg tea.Msg) tea.Cmd
	PendingCommand() (cmdline.Submitted, bool)
	View() string
	// SetRepos gives `:repo` the workspace's repo keys and labels to
	// complete (M5.3).
	SetRepos(names []string)
}

var _ cmdPane = (*cmdline.Model)(nil)

// tierDoneMsg answers a `:tier` over several groups.
type tierDoneMsg struct {
	tier string
	n    int
	errs []error
}

// errTierFast is the refusal of `:tier fast` (DECISIONS "Branch states,
// search and command line").
var errTierFast = errors.New("fast is chosen by the rules; use :tier rules")

// updateCmdline hands msg to the open command line and runs a submitted
// command.
func (m Model) updateCmdline(msg tea.Msg) (Model, tea.Cmd) {
	cmd := m.cmd.Update(msg)
	sub, ok := m.cmd.PendingCommand()
	if !ok {
		return m, cmd
	}
	m.cmd.Close()
	next, run := m.runCommand(sub)
	return next, batch(cmd, run)
}

// runCommand executes one submitted command line (M5.1 adds :auto, :stop
// [all], :resume [all] and :settings; M5.3 :repo, and an unqualified :add
// takes the repo filter).
func (m Model) runCommand(sub cmdline.Submitted) (Model, tea.Cmd) {
	if sub.Err != nil {
		m.note = sub.Err.Error()
		return m, nil
	}
	c := sub.Cmd
	switch c.Kind {
	case cmdline.Add:
		return m, m.addCmd(m.qualifyAdd(c.Arg))
	case cmdline.Repo:
		return m.repoCommand(c.Arg)
	case cmdline.Tier:
		return m, m.tierCmd(c.Tier)
	case cmdline.Cap:
		return m, m.capCmd(c)
	case cmdline.Quit:
		return m.quit()
	case cmdline.Auto:
		return m, m.autoCmd(c.Strategy)
	case cmdline.Stop:
		return m, m.stopCmd(c.All)
	case cmdline.Resume:
		return m, m.resumeCmd(c.All)
	case cmdline.Settings:
		return m, m.openSettings()
	}
	m.note = "unknown command :" + sub.Line
	return m, nil
}

// tierAction maps a :tier argument to its review action.
func tierAction(tier string) (proto.ReviewAction, error) {
	switch tier {
	case "deep":
		return proto.ActionDeep, nil
	case "none":
		return proto.ActionSkip, nil
	case "rules":
		return proto.ActionReset, nil
	case "fast":
		return "", errTierFast
	}
	return "", fmt.Errorf("unknown tier %q — try :tier deep, none or rules", tier)
}

// inState reports whether a routing row already has the tier :tier asks
// for, so acting on every group skips it.
func inState(r model.Review, a proto.ReviewAction) bool {
	switch a {
	case proto.ActionDeep:
		return r.Tier == model.TierDeep
	case proto.ActionSkip:
		return r.Tier == model.TierNone
	}
	return !r.Forced && !r.UserSkipped
}

// tierCmd runs :tier on the focused group (Code page by change, Pipeline
// row), else on every routed group of the shown item not already there.
func (m *Model) tierCmd(tier string) tea.Cmd {
	act, err := tierAction(tier)
	if err != nil {
		m.note = err.Error()
		return nil
	}
	id := m.shownItemID()
	if id == "" {
		m.note = "no item selected"
		return nil
	}
	if gid, ok := m.focusedGroup(); ok {
		m.note = "tier " + tier + ": 1 group…"
		return sendReviewAction(m.client, proto.ReviewActionParams{ItemID: id, GroupID: gid, Action: act})
	}
	if m.detail == nil || m.detail.Item.ID != id {
		m.note = "loading — try again in a moment"
		return nil
	}
	var gids []string
	for _, r := range m.detail.Reviews {
		if !inState(r, act) {
			gids = append(gids, r.GroupID)
		}
	}
	m.note = fmt.Sprintf("tier %s: %s", tier, plural(len(gids), "group"))
	if len(gids) == 0 {
		return nil
	}
	c := m.client
	return func() tea.Msg {
		var errs []error
		for _, g := range gids {
			if err := c.ReviewAction(context.Background(), proto.ReviewActionParams{ItemID: id, GroupID: g, Action: act}); err != nil {
				errs = append(errs, err)
			}
		}
		return tierDoneMsg{tier: tier, n: len(gids), errs: errs}
	}
}

// capCmd runs :cap on the shown item (0 restores the routing file's cap).
func (m *Model) capCmd(c cmdline.Command) tea.Cmd {
	id := m.shownItemID()
	if id == "" {
		m.note = "no item selected"
		return nil
	}
	usd := max(0, c.CapUSD)
	cl := m.client
	return func() tea.Msg {
		err := cl.SetCap(context.Background(), proto.CapParams{ItemID: id, CapUSD: usd})
		return capSetMsg{capUSD: usd, err: err}
	}
}

// onTierDone reports a :tier over several groups.
func (m Model) onTierDone(msg tierDoneMsg) Model {
	if len(msg.errs) > 0 {
		m.note = fmt.Sprintf("tier %s: %d of %d failed: %v", msg.tier, len(msg.errs), msg.n, msg.errs[0])
		return m
	}
	m.note = fmt.Sprintf("tier %s: %s re-routing…", msg.tier, plural(msg.n, "group"))
	return m
}

// quit quits like q; an open composer's text is saved as a draft first
// (spec: drafts are saved).
func (m Model) quit() (Model, tea.Cmd) {
	if m.code.SaveOpenComposer() {
		if cmd := m.sendComment(); cmd != nil {
			return m, tea.Sequence(cmd, tea.Quit)
		}
	}
	return m, tea.Quit
}
