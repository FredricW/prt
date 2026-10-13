package codeview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// loadComments reads the comments fixture: detail.json plus user drafts
// (one a range), a pending and a posted comment, an ai-draft, an open and a
// resolved GitHub thread, an outdated draft, and Agg.Since.
func loadComments(t testing.TB) proto.Detail {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "detail_comments.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var d proto.Detail
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return d
}

// commentModel is the comments fixture with every filter off, on
// client.ts; the cursor starts on u1's first changed line (old 11, under
// the open thread T1).
func commentModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := newModel(t, loadComments(t), w, h)
	allOff(&m)
	return m
}

// typeText types s into whatever has the keyboard ("\n" is enter).
func typeText(t *testing.T, m *Model, s string) {
	t.Helper()
	for _, r := range s {
		k := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
		if r == '\n' {
			k = tea.KeyMsg{Type: tea.KeyEnter}
		}
		if !m.HandleKey(k) {
			t.Fatalf("rune %q not consumed", r)
		}
	}
}

// moveTo presses j until the cursor is on the diff line (side, n) of the
// pane, or on block id when id is set.
func moveTo(t *testing.T, m *Model, side model.Side, n int, block string) {
	t.Helper()
	for range 60 {
		if block != "" && m.cur.block == block {
			return
		}
		if pos, ok := m.CursorLine(); block == "" && m.cur.block == "" && ok && pos.Side == side && pos.Line == n {
			return
		}
		press(t, m, "j")
	}
	t.Fatalf("cursor never reached %s %d %q (at %+v)", side, n, block, m.cur)
}

const (
	client  = "web/src/api/client.ts"
	session = "web/src/api/session.ts"
)

func TestCommentOps(t *testing.T) {
	d := loadComments(t)
	byID := map[string]model.Comment{}
	for _, c := range d.Comments {
		byID[c.ID] = c
	}
	edited := byID["d-single"]
	edited.Body += "!"
	tests := []struct {
		name string
		run  func(t *testing.T, m *Model)
		want *CommentOp // nil: nothing pending
	}{
		{"save new on a deleted line (old side)", func(t *testing.T, m *Model) {
			press(t, m, "C")
			typeText(t, m, "hi")
			press(t, m, "ctrl+s")
		}, &CommentOp{Kind: CommentOpSave, Comment: model.Comment{ItemID: "pr:7", UnitID: "u1", Path: client, Line: 11, Side: model.SideOld, Body: "hi"}}},
		{"save new on an added line (new side), multi-line", func(t *testing.T, m *Model) {
			moveTo(t, m, model.SideNew, 11, "")
			press(t, m, "C")
			typeText(t, m, "a\nb")
			press(t, m, "ctrl+s")
		}, &CommentOp{Kind: CommentOpSave, Comment: model.Comment{ItemID: "pr:7", UnitID: "u1", Path: client, Line: 11, Side: model.SideNew, Body: "a\nb"}}},
		{"save new range across sides", func(t *testing.T, m *Model) {
			press(t, m, "V", "j", "C")
			typeText(t, m, "range")
			press(t, m, "ctrl+s")
		}, &CommentOp{Kind: CommentOpSave, Comment: model.Comment{ItemID: "pr:7", UnitID: "u1", Path: client,
			StartLine: 11, StartSide: model.SideOld, Line: 11, Side: model.SideNew, Body: "range"}}},
		{"V on the existing range edits it", func(t *testing.T, m *Model) {
			moveTo(t, m, model.SideNew, 11, "")
			press(t, m, "V", "j", "j", "C")
			typeText(t, m, "!")
			press(t, m, "ctrl+s")
		}, &CommentOp{Kind: CommentOpSave, Comment: func() model.Comment {
			c := byID["d-range"]
			c.Body += "!"
			return c
		}()}},
		{"C on a line with a draft edits it", func(t *testing.T, m *Model) {
			moveTo(t, m, model.SideNew, 12, "")
			press(t, m, "C")
			typeText(t, m, "!")
			press(t, m, "ctrl+s")
		}, &CommentOp{Kind: CommentOpSave, Comment: edited}},
		{"empty body deletes an existing draft", func(t *testing.T, m *Model) {
			moveTo(t, m, "", 0, "d-range")
			press(t, m, "C", "ctrl+u", "ctrl+s")
		}, &CommentOp{Kind: CommentOpDelete, Comment: byID["d-range"]}},
		{"empty body on a new comment saves nothing", func(t *testing.T, m *Model) {
			press(t, m, "C")
			typeText(t, m, "  \n")
			press(t, m, "ctrl+s")
			if m.Note() != "empty comment not saved" {
				t.Errorf("note %q", m.Note())
			}
		}, nil},
		{"quit saves the open composer", func(t *testing.T, m *Model) {
			press(t, m, "C")
			typeText(t, m, "unsent")
			if !m.SaveOpenComposer() || m.Capturing() {
				t.Error("SaveOpenComposer did not queue and close")
			}
		}, &CommentOp{Kind: CommentOpSave, Comment: model.Comment{ItemID: "pr:7", UnitID: "u1", Path: client, Line: 11, Side: model.SideOld, Body: "unsent"}}},
		{"quit with a blank composer saves nothing", func(t *testing.T, m *Model) {
			press(t, m, "C")
			if m.SaveOpenComposer() {
				t.Error("blank composer queued a save")
			}
		}, nil},
		{"esc discards", func(t *testing.T, m *Model) {
			press(t, m, "C")
			typeText(t, m, "draft")
			press(t, m, "esc")
			if m.Capturing() {
				t.Error("composer still open")
			}
		}, nil},
		{"a accepts the ai-draft on the line", func(t *testing.T, m *Model) {
			moveTo(t, m, model.SideNew, 13, "")
			press(t, m, "a")
		}, &CommentOp{Kind: CommentOpAccept, CommentID: "ai-k1"}},
		{"d dismisses the ai-draft block under the cursor", func(t *testing.T, m *Model) {
			moveTo(t, m, "", 0, "ai-k1")
			press(t, m, "d")
		}, &CommentOp{Kind: CommentOpDismiss, CommentID: "ai-k1"}},
		{"! on an ai-draft dismisses with an override", func(t *testing.T, m *Model) {
			moveTo(t, m, model.SideNew, 13, "")
			press(t, m, "!")
			if !m.Capturing() {
				t.Fatal("picker not capturing")
			}
			press(t, m, "j", "j", "enter", "enter") // kind → bugfix
		}, &CommentOp{Kind: CommentOpDismiss, CommentID: "ai-k1", Override: &model.Override{UnitID: "u1", Question: "kind", Value: "bugfix"}}},
		{"! without an ai-draft marks a label wrong", func(t *testing.T, m *Model) {
			press(t, m, "!", "enter", "enter") // behaviour yes → no
		}, &CommentOp{Kind: CommentOpOverride, Override: &model.Override{UnitID: "u1", Question: "behaviour", Value: "no"}}},
		{"! on a score label offers 1–5 without the current rung", func(t *testing.T, m *Model) {
			press(t, m, "!", "j", "j", "j", "enter", "enter") // risk (4) → 1
		}, &CommentOp{Kind: CommentOpOverride, Override: &model.Override{UnitID: "u1", Question: "risk", Value: "1"}}},
		{"d without an ai-draft notes and is consumed", func(t *testing.T, m *Model) {
			press(t, m, "d")
			if !strings.Contains(m.Note(), "no AI finding") {
				t.Errorf("note %q", m.Note())
			}
		}, nil},
		{"t then ctrl+s replies", func(t *testing.T, m *Model) {
			press(t, m, "t")
			typeText(t, m, "ok")
			press(t, m, "ctrl+s")
		}, &CommentOp{Kind: CommentOpReply, ThreadID: "T1", Body: "ok"}},
		{"t then ctrl+r resolves", func(t *testing.T, m *Model) {
			press(t, m, "t", "ctrl+r")
		}, &CommentOp{Kind: CommentOpResolve, ThreadID: "T1"}},
		{"empty reply sends nothing", func(t *testing.T, m *Model) {
			press(t, m, "t", "ctrl+s")
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := commentModel(t, 120, 40)
			tt.run(t, &m)
			op, ok := m.PendingComment()
			switch {
			case tt.want == nil && ok:
				t.Fatalf("unexpected op %+v", op)
			case tt.want == nil:
				return
			case !ok:
				t.Fatalf("no op (note %q)", m.Note())
			case !reflect.DeepEqual(op, *tt.want):
				t.Fatalf("op\n got %+v\nwant %+v", op, *tt.want)
			}
			if _, again := m.PendingComment(); again {
				t.Error("PendingComment did not clear")
			}
		})
	}
}

func TestAcceptNotConsumedWithoutAIDraft(t *testing.T) {
	m := commentModel(t, 120, 40)
	if m.HandleKey(keyMsg("a")) {
		t.Fatal("a consumed with no ai-draft under the cursor (the root's approve must get it)")
	}
	if _, ok := m.PendingComment(); ok {
		t.Error("a queued an op")
	}
	press(t, &m, "t")
	if m.HandleKey(keyMsg("a")) != true {
		t.Error("a not swallowed by the open thread panel")
	}
	if _, ok := m.PendingComment(); ok {
		t.Error("a in the panel queued an op")
	}
}

func TestCursorSides(t *testing.T) {
	m := commentModel(t, 120, 40)
	want := []LinePos{
		{client, model.SideOld, 11, "u1"},
		{client, model.SideNew, 11, "u1"},
		{client, model.SideNew, 12, "u1"},
	}
	var got []LinePos
	for len(got) < len(want) {
		if pos, ok := m.CursorLine(); ok && m.cur.block == "" {
			got = append(got, pos)
		}
		press(t, &m, "j")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cursor lines\n got %+v\nwant %+v", got, want)
	}
	// A context line comments on the new side.
	moveTo(t, &m, model.SideNew, 14, "")
	if pos, _ := m.CursorLine(); pos.Side != model.SideNew {
		t.Errorf("context line side %q", pos.Side)
	}
}

func TestCommentCapturing(t *testing.T) {
	cases := map[string][]string{
		"composer": {"C"},
		"panel":    {"t"},
		"picker":   {"!"},
	}
	for name, open := range cases {
		t.Run(name, func(t *testing.T) {
			m := commentModel(t, 120, 40)
			press(t, &m, open...)
			if !m.Capturing() {
				t.Fatal("not capturing")
			}
			for _, k := range []string{"q", "1", "n", "J"} {
				if !m.HandleKey(keyMsg(k)) {
					t.Errorf("%q not consumed", k)
				}
			}
			if m.HandleKey(keyMsg("ctrl+c")) {
				t.Error("ctrl+c swallowed")
			}
			press(t, &m, "esc")
			if m.Capturing() {
				t.Error("esc did not close")
			}
		})
	}
	m := commentModel(t, 120, 40)
	press(t, &m, "V")
	if m.Capturing() {
		t.Error("a V selection captures every key")
	}
	press(t, &m, "esc")
	if m.vis != nil {
		t.Error("esc did not cancel the selection")
	}
}

func TestVisualStaysInUnit(t *testing.T) {
	m := commentModel(t, 120, 60)
	moveTo(t, &m, model.SideNew, 15, "") // u1's last line
	press(t, &m, "V", "j")
	if !strings.Contains(m.Note(), "within one unit") {
		t.Errorf("note %q", m.Note())
	}
	if pos, _ := m.CursorLine(); pos.Line != 15 || pos.UnitID != "u1" {
		t.Errorf("cursor left the unit: %+v", pos)
	}
}

func TestThreadKeyWithoutThread(t *testing.T) {
	m := commentModel(t, 120, 40)
	moveTo(t, &m, model.SideNew, 12, "")
	press(t, &m, "t")
	if m.Capturing() || !strings.Contains(m.Note(), "no GitHub thread") {
		t.Errorf("t: capturing %v note %q", m.Capturing(), m.Note())
	}
}

func TestStrip(t *testing.T) {
	m := commentModel(t, 120, 40)
	if !m.HasStrip() {
		t.Fatal("no strip")
	}
	ls := lines(m)
	if len(ls) != 40 {
		t.Fatalf("%d lines", len(ls))
	}
	want := "Comments 8 · 3 drafts · 1 pending · 1 posted · 1 ai · 2 threads (1 open)"
	if !strings.HasPrefix(ls[39], want) {
		t.Errorf("strip = %q, want prefix %q", ls[39], want)
	}
	m.SetStripFocus(true)
	if !m.StripFocused() {
		t.Fatal("strip not focused")
	}
	ls = lines(m)
	if len(ls) != 40 || !strings.HasPrefix(ls[33], "Comments 8") || !strings.HasPrefix(ls[34], "▸ posted") {
		t.Fatalf("focused strip:\n%s", strings.Join(ls[32:], "\n"))
	}
	press(t, &m, "G", "enter")
	if m.StripFocused() {
		t.Error("jump kept the strip focused")
	}
	pos, ok := m.CursorLine()
	if !ok || pos != (LinePos{session, model.SideNew, 24, "u5"}) || m.SelectedFile() != session {
		t.Errorf("jump: %+v %v file %q", pos, ok, m.SelectedFile())
	}
	if !m.cursorVisible() {
		t.Error("cursor not in view after the jump")
	}
	// JumpToComment lands where the strip's jump does.
	j := commentModel(t, 120, 40)
	if !j.JumpToComment(j.c.blocks[len(j.c.blocks)-1].c.ID) {
		t.Fatal("JumpToComment: id not found")
	}
	if got, ok := j.CursorLine(); !ok || got != pos || j.StripFocused() {
		t.Errorf("JumpToComment: %+v %v, want %+v", got, ok, pos)
	}
	if j.JumpToComment("no-such-id") {
		t.Error("JumpToComment found an unknown id")
	}
	// Without comments there is no strip and focus is refused.
	plain := newModel(t, loadDetail(t), 120, 30)
	plain.SetStripFocus(true)
	if plain.HasStrip() || plain.StripFocused() {
		t.Error("strip without comments")
	}
}

func TestCommentFilters(t *testing.T) {
	m := commentModel(t, 120, 40)
	visible := func() string {
		var out []string
		for _, id := range []string{"u1", "u2", "u3", "u4", "u5", "u6", "u7"} {
			if m.visible(id) {
				out = append(out, id)
			}
		}
		return strings.Join(out, " ")
	}
	m.SetFilter(FilterComments, true)
	if got := visible(); got != "u1 u3 u5 u7" {
		t.Errorf("has comments: %q", got)
	}
	m.SetFilter(FilterComments, false)
	m.SetFilter(FilterSince, true)
	if !m.FilterOn(FilterSince) {
		t.Fatal("since filter refused with Agg.Since set")
	}
	if got := visible(); got != "u1 u7" {
		t.Errorf("changed since last look: %q", got)
	}
	contains(t, m.barText(), "[10 changed since last look ✓]")
}

func TestCommentedNoiseNeverCollapses(t *testing.T) {
	d := loadComments(t)
	d.Comments = append(d.Comments, model.Comment{ID: "d-fmt", ItemID: "pr:7", UnitID: "u2", Path: client, Line: 32,
		Side: model.SideNew, Body: "why reformat?", Source: model.CommentUser, State: model.CommentDraft})
	m := newModel(t, d, 120, 60)
	allOff(&m)
	v := m.View()
	lacks(t, v, "formatting · +2 −2 (z to expand)")
	contains(t, v, "why reformat?")
}

func TestThreadGutterAndBlocks(t *testing.T) {
	m := commentModel(t, 140, 60)
	v := m.View()
	contains(t, v, "❱▸11    -   const res = await fetch(url);")
	contains(t, v, "❱ thread · alice · 1 reply · open")
	contains(t, v, "✎ draft · you · L11–13")
	contains(t, v, "╭─ ai-draft · medium")
	contains(t, v, aiHint)
	contains(t, v, "✎ draft · you · outdated (L40)")
	press(t, &m, "J")
	v = m.View()
	contains(t, v, "✎ pending · you")
	contains(t, v, "❱ thread · bob · 0 replies · resolved (t opens)")
	lacks(t, v, "nit: naming") // resolved threads are collapsed
	lacks(t, v, "❱ 23")        // and have no gutter mark
}

func TestAIDraftDimBorderInColour(t *testing.T) {
	m := New(colourRenderer())
	m.SetSize(140, 60)
	m.SetDetail(loadComments(t))
	allOff(&m)
	v := m.View()
	if !strings.Contains(v, "\x1b[2m╰") {
		t.Errorf("ai-draft border not faint:\n%q", v)
	}
	checkView(t, "colour", v, 140, 60)
}

func TestCommentHints(t *testing.T) {
	m := commentModel(t, 120, 40)
	has := func(bs []string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(strings.Join(bs, " "), w) {
				t.Errorf("hints %v lack %q", bs, w)
			}
		}
	}
	keys := func() []string {
		var out []string
		for _, b := range m.ShortBindings() {
			out = append(out, b.Help().Key)
		}
		return out
	}
	has(keys(), "C", "V", "t", "a", "d", "!")
	press(t, &m, "C")
	has(keys(), "ctrl+s", "esc")
	press(t, &m, "esc", "t")
	has(keys(), "ctrl+s", "ctrl+r", "esc")
	var help []string
	for _, row := range m.HelpBindings() {
		for _, b := range row {
			help = append(help, b.Help().Key)
		}
	}
	has(help, "ctrl+r", "C", "V", "!")
}

func TestCommentViewDimensions(t *testing.T) {
	prep := map[string]func(m *Model){
		"inline":   func(*Model) {},
		"composer": func(m *Model) { m.HandleKey(keyMsg("C")) },
		"panel":    func(m *Model) { m.HandleKey(keyMsg("t")) },
		"picker":   func(m *Model) { m.HandleKey(keyMsg("!")) },
		"strip":    func(m *Model) { m.SetStripFocus(true) },
	}
	for name, p := range prep {
		for _, w := range []int{1, 20, 59, 80, 99, 100, 160} {
			for _, h := range []int{1, 2, 5, 30} {
				m := New(plainRenderer())
				m.SetSize(w, h)
				m.SetDetail(loadComments(t))
				allOff(&m)
				p(&m)
				checkView(t, name, m.View(), w, h)
			}
		}
	}
}

func TestReloadKeepsComposerAndClosesGonePanel(t *testing.T) {
	m := commentModel(t, 120, 40)
	moveTo(t, &m, model.SideNew, 11, "")
	press(t, &m, "C")
	typeText(t, &m, "wip")
	m.SetDetail(loadComments(t))
	if m.comp == nil || m.comp.row < 0 || m.comp.ta.Value() != "wip" {
		t.Fatalf("reload lost the composer: %+v", m.comp)
	}
	press(t, &m, "esc")
	press(t, &m, "t") // no thread on new 11: note only
	press(t, &m, "k") // up onto the thread block under old 11
	if m.cur.block != "thread:T1" {
		t.Fatalf("cursor %+v, want the T1 block", m.cur)
	}
	press(t, &m, "t")
	d := loadComments(t)
	var kept []model.Comment
	for _, c := range d.Comments {
		if c.ThreadID != "T1" {
			kept = append(kept, c)
		}
	}
	d.Comments = kept
	m.SetDetail(d)
	if m.panel != nil || m.Capturing() {
		t.Error("panel kept for a thread that is gone")
	}
}

func TestGoldenComments(t *testing.T) {
	cases := map[string]func(t *testing.T) Model{
		"comments_inline": func(t *testing.T) Model { return commentModel(t, 120, 50) },
		"comments_default_filters": func(t *testing.T) Model {
			return newModel(t, loadComments(t), 120, 40)
		},
		"composer_open": func(t *testing.T) Model {
			m := commentModel(t, 120, 30)
			moveTo(t, &m, model.SideNew, 11, "")
			press(t, &m, "C")
			typeText(t, &m, "Is **this** right?\nsecond line")
			return m
		},
		"visual_selection": func(t *testing.T) Model {
			m := commentModel(t, 120, 30)
			press(t, &m, "V", "j", "j")
			return m
		},
		"thread_panel_wide": func(t *testing.T) Model {
			m := commentModel(t, 140, 30)
			press(t, &m, "t")
			typeText(t, &m, "Agreed")
			return m
		},
		"thread_panel_narrow": func(t *testing.T) Model {
			m := commentModel(t, 80, 24)
			press(t, &m, "t")
			return m
		},
		"label_picker": func(t *testing.T) Model {
			m := commentModel(t, 120, 20)
			press(t, &m, "!", "j", "j", "enter")
			return m
		},
		"strip_focused": func(t *testing.T) Model {
			m := commentModel(t, 120, 30)
			m.SetStripFocus(true)
			press(t, &m, "j")
			return m
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			m := build(t)
			checkGolden(t, name, m.View()+"\n")
		})
	}
}
