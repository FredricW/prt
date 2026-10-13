package tui

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// Runner runs the pipeline synchronously for snapshots (InProcess has it).
type Runner interface {
	RunSync(ctx context.Context, itemID string) error
}

// Snapshot renders screens to w without a TTY, for agents and CI. See
// SnapshotWithOptions; ages are relative to the real clock.
func Snapshot(ctx context.Context, c daemon.Client, w io.Writer, width, height, maxItems int) error {
	return SnapshotWithOptions(ctx, c, w, width, height, maxItems, Options{})
}

// SnapshotWithOptions discovers items, analyses every item synchronously
// (when c is a Runner), then writes, for each of the first maxItems items,
// the picker with the Overview tab (a branch shows its branch state box),
// the picker with the Code tab by file, the picker with the Code tab by
// change ("code-by-change"; by file with the note "no groups yet" when the
// item has no groups), the Code tab with side-by-side requested
// ("code-split"; below 140 columns it renders unified with the degrade
// note), the Pipeline page ("pipeline") and the Architecture tab
// ("architecture"), then the comment screens the item has data for (see
// commentScreens). Then one search screen (the `/` prompt with the first
// item's ref or number typed, when the client's Search answers without an
// error) and one help-overlay screen. Screens are separated by "===
// screen: … ===" headers. Output has no colour; pass o.Now for
// deterministic ages.
func SnapshotWithOptions(ctx context.Context, c daemon.Client, w io.Writer, width, height, maxItems int, o Options) error {
	if err := c.Discover(ctx); err != nil {
		_, _ = fmt.Fprintf(w, "# discover failed: %v\n", err)
	}
	items, err := c.Items(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: items: %w", err)
	}
	if r, ok := c.(Runner); ok {
		for _, it := range items {
			if err := r.RunSync(ctx, it.ID); err != nil {
				_, _ = fmt.Fprintf(w, "# analysis of %s failed: %v\n", it.ID, err)
			}
		}
		if items, err = c.Items(ctx); err != nil {
			return fmt.Errorf("snapshot: items: %w", err)
		}
	}
	o.RefreshEvery = 0
	m := NewWithOptions(c, lipgloss.NewRenderer(io.Discard), o)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: width, Height: height})
	tm, _ = tm.Update(itemsMsg{items: items})
	m = tm.(Model)
	// The strategy line of a not-analysed or stopped item's Overview (M5.1);
	// a client without settings leaves it out.
	if info, err := c.Settings(ctx, proto.SettingsParams{}); err == nil {
		m.setStrategy(info.Values.AutoReview)
	}
	n := len(m.picker.items)
	if n == 0 {
		_, err := fmt.Fprintf(w, "=== screen: picker (empty) ===\n%s\n", m.View())
		return err
	}
	for i := 0; i < n && i < maxItems; i++ {
		if err := itemScreens(ctx, w, &m, c, i); err != nil {
			return err
		}
	}
	// Back on the first item, with its detail, so the main pane matches the
	// picker cursor on the search and help screens.
	m.picker.moveTo(0)
	if it, ok := m.picker.selected(); ok {
		if d, err := c.Detail(ctx, it.ID); err == nil {
			m.applyDetail(d)
		}
	}
	if err := searchScreen(ctx, w, &m, c); err != nil {
		return err
	}
	m.tab, m.focus, m.showHelp = TabOverview, FocusPicker, true
	_, err = fmt.Fprintf(w, "=== screen: help · picker ===\n%s\n", m.View())
	return err
}

// screen writes one "=== screen: picker + <name> · <id> ===" block.
func screen(w io.Writer, m *Model, name, id string) error {
	_, err := fmt.Fprintf(w, "=== screen: picker + %s · %s ===\n%s\n\n", name, id, m.View())
	return err
}

// itemScreens writes the screens of the i-th picker item.
func itemScreens(ctx context.Context, w io.Writer, m *Model, c daemon.Client, i int) error {
	m.picker.moveTo(i)
	it, _ := m.picker.selected()
	d, err := c.Detail(ctx, it.ID)
	if err != nil {
		return fmt.Errorf("snapshot: detail %s: %w", it.ID, err)
	}
	m.applyDetail(d)
	for _, tab := range []struct {
		t    Tab
		name string
	}{{TabOverview, "overview"}, {TabCode, "code"}} {
		m.tab = tab.t
		if err := screen(w, m, tab.name, it.ID); err != nil {
			return err
		}
	}
	// By change; without groups the page stays by file and says so.
	m.code.SetMode(codeview.ModeChange)
	err = screen(w, m, "code-by-change", it.ID)
	m.code.SetMode(codeview.ModeFile)
	if err != nil {
		return err
	}
	// Side-by-side requested, by file; the status line says when the pane
	// is too narrow for it.
	m.code.SetDiffLayout(codeview.LayoutSplit)
	m.layoutNote()
	err = screen(w, m, "code-split", it.ID)
	m.code.SetDiffLayout(codeview.LayoutUnified)
	m.note = ""
	if err != nil {
		return err
	}
	// The Pipeline page, clocked like the rest of the snapshot.
	m.tab = TabPipeline
	m.pipe.SetNow(m.now())
	err = screen(w, m, "pipeline", it.ID)
	if err == nil {
		m.tab = TabArch
		err = screen(w, m, "architecture", it.ID)
	}
	m.tab = TabOverview
	if err != nil {
		return err
	}
	return commentScreens(w, m, d)
}

// searchScreen writes the `/` prompt with the first item's ref (or number)
// typed and the results that came back, when the client's Search answers
// that query without an error; otherwise nothing.
func searchScreen(ctx context.Context, w io.Writer, m *Model, c daemon.Client) error {
	it, ok := m.picker.selected()
	if !ok {
		return nil
	}
	q := it.Ref
	if it.Kind == model.KindPR || q == "" {
		q = fmt.Sprintf("#%d", it.Number)
	}
	if _, err := c.Search(ctx, proto.SearchParams{Query: q, Source: proto.SearchLocal}); err != nil {
		return nil // no search on this client: the screen is skipped
	}
	m.tab, m.focus = TabOverview, FocusPicker
	cmd := m.openSearch()
	for _, r := range q {
		cmd = batch(cmd, m.search.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}))
	}
	drainSearch(m, cmd)
	_, err := fmt.Fprintf(w, "=== screen: search · %s ===\n%s\n\n", q, m.View())
	m.search.Close()
	m.sizeSidebar()
	return err
}

// searchRounds bounds how many command rounds the snapshot runs for the
// search prompt (keys → local results and debounce → GitHub sources →
// the closed source); roundTimeout drops a command still running (the
// input's cursor blink, a slow network source) so the snapshot never
// hangs. The prompt draws a static cursor, so a dropped blink changes
// nothing on screen.
const (
	searchRounds = 4
	roundTimeout = 500 * time.Millisecond
)

// drainSearch runs the prompt's commands and feeds their messages back to
// it, round by round, in a fixed order.
func drainSearch(m *Model, cmd tea.Cmd) {
	pending := []tea.Cmd{cmd}
	for range searchRounds {
		msgs := runCmds(pending, time.Now().Add(roundTimeout))
		pending = pending[:0]
		for _, msg := range msgs {
			if c := m.search.Update(msg); c != nil {
				pending = append(pending, c)
			}
		}
		if len(pending) == 0 {
			return
		}
	}
}

// runCmds runs cmds concurrently until deadline and returns their messages
// in cmds order (batches expanded; a command still running is dropped).
func runCmds(cmds []tea.Cmd, deadline time.Time) []tea.Msg {
	res := make([][]tea.Msg, len(cmds))
	var wg sync.WaitGroup
	for i, c := range cmds {
		if c == nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i] = runOne(c, deadline)
		}()
	}
	wg.Wait()
	var out []tea.Msg
	for _, r := range res {
		out = append(out, r...)
	}
	return out
}

func runOne(c tea.Cmd, deadline time.Time) []tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- c() }()
	select {
	case msg := <-ch:
		if b, ok := msg.(tea.BatchMsg); ok {
			return runCmds(b, deadline)
		}
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(time.Until(deadline)):
		return nil
	}
}

// commentJumper is the Code pane's jump to one comment (codeview has it;
// test panes need not).
type commentJumper interface{ JumpToComment(id string) bool }

// commentScreens writes the M4 screens an item has data for: the Overview
// scrolled to "Since you looked" ("overview-since"), and the Code tab at the
// first local draft ("code-draft"), the first ai-draft ("code-ai-draft") and
// the first open GitHub thread ("code-thread"), in Detail.Comments order. Each screen is skipped when the item has nothing for it.
func commentScreens(w io.Writer, m *Model, d proto.Detail) error {
	if d.Agg != nil && d.Agg.Since != nil && m.overview.scrollToBlock(blockSince) {
		err := screen(w, m, "overview-since", d.Item.ID)
		m.overview.vp.GotoTop()
		if err != nil {
			return err
		}
	}
	j, ok := m.code.(commentJumper)
	if !ok {
		return nil
	}
	var draft, ai, thread string
	for _, c := range d.Comments {
		switch {
		case draft == "" && c.Source == model.CommentUser && c.State == model.CommentDraft:
			draft = c.ID
		case ai == "" && c.Source == model.CommentAIDraft:
			ai = c.ID
		case thread == "" && c.Source == model.CommentGitHub && c.InReplyTo == 0 && c.State != model.CommentResolved:
			thread = c.ID
		}
	}
	for _, s := range []struct{ name, id string }{{"code-draft", draft}, {"code-ai-draft", ai}, {"code-thread", thread}} {
		if s.id == "" {
			continue
		}
		m.code.SetDetail(d) // fresh scroll and cursor for each jump
		if !j.JumpToComment(s.id) {
			continue
		}
		m.tab = TabCode
		err := screen(w, m, s.name, d.Item.ID)
		m.tab = TabOverview
		if err != nil {
			return err
		}
	}
	return nil
}
