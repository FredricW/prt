// Package cmdline is the `:` command line (spec Keybindings: `:add #482`,
// `:add feat/x`, `:tier deep`, `:cap 1.00`, `:q`; M5.1 adds `:auto
// <strategy>`, `:stop [all]`, `:resume [all]` and `:settings`): a parser,
// tab completion of command names and arguments (Complete), the command
// list for the `?` overlay (Help) and a one-line input component the root
// model shows in the status bar. The root executes the parsed Command
// through daemon.Client.
//
// Every command is described once, in the table behind commands(): Parse,
// Complete, Help and the "unknown command" hint all read it, so the lists
// never drift apart.
//
// The API below was fixed by the M5 and M5.1 leads (DECISIONS "M5 shared
// contracts", "M5.1 shared contracts"); the work packages implement it
// without changing these signatures.
package cmdline

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
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
	// Auto sets the auto-review strategy live and saves it to the repo
	// prt.toml: Strategy is one of model.AutoReviews (M5.1, `:auto
	// needs-you`).
	Auto
	// Stop stops the selected item's analysis, or every analysis with All
	// (M5.1, `:stop`, `:stop all`).
	Stop
	// Resume resumes the selected item, or every stopped item with All
	// (M5.1, `:resume`, `:resume all`).
	Resume
	// Settings opens the settings page (M5.1, `:settings`).
	Settings
)

// Command is one parsed command line.
type Command struct {
	Kind     Kind
	Arg      string           // Add: the query
	Tier     string           // Tier: deep | none | rules
	CapUSD   float64          // Cap
	Strategy model.AutoReview // Auto (M5.1)
	All      bool             // Stop, Resume: every item (M5.1)
}

// HelpEntry is one command for the `?` overlay and the README (M5.1).
type HelpEntry struct {
	Usage string // ":stop [all]"
	Desc  string // "stop the selected item's analysis (all: every item)"
}

// cmdSpec describes one command: its name and aliases, the arguments tab
// completes, and its `?` overlay entry.
type cmdSpec struct {
	kind    Kind
	name    string
	aliases []string
	args    []string // completion candidates for the argument, in order
	usage   string
	desc    string
}

// commands is the command table in display order (Help, completion of an
// empty line). It is rebuilt on each call so no package state can be
// mutated.
func commands() []cmdSpec {
	auto := make([]string, 0, len(model.AutoReviews))
	for _, a := range model.AutoReviews {
		auto = append(auto, string(a))
	}
	return []cmdSpec{
		{kind: Add, name: "add", usage: ":add #482 | :add feat/x",
			desc: "add a PR or branch and analyse it now"},
		{kind: Auto, name: "auto", args: auto, usage: ":auto " + strings.Join(auto, "|"),
			desc: "set the auto-review strategy (saved to .git/prt/prt.toml)"},
		{kind: Tier, name: "tier", args: []string{"deep", "none", "rules"}, usage: ":tier deep|none|rules",
			desc: "set the review tier of the focused group (or every group)"},
		{kind: Cap, name: "cap", args: []string{"default"}, usage: ":cap 1.00|default",
			desc: "set the selected item's review cost cap"},
		{kind: Stop, name: "stop", args: []string{"all"}, usage: ":stop [all]",
			desc: "stop the selected item's analysis (all: every item)"},
		{kind: Resume, name: "resume", args: []string{"all"}, usage: ":resume [all]",
			desc: "resume the selected item from its last finished stage (all: every stopped item)"},
		{kind: Settings, name: "settings", usage: ":settings",
			desc: "open the settings page (also ,)"},
		{kind: Quit, name: "q", aliases: []string{"quit", "q!"}, usage: ":q",
			desc: "quit (drafts are saved)"},
	}
}

// lookup finds a command by name or alias.
func lookup(name string) (cmdSpec, bool) {
	for _, c := range commands() {
		if c.name == name || slices.Contains(c.aliases, name) {
			return c, true
		}
	}
	return cmdSpec{}, false
}

// tryList is the hint after a bad command: every name, alphabetical, with
// :q last (":add, :auto, :cap, :resume, :settings, :stop, :tier, :q").
func tryList() string {
	var names []string
	for _, c := range commands() {
		if c.kind != Quit {
			names = append(names, ":"+c.name)
		}
	}
	slices.Sort(names)
	return strings.Join(append(names, ":q"), ", ")
}

// Help lists every command in display order (M5.1) for the `?` overlay
// and the README.
func Help() []HelpEntry {
	cs := commands()
	out := make([]HelpEntry, 0, len(cs))
	for _, c := range cs {
		out = append(out, HelpEntry{Usage: c.usage, Desc: c.desc})
	}
	return out
}

// ErrNotImplemented was returned by the stub Parse before the work package
// landed; Parse no longer returns it. Kept for API compatibility.
var ErrNotImplemented = errors.New("cmdline: not implemented yet (M5)")

// Parse errors whose text the status bar shows.
var (
	errAddUsage    = errors.New(":add needs a PR or ref, e.g. :add #482 or :add feat/x")
	errTierUsage   = errors.New(":tier takes deep, none or rules")
	errTierFast    = errors.New("fast is chosen by the rules; use :tier rules")
	errCapUsage    = errors.New(":cap takes an amount in dollars > 0 (e.g. :cap 1.00) or default")
	errAutoUsage   = errors.New(":auto takes manual, needs-you, needs-you+yours or all")
	errStopUsage   = errors.New(":stop takes no argument or all")
	errResumeUsage = errors.New(":resume takes no argument or all")
	errEmpty       = errors.New("empty command — try " + tryList())
)

// Parse parses one line without its leading ':' (a leading ':' is
// tolerated). Unknown commands and bad arguments return a one-line error
// whose text is shown in the status bar ("unknown command :foo — try :add,
// :auto, :cap, :resume, :settings, :stop, :tier, :q").
func Parse(line string) (Command, error) {
	s := strings.TrimSpace(line)
	s = strings.TrimSpace(strings.TrimPrefix(s, ":"))
	name, rest, _ := strings.Cut(s, " ")
	rest = strings.TrimSpace(rest)
	if name == "" {
		return Command{}, errEmpty
	}
	c, ok := lookup(name)
	if !ok {
		return Command{}, fmt.Errorf("unknown command :%s — try %s", name, tryList())
	}
	switch c.kind {
	case Add:
		if rest == "" {
			return Command{}, errAddUsage
		}
		return Command{Kind: Add, Arg: rest}, nil
	case Auto:
		return parseAuto(rest)
	case Tier:
		return parseTier(rest)
	case Cap:
		return parseCap(rest)
	case Stop:
		return parseAll(Stop, rest, errStopUsage)
	case Resume:
		return parseAll(Resume, rest, errResumeUsage)
	}
	// Settings and Quit take no argument.
	if rest != "" {
		return Command{}, fmt.Errorf(":%s takes no argument", name)
	}
	return Command{Kind: c.kind}, nil
}

// parseAuto reads ":auto <strategy>" (case-insensitive). An empty argument
// is an error, not the default: the user meant to choose.
func parseAuto(arg string) (Command, error) {
	if arg == "" {
		return Command{}, errAutoUsage
	}
	a, err := model.ParseAutoReview(arg)
	if err != nil {
		return Command{}, errAutoUsage
	}
	return Command{Kind: Auto, Strategy: a}, nil
}

// parseAll reads ":stop" / ":stop all" (and resume): no argument or "all".
func parseAll(k Kind, arg string, usage error) (Command, error) {
	switch strings.ToLower(arg) {
	case "":
		return Command{Kind: k}, nil
	case "all":
		return Command{Kind: k, All: true}, nil
	}
	return Command{}, usage
}

// Complete completes line (what follows the ':'; a leading ':' is kept)
// for the tab key and returns the completed line and the candidates. In
// the first word it completes command names: a unique match gives the name
// and a trailing space, several give their longest common prefix. After a
// command it completes that command's argument (:auto strategies, :tier
// deep|none|rules, :stop and :resume all, :cap default). No match returns
// line unchanged and no candidates; a single candidate is returned too, so
// callers can tell "done" from "nothing".
func Complete(line string) (string, []string) {
	base, cands := completion(line)
	switch len(cands) {
	case 0:
		return line, nil
	case 1:
		if isArgBase(base) {
			return base + cands[0], cands
		}
		return base + cands[0] + " ", cands
	}
	return base + commonPrefix(cands), cands
}

// completion returns the fixed part of line before the word being
// completed (with any ':' kept and spacing normalised) and the candidates
// for that word.
func completion(line string) (string, []string) {
	lead := ""
	s := strings.TrimLeft(line, " ")
	if strings.HasPrefix(s, ":") {
		lead, s = ":", s[1:]
	}
	name, arg, hasArg := strings.Cut(strings.TrimLeft(s, " "), " ")
	if !hasArg {
		var out []string
		for _, c := range commands() {
			if strings.HasPrefix(c.name, strings.ToLower(name)) {
				out = append(out, c.name)
			}
		}
		return lead, out
	}
	c, ok := lookup(name)
	if !ok {
		return lead, nil
	}
	arg = strings.ToLower(strings.TrimSpace(arg))
	var out []string
	for _, a := range c.args {
		if strings.HasPrefix(a, arg) {
			out = append(out, a)
		}
	}
	return lead + name + " ", out
}

// isArgBase reports whether base (from completion) ends before an argument
// rather than before the command name.
func isArgBase(base string) bool { return strings.HasSuffix(base, " ") }

// commonPrefix is the longest common prefix of ss (byte-wise; the
// candidates are ASCII).
func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		n := 0
		for n < len(p) && n < len(s) && p[n] == s[n] {
			n++
		}
		p = p[:n]
	}
	return p
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
	comp    cycle
}

// cycle is the tab-completion state while several candidates match: the
// fixed part of the line, the candidates, the one shown (-1: only the
// common prefix so far) and the line as completion last left it (any other
// edit ends the cycle).
type cycle struct {
	base  string
	cands []string
	idx   int
	line  string
}

// active reports whether the candidates are being shown/cycled for line.
func (c cycle) active(line string) bool { return len(c.cands) > 1 && c.line == line }

// keyMap holds the bindings the open prompt handles itself.
type keyMap struct {
	submit, close, prev, next, complete, back key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		submit:   key.NewBinding(key.WithKeys("enter")),
		close:    key.NewBinding(key.WithKeys("esc")),
		prev:     key.NewBinding(key.WithKeys("up", "ctrl+p")),
		next:     key.NewBinding(key.WithKeys("down", "ctrl+n")),
		complete: key.NewBinding(key.WithKeys("tab")),
		back:     key.NewBinding(key.WithKeys("shift+tab")),
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
	m.comp = cycle{}
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
// closes, up/down walk this session's history, tab completes (see
// Complete; repeated tab cycles through several candidates, shift+tab
// backwards). The root routes every key here while Active, tab included.
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
	case key.Matches(k, m.keys.complete):
		m.tab(1)
	case key.Matches(k, m.keys.back):
		m.tab(-1)
	default:
		var cmd tea.Cmd
		m.in, cmd = m.in.Update(k)
		return cmd
	}
	return nil
}

// tab completes the line (d 1: tab, -1: shift+tab). While several
// candidates match, further presses cycle through them; otherwise it
// applies Complete and starts a cycle when the result is ambiguous.
func (m *Model) tab(d int) {
	line := m.in.Value()
	if m.comp.active(line) {
		n := len(m.comp.cands)
		switch {
		case m.comp.idx < 0 && d < 0:
			m.comp.idx = n - 1
		case m.comp.idx < 0:
			m.comp.idx = 0
		default:
			m.comp.idx = (m.comp.idx + d + n) % n
		}
		m.setLine(m.comp.base + m.comp.cands[m.comp.idx])
		m.comp.line = m.in.Value()
		return
	}
	if d < 0 {
		return // shift+tab only walks an open cycle
	}
	done, cands := Complete(line)
	m.setLine(done)
	m.comp = cycle{}
	if len(cands) > 1 {
		base, _ := completion(line)
		m.comp = cycle{base: base, cands: cands, idx: -1, line: m.in.Value()}
	}
}

// setLine replaces the input's text and puts the cursor at its end.
func (m *Model) setLine(s string) {
	m.in.SetValue(s)
	m.in.CursorEnd()
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
	line := theme.Truncate(m.th.Accent.Render(":")+before+cursor+after, m.w)
	return m.withCandidates(line)
}

// withCandidates sets the ambiguous completion candidates ("add · auto")
// in dim text at the right end of the prompt line, cut to the room left
// (dropped when fewer than four cells remain).
func (m Model) withCandidates(line string) string {
	if !m.comp.active(m.in.Value()) {
		return line
	}
	room := m.w - ansi.StringWidth(line) - 2
	if room < 4 {
		return line
	}
	right := theme.Truncate(strings.Join(m.comp.cands, " · "), room)
	gap := m.w - ansi.StringWidth(line) - ansi.StringWidth(right)
	return line + strings.Repeat(" ", gap) + m.th.Dim.Render(right)
}
