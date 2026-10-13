package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/archview"
	"github.com/fredricw/prt/internal/cmdline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/searchview"
)

// fakeSearch is a scripted searchPane: typing runs one local-source query
// through the Searcher the root set, its ResultMsg replaces the hits;
// down moves, enter chooses Add, tab Preview, esc closes. Mouse messages
// are recorded.
type fakeSearch struct {
	active   bool
	query    string
	searcher searchview.Searcher
	hits     []proto.SearchHit
	cursor   int
	pending  *searchview.Choice
	sizes    [][2]int
	mice     []tea.MouseMsg
}

var _ searchPane = (*fakeSearch)(nil)

func (s *fakeSearch) SetSearcher(f searchview.Searcher) { s.searcher = f }
func (s *fakeSearch) SetNow(func() time.Time)           {}
func (s *fakeSearch) SetSize(w, h int)                  { s.sizes = append(s.sizes, [2]int{w, h}) }
func (s *fakeSearch) Close()                            { s.active = false }
func (s *fakeSearch) Active() bool                      { return s.active }

func (s *fakeSearch) Open() tea.Cmd {
	s.active, s.query, s.hits, s.cursor = true, "", nil, 0
	return nil
}

func (s *fakeSearch) PendingChoice() (searchview.Choice, bool) {
	if s.pending == nil {
		return searchview.Choice{}, false
	}
	c := *s.pending
	s.pending = nil
	return c, true
}

func (s *fakeSearch) View() string {
	lines := make([]string, 0, len(s.hits)+1)
	for i, h := range s.hits {
		mark := "  "
		if i == s.cursor {
			mark = "> "
		}
		lines = append(lines, mark+hitLabel(h)+" "+h.Title)
	}
	return strings.Join(append(lines, "/ "+s.query), "\n")
}

func (s *fakeSearch) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case searchview.ResultMsg:
		s.hits = msg.Result.Hits
	case tea.MouseMsg:
		s.mice = append(s.mice, msg)
	case tea.KeyMsg:
		return s.key(msg)
	}
	return nil
}

func (s *fakeSearch) key(k tea.KeyMsg) tea.Cmd {
	switch k.Type {
	case tea.KeyEnter, tea.KeyTab:
		if s.cursor < len(s.hits) {
			kind := searchview.ChoiceAdd
			if k.Type == tea.KeyTab {
				kind = searchview.ChoicePreview
			}
			s.pending = &searchview.Choice{Kind: kind, Hit: s.hits[s.cursor]}
		}
	case tea.KeyEsc:
		s.active = false
	case tea.KeyDown:
		s.cursor = min(s.cursor+1, max(0, len(s.hits)-1))
	case tea.KeyRunes:
		s.query += string(k.Runes)
		q, f := s.query, s.searcher
		return func() tea.Msg {
			r, err := f(context.Background(), proto.SearchParams{Query: q, Source: proto.SearchLocal})
			return searchview.ResultMsg{Result: r, Err: err}
		}
	}
	return nil
}

// fakeCmd is a scripted cmdPane: runes type, enter submits next[typed] (an
// unknown line submits an error), esc closes.
type fakeCmd struct {
	active  bool
	typed   string
	width   int
	next    map[string]cmdline.Submitted
	pending *cmdline.Submitted
	repos   []string // the last SetRepos (M5.3)
}

var _ cmdPane = (*fakeCmd)(nil)

func (c *fakeCmd) SetWidth(w int)          { c.width = w }
func (c *fakeCmd) SetRepos(names []string) { c.repos = names }
func (c *fakeCmd) Close()                  { c.active = false }
func (c *fakeCmd) Active() bool            { return c.active }
func (c *fakeCmd) View() string            { return ":" + c.typed }
func (c *fakeCmd) Open() tea.Cmd           { c.active, c.typed = true, ""; return nil }
func (c *fakeCmd) PendingCommand() (cmdline.Submitted, bool) {
	if c.pending == nil {
		return cmdline.Submitted{}, false
	}
	s := *c.pending
	c.pending = nil
	return s, true
}

func (c *fakeCmd) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.Type {
	case tea.KeyRunes:
		c.typed += string(k.Runes)
	case tea.KeyEsc:
		c.active = false
	case tea.KeyEnter:
		s, ok := c.next[c.typed]
		if !ok {
			s = cmdline.Submitted{Line: c.typed, Err: errors.New("unknown command :" + c.typed)}
		}
		s.Line = c.typed
		c.pending = &s
		c.active = false
	}
	return nil
}

// fakeArch is a recording archPane: h/j/k/l, +, - and d are consumed;
// enter opens a "file list" that captures every key until esc, and enter
// in it queues a jump to jumpPath.
type fakeArch struct {
	keys      archview.Keys
	offered   []string
	captured  []string
	capturing bool
	details   []string
	clears    int
	jumpPath  string
	jump      string
	mice      []tea.MouseMsg
	origin    [2]int
	size      [2]int
}

var _ archPane = (*fakeArch)(nil)

func (a *fakeArch) SetKeys(k archview.Keys)  { a.keys = k }
func (a *fakeArch) SetSize(w, h int)         { a.size = [2]int{w, h} }
func (a *fakeArch) SetOrigin(x, y int)       { a.origin = [2]int{x, y} }
func (a *fakeArch) SetDetail(d proto.Detail) { a.details = append(a.details, d.Item.ID) }
func (a *fakeArch) Clear()                   { a.clears++ }
func (a *fakeArch) Capturing() bool          { return a.capturing }
func (a *fakeArch) View() string             { return "fake architecture page" }

func (a *fakeArch) Update(msg tea.Msg) tea.Cmd {
	if mm, ok := msg.(tea.MouseMsg); ok {
		a.mice = append(a.mice, mm)
		if mm.Button == tea.MouseButtonLeft && a.jumpPath != "" {
			a.jump = a.jumpPath
		}
	}
	return nil
}

func (a *fakeArch) PendingJump() (string, bool) {
	if a.jump == "" {
		return "", false
	}
	j := a.jump
	a.jump = ""
	return j, true
}

func (a *fakeArch) HelpBindings() [][]key.Binding {
	k := a.keys
	return [][]key.Binding{{k.Left, k.Down, k.Up, k.Right}, {k.Enter, k.DepthUp, k.DepthDown, k.DiffOnly}}
}

func (a *fakeArch) ShortBindings() []key.Binding { return []key.Binding{a.keys.Right, a.keys.Enter} }

func (a *fakeArch) HandleKey(k tea.KeyMsg) bool {
	a.offered = append(a.offered, k.String())
	if a.capturing {
		a.captured = append(a.captured, k.String())
		switch {
		case key.Matches(k, a.keys.Close):
			a.capturing = false
		case key.Matches(k, a.keys.Enter):
			a.jump, a.capturing = a.jumpPath, false
		}
		return true
	}
	switch {
	case key.Matches(k, a.keys.Enter):
		a.capturing = true
		return true
	case key.Matches(k, a.keys.Left), key.Matches(k, a.keys.Right), key.Matches(k, a.keys.Up), key.Matches(k, a.keys.Down),
		key.Matches(k, a.keys.DepthUp), key.Matches(k, a.keys.DepthDown), key.Matches(k, a.keys.DiffOnly):
		return true
	}
	return false
}

// withFakeSearch installs a scripted search prompt on m's client.
func withFakeSearch(m Model) (Model, *fakeSearch) {
	s := &fakeSearch{}
	s.SetSearcher(m.client.Search)
	m.search = s
	return m, s
}

// withFakeCmd installs a scripted command line answering next.
func withFakeCmd(m Model, next map[string]cmdline.Submitted) (Model, *fakeCmd) {
	c := &fakeCmd{next: next}
	m.cmd = c
	return m, c
}

// withFakeArch installs a recording Architecture tab keyed like m.
func withFakeArch(m Model) (Model, *fakeArch) {
	a := &fakeArch{}
	a.SetKeys(m.keys.archKeys())
	m.arch = a
	m.layout()
	return m, a
}

// typeKeys sends each rune of s as a key message, applying the messages
// the commands produce.
func typeKeys(m Model, s string) Model {
	for _, r := range s {
		next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(Model)
		for _, msg := range run(cmd) {
			next, _ := m.Update(msg)
			m = next.(Model)
		}
	}
	return m
}
