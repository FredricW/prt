// Package searchview is the picker's search-and-add prompt (spec Picker,
// "Search and add (/)"): a text input at the bottom of the sidebar whose
// results stream in from three sources in parallel (local refs, open PRs,
// closed and merged PRs) and merge into one list. Each result shows kind,
// number or ref, title, author and age (in a workspace, M5.3, also its
// repo key); enter adds it, tab previews its overview without adding, esc
// closes. Results are deduplicated by ItemID, which a workspace prefixes
// with the repo key, so two repos' #12 stay two results.
//
// The component never talks to the daemon itself: the root sets a Searcher
// (daemon.Client.Search) and reads the user's choice with PendingChoice.
// The API below was fixed by the M5 lead (DECISIONS "M5 shared
// contracts"); the search work package implements it without changing
// these signatures.
//
// Message flow: every query change bumps a sequence number. Local refs are
// asked at once; open PRs after a 250 ms debounce (an internal tick
// message); closed PRs when the query starts with "!" (with the debounce)
// or when the open answer for the same query has no hits. Answers arrive
// as ResultMsg; answers to an older query are dropped. The root must route
// every message it does not handle itself (ResultMsg and the internal tick
// message) to Update while the prompt is open.
package searchview

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/theme"
)

// Debounce is how long the GitHub sources wait after the last keystroke
// (DECISIONS "Branch states, search and command line": 250 ms).
const Debounce = 250 * time.Millisecond

// Searcher runs one source (daemon.Client.Search).
type Searcher func(ctx context.Context, p proto.SearchParams) (proto.SearchResult, error)

// ResultMsg carries one source's answer back to Update. Seq identifies the
// query it answers; answers to an older query are dropped.
type ResultMsg struct {
	Seq    int
	Result proto.SearchResult
	Err    error
}

// debounceMsg fires Debounce after the query with sequence seq was typed;
// a stale one (the query changed since) does nothing.
type debounceMsg struct{ seq int }

// ChoiceKind is what the user did with a result.
type ChoiceKind int

// Choices.
const (
	ChoiceAdd     ChoiceKind = iota + 1 // enter: add to the Added section and analyse
	ChoicePreview                       // tab: show its overview without adding
)

// Choice is the user's pick (PendingChoice).
type Choice struct {
	Kind ChoiceKind
	Hit  proto.SearchHit
}

// Keys are the bindings the open prompt handles.
type Keys struct {
	Up, Down, Add, Preview, Close key.Binding
}

// DefaultKeys returns the spec's bindings (enter adds, tab previews, esc
// closes; up/down and ctrl+p/ctrl+n move, since j/k type).
func DefaultKeys() Keys {
	b := func(desc string, keys ...string) key.Binding {
		return key.NewBinding(key.WithKeys(keys...), key.WithHelp(keys[0], desc))
	}
	return Keys{
		Up: b("previous result", "up", "ctrl+p"), Down: b("next result", "down", "ctrl+n"),
		Add: b("add", "enter"), Preview: b("preview", "tab"), Close: b("close", "esc"),
	}
}

// sources in merge order.
var sources = [...]proto.SearchSource{proto.SearchLocal, proto.SearchOpen, proto.SearchClosed}

// srcState is one source's answer to the current query.
type srcState struct {
	asked, done bool
	hits        []proto.SearchHit
	note        string
}

// Model is the component. The zero value is not usable; call New.
type Model struct {
	keys   Keys
	r      *lipgloss.Renderer
	th     theme.Theme
	search Searcher
	now    func() time.Time
	// tick schedules the debounce (tea.Tick; tests replace it).
	tick   func(time.Duration, func(time.Time) tea.Msg) tea.Cmd
	w, h   int
	ox, oy int // screen position of the top-left cell (mouse)
	open   bool

	in         textinput.Model
	query      string
	seq        int
	debouncing bool // the open (and "!" closed) query waits for its tick
	src        [len(sources)]srcState
	hits       []proto.SearchHit // merged, deduplicated
	sel        int
	top        int // first result row shown
	lastClick  int // hit index of the last left click (-1 none)
	pending    *Choice
}

// New builds a closed prompt. r may be nil (default renderer).
func New(r *lipgloss.Renderer) Model {
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	in := textinput.New()
	in.Prompt = ""
	in.CharLimit = 256
	return Model{keys: DefaultKeys(), r: r, th: theme.New(r), now: time.Now, tick: tea.Tick, w: 30, h: 10, in: in, lastClick: -1}
}

// SetKeys replaces the bindings.
func (m *Model) SetKeys(k Keys) { m.keys = k }

// SetSearcher sets the function each source query runs through.
func (m *Model) SetSearcher(s Searcher) { m.search = s }

// SetNow sets the clock for result ages (tests and snapshots).
func (m *Model) SetNow(now func() time.Time) { m.now = now }

// SetSize sets the width and the rows available (prompt plus results).
func (m *Model) SetSize(w, h int) { m.w, m.h = max(1, w), max(1, h) }

// SetOrigin sets the screen cell of the view's top-left corner, so mouse
// clicks (absolute coordinates) map onto result rows.
func (m *Model) SetOrigin(x, y int) { m.ox, m.oy = x, y }

// Open focuses an empty prompt and returns the input's blink command,
// batched with the local query for the empty prompt (the most recently
// committed branches).
func (m *Model) Open() tea.Cmd {
	m.reset()
	m.open = true
	return tea.Batch(m.in.Focus(), m.queryChanged())
}

// Close hides the prompt and forgets the query and results.
func (m *Model) Close() {
	m.reset()
	m.open = false
	m.in.Blur()
}

// reset forgets the query and results; the sequence number moves on so
// answers still in flight are dropped.
func (m *Model) reset() {
	m.in.SetValue("")
	m.query = ""
	m.seq++
	m.debouncing = false
	m.src = [len(sources)]srcState{}
	m.hits, m.sel, m.top, m.lastClick = nil, 0, 0, -1
}

// Active reports whether the prompt is open (it then takes every key).
func (m Model) Active() bool { return m.open }

// Query returns the text typed so far.
func (m Model) Query() string { return m.in.Value() }

// Hits returns the merged results in display order.
func (m Model) Hits() []proto.SearchHit { return m.hits }

// Selected returns the selected result, if any.
func (m Model) Selected() (proto.SearchHit, bool) {
	if m.sel < 0 || m.sel >= len(m.hits) {
		return proto.SearchHit{}, false
	}
	return m.hits[m.sel], true
}

// Update handles key messages while open and ResultMsg; the returned
// command runs the source queries for a changed query.
func (m *Model) Update(msg tea.Msg) tea.Cmd {
	if !m.open {
		return nil
	}
	switch msg := msg.(type) {
	case ResultMsg:
		return m.onResult(msg)
	case debounceMsg:
		return m.onDebounce(msg)
	case tea.KeyMsg:
		return m.onKey(msg)
	case tea.MouseMsg:
		m.onMouse(msg)
		return nil
	}
	var cmd tea.Cmd
	m.in, cmd = m.in.Update(msg) // cursor blink
	return cmd
}

// onKey moves, chooses, closes, or edits the query.
func (m *Model) onKey(k tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(k, m.keys.Close):
		m.Close()
	case key.Matches(k, m.keys.Up):
		m.move(-1)
	case key.Matches(k, m.keys.Down):
		m.move(1)
	case key.Matches(k, m.keys.Add):
		m.choose(ChoiceAdd)
	case key.Matches(k, m.keys.Preview):
		m.choose(ChoicePreview)
	default:
		var cmd tea.Cmd
		m.in, cmd = m.in.Update(k)
		if m.in.Value() != m.query {
			return tea.Batch(cmd, m.queryChanged())
		}
		return cmd
	}
	return nil
}

// choose records the selected result as the pending choice.
func (m *Model) choose(k ChoiceKind) {
	if h, ok := m.Selected(); ok {
		m.pending = &Choice{Kind: k, Hit: h}
	}
}

// move moves the selection by d within the results.
func (m *Model) move(d int) {
	if len(m.hits) == 0 {
		return
	}
	m.sel = min(max(m.sel+d, 0), len(m.hits)-1)
	m.fit() // scroll to keep the selection visible
}

// onMouse: a left click selects a result row and a second click on the
// same row adds it; the wheel moves the selection.
func (m *Model) onMouse(msg tea.MouseMsg) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.move(-1)
		return
	case tea.MouseButtonWheelDown:
		m.move(1)
		return
	case tea.MouseButtonLeft:
	default:
		return
	}
	if msg.Action != tea.MouseActionPress {
		return
	}
	i, ok := m.hitAt(msg.X-m.ox, msg.Y-m.oy)
	if !ok {
		return
	}
	if i == m.lastClick && i == m.sel {
		m.choose(ChoiceAdd)
		return
	}
	m.sel, m.lastClick = i, i
}

// hitAt maps a view cell to a result index.
func (m Model) hitAt(x, y int) (int, bool) {
	if x < 0 || x >= m.w || y < 0 {
		return 0, false
	}
	l := m.layout()
	if y < l.pad || y >= l.pad+len(l.rows) {
		return 0, false
	}
	return l.rows[y-l.pad], true
}

// queryChanged starts the searches for the input's current value: local
// at once, the GitHub sources after the debounce (none for an empty
// query).
func (m *Model) queryChanged() tea.Cmd {
	m.query = m.in.Value()
	m.seq++
	m.src = [len(sources)]srcState{}
	m.debouncing = false
	m.merge()
	cmds := []tea.Cmd{m.ask(proto.SearchLocal)}
	if strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(m.query), "!")) != "" && m.search != nil {
		m.debouncing = true
		seq := m.seq
		cmds = append(cmds, m.tick(Debounce, func(time.Time) tea.Msg { return debounceMsg{seq: seq} }))
	}
	return tea.Batch(cmds...)
}

// onDebounce asks the GitHub sources once typing paused.
func (m *Model) onDebounce(msg debounceMsg) tea.Cmd {
	if msg.seq != m.seq || !m.debouncing {
		return nil
	}
	m.debouncing = false
	cmds := []tea.Cmd{m.ask(proto.SearchOpen)}
	if strings.HasPrefix(strings.TrimSpace(m.query), "!") {
		cmds = append(cmds, m.ask(proto.SearchClosed))
	}
	return tea.Batch(cmds...)
}

// onResult stores a current answer and asks the closed source when the
// open one found nothing.
func (m *Model) onResult(msg ResultMsg) tea.Cmd {
	if msg.Seq != m.seq {
		return nil
	}
	i := srcIndex(msg.Result.Source)
	if i < 0 {
		return nil
	}
	s := &m.src[i]
	s.done, s.hits, s.note = true, msg.Result.Hits, msg.Result.Note
	if msg.Err != nil {
		s.hits, s.note = nil, fmt.Sprintf("%s: %v", sourceName(msg.Result.Source), msg.Err)
	}
	m.merge()
	if msg.Result.Source == proto.SearchOpen && msg.Err == nil && s.note == "" && len(s.hits) == 0 {
		return m.ask(proto.SearchClosed)
	}
	return nil
}

// ask runs one source for the current query (once per query).
func (m *Model) ask(src proto.SearchSource) tea.Cmd {
	i := srcIndex(src)
	if m.search == nil || m.src[i].asked {
		return nil
	}
	m.src[i].asked = true
	search, seq, q := m.search, m.seq, m.query
	return func() tea.Msg {
		res, err := search(context.Background(), proto.SearchParams{Query: q, Source: src})
		res.Source = src
		return ResultMsg{Seq: seq, Result: res, Err: err}
	}
}

func srcIndex(src proto.SearchSource) int {
	for i, s := range sources {
		if s == src {
			return i
		}
	}
	return -1
}

func sourceName(src proto.SearchSource) string {
	switch src {
	case proto.SearchLocal:
		return "branches"
	case proto.SearchOpen:
		return "open PRs"
	case proto.SearchClosed:
		return "closed PRs"
	}
	return string(src)
}

// merge rebuilds the result list: local, then open, then closed, each in
// the daemon's order, without duplicate items. The selection follows the
// selected item when it is still there.
func (m *Model) merge() {
	prev, hadSel := m.Selected()
	seen := map[string]bool{}
	m.hits = m.hits[:0:0]
	for _, s := range m.src {
		for _, h := range s.hits {
			if seen[h.ItemID] {
				continue
			}
			seen[h.ItemID] = true
			m.hits = append(m.hits, h)
		}
	}
	m.sel, m.lastClick = 0, -1
	if hadSel {
		for i, h := range m.hits {
			if h.ItemID == prev.ItemID {
				m.sel = i
				break
			}
		}
	}
	m.fit()
}

// PendingChoice returns and clears the result the user chose.
func (m *Model) PendingChoice() (Choice, bool) {
	if m.pending == nil {
		return Choice{}, false
	}
	c := *m.pending
	m.pending = nil
	return c, true
}

// layout is what the view shows above the prompt: pad blank lines, then
// one row per shown result (rows holds their hit indexes), then the status
// lines (notes, pending sources).
type layout struct {
	pad    int
	rows   []int
	status []string
}

// statusLines are the dim lines under the results: each source's note,
// "… searching <source>" while one is pending, "no matches" when all
// answered with nothing.
func (m Model) statusLines() []string {
	var out []string
	seenNote := map[string]bool{}
	for _, s := range m.src {
		if s.note != "" && !seenNote[s.note] {
			seenNote[s.note] = true
			out = append(out, s.note)
		}
	}
	busy := false
	for i, s := range m.src {
		pending := s.asked && !s.done
		if sources[i] == proto.SearchOpen && m.debouncing {
			pending = true
		}
		if pending {
			busy = true
			out = append(out, "… searching "+sourceName(sources[i]))
		}
	}
	if !busy && len(m.hits) == 0 && len(out) == 0 && strings.TrimSpace(m.query) != "" {
		out = append(out, "no matches")
	}
	return out
}

// fit lays the results and status lines out in h-1 rows, scrolling (top)
// to keep the selection visible; status lines win over results when space
// is short.
func (m *Model) fit() layout {
	avail := max(0, m.h-1)
	st := m.statusLines()
	if len(st) > avail {
		st = st[:avail]
	}
	n := min(len(m.hits), avail-len(st))
	switch {
	case m.sel < m.top:
		m.top = m.sel
	case m.sel >= m.top+n:
		m.top = m.sel - n + 1
	}
	m.top = min(max(0, m.top), max(0, len(m.hits)-n))
	l := layout{status: st, pad: avail - n - len(st)}
	for i := m.top; i < m.top+n; i++ {
		l.rows = append(l.rows, i)
	}
	return l
}

// layout is fit on a copy, for View and hitAt (value receivers).
func (m Model) layout() layout { return m.fit() }

// View renders the results above the prompt line, at most the set height.
func (m Model) View() string {
	if !m.open {
		return ""
	}
	l := m.layout()
	lines := make([]string, 0, m.h)
	for range l.pad {
		lines = append(lines, "")
	}
	now := m.now()
	for _, i := range l.rows {
		lines = append(lines, m.row(m.hits[i], i == m.sel, now))
	}
	for _, s := range l.status {
		lines = append(lines, m.th.Dim.Render(theme.Truncate(s, m.w)))
	}
	lines = append(lines, m.promptLine())
	return strings.Join(lines, "\n")
}

// promptLine is "/ " plus the query and a block cursor (drawn here so the
// output is colour-free without a colour profile), cut to the width.
func (m Model) promptLine() string {
	val := []rune(m.in.Value())
	pos := min(max(m.in.Position(), 0), len(val))
	before, after := string(val[:pos]), string(val[pos:])
	cursor := "█"
	if m.th.Colour {
		under := " "
		if pos < len(val) {
			under, after = string(val[pos]), string(val[pos+1:])
		}
		cursor = m.r.NewStyle().Reverse(true).Render(under)
	}
	room := m.w - 2 - 1 // prompt and cursor
	if w := ansi.StringWidth(before); w > room && room > 1 {
		before = "…" + ansi.TruncateLeft(before, w-room+1, "")
	}
	return theme.Truncate(m.th.Accent.Render("/ ")+before+cursor+after, m.w)
}

// row formats one result: marker, kind, number or ref, title, then the
// dim "· repo · author · state · age · listed" tail (the repo key only in
// a workspace, M5.3). The title is cut first so the tail stays visible;
// when fewer than minTitle cells would be left for it, the author is
// dropped from the tail, then the repo; a line still too wide is cut at
// the end.
//
//	▸ pr  #482 Migrate session to JWT · ingrid · 2d
//	  br  feat/x  Add login form · 5h
//	  pr  #12 Retry webhooks · billing-svc · alice · 2d
func (m Model) row(h proto.SearchHit, selected bool, now time.Time) string {
	marker := "  "
	if selected {
		marker = m.th.Accent.Render("▸") + " "
	}
	kind, ident := "br", h.Ref+"  "
	if h.Kind == model.KindPR {
		kind, ident = "pr", fmt.Sprintf("#%d ", h.Number)
	}
	head := marker + m.th.Dim.Render(kind) + "  " + m.th.Blue.Render(ident)
	tail, room := m.fitTail(h, now, ansi.StringWidth(head))
	title := h.Title
	if room >= minTitle {
		title = theme.Truncate(title, room)
	}
	tail = m.th.Dim.Render(tail)
	if selected {
		title = m.th.Selected.Render(title)
	}
	return theme.Truncate(head+title+tail, m.w)
}

// minTitle is the fewest title cells a row keeps before it drops the
// author (then the repo) from its tail.
const minTitle = 5

// fitTail returns the row tail that leaves the title at least minTitle
// cells after a head headW cells wide, and the room left for the title:
// the full tail, else without the author, else without the repo too (the
// shortest tail even when it still does not fit).
func (m Model) fitTail(h proto.SearchHit, now time.Time, headW int) (string, int) {
	tail := ""
	for _, t := range []struct{ repo, author bool }{{true, true}, {true, false}, {false, false}} {
		tail = meta(h, now, t.repo, t.author)
		if room := m.w - headW - ansi.StringWidth(tail); room >= minTitle {
			return tail, room
		}
	}
	return tail, m.w - headW - ansi.StringWidth(tail)
}

// meta is the row tail: the workspace repo key (when withRepo), author
// (when withAuthor), the state of a closed or merged PR, age and the listed
// mark, each after " · ".
func meta(h proto.SearchHit, now time.Time, withRepo, withAuthor bool) string {
	var parts []string
	if withRepo && h.Repo != "" {
		parts = append(parts, h.Repo)
	}
	if withAuthor && h.Author != "" {
		parts = append(parts, h.Author)
	}
	if h.State == "closed" || h.State == "merged" {
		parts = append(parts, h.State)
	}
	if a := Age(h.UpdatedAt, now); a != "" {
		parts = append(parts, a)
	}
	if h.Listed {
		parts = append(parts, "listed")
	}
	if len(parts) == 0 {
		return ""
	}
	return " · " + strings.Join(parts, " · ")
}

// Age formats the time since t compactly: "now", "5m", "5h", "2d", "3w",
// "4mo", "2y"; "" for a zero time.
func Age(t, now time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	days := int(d.Hours() / 24)
	switch {
	case days < 14:
		return fmt.Sprintf("%dd", days)
	case days < 60:
		return fmt.Sprintf("%dw", days/7)
	case days < 365:
		return fmt.Sprintf("%dmo", days/30)
	}
	return fmt.Sprintf("%dy", days/365)
}
