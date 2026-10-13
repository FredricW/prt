// Package cmdline is the `:` command line (spec Keybindings: `:add #482`,
// `:add feat/x`, `:tier deep`, `:cap 1.00`, `:q`): a parser and a one-line
// input component the root model shows in the status bar. The root executes
// the parsed Command through daemon.Client.
//
// The API below was fixed by the M5 lead (DECISIONS "M5 shared
// contracts"); the search work package implements it without changing
// these signatures.
package cmdline

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/theme"
)

// Kind is a command.
type Kind int

// Commands.
const (
	// Add adds a PR ("#482", "482") or a ref ("feat/x") like enter on a
	// search result: Arg is the query.
	Add Kind = iota + 1
	// Tier sets the review tier of the focused group (Code page by change,
	// Pipeline page row) or, with no group focused, of every routed group
	// of the selected item: Tier is "deep", "none" or "rules" (back to the
	// routing rules); see DECISIONS "M5 shared contracts".
	Tier
	// Cap sets the selected item's review cost cap: CapUSD (> 0; 0 with
	// Arg "default" restores prt.routing.toml's cost_cap_usd).
	Cap
	// Quit quits (drafts are saved).
	Quit
)

// Command is one parsed command line.
type Command struct {
	Kind   Kind
	Arg    string  // Add: the query
	Tier   string  // Tier: deep | none | rules
	CapUSD float64 // Cap
}

// ErrNotImplemented was returned by the stub Parse before the work package
// landed; Parse no longer returns it. Kept for API compatibility.
var ErrNotImplemented = errors.New("cmdline: not implemented yet (M5)")

// Parse errors whose text the status bar shows.
var (
	errAddUsage  = errors.New(":add needs a PR or ref, e.g. :add #482 or :add feat/x")
	errTierUsage = errors.New(":tier takes deep, none or rules")
	errTierFast  = errors.New("fast is chosen by the rules; use :tier rules")
	errCapUsage  = errors.New(":cap takes an amount in dollars > 0 (e.g. :cap 1.00) or default")
	errEmpty     = errors.New("empty command — try :add, :tier, :cap, :q")
)

// Parse parses one line without its leading ':' (a leading ':' is
// tolerated). Unknown commands and bad arguments return an error whose text
// is shown in the status bar ("unknown command :foo — try :add, :tier,
// :cap, :q").
func Parse(line string) (Command, error) {
	s := strings.TrimSpace(line)
	s = strings.TrimSpace(strings.TrimPrefix(s, ":"))
	name, rest, _ := strings.Cut(s, " ")
	rest = strings.TrimSpace(rest)
	switch name {
	case "":
		return Command{}, errEmpty
	case "add":
		if rest == "" {
			return Command{}, errAddUsage
		}
		return Command{Kind: Add, Arg: rest}, nil
	case "tier":
		return parseTier(rest)
	case "cap":
		return parseCap(rest)
	case "q", "quit", "q!":
		if rest != "" {
			return Command{}, fmt.Errorf(":%s takes no argument", name)
		}
		return Command{Kind: Quit}, nil
	}
	return Command{}, fmt.Errorf("unknown command :%s — try :add, :tier, :cap, :q", name)
}

// parseTier reads ":tier deep|none|rules" (fast is refused: the rules
// choose it).
func parseTier(arg string) (Command, error) {
	switch t := strings.ToLower(arg); t {
	case "deep", "none", "rules":
		return Command{Kind: Tier, Tier: t}, nil
	case "fast":
		return Command{}, errTierFast
	}
	return Command{}, errTierUsage
}

// parseCap reads ":cap 1.00", ":cap $1" or ":cap default".
func parseCap(arg string) (Command, error) {
	if strings.EqualFold(arg, "default") {
		return Command{Kind: Cap, Arg: "default"}, nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(arg, "$")), 64)
	if err != nil || v <= 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return Command{}, errCapUsage
	}
	return Command{Kind: Cap, CapUSD: v}, nil
}

// maxHistory is how many submitted lines the session history keeps.
const maxHistory = 50

// Model is the input component (shown in place of the status line while
// open). The zero value is not usable; call New.
type Model struct {
	r       *lipgloss.Renderer
	th      theme.Theme
	w       int
	open    bool
	in      textinput.Model
	keys    keyMap
	history []string // oldest first, at most maxHistory
	// hpos is the history entry shown (len(history) = the line being typed);
	// draft keeps that line while walking the history.
	hpos    int
	draft   string
	pending *Submitted
}

// keyMap holds the bindings the open prompt handles itself.
type keyMap struct {
	submit, close, prev, next key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		submit: key.NewBinding(key.WithKeys("enter")),
		close:  key.NewBinding(key.WithKeys("esc")),
		prev:   key.NewBinding(key.WithKeys("up", "ctrl+p")),
		next:   key.NewBinding(key.WithKeys("down", "ctrl+n")),
	}
}

// New builds a closed command line. r may be nil.
func New(r *lipgloss.Renderer) Model {
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 256
	return Model{r: r, th: theme.New(r), w: 80, in: in, keys: defaultKeys()}
}

// SetWidth sets the line width.
func (m *Model) SetWidth(w int) { m.w = max(1, w) }

// Open shows an empty ":" prompt and returns the input's blink command.
func (m *Model) Open() tea.Cmd {
	m.open = true
	m.in.SetValue("")
	m.hpos, m.draft = len(m.history), ""
	return m.in.Focus()
}

// Close hides the prompt.
func (m *Model) Close() {
	m.open = false
	m.in.Blur()
}

// Active reports whether the prompt is open (it then takes every key).
func (m Model) Active() bool { return m.open }

// Update handles keys while open: enter submits (see PendingCommand), esc
// closes, up/down walk this session's history.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	if !m.open {
		return nil
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		m.in, cmd = m.in.Update(msg) // cursor blink
		return cmd
	}
	switch {
	case key.Matches(k, m.keys.submit):
		m.submit()
	case key.Matches(k, m.keys.close):
		m.Close()
	case key.Matches(k, m.keys.prev):
		m.walk(-1)
	case key.Matches(k, m.keys.next):
		m.walk(1)
	default:
		var cmd tea.Cmd
		m.in, cmd = m.in.Update(k)
		return cmd
	}
	return nil
}

// submit parses the line, records it in the history and closes. An empty
// line just closes.
func (m *Model) submit() {
	line := strings.TrimSpace(m.in.Value())
	m.Close()
	if line == "" {
		return
	}
	m.remember(line)
	cmd, err := Parse(line)
	m.pending = &Submitted{Line: line, Cmd: cmd, Err: err}
}

// remember appends line to the history (no consecutive duplicates).
func (m *Model) remember(line string) {
	if n := len(m.history); n > 0 && m.history[n-1] == line {
		return
	}
	m.history = append(m.history, line)
	if len(m.history) > maxHistory {
		m.history = m.history[len(m.history)-maxHistory:]
	}
}

// walk moves through the history by d (-1 older, +1 newer); past the
// newest entry the line being typed comes back.
func (m *Model) walk(d int) {
	pos := m.hpos + d
	if pos < 0 || pos > len(m.history) {
		return
	}
	if m.hpos == len(m.history) {
		m.draft = m.in.Value()
	}
	m.hpos = pos
	if pos == len(m.history) {
		m.in.SetValue(m.draft)
	} else {
		m.in.SetValue(m.history[pos])
	}
	m.in.CursorEnd()
}

// Submitted is one submitted line: the parsed command, or the parse error
// to show (Err set, Cmd zero).
type Submitted struct {
	Line string
	Cmd  Command
	Err  error
}

// PendingCommand returns and clears the submitted line.
func (m *Model) PendingCommand() (Submitted, bool) {
	if m.pending == nil {
		return Submitted{}, false
	}
	s := *m.pending
	m.pending = nil
	return s, true
}

// View renders the one-line prompt (":" + input), "" when closed. The
// cursor is drawn as a block (reverse video in colour, "█" without), and
// the line is cut to the width keeping the cursor's end visible.
func (m Model) View() string {
	if !m.open {
		return ""
	}
	val := []rune(m.in.Value())
	pos := min(max(m.in.Position(), 0), len(val))
	cursor := "█"
	if m.th.Colour {
		under := " "
		if pos < len(val) {
			under = string(val[pos])
		}
		cursor = m.r.NewStyle().Reverse(true).Render(under)
	}
	after := ""
	if pos < len(val) {
		after = string(val[pos:])
		if m.th.Colour {
			after = string(val[pos+1:])
		}
	}
	before := string(val[:pos])
	// Keep the cursor on screen: drop the start of a long line.
	room := m.w - 1 - 1 // prompt and cursor
	if w := ansi.StringWidth(before); w > room && room > 1 {
		before = "…" + ansi.TruncateLeft(before, w-room+1, "")
	}
	line := m.th.Accent.Render(":") + before + cursor + after
	return theme.Truncate(line, m.w)
}
