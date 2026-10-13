package pipeview

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/model"
)

// callLogTitle names the row whose calls are shown ("TL;DR" for the
// item's own calls).
func (m *Model) callLogTitle() string {
	if m.callGroup == "" {
		return "TL;DR"
	}
	for _, rw := range m.rows {
		if rw.rev.GroupID == m.callGroup {
			return rw.nameText()
		}
	}
	return shortID(m.callGroup)
}

// openCallLog sizes the call-log viewport and fills it.
func (m *Model) openCallLog() {
	m.callLog = true
	m.vp = viewport.New(m.w, max(1, m.h-1))
	m.vp.SetContent(m.callLogContent())
}

// callLogContent renders every call: purpose, tier/model, tokens, cost,
// time, cached, then system, prompt, context blocks and response.
func (m *Model) callLogContent() string {
	var lines []string
	add := func(s ...string) { lines = append(lines, s...) }
	block := func(title, body string) {
		add(m.st.title.Render("  " + title))
		if strings.TrimSpace(body) == "" {
			add(m.st.dim.Render("    (empty)"))
			return
		}
		for _, l := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
			add(wrap("    "+l, m.w, 4)...)
		}
	}
	switch {
	case m.callErr != nil:
		add(m.st.warn.Render("call log unavailable: " + m.callErr.Error()))
	case len(m.calls) == 0:
		add(m.st.dim.Render("no calls recorded for this row"))
	}
	for i, c := range m.calls {
		if i > 0 || m.callErr != nil {
			add("")
		}
		add(m.st.head.Render(fmt.Sprintf("── %d · %s · %s · %s", i+1, c.Purpose, c.Tier, c.Model)) + m.st.dim.Render(" "+clock(c.At)))
		add("  " + callFacts(c))
		if c.Err != "" {
			add(m.st.warn.Render("  error: " + c.Err))
		}
		block("system", c.System)
		block("prompt", c.Prompt)
		for j, ctx := range c.Context {
			block(fmt.Sprintf("context %d/%d", j+1, len(c.Context)), ctx)
		}
		block("response", c.Response)
	}
	return strings.Join(lines, "\n")
}

// callFacts is "tokens 1840 in / 212 out · $0.0029 · 2.1 s · cached".
func callFacts(c model.ReviewCall) string {
	s := fmt.Sprintf("tokens %d in / %d out · %s · %s", c.InputTokens, c.OutputTokens, money(c.Cost), dur(c.Ms))
	if c.Cached {
		s += " · cached"
	}
	if c.Task != "" {
		s += " · task " + c.Task
	}
	return s
}

// callLogView is the full-size overlay: a title line and the viewport.
func (m *Model) callLogView() string {
	title := m.st.title.Render("Call log · "+m.callLogTitle()) +
		m.st.dim.Render(fmt.Sprintf(" · %s · j/k scroll · esc closes", plural(len(m.calls), "call", "calls")))
	return title + "\n" + m.vp.View()
}

// callLogKey drives the call log: scroll keys scroll it, Close closes it,
// anything else is passed to the viewport.
func (m *Model) callLogKey(k tea.KeyMsg) {
	km := m.keys
	switch {
	case key.Matches(k, km.Close):
		m.callLog, m.calls, m.callErr = false, nil, nil
	case key.Matches(k, km.Down):
		m.vp.ScrollDown(1)
	case key.Matches(k, km.Up):
		m.vp.ScrollUp(1)
	case key.Matches(k, km.Top):
		m.vp.GotoTop()
	case key.Matches(k, km.Bottom):
		m.vp.GotoBottom()
	default:
		m.vp, _ = m.vp.Update(k)
	}
}

// openPrompt opens the cap prompt, prefilled with twice the current cap.
func (m *Model) openPrompt() {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 12
	if c := capNow(m.d); c > 0 {
		ti.SetValue(strconv.FormatFloat(2*c, 'f', 2, 64))
	}
	ti.Focus()
	m.ti, m.prompt = ti, true
}

// promptKey drives the cap prompt: Enter validates and asks for
// ActRaiseCap, Close cancels, other keys edit the value.
func (m *Model) promptKey(k tea.KeyMsg) {
	switch {
	case key.Matches(k, m.keys.Close):
		m.prompt = false
	case key.Matches(k, m.keys.Enter):
		v, ok := parseCap(m.ti.Value())
		if !ok {
			m.note = "cap must be a number > 0"
			return
		}
		m.prompt = false
		m.pending = &Action{Kind: ActRaiseCap, ItemID: m.d.Item.ID, CapUSD: v}
	default:
		m.ti, _ = m.ti.Update(k)
	}
}

// parseCap reads a cap in dollars ("0.80", "$1"); ok only for a finite
// number > 0.
func parseCap(s string) (float64, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, false
	}
	return v, true
}

// promptLine renders the cap prompt with a block cursor at the input's
// position (drawn here, not by textinput, so it is colour-free).
func (m *Model) promptLine() string {
	val := []rune(m.ti.Value())
	pos := clampInt(m.ti.Position(), 0, len(val))
	in := string(val[:pos]) + "█" + string(val[pos:])
	line := m.st.title.Render("raise cost cap for this item: $") + in + m.st.dim.Render("  enter sets · esc cancels")
	if m.note != "" {
		line += m.st.note.Render(" · " + m.note)
	}
	return line
}
