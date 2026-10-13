package tui

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// scriptDialog is a scripted form: enter submits, esc cancels, other keys
// are recorded; tests set the values directly.
type scriptDialog struct {
	spec  formSpec
	v     *formValues
	state huh.FormState
	keys  []string
}

func (d *scriptDialog) Init() tea.Cmd        { return nil }
func (d *scriptDialog) View() string         { return "scripted form" }
func (d *scriptDialog) State() huh.FormState { return d.state }

func (d *scriptDialog) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "enter":
		d.state = huh.StateCompleted
	case "esc":
		d.state = huh.StateAborted
	default:
		d.keys = append(d.keys, k.String())
	}
	return nil
}

// withScriptedForms makes m build scriptDialogs; the returned func gives
// the last one built.
func withScriptedForms(m Model) (Model, func() *scriptDialog) {
	var last *scriptDialog
	m.newDialog = func(s formSpec, v *formValues) dialog {
		last = &scriptDialog{spec: s, v: v}
		return last
	}
	return m, func() *scriptDialog { return last }
}

// m4Items are a PR (analysed at s1) and a branch without a PR.
func m4Items() []model.Item {
	return []model.Item{
		{ID: "pr:1", Kind: model.KindPR, Number: 1, Title: "one", Section: model.SectionNeedsYou, Urgency: 3,
			State: model.StateReady, HeadSHA: "s1", AnalysedSHA: "s1", BaseRef: "main"},
		{ID: "branch:feat/x", Kind: model.KindBranch, Ref: "feat/x", Title: "Add feature x", BaseRef: "develop",
			Section: model.SectionBranches, State: model.StateReady, HeadSHA: "b1", AnalysedSHA: "b1"},
	}
}

// m4Model returns a model over m4Items with pr:1's detail loaded (with the
// drafts of detail_since.json), the recording Code pane and the scripted
// forms.
func m4Model(t *testing.T) (Model, *fakeClient, *fakePane, func() *scriptDialog) {
	t.Helper()
	f := newFake(m4Items()...)
	d := loadDetail(t, "detail_since.json")
	d.Item = m4Items()[0]
	f.details["pr:1"] = d
	m, p := withFakePane(newTestModel(t, f, Options{}))
	m = feed[detailMsg](t, m, m.selectionChanged())
	m, last := withScriptedForms(m)
	return m, f, p, last
}

// pressRun applies key k, runs the resulting command and applies every
// message it produced.
func pressRun(t *testing.T, m Model, k string) Model {
	t.Helper()
	m, cmd := updateCmd(t, m, keyMsg(k))
	for _, msg := range run(cmd) {
		m = update(t, m, msg)
	}
	return m
}

func TestCommentOpsReachClient(t *testing.T) {
	ov := &model.Override{UnitID: "u-refresh", Question: "breaking", Value: "no"}
	draft := model.Comment{Path: "web/src/session.ts", Line: 20, Side: model.SideNew, UnitID: "u-refresh", Body: "why?"}
	withItem := draft
	withItem.ItemID = "pr:1"
	tests := []struct {
		name    string
		op      codeview.CommentOp
		saves   []proto.CommentParams
		actions []proto.CommentActionParams
		ovs     []proto.OverrideParams
		note    string
	}{
		{name: "save", op: codeview.CommentOp{Kind: codeview.CommentOpSave, Comment: draft},
			saves: []proto.CommentParams{{ItemID: "pr:1", Comment: withItem}}, note: "draft saved"},
		{name: "delete", op: codeview.CommentOp{Kind: codeview.CommentOpDelete, Comment: model.Comment{ID: "d-1", ItemID: "pr:1"}},
			saves: []proto.CommentParams{{ItemID: "pr:1", Comment: model.Comment{ID: "d-1", ItemID: "pr:1"}, Delete: true}}, note: "draft deleted"},
		{name: "accept", op: codeview.CommentOp{Kind: codeview.CommentOpAccept, CommentID: "ai-k1"},
			actions: []proto.CommentActionParams{{ItemID: "pr:1", Action: proto.CommentAccept, CommentID: "ai-k1"}}, note: "finding accepted"},
		{name: "dismiss", op: codeview.CommentOp{Kind: codeview.CommentOpDismiss, CommentID: "ai-k1"},
			actions: []proto.CommentActionParams{{ItemID: "pr:1", Action: proto.CommentDismiss, CommentID: "ai-k1"}}, note: "finding dismissed"},
		{name: "dismiss with override", op: codeview.CommentOp{Kind: codeview.CommentOpDismiss, CommentID: "ai-k1", Override: ov},
			actions: []proto.CommentActionParams{{ItemID: "pr:1", Action: proto.CommentDismiss, CommentID: "ai-k1", Override: ov}},
			note:    "finding dismissed; label override filed"},
		{name: "reply", op: codeview.CommentOp{Kind: codeview.CommentOpReply, ThreadID: "T1", Body: "done"},
			actions: []proto.CommentActionParams{{ItemID: "pr:1", Action: proto.CommentReply, ThreadID: "T1", Body: "done"}}, note: "reply posted"},
		{name: "resolve", op: codeview.CommentOp{Kind: codeview.CommentOpResolve, ThreadID: "T1"},
			actions: []proto.CommentActionParams{{ItemID: "pr:1", Action: proto.CommentResolve, ThreadID: "T1"}}, note: "thread resolved"},
		{name: "override", op: codeview.CommentOp{Kind: codeview.CommentOpOverride, Override: ov},
			ovs: []proto.OverrideParams{{ItemID: "pr:1", Override: *ov}}, note: "label override filed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, f, p, _ := m4Model(t)
			m.tab, m.focus = TabCode, FocusMain
			p.opKeys = map[string]codeview.CommentOp{"C": tt.op}
			m = pressRun(t, m, "C")
			if !reflect.DeepEqual(f.cc.saves, tt.saves) {
				t.Errorf("saves = %+v, want %+v", f.cc.saves, tt.saves)
			}
			if !reflect.DeepEqual(f.cc.actions, tt.actions) {
				t.Errorf("actions = %+v, want %+v", f.cc.actions, tt.actions)
			}
			if !reflect.DeepEqual(f.cc.overrides, tt.ovs) {
				t.Errorf("overrides = %+v, want %+v", f.cc.overrides, tt.ovs)
			}
			if m.note != tt.note {
				t.Errorf("note = %q, want %q", m.note, tt.note)
			}
		})
	}
}

// ctrl+c with the composer open saves its text as a draft, then quits
// (spec: quit saves drafts; regression: the text was dropped).
func TestQuitSavesOpenComposer(t *testing.T) {
	m, f, p, _ := m4Model(t)
	m.tab, m.focus = TabCode, FocusMain
	draft := model.Comment{Path: "web/src/session.ts", Line: 20, Side: model.SideNew, UnitID: "u-refresh", Body: "half-typed"}
	p.composerOp = &codeview.CommentOp{Kind: codeview.CommentOpSave, Comment: draft}
	_, cmd := updateCmd(t, m, keyMsg("ctrl+c"))
	if cmd == nil {
		t.Fatal("no command")
	}
	// Run batches and sequences (both slices of commands) in order.
	var msgs []tea.Msg
	var flat func(c tea.Cmd)
	flat = func(c tea.Cmd) {
		if c == nil {
			return
		}
		msg := c()
		if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice {
			for i := range v.Len() {
				if sub, ok := v.Index(i).Interface().(tea.Cmd); ok {
					flat(sub)
				}
			}
			return
		}
		msgs = append(msgs, msg)
	}
	flat(cmd)
	saved, quit := -1, -1
	for i, msg := range msgs {
		switch msg.(type) {
		case commentDoneMsg:
			saved = i
		case tea.QuitMsg:
			quit = i
		}
	}
	if saved < 0 || quit < saved {
		t.Errorf("messages %v: want the save done, then quit", msgs)
	}
	want := draft
	want.ItemID = "pr:1"
	if !reflect.DeepEqual(f.cc.saves, []proto.CommentParams{{ItemID: "pr:1", Comment: want}}) {
		t.Errorf("saves = %+v", f.cc.saves)
	}
	// Nothing open: plain quit.
	if _, cmd := updateCmd(t, m, keyMsg("ctrl+c")); !isQuit(cmd) {
		t.Error("ctrl+c without a composer must quit at once")
	}
}

func TestCommentOpError(t *testing.T) {
	m, f, p, _ := m4Model(t)
	f.cc.commentErr = errors.New("boom")
	m.tab, m.focus = TabCode, FocusMain
	p.opKeys = map[string]codeview.CommentOp{"t": {Kind: codeview.CommentOpResolve, ThreadID: "T1"}}
	m = pressRun(t, m, "t")
	if m.note != "comment failed: boom" {
		t.Errorf("note = %q", m.note)
	}
}

// The Code page sees `a` first: only when it does not consume it does the
// root open the approve form (DECISIONS "M4 shared contracts").
func TestApproveKeyCodePageFirst(t *testing.T) {
	for _, consumed := range []bool{true, false} {
		t.Run(fmt.Sprint("consumed=", consumed), func(t *testing.T) {
			m, f, p, last := m4Model(t)
			m.tab, m.focus = TabCode, FocusMain
			if consumed {
				p.opKeys = map[string]codeview.CommentOp{"a": {Kind: codeview.CommentOpAccept, CommentID: "ai-k1"}}
			}
			m = pressRun(t, m, "a")
			if consumed {
				if m.form != nil || len(f.cc.actions) != 1 {
					t.Errorf("consumed a: form %v, actions %+v", m.form != nil, f.cc.actions)
				}
				return
			}
			if m.form == nil || last().spec.kind != formReview || last().v.Event != proto.EventApprove {
				t.Fatalf("a not consumed: want the approve form, got %+v", m.form)
			}
			if !strings.Contains(m.View(), "scripted form") {
				t.Error("form not drawn in the main pane")
			}
		})
	}
}

func TestSubmitForm(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		event   proto.ReviewEvent // preselected
		setBody string
		drafts  bool // keep the fixture's drafts
		submits []proto.SubmitParams
		formErr string
	}{
		{name: "S comment with drafts", key: "S", event: proto.EventComment, drafts: true,
			submits: []proto.SubmitParams{{ItemID: "pr:1", Event: proto.EventComment}}},
		{name: "a approve without anything", key: "a", event: proto.EventApprove,
			submits: []proto.SubmitParams{{ItemID: "pr:1", Event: proto.EventApprove}}},
		{name: "A request changes with summary", key: "A", event: proto.EventRequestChanges, setBody: " fix the lock \n",
			submits: []proto.SubmitParams{{ItemID: "pr:1", Event: proto.EventRequestChanges, Body: "fix the lock"}}},
		{name: "A without drafts or summary", key: "A", event: proto.EventRequestChanges,
			formErr: "nothing to send: write a summary or add drafts"},
		{name: "S comment without drafts or summary", key: "S", event: proto.EventComment,
			formErr: "nothing to send: write a summary or add drafts"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, f, _, last := m4Model(t)
			if !tt.drafts {
				m.detail.Comments = nil
			}
			f.cc.submitted = proto.SubmitResult{Posted: []model.Comment{{ID: "d-1"}}}
			m = pressRun(t, m, tt.key)
			d := last()
			if m.form == nil || d == nil || d.v.Event != tt.event {
				t.Fatalf("%s: form %v, preselected %+v", tt.key, m.form != nil, d)
			}
			d.v.Body = tt.setBody
			m = pressRun(t, m, "enter")
			if !reflect.DeepEqual(f.cc.submits, tt.submits) {
				t.Errorf("submits = %+v, want %+v", f.cc.submits, tt.submits)
			}
			if tt.formErr != "" {
				if m.form == nil || m.form.err != tt.formErr || !strings.Contains(m.View(), tt.formErr) {
					t.Errorf("want the form reopened with %q, got %+v", tt.formErr, m.form)
				}
				return
			}
			if m.form != nil || m.note != "review submitted (1 comment)" {
				t.Errorf("form %v, note %q", m.form != nil, m.note)
			}
		})
	}
}

func TestSubmitFormConfirmLineAndCancel(t *testing.T) {
	m, f, _, last := m4Model(t)
	m = pressRun(t, m, "S")
	// detail_since.json: one sendable draft, one outdated, one ai-draft.
	if got := last().spec.drafts.confirmLine(); got != "1 draft will be posted as line comments (1 outdated stay drafts)" {
		t.Errorf("confirm line = %q", got)
	}
	m = pressRun(t, m, "q") // captured by the form, not quit
	if m.form == nil || fmt.Sprint(last().keys) != "[q]" {
		t.Fatalf("form did not capture q: %v", last().keys)
	}
	m = pressRun(t, m, "esc")
	if m.form != nil || m.note != "cancelled" || len(f.cc.submits) != 0 {
		t.Errorf("esc: form %v note %q submits %v", m.form != nil, m.note, f.cc.submits)
	}
}

func TestSubmitFailure(t *testing.T) {
	m, f, _, _ := m4Model(t)
	f.cc.commentErr = errors.New("422 Unprocessable")
	m = pressRun(t, m, "S")
	m = pressRun(t, m, "enter")
	if m.note != "submit failed: 422 Unprocessable" {
		t.Errorf("note = %q", m.note)
	}
}

func TestCreatePRFormOnBranch(t *testing.T) {
	m, f, _, last := m4Model(t)
	m.picker.moveTo(1) // the branch
	m = feed[detailMsg](t, m, m.selectionChanged())
	// Approve and request changes need a PR: the verdict is never silently
	// turned into a COMMENT review on a new PR.
	for _, k := range []string{"A", "a"} {
		m = pressRun(t, m, k)
		if m.form != nil || !strings.Contains(m.note, "no PR yet") {
			t.Errorf("%s on a branch: form %v note %q", k, m.form != nil, m.note)
		}
	}
	m = pressRun(t, m, "S")
	if d := last(); m.form == nil || d.spec.kind != formCreatePR || d.v.Title != "Add feature x" || d.v.Base != "develop" {
		t.Fatalf("S on a branch: want the create-PR form prefilled, got %+v", d)
	}
	d := last()
	d.v.Body, d.v.Draft, d.v.Title = "Body text", true, " Feature x "
	f.cc.created = proto.CreatePRResult{Item: model.Item{ID: "pr:9", Kind: model.KindPR, Number: 9}, Attached: 2}
	m = pressRun(t, m, "enter")
	want := []proto.CreatePRParams{{ItemID: "branch:feat/x", Title: "Feature x", Body: "Body text", Base: "develop", Draft: true}}
	if !reflect.DeepEqual(f.cc.createPRs, want) {
		t.Errorf("createPRs = %+v, want %+v", f.cc.createPRs, want)
	}
	if m.note != "PR #9 created (2 comments attached)" {
		t.Errorf("note = %q", m.note)
	}
	// The new item appears with the next list: the picker moves to it.
	items := append(m4Items(), model.Item{ID: "pr:9", Kind: model.KindPR, Number: 9, Section: model.SectionYours})
	m = update(t, m, itemsMsg{items: items})
	if got := selectedID(m); got != "pr:9" {
		t.Errorf("selected %s, want the new PR", got)
	}
}

// Right after the picker moves, the new item's detail (and so its draft
// count) is still loading: S must not open a form claiming "0 drafts".
func TestSubmitWaitsForDetail(t *testing.T) {
	m, f, _, _ := m4Model(t)
	m.picker.moveTo(1) // the branch; its detail is not fed in
	for _, k := range []string{"S", "a", "A"} {
		m = update(t, m, keyMsg(k))
		if m.form != nil || !strings.Contains(m.note, "loading") {
			t.Errorf("%s before the detail loaded: form %v note %q", k, m.form != nil, m.note)
		}
	}
	if len(f.cc.submits) != 0 || len(f.cc.createPRs) != 0 {
		t.Errorf("sent: %v %v", f.cc.submits, f.cc.createPRs)
	}
}

func TestPipelineKeyOnBranchStaysPipeline(t *testing.T) {
	m, _, _, _ := m4Model(t)
	m.picker.moveTo(1)
	m = feed[detailMsg](t, m, m.selectionChanged())
	m = pressRun(t, m, "P")
	if m.tab != TabPipeline || m.form != nil {
		t.Errorf("P on a branch: tab %v form %v", m.tab, m.form != nil)
	}
}

// The real Huh form opens, shows the confirm line and cancels on esc,
// without a TTY.
func TestHuhSubmitForm(t *testing.T) {
	m, _, _, _ := m4Model(t)
	m.newDialog = nil
	m = update(t, m, keyMsg("A"))
	if m.form == nil {
		t.Fatal("A did not open the form")
	}
	v := m.View()
	for _, want := range []string{"Submit review · #1 one", "Request changes", "1 draft will be posted as line comments"} {
		if !strings.Contains(v, want) {
			t.Errorf("form view missing %q:\n%s", want, v)
		}
	}
	m = update(t, m, keyMsg("esc"))
	if m.form != nil || m.note != "cancelled" {
		t.Errorf("esc: form %v note %q", m.form != nil, m.note)
	}
}

func TestMarkViewedOncePerHeadWhenFocused(t *testing.T) {
	m, f, _, _ := m4Model(t)
	if len(f.cc.viewed) != 0 {
		t.Fatalf("viewed with the picker focused: %+v", f.cc.viewed)
	}
	for _, k := range []string{"enter", "2", "1", "tab", "tab", "j"} {
		m = pressRun(t, m, k)
	}
	want := []proto.ViewedParams{{ItemID: "pr:1", HeadSHA: "s1"}}
	if !reflect.DeepEqual(f.cc.viewed, want) {
		t.Fatalf("viewed = %+v, want %+v", f.cc.viewed, want)
	}
	// A new analysed head shown in the focused pane is marked again.
	d := *m.detail
	d.Item.AnalysedSHA = "s2"
	m, cmd := updateCmd(t, m, detailMsg{seq: m.detailSeq, d: d})
	run(cmd)
	want = append(want, proto.ViewedParams{ItemID: "pr:1", HeadSHA: "s2"})
	if !reflect.DeepEqual(f.cc.viewed, want) {
		t.Errorf("viewed = %+v, want %+v", f.cc.viewed, want)
	}
	// Not analysed yet: nothing to mark.
	d.Item.AnalysedSHA = ""
	m, cmd = updateCmd(t, m, detailMsg{seq: m.detailSeq, d: d})
	run(cmd)
	_ = m
	if len(f.cc.viewed) != 2 {
		t.Errorf("viewed without an analysed head: %+v", f.cc.viewed)
	}
}

func TestMarkViewedNotOnPipelinePage(t *testing.T) {
	m, f, _, _ := m4Model(t)
	m = pressRun(t, m, "P") // focuses the main pane on the Pipeline page
	if m.tab != TabPipeline || len(f.cc.viewed) != 0 {
		t.Errorf("Pipeline page marked viewed: %+v", f.cc.viewed)
	}
}

func TestTabCycleWithStrip(t *testing.T) {
	tests := []struct {
		name  string
		strip bool
		tab   Tab
		keys  []string
		want  []Focus
	}{
		{"code with strip", true, TabCode, []string{"tab", "tab", "tab"}, []Focus{FocusMain, FocusStrip, FocusPicker}},
		{"code with strip backwards", true, TabCode, []string{"shift+tab", "shift+tab", "shift+tab"}, []Focus{FocusStrip, FocusMain, FocusPicker}},
		{"code without strip", false, TabCode, []string{"tab", "tab"}, []Focus{FocusMain, FocusPicker}},
		{"overview ignores strip", true, TabOverview, []string{"tab", "tab"}, []Focus{FocusMain, FocusPicker}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _, p, _ := m4Model(t)
			p.hasStrip, m.tab = tt.strip, tt.tab
			for i, k := range tt.keys {
				m = pressRun(t, m, k)
				if m.focus != tt.want[i] {
					t.Fatalf("after %d× %s: focus %v, want %v", i+1, k, m.focus, tt.want[i])
				}
				if p.stripFocus != (m.focus == FocusStrip) {
					t.Fatalf("strip focus %v with root focus %v", p.stripFocus, m.focus)
				}
			}
		})
	}
}

func TestStripFocusRoutesKeysAndClears(t *testing.T) {
	m, _, p, _ := m4Model(t)
	p.hasStrip, m.tab = true, TabCode
	m = pressRun(t, m, "shift+tab")
	if m.focus != FocusStrip || !p.stripFocus {
		t.Fatalf("focus %v strip %v", m.focus, p.stripFocus)
	}
	m = pressRun(t, m, "n")
	if p.offered[len(p.offered)-1] != "n" {
		t.Errorf("strip keys must reach the Code page: %v", p.offered)
	}
	m = pressRun(t, m, "1") // Overview: no strip there
	if m.tab != TabOverview || m.focus != FocusMain || p.stripFocus {
		t.Errorf("after 1: tab %v focus %v strip %v", m.tab, m.focus, p.stripFocus)
	}
	if fmt.Sprint(p.stripCalls) != "[true false]" {
		t.Errorf("SetStripFocus calls = %v", p.stripCalls)
	}
}

func TestStatusShowsDraftCount(t *testing.T) {
	m, _, _, _ := m4Model(t)
	if s := m.viewHeader(); !strings.Contains(s, "2 drafts") {
		t.Errorf("header = %q", s)
	}
	m.detail.Comments = nil
	if s := m.viewHeader(); strings.Contains(s, "draft") {
		t.Errorf("header with no drafts = %q", s)
	}
}

func TestRebindM4Keys(t *testing.T) {
	km, err := KeyMapFromConfig(map[string]string{
		"approve": "ctrl+a", "request_changes": "ctrl+x", "submit": "ctrl+t",
		"comment": "c", "visual": "v", "save_draft": "ctrl+w", "accept": "y", "dismiss": "D",
		"override": "?", "thread": "T", "resolve": "ctrl+e",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(km.Approve.Keys(), km.RequestChanges.Keys(), km.Submit.Keys()); got != "[ctrl+a] [ctrl+x] [ctrl+t]" {
		t.Errorf("review keys = %s", got)
	}
	ck := km.codeKeys()
	got := fmt.Sprint(ck.Comment.Keys(), ck.Visual.Keys(), ck.SaveDraft.Keys(), ck.Accept.Keys(), ck.Dismiss.Keys(),
		ck.OverrideLabel.Keys(), ck.Thread.Keys(), ck.Resolve.Keys(), ck.StripJump.Keys())
	if got != "[c] [v] [ctrl+w] [y] [D] [?] [T] [ctrl+e] [enter]" {
		t.Errorf("code comment keys = %s", got)
	}
	def := DefaultKeyMap().codeKeys()
	got = fmt.Sprint(def.Comment.Keys(), def.Visual.Keys(), def.SaveDraft.Keys(), def.Accept.Keys(), def.Dismiss.Keys(),
		def.OverrideLabel.Keys(), def.Thread.Keys(), def.Resolve.Keys())
	if got != "[C] [V] [ctrl+s] [a] [d] [!] [t] [ctrl+r]" {
		t.Errorf("default code comment keys = %s", got)
	}
	acts := strings.Join(KeyActions(), " ")
	for _, a := range []string{"approve", "request_changes", "submit", "comment", "visual", "save_draft", "accept", "dismiss", "override", "thread", "resolve"} {
		if !strings.Contains(acts, a) {
			t.Errorf("KeyActions missing %s", a)
		}
	}

	// A rebound submit key opens the form; the old one no longer does.
	f := newFake(m4Items()...)
	m := newTestModel(t, f, Options{Keys: &km})
	m = feed[detailMsg](t, m, m.selectionChanged())
	m, _ = withScriptedForms(m)
	m = pressRun(t, m, "S")
	if m.form != nil {
		t.Error("S still opens the form after rebinding")
	}
	m = pressRun(t, m, "ctrl+t")
	if m.form == nil {
		t.Error("rebound submit key did not open the form")
	}
}

func TestHelpListsReviewKeys(t *testing.T) {
	m, _, _, _ := m4Model(t)
	m = pressRun(t, m, "?")
	v := m.View()
	for _, want := range []string{"submit review", "approve", "request changes"} {
		if !strings.Contains(v, want) {
			t.Errorf("help missing %q:\n%s", want, v)
		}
	}
}

func TestOverviewSince(t *testing.T) {
	since := loadDetail(t, "detail_since.json")
	got, anchors := renderOverviewText(since)
	golden(t, "overview_since", got+"\n")
	lines := strings.Split(got, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "  web/src/session.ts:12") && anchors[i] != "u-refresh" {
			t.Errorf("changed-unit row %q anchors %q", l, anchors[i])
		}
	}

	many := loadDetail(t, "detail_since.json")
	s := *many.Agg.Since
	s.ChangedUnits = []string{"u-refresh", "u-expired", "u-devices", "u-test", "u-server", "u-format", "u-gone"}
	s.Comments = nil
	s.Note = "base head not available locally"
	s.Source = model.SinceViewed
	many.Agg = &model.ItemAgg{Since: &s}

	unsure := loadDetail(t, "detail_since.json")
	u := *unsure.Agg.Since
	u.Comments = []model.AddressedComment{
		{Path: "a.go", Line: 3, Excerpt: "maybe", Known: true, Addressed: true, P: 0.55},
		{Path: "b.go", Line: 4, Excerpt: "who knows", Known: false},
		{Path: "c.go", Line: 5, Excerpt: "done", Known: true, Addressed: true, P: 0.9, Resolved: true},
	}
	unsure.Agg.Since = &u

	branch := loadDetail(t, "detail_since.json")
	branch.Item.Kind, branch.Item.Number = model.KindBranch, 0

	none := loadDetail(t, "detail_full.json")

	tests := []struct {
		name string
		d    proto.Detail
		want []string
	}{
		{"first look", none, []string{"Since you looked", "nothing to compare yet (first look)"}},
		{"many units, no comments", many, []string{
			"since e661461 (you looked) · 7 units changed", "  base head not available locally",
			"  web/src/session.ts:12", "  server/api/session.go:20", "  … 2 more", "  no earlier comments of yours"}},
		{"unsure, unknown, resolved", unsure, []string{
			"? ✓ addressed .55  a.go:3  maybe", "  ? unknown  b.go:4  who knows", "  ✓ addressed .90  c.go:5  done  resolved"}},
	}
	// A branch has the branch state box instead (spec Overview, Branches).
	if got, _ := renderOverviewText(branch); sinceSection(got) != "" || !strings.Contains(got, blockBranch) {
		t.Errorf("branch overview has a Since block or lacks the branch state:\n%s", got)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := renderOverviewText(tt.d)
			sec := sinceSection(got)
			for _, w := range tt.want {
				if !strings.Contains(sec, w) {
					t.Errorf("since block missing %q:\n%s", w, sec)
				}
			}
			if strings.Contains(sec, "server/api/format.go:1") {
				t.Error("more than five changed units listed")
			}
		})
	}
}

// sinceSection cuts the "Since you looked" block out of an Overview.
func sinceSection(page string) string {
	i := strings.Index(page, blockSince)
	if i < 0 {
		return ""
	}
	rest := page[i:]
	if j := strings.Index(rest, "└"); j >= 0 { // the box's bottom border
		rest = rest[:j]
	}
	return rest
}
