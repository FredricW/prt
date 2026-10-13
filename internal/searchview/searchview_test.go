package searchview

import (
	"context"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

var now = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// fakeSearch answers from a table keyed by "source:query" and records calls.
type fakeSearch struct {
	mu    sync.Mutex
	table map[string]proto.SearchResult
	calls []string
}

func (f *fakeSearch) search(_ context.Context, p proto.SearchParams) (proto.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := string(p.Source) + ":" + p.Query
	f.calls = append(f.calls, k)
	r := f.table[k]
	r.Source, r.Query = p.Source, p.Query
	return r, nil
}

func (f *fakeSearch) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// newModel is an open-able model without colour whose debounce tick fires
// at once (the test holds the tick message and delivers it by hand).
func newModel(f *fakeSearch) Model {
	m := New(lipgloss.NewRenderer(io.Discard))
	m.SetSearcher(f.search)
	m.SetNow(func() time.Time { return now })
	m.tick = func(_ time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		return func() tea.Msg { return fn(now) }
	}
	m.SetSize(36, 8)
	return m
}

// run executes cmd and returns the messages it produced, flattening
// batches. Commands that do not answer quickly (cursor blink) are dropped.
func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-time.After(50 * time.Millisecond):
		return nil
	}
	if b, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range b {
			out = append(out, run(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// typeText types s one rune at a time and returns every message the
// resulting commands produced.
func typeText(m *Model, s string) []tea.Msg {
	var out []tea.Msg
	for _, r := range s {
		out = append(out, run(m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}))...)
	}
	return out
}

// deliver feeds msgs to Update and returns the messages its commands
// produced.
func deliver(m *Model, msgs ...tea.Msg) []tea.Msg {
	var out []tea.Msg
	for _, msg := range msgs {
		out = append(out, run(m.Update(msg))...)
	}
	return out
}

// settle delivers messages until none are left (results, ticks, follow-up
// queries).
func settle(m *Model, msgs []tea.Msg) {
	for len(msgs) > 0 {
		msgs = deliver(m, msgs...)
	}
}

func split(msgs []tea.Msg) (results []ResultMsg, ticks []debounceMsg) {
	for _, msg := range msgs {
		switch v := msg.(type) {
		case ResultMsg:
			results = append(results, v)
		case debounceMsg:
			ticks = append(ticks, v)
		}
	}
	return results, ticks
}

func prHit(src proto.SearchSource, n int, title, author, state string, age time.Duration) proto.SearchHit {
	return proto.SearchHit{
		Source: src, Kind: model.KindPR, Number: n, Title: title, Author: author, State: state,
		UpdatedAt: now.Add(-age), Query: "#" + strconv.Itoa(n), ItemID: model.PRItemID(n),
	}
}

func brHit(ref, subject, author string, age time.Duration) proto.SearchHit {
	return proto.SearchHit{
		Source: proto.SearchLocal, Kind: model.KindBranch, Ref: ref, Title: subject, Author: author,
		UpdatedAt: now.Add(-age), Query: ref, ItemID: model.BranchItemID(ref),
	}
}

func ids(hs []proto.SearchHit) []string {
	out := make([]string, 0, len(hs))
	for _, h := range hs {
		out = append(out, h.ItemID)
	}
	return out
}

func TestOpenAsksLocalAtOnce(t *testing.T) {
	f := &fakeSearch{table: map[string]proto.SearchResult{"local:": {Hits: []proto.SearchHit{brHit("feat/x", "Add x", "", time.Hour)}}}}
	m := newModel(f)
	if m.Active() || m.View() != "" {
		t.Fatal("new model is open")
	}
	msgs := run(m.Open())
	if !m.Active() {
		t.Fatal("not open")
	}
	res, ticks := split(msgs)
	if len(res) != 1 || len(ticks) != 0 || !slices.Equal(f.called(), []string{"local:"}) {
		t.Fatalf("open: results %+v, ticks %+v, calls %v", res, ticks, f.called())
	}
	settle(&m, msgs)
	if got := ids(m.Hits()); !slices.Equal(got, []string{"branch:feat/x"}) {
		t.Errorf("hits = %v", got)
	}
}

// Every keystroke bumps the sequence; a stale tick or result does nothing,
// and the open source is asked only for the query typing stopped at.
func TestDebounceAndStaleResults(t *testing.T) {
	f := &fakeSearch{table: map[string]proto.SearchResult{
		"local:s":  {Hits: []proto.SearchHit{brHit("spike", "s", "", time.Hour)}},
		"local:se": {Hits: []proto.SearchHit{brHit("session", "se", "", time.Hour)}},
		"open:se":  {Hits: []proto.SearchHit{prHit(proto.SearchOpen, 482, "Migrate session", "ingrid", "open", time.Hour)}},
	}}
	m := newModel(f)
	settle(&m, run(m.Open()))
	r1, t1 := split(typeText(&m, "s"))
	r2, t2 := split(typeText(&m, "e"))
	if len(t1) != 1 || len(t2) != 1 || len(r1) != 1 || len(r2) != 1 {
		t.Fatalf("ticks %v %v, results %v %v", t1, t2, r1, r2)
	}
	if strings.Contains(m.View(), "… searching open PRs") == false {
		t.Errorf("no pending line while debouncing:\n%s", m.View())
	}
	// Stale tick: no query.
	if out := deliver(&m, t1[0]); len(out) != 0 {
		t.Errorf("stale tick produced %v", out)
	}
	// Stale local result: dropped.
	deliver(&m, r1[0])
	if len(m.Hits()) != 0 {
		t.Errorf("stale result merged: %v", ids(m.Hits()))
	}
	deliver(&m, r2[0])
	if got := ids(m.Hits()); !slices.Equal(got, []string{"branch:session"}) {
		t.Errorf("hits = %v", got)
	}
	settle(&m, deliver(&m, t2[0]))
	want := []string{"local:", "local:s", "local:se", "open:se"}
	if got := f.called(); !slices.Equal(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	if got := ids(m.Hits()); !slices.Equal(got, []string{"branch:session", "pr:482"}) {
		t.Errorf("hits = %v", got)
	}
	if strings.Contains(m.View(), "searching") {
		t.Errorf("pending line after all answers:\n%s", m.View())
	}
}

func TestClosedSource(t *testing.T) {
	cases := []struct {
		name  string
		query string
		table map[string]proto.SearchResult
		want  []string // GitHub calls, in order
	}{
		{
			name: "open hits: closed not asked", query: "jwt",
			table: map[string]proto.SearchResult{"open:jwt": {Hits: []proto.SearchHit{prHit(proto.SearchOpen, 1, "JWT", "a", "open", 0)}}},
			want:  []string{"open:jwt"},
		},
		{
			name: "open empty: closed asked", query: "old",
			want: []string{"open:old", "closed:old"},
		},
		{
			name: "bang: both after the debounce", query: "!old",
			want: []string{"open:!old", "closed:!old"},
		},
		{
			name: "open note: closed not asked", query: "x",
			table: map[string]proto.SearchResult{"open:x": {Note: "GitHub unavailable"}},
			want:  []string{"open:x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeSearch{table: tc.table}
			m := newModel(f)
			settle(&m, run(m.Open()))
			settle(&m, typeText(&m, tc.query))
			var gh []string
			for _, c := range f.called() {
				if !strings.HasPrefix(c, "local:") {
					gh = append(gh, c)
				}
			}
			if !slices.Equal(gh, tc.want) {
				t.Errorf("GitHub calls = %v, want %v", gh, tc.want)
			}
		})
	}
}

func TestMergeOrderAndDedupe(t *testing.T) {
	m := newModel(&fakeSearch{})
	m.SetSearcher(nil) // results are delivered by hand
	run(m.Open())
	typeText(&m, "x")
	seq := m.seq
	shared := prHit(proto.SearchClosed, 7, "Shared", "a", "closed", 0)
	deliver(&m,
		ResultMsg{Seq: seq, Result: proto.SearchResult{Source: proto.SearchClosed, Hits: []proto.SearchHit{prHit(proto.SearchClosed, 9, "Closed", "c", "merged", 0), shared}}},
		ResultMsg{Seq: seq, Result: proto.SearchResult{Source: proto.SearchOpen, Hits: []proto.SearchHit{prHit(proto.SearchOpen, 7, "Shared", "a", "open", 0), prHit(proto.SearchOpen, 3, "Three", "b", "open", 0)}}},
		ResultMsg{Seq: seq, Result: proto.SearchResult{Source: proto.SearchLocal, Hits: []proto.SearchHit{brHit("x/b", "", "", 0), brHit("x/a", "", "", 0)}}},
	)
	want := []string{"branch:x/b", "branch:x/a", "pr:7", "pr:3", "pr:9"}
	if got := ids(m.Hits()); !slices.Equal(got, want) {
		t.Errorf("hits = %v, want %v", got, want)
	}
	if m.Hits()[2].Source != proto.SearchOpen {
		t.Errorf("duplicate kept the closed copy: %+v", m.Hits()[2])
	}
}

func choiceModel(t *testing.T) Model {
	t.Helper()
	f := &fakeSearch{table: map[string]proto.SearchResult{
		"local:": {Hits: []proto.SearchHit{brHit("a", "A", "", 0), brHit("b", "B", "", 0), brHit("c", "C", "", 0)}},
	}}
	m := newModel(f)
	settle(&m, run(m.Open()))
	return m
}

func TestChoiceKeys(t *testing.T) {
	m := choiceModel(t)
	if _, ok := m.PendingChoice(); ok {
		t.Fatal("choice before any key")
	}
	deliver(&m, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyCtrlN}, tea.KeyMsg{Type: tea.KeyDown})
	deliver(&m, tea.KeyMsg{Type: tea.KeyCtrlP})
	deliver(&m, tea.KeyMsg{Type: tea.KeyEnter})
	c, ok := m.PendingChoice()
	if !ok || c.Kind != ChoiceAdd || c.Hit.Ref != "b" {
		t.Fatalf("enter choice = %+v, %v", c, ok)
	}
	if _, ok := m.PendingChoice(); ok {
		t.Error("PendingChoice not cleared")
	}
	deliver(&m, tea.KeyMsg{Type: tea.KeyUp}, tea.KeyMsg{Type: tea.KeyUp}, tea.KeyMsg{Type: tea.KeyTab})
	c, ok = m.PendingChoice()
	if !ok || c.Kind != ChoicePreview || c.Hit.Ref != "a" {
		t.Fatalf("tab choice = %+v, %v", c, ok)
	}
	if !m.Active() {
		t.Error("a choice closed the prompt (the root decides)")
	}
	deliver(&m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.Active() || m.View() != "" {
		t.Error("esc did not close")
	}
	if _, ok := m.PendingChoice(); ok {
		t.Error("esc chose")
	}
	// Reopened: empty query, old answers dropped.
	seq := m.seq
	run(m.Open())
	if m.Query() != "" || m.seq == seq {
		t.Errorf("reopen: query %q, seq %d (was %d)", m.Query(), m.seq, seq)
	}
}

func TestRowsGolden(t *testing.T) {
	m := newModel(&fakeSearch{})
	m.SetSearcher(nil)
	m.SetSize(36, 8)
	run(m.Open())
	typeText(&m, "se")
	seq := m.seq
	listed := prHit(proto.SearchOpen, 483, "Session cleanup", "bob", "open", 3*time.Hour)
	listed.Listed = true
	deliver(&m,
		ResultMsg{Seq: seq, Result: proto.SearchResult{Source: proto.SearchLocal, Hits: []proto.SearchHit{brHit("feat/x", "Add session store", "", 5*time.Hour)}}},
		ResultMsg{Seq: seq, Result: proto.SearchResult{Source: proto.SearchOpen, Hits: []proto.SearchHit{
			prHit(proto.SearchOpen, 482, "Migrate session to JWT", "ingrid", "open", 48*time.Hour), listed,
		}}},
		ResultMsg{Seq: seq, Result: proto.SearchResult{Source: proto.SearchClosed, Hits: []proto.SearchHit{
			prHit(proto.SearchClosed, 300, "Old session", "carol", "merged", 30*24*time.Hour),
		}, Note: "GitHub search failed: boom"}},
	)
	want := strings.Join([]string{
		"",
		"",
		"▸ br  feat/x  Add session store · 5h",
		"  pr  #482 Migrate se… · ingrid · 2d",
		"  pr  #483 Sess… · bob · 3h · listed",
		"  pr  #300 Old session · merged · 4w", // author dropped to keep the title
		"GitHub search failed: boom",
		"/ se█",
	}, "\n")
	got := m.View()
	if got != want {
		t.Errorf("view:\n%s\nwant:\n%s", got, want)
	}
	for i, l := range strings.Split(got, "\n") {
		if w := lipgloss.Width(l); w > 36 {
			t.Errorf("line %d is %d wide: %q", i, w, l)
		}
	}
}

func TestViewHeightAndScroll(t *testing.T) {
	m := newModel(&fakeSearch{})
	m.SetSearcher(nil)
	m.SetSize(30, 4)
	run(m.Open())
	typeText(&m, "b")
	var hs []proto.SearchHit
	for _, r := range []string{"b1", "b2", "b3", "b4", "b5"} {
		hs = append(hs, brHit(r, "", "", 0))
	}
	deliver(&m, ResultMsg{Seq: m.seq, Result: proto.SearchResult{Source: proto.SearchLocal, Hits: hs}})
	if n := len(strings.Split(m.View(), "\n")); n != 4 {
		t.Fatalf("view has %d lines, want 4:\n%s", n, m.View())
	}
	for range 4 {
		deliver(&m, tea.KeyMsg{Type: tea.KeyDown})
	}
	v := m.View()
	if !strings.Contains(v, "▸ br  b5") || strings.Contains(v, "b1") {
		t.Errorf("selection not scrolled into view:\n%s", v)
	}
}

func TestNotesAndPending(t *testing.T) {
	m := newModel(&fakeSearch{})
	m.SetSearcher(nil)
	run(m.Open())
	typeText(&m, "q")
	m.src[srcIndex(proto.SearchOpen)].asked = true // in flight
	deliver(&m, ResultMsg{Seq: m.seq, Result: proto.SearchResult{Source: proto.SearchLocal}})
	if v := m.View(); !strings.Contains(v, "… searching open PRs") {
		t.Errorf("no pending line:\n%s", v)
	}
	deliver(&m, ResultMsg{Seq: m.seq, Result: proto.SearchResult{Source: proto.SearchOpen, Note: "GitHub unavailable"}})
	v := m.View()
	if !strings.Contains(v, "GitHub unavailable") || strings.Contains(v, "searching") {
		t.Errorf("view:\n%s", v)
	}
}

func TestNoMatches(t *testing.T) {
	f := &fakeSearch{}
	m := newModel(f)
	settle(&m, run(m.Open()))
	settle(&m, typeText(&m, "zzz"))
	if v := m.View(); !strings.Contains(v, "no matches") {
		t.Errorf("view:\n%s", v)
	}
}

func TestMouse(t *testing.T) {
	m := choiceModel(t) // 3 results, size 36×8: rows 4, 5, 6 then the prompt
	m.SetOrigin(2, 10)
	click := func(y int) {
		deliver(&m, tea.MouseMsg{X: 5, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	}
	click(10 + 5)
	if h, _ := m.Selected(); h.Ref != "b" {
		t.Fatalf("click selected %q, want b", h.Ref)
	}
	if _, ok := m.PendingChoice(); ok {
		t.Fatal("first click added")
	}
	click(10 + 5)
	if c, ok := m.PendingChoice(); !ok || c.Kind != ChoiceAdd || c.Hit.Ref != "b" {
		t.Fatalf("second click = %+v, %v", c, ok)
	}
	click(10 + 1) // blank padding row
	click(10 + 7) // prompt row
	if h, _ := m.Selected(); h.Ref != "b" {
		t.Errorf("click outside rows moved the selection to %q", h.Ref)
	}
	deliver(&m, tea.MouseMsg{X: 5, Y: 14, Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if h, _ := m.Selected(); h.Ref != "c" {
		t.Errorf("wheel down selected %q, want c", h.Ref)
	}
	deliver(&m, tea.MouseMsg{X: 5, Y: 14, Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress})
	if h, _ := m.Selected(); h.Ref != "b" {
		t.Errorf("wheel up selected %q, want b", h.Ref)
	}
	click(10 + 4)
	if h, _ := m.Selected(); h.Ref != "a" {
		t.Errorf("click selected %q, want a", h.Ref)
	}
	if _, ok := m.PendingChoice(); ok {
		t.Error("click on another row added")
	}
}

func TestAge(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "now"}, {5 * time.Minute, "5m"}, {5 * time.Hour, "5h"}, {50 * time.Hour, "2d"},
		{20 * 24 * time.Hour, "2w"}, {90 * 24 * time.Hour, "3mo"}, {800 * 24 * time.Hour, "2y"},
	}
	for _, tc := range cases {
		if got := Age(now.Add(-tc.d), now); got != tc.want {
			t.Errorf("Age(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
	if Age(time.Time{}, now) != "" {
		t.Error("zero time has an age")
	}
}

func TestClosedIgnoresMessages(t *testing.T) {
	m := newModel(&fakeSearch{})
	if cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}); cmd != nil || m.Query() != "" {
		t.Error("closed model took a key")
	}
	if cmd := m.Update(ResultMsg{Seq: m.seq}); cmd != nil {
		t.Error("closed model handled a result")
	}
}
