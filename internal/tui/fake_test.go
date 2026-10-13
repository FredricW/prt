package tui

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeview"
	"github.com/fredricw/prt/internal/proto"
)

// fakeClient is a canned daemon.Client recording every call.
type fakeClient struct {
	mu        sync.Mutex
	items     []model.Item
	details   map[string]proto.Detail
	events    chan proto.Event
	dirty     bool
	discovers int
	refreshes []proto.ItemParams
	checkouts []proto.CheckoutParams
	detailIDs []string
	// groupEdits records EditGroups calls (Code page ⇧M / X); editErr is
	// returned by EditGroups when set.
	groupEdits []proto.GroupEditParams
	editErr    error
	// M3 review-pipeline calls (Pipeline page D/s/e/c/$, Code page R);
	// routing and calls are the canned answers of Routing and CallLog.
	reviewActions []proto.ReviewActionParams
	routingCalls  []proto.RoutingParams
	callLogs      []proto.CallLogParams
	caps          []proto.CapParams
	routing       proto.RoutingInfo
	calls         []model.ReviewCall
}

var _ daemon.Client = (*fakeClient)(nil)

func newFake(items ...model.Item) *fakeClient {
	return &fakeClient{items: items, details: map[string]proto.Detail{}, events: make(chan proto.Event, 16)}
}

func (f *fakeClient) Discover(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.discovers++
	return nil
}

func (f *fakeClient) Items(context.Context) ([]model.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]model.Item(nil), f.items...), nil
}

func (f *fakeClient) Detail(_ context.Context, id string) (proto.Detail, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detailIDs = append(f.detailIDs, id)
	if d, ok := f.details[id]; ok {
		return d, nil
	}
	for _, it := range f.items {
		if it.ID == id {
			return proto.Detail{Item: it}, nil
		}
	}
	return proto.Detail{}, errors.New("not found")
}

func (f *fakeClient) Refresh(_ context.Context, p proto.ItemParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes = append(f.refreshes, p)
	return nil
}

func (f *fakeClient) Add(context.Context, string) (model.Item, error) {
	return model.Item{}, errors.New("not implemented")
}

func (f *fakeClient) Remove(context.Context, string) error { return nil }

func (f *fakeClient) Checkout(_ context.Context, p proto.CheckoutParams) (proto.CheckoutResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkouts = append(f.checkouts, p)
	res := proto.CheckoutResult{Ref: "feat/x"}
	if f.dirty && !p.Force {
		res.Dirty = true
		return res, nil
	}
	res.CheckedOut = true
	return res, nil
}

func (f *fakeClient) EditGroups(_ context.Context, p proto.GroupEditParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupEdits = append(f.groupEdits, p)
	return f.editErr
}

func (f *fakeClient) ReviewAction(_ context.Context, p proto.ReviewActionParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviewActions = append(f.reviewActions, p)
	return nil
}

func (f *fakeClient) Routing(_ context.Context, p proto.RoutingParams) (proto.RoutingInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.routingCalls = append(f.routingCalls, p)
	return f.routing, nil
}

func (f *fakeClient) CallLog(_ context.Context, p proto.CallLogParams) ([]model.ReviewCall, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.callLogs = append(f.callLogs, p)
	return f.calls, nil
}

func (f *fakeClient) SetCap(_ context.Context, p proto.CapParams) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.caps = append(f.caps, p)
	return nil
}

// listenerStarted is signalled each time a command asks for the event
// channel: that command is the (blocking) event listener, so run abandons it.
var listenerStarted = make(chan struct{}, 64)

func (f *fakeClient) Events() <-chan proto.Event {
	select {
	case listenerStarted <- struct{}{}:
	default:
	}
	return f.events
}
func (f *fakeClient) Close() error { return nil }

// fakePane is a recording codePane: it handles NextUnit/PrevUnit, opens a
// "filters bar" on Filters (capturing every key until Close), toggles its
// mode on Mode, and records every key it was offered and every FocusUnit
// call. pending is handed out (once) by PendingEdit.
type fakePane struct {
	keys      codeview.Keys
	offered   []string // every key passed to HandleKey
	captured  []string // keys received while capturing
	capturing bool
	focused   []string
	details   []string // item ids passed to SetDetail
	clears    int
	focusOK   bool
	mode      codeview.Mode
	pending   *model.GroupEdit
	// review is handed out (once) by PendingReview; R sets it to "g-r".
	review string
}

func (p *fakePane) PendingReview() (string, bool) {
	if p.review == "" {
		return "", false
	}
	g := p.review
	p.review = ""
	return g, true
}

var _ codePane = (*fakePane)(nil)

func (p *fakePane) SetKeys(k codeview.Keys)       { p.keys = k }
func (p *fakePane) SetSize(int, int)              {}
func (p *fakePane) SetDetail(d proto.Detail)      { p.details = append(p.details, d.Item.ID) }
func (p *fakePane) Clear()                        { p.clears++ }
func (p *fakePane) Update(tea.Msg) tea.Cmd        { return nil }
func (p *fakePane) Capturing() bool               { return p.capturing }
func (p *fakePane) HelpBindings() [][]key.Binding { return [][]key.Binding{{p.keys.NextUnit}} }
func (p *fakePane) ShortBindings() []key.Binding  { return []key.Binding{p.keys.NextUnit} }
func (p *fakePane) View() string                  { return "fake code pane" }
func (p *fakePane) Mode() codeview.Mode           { return p.mode }

func (p *fakePane) SetMode(md codeview.Mode) bool {
	p.mode = md
	return true
}

func (p *fakePane) PendingEdit() (model.GroupEdit, bool) {
	if p.pending == nil {
		return model.GroupEdit{}, false
	}
	e := *p.pending
	p.pending = nil
	return e, true
}

func (p *fakePane) FocusUnit(id string) bool {
	p.focused = append(p.focused, id)
	return p.focusOK
}

func (p *fakePane) HandleKey(k tea.KeyMsg) bool {
	p.offered = append(p.offered, k.String())
	if p.capturing {
		p.captured = append(p.captured, k.String())
		if key.Matches(k, p.keys.Close) {
			p.capturing = false
		}
		return true
	}
	switch {
	case key.Matches(k, p.keys.Filters):
		p.capturing = true
		return true
	case key.Matches(k, p.keys.NextUnit), key.Matches(k, p.keys.PrevUnit):
		return true
	case key.Matches(k, p.keys.Mode):
		p.mode = 1 - p.mode
		return true
	case key.Matches(k, p.keys.ReReview):
		p.review = "g-r"
		return true
	}
	return false
}

// fakePipe is a recording pipePane: it handles the movement keys and the
// action keys (each queues the action set in actions[key]), opens a
// "prompt" on RaiseCap that captures every key until Close, and records
// everything the root model tells it.
type fakePipe struct {
	keys      pipeview.Keys
	offered   []string
	captured  []string
	capturing bool
	details   []string
	clears    int
	nows      []time.Time
	notes     []string
	callLogs  []string // group ids passed to SetCallLog
	callN     []int    // number of calls passed with each
	actions   map[string]pipeview.Action
	pending   *pipeview.Action
	origin    [2]int // last SetOrigin
}

var _ pipePane = (*fakePipe)(nil)

func newFakePipe() *fakePipe { return &fakePipe{actions: map[string]pipeview.Action{}} }

func (p *fakePipe) SetOrigin(x, y int)            { p.origin = [2]int{x, y} }
func (p *fakePipe) SetKeys(k pipeview.Keys)       { p.keys = k }
func (p *fakePipe) SetSize(int, int)              {}
func (p *fakePipe) SetDetail(d proto.Detail)      { p.details = append(p.details, d.Item.ID) }
func (p *fakePipe) Clear()                        { p.clears++ }
func (p *fakePipe) SetNow(t time.Time)            { p.nows = append(p.nows, t) }
func (p *fakePipe) SetNote(s string)              { p.notes = append(p.notes, s) }
func (p *fakePipe) Update(tea.Msg) tea.Cmd        { return nil }
func (p *fakePipe) Capturing() bool               { return p.capturing }
func (p *fakePipe) HelpBindings() [][]key.Binding { return [][]key.Binding{{p.keys.ForceDeep}} }
func (p *fakePipe) ShortBindings() []key.Binding  { return []key.Binding{p.keys.ForceDeep} }
func (p *fakePipe) View() string                  { return "fake pipeline page" }

func (p *fakePipe) SetCallLog(groupID string, calls []model.ReviewCall, _ error) {
	p.callLogs = append(p.callLogs, groupID)
	p.callN = append(p.callN, len(calls))
}

func (p *fakePipe) PendingAction() (pipeview.Action, bool) {
	if p.pending == nil {
		return pipeview.Action{}, false
	}
	a := *p.pending
	p.pending = nil
	return a, true
}

func (p *fakePipe) HandleKey(k tea.KeyMsg) bool {
	p.offered = append(p.offered, k.String())
	if p.capturing {
		p.captured = append(p.captured, k.String())
		if key.Matches(k, p.keys.Close) {
			p.capturing = false
		}
		return true
	}
	if key.Matches(k, p.keys.RaiseCap) && p.actions[k.String()].Kind == pipeview.ActNone {
		p.capturing = true
		return true
	}
	if a, ok := p.actions[k.String()]; ok {
		p.pending = &a
		return true
	}
	return key.Matches(k, p.keys.Down) || key.Matches(k, p.keys.Up)
}

// withFakePipe installs a recording Pipeline page keyed like m.
func withFakePipe(m Model) (Model, *fakePipe) {
	p := newFakePipe()
	p.SetKeys(m.keys.pipeKeys())
	m.pipe = p
	return m, p
}

// withFakePane installs a recording Code pane keyed like m.
func withFakePane(m Model) (Model, *fakePane) {
	p := &fakePane{}
	p.SetKeys(m.keys.codeKeys())
	m.code = p
	return m, p
}

// fixedNow is the clock used by every test.
var fixedNow = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func plainRenderer() *lipgloss.Renderer { return lipgloss.NewRenderer(io.Discard) }

// newTestModel returns a sized, colour-free model fed with the fake's items.
func newTestModel(t *testing.T, f *fakeClient, o Options) Model {
	t.Helper()
	if o.Now == nil {
		o.Now = func() time.Time { return fixedNow }
	}
	m := NewWithOptions(f, plainRenderer(), o)
	m = update(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	items, _ := f.Items(context.Background())
	m = update(t, m, itemsMsg{items: items})
	return m
}

// update applies msg and returns the root model (dropping the command).
func update(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

// updateCmd applies msg and returns the root model and command.
func updateCmd(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// cmdTimeout bounds a non-listener command; exceeding it is a test bug.
const cmdTimeout = 10 * time.Second

// run executes cmd, expanding batches, and returns the produced messages.
// The event listener (detected through fakeClient.Events) blocks by design
// and is abandoned; every other command runs to completion, and one that
// hangs fails loudly instead of being silently dropped.
func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	for drained := false; !drained; {
		select {
		case <-listenerStarted:
		default:
			drained = true
		}
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-ch:
	case <-listenerStarted:
		select { // a listener with an event already queued still answers
		case msg = <-ch:
		case <-time.After(10 * time.Millisecond):
			return nil
		}
	case <-time.After(cmdTimeout):
		panic("tui test: command did not finish within " + cmdTimeout.String())
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

// feed runs cmd and applies every resulting message of type T to m.
func feed[T any](t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for _, msg := range run(cmd) {
		if _, ok := msg.(T); ok {
			m = update(t, m, msg)
		}
	}
	return m
}

// mouseClick is a left-button press at cell (x, y).
func mouseClick(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

// keyMsg builds a key message from its bubbles/key name.
func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}
