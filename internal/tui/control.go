package tui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// Analysis control (M5.1, DECISIONS "M5.1 stop / resume"): ctrl+x stops the
// selected item's analysis, ctrl+k every analysis, ctrl+o resumes the
// selected item; `r` on a stopped or not-analysed item resumes or runs it;
// `:stop [all]` and `:resume [all]` do the same from the command line.

// Analysis control messages: the answers of Stop and Resume.
type (
	stoppedMsg struct {
		all   bool
		label string // the selected item's short label ("" with all)
		res   proto.StopResult
		err   error
	}
	resumedMsg struct {
		all   bool
		label string
		// verb is the note's verb on success ("resumed", "started").
		verb string
		res  proto.ResumeResult
		err  error
	}
	// strategyMsg answers the Settings read behind the Overview's
	// strategy line.
	strategyMsg struct {
		info proto.SettingsInfo
		err  error
	}
)

// resumable reports whether `r` on it resumes or runs it instead of the
// spec's forced refresh: it was stopped by the user, or only the free
// stages ran (outside the auto_review strategy).
func resumable(it model.Item) bool {
	return it.State == model.StateStopped || it.State == model.StateNotAnalysed
}

// controlKey runs Stop, StopAll or Resume for the key k.
func (m *Model) controlKey(k tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(k, m.keys.StopAll):
		return m.stopCmd(true)
	case key.Matches(k, m.keys.Stop):
		return m.stopCmd(false)
	}
	return m.resumeCmd(false)
}

// stopCmd stops the selected item's analysis, or every analysis with all.
func (m *Model) stopCmd(all bool) tea.Cmd {
	p := proto.StopParams{All: all}
	label := ""
	if !all {
		it, ok := m.picker.selected()
		if !ok {
			m.note = "no item selected"
			return nil
		}
		p.ItemID, label = it.ID, m.itemName(it)
		m.note = "stopping " + label + "…"
	} else {
		m.note = "stopping every analysis…"
	}
	c := m.client
	return func() tea.Msg {
		res, err := c.Stop(context.Background(), p)
		return stoppedMsg{all: all, label: label, res: res, err: err}
	}
}

// resumeCmd resumes the selected item, or every stopped item with all.
func (m *Model) resumeCmd(all bool) tea.Cmd {
	if all {
		m.note = "resuming every stopped item…"
		return m.sendResume(proto.ResumeParams{All: true}, "", "resumed")
	}
	it, ok := m.picker.selected()
	if !ok {
		m.note = "no item selected"
		return nil
	}
	m.note = "resuming " + m.itemName(it) + "…"
	return m.sendResume(proto.ResumeParams{ItemID: it.ID}, m.itemName(it), "resumed")
}

// resumeSelected is `r` on a stopped item ("resuming #482…") or a
// not-analysed one ("running feat/x…"): an explicit, non-forced run.
func (m *Model) resumeSelected(it model.Item) tea.Cmd {
	label := m.itemName(it)
	if it.State == model.StateNotAnalysed {
		m.note = "running " + label + "…"
		return m.sendResume(proto.ResumeParams{ItemID: it.ID}, label, "started")
	}
	m.note = "resuming " + label + "…"
	return m.sendResume(proto.ResumeParams{ItemID: it.ID}, label, "resumed")
}

// sendResume returns the command calling client.Resume.
func (m Model) sendResume(p proto.ResumeParams, label, verb string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		res, err := c.Resume(context.Background(), p)
		return resumedMsg{all: p.All, label: label, verb: verb, res: res, err: err}
	}
}

// loadStrategy reads the daemon's settings for the auto_review strategy.
func (m Model) loadStrategy() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		info, err := c.Settings(context.Background(), proto.SettingsParams{})
		return strategyMsg{info: info, err: err}
	}
}

// onControlMsg handles the answers of Stop, Resume and the strategy read;
// ok is false for any other message.
func (m Model) onControlMsg(msg tea.Msg) (Model, bool) {
	switch msg := msg.(type) {
	case stoppedMsg:
		m.note = m.stoppedNote(msg)
	case resumedMsg:
		m.note = resumedNote(msg)
	case strategyMsg:
		// A daemon without settings support (or an error) leaves the
		// strategy unknown: the Overview then omits its line.
		if msg.err == nil {
			m.setLiveSettings(msg.info.Values)
		}
	default:
		return m, false
	}
	return m, true
}

// setLiveSettings records what the TUI shows from the daemon's live
// settings: the strategy and the header's role (a role saved on the
// settings page, here or in another TUI, shows without a restart).
func (m *Model) setLiveSettings(v proto.SettingsValues) {
	m.opts.Role = v.Role
	m.setStrategy(v.AutoReview)
}

// setStrategy records the strategy and redraws the Overviews naming it.
func (m *Model) setStrategy(s string) {
	if s == m.strategy {
		return
	}
	m.strategy = s
	m.renderOverview()
	m.renderPreview()
}

// stoppedNote is "stopped #482 at jev", "stopped 3 analyses", "nothing to
// stop" or the error.
func (m Model) stoppedNote(msg stoppedMsg) string {
	n := len(msg.res.Stopped)
	switch {
	case msg.err != nil:
		return "stop failed: " + msg.err.Error()
	case n == 0:
		return "nothing to stop"
	case msg.all:
		if n == 1 {
			return "stopped 1 analysis"
		}
		return fmt.Sprintf("stopped %d analyses", n)
	}
	if stage := m.stageOf(msg.res.Stopped[0]); stage != "" {
		return "stopped " + msg.label + " at " + stage
	}
	return "stopped " + msg.label
}

// stageOf is the stage an item is at (or stopped at): its StateNote while
// analysing or stopped, else the latest stage event; "" when unknown.
func (m Model) stageOf(id string) string {
	if i := m.picker.indexOf(id); i >= 0 {
		it := m.picker.items[i]
		if (it.State == model.StateStopped || it.State == model.StateAnalysing) && it.StateNote != "" {
			return it.StateNote
		}
	}
	return string(m.stages[id])
}

// resumedNote is "resumed #482", "resumed 3 items", "nothing to resume" or
// the error.
func resumedNote(msg resumedMsg) string {
	n := len(msg.res.Resumed)
	switch {
	case msg.err != nil:
		return "resume failed: " + msg.err.Error()
	case n == 0:
		return "nothing to resume"
	case msg.all:
		return "resumed " + plural(n, "item")
	}
	return msg.verb + " " + msg.label
}

// strategyWords describes an auto_review strategy for the Overview ("runs
// PRs that need you"); "" for an unknown one.
func strategyWords(s string) string {
	switch model.AutoReview(s) {
	case model.AutoManual:
		return "runs nothing until you ask"
	case model.AutoNeedsYou:
		return "runs PRs that need you"
	case model.AutoNeedsYouYours:
		return "runs PRs that need you and your own"
	case model.AutoAll:
		return "runs every item"
	}
	return ""
}

// ovOpts is what the Overview needs besides the item: the strategy and the
// keys its not-analysed / stopped box names, and the focused block's title
// (M5.2).
type ovOpts struct {
	strategy                       string
	runKey, resumeKey, settingsKey string
	focus                          string
}

// ovOpts returns the Overview options from the model (no focused block)
// for the shown item.
func (m Model) ovOpts() ovOpts { return m.ovOptsFor(m.shownRepo()) }

// ovOptsFor returns the Overview options for an item of repo: in a
// workspace the strategy is that repo's (M5.3).
func (m Model) ovOptsFor(repo string) ovOpts {
	return ovOpts{strategy: m.strategyFor(repo), runKey: m.keys.Refresh.Help().Key, resumeKey: m.keys.Resume.Help().Key,
		settingsKey: m.keys.Settings.Help().Key}
}

// strategyLine is "auto-review: needs-you — runs PRs that need you; change
// with , or :auto"; "" when the strategy is unknown.
func (o ovOpts) strategyLine() string {
	if o.strategy == "" {
		return ""
	}
	s := "auto-review: " + o.strategy
	if w := strategyWords(o.strategy); w != "" {
		s += " — " + w
	}
	change := ":auto"
	if o.settingsKey != "" {
		change = o.settingsKey + " or :auto"
	}
	return s + "; change with " + change
}
