// Package tui is the Bubble Tea front end. The root Model holds the picker
// (sidebar, always visible on the left), the main pane (the label-driven
// Overview and the Code tab, which wraps internal/codeview) and a one-line
// status bar. It talks to the analysis side only
// through daemon.Client; long operations are tea.Cmds returning messages and
// never run in Update.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// Focus is which pane has keyboard focus.
type Focus int

// Focus targets. The bottom strip (spec: tab cycles picker → main → strip)
// arrives with later milestones.
const (
	FocusPicker Focus = iota
	FocusMain
)

// Tab is the main-pane tab.
type Tab int

// Main-pane tabs. Architecture (3) is shown as "later" until M5.
const (
	TabOverview Tab = iota
	TabCode
)

// Options configures the root model; the zero value is usable.
type Options struct {
	// Keys overrides the key map (see KeyMapFromConfig); nil means defaults.
	Keys *KeyMap
	// Open opens a URL in the browser (key `o`); nil shows "no opener".
	Open func(url string) error
	// RefreshEvery is the periodic discovery interval (spec: every 60 s);
	// 0 disables it.
	RefreshEvery time.Duration
	// Now returns the current time for ages; nil means time.Now. Snapshots
	// and tests inject a fixed clock.
	Now func() time.Time
}

// Messages.
type (
	itemsMsg struct {
		items []model.Item
		err   error // discovery error; items are still the stored ones
	}
	detailMsg struct {
		seq int
		d   proto.Detail
	}
	eventMsg       proto.Event
	errMsg         struct{ err error }
	closedMsg      struct{}
	refreshTickMsg struct{}
	checkoutMsg    struct {
		itemID string
		res    proto.CheckoutResult
		err    error
	}
	openedMsg struct {
		url string
		err error
	}
	refreshedMsg struct {
		label string
		err   error
	}
	autoRefreshedMsg struct{ errs []error }
	// reloadedMsg answers the single pending list reload that item events
	// trigger (see onEvent).
	reloadedMsg struct {
		items []model.Item
		err   error
	}
)

// confirmCheckout is a pending "tree is dirty, checkout anyway?" prompt.
type confirmCheckout struct {
	itemID, ref string
}

// Model is the root model.
type Model struct {
	client daemon.Client
	opts   Options
	keys   KeyMap
	r      *lipgloss.Renderer
	st     styles
	md     *markdown

	picker   picker
	focus    Focus
	tab      Tab
	showHelp bool
	help     help.Model
	spin     spinner.Model
	spinning bool

	detail        *proto.Detail
	detailSeq     int
	detailPending bool
	// reloadPending is set while an event-triggered list reload is in
	// flight, so a burst of item events causes one reload, not one each.
	reloadPending, reloadAgain bool
	overview                   overview
	code                       codePane

	requested map[string]string      // item id → head SHA auto-refreshed
	stages    map[string]model.Stage // item id → latest stage seen

	note    string // transient; cleared on the next key press
	logNote string // latest daemon log note ("GitHub unavailable")
	confirm *confirmCheckout

	width, height int
}

// New builds the root model with default options. r may be nil (default
// renderer).
func New(c daemon.Client, r *lipgloss.Renderer) Model {
	return NewWithOptions(c, r, Options{})
}

// NewWithOptions builds the root model. r may be nil (default renderer).
func NewWithOptions(c daemon.Client, r *lipgloss.Renderer, o Options) Model {
	if r == nil {
		r = lipgloss.DefaultRenderer()
	}
	km := DefaultKeyMap()
	if o.Keys != nil {
		km = *o.Keys
	}
	h := help.New()
	h.Styles = helpStyles(r)
	m := Model{
		client:    c,
		opts:      o,
		keys:      km,
		r:         r,
		st:        newStyles(r),
		md:        newMarkdown(r),
		help:      h,
		spin:      spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(r.NewStyle())),
		overview:  newOverview(),
		code:      newCodePane(r),
		requested: map[string]string{},
		stages:    map[string]model.Stage{},
		width:     120,
		height:    40,
	}
	m.code.SetKeys(km.codeKeys())
	m.layout()
	return m
}

func (m Model) now() time.Time {
	if m.opts.Now != nil {
		return m.opts.Now()
	}
	return time.Now()
}

// Init implements tea.Model: show the stored items at once (the TUI starts
// against the cache), then discover, listen for events, schedule refresh.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadItems(), m.discover(), m.listen(), m.scheduleRefresh())
}

// ---- commands -------------------------------------------------------------

func (m Model) discover() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		ctx := context.Background()
		derr := c.Discover(ctx)
		items, err := c.Items(ctx)
		if err != nil {
			return errMsg{fmt.Errorf("load items: %w", err)}
		}
		if derr != nil {
			derr = fmt.Errorf("discover: %w", derr)
		}
		return itemsMsg{items: items, err: derr}
	}
}

func (m Model) loadItems() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		items, err := c.Items(context.Background())
		if err != nil {
			return errMsg{fmt.Errorf("load items: %w", err)}
		}
		return itemsMsg{items: items}
	}
}

// reloadItems is loadItems for event-driven reloads; its answer clears
// reloadPending even on error.
func (m Model) reloadItems() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		items, err := c.Items(context.Background())
		return reloadedMsg{items: items, err: err}
	}
}

// queueReload starts an event-driven list reload unless one is in flight.
func (m *Model) queueReload() tea.Cmd {
	if m.reloadPending {
		// The in-flight read may predate this event: read once more after.
		m.reloadAgain = true
		return nil
	}
	m.reloadPending = true
	return m.reloadItems()
}

func (m Model) loadDetail(id string, seq int) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		d, err := c.Detail(context.Background(), id)
		if err != nil {
			return errMsg{fmt.Errorf("load %s: %w", id, err)}
		}
		return detailMsg{seq: seq, d: d}
	}
}

func (m Model) listen() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		e, ok := <-c.Events()
		if !ok {
			return closedMsg{}
		}
		return eventMsg(e)
	}
}

func (m Model) scheduleRefresh() tea.Cmd {
	if m.opts.RefreshEvery <= 0 {
		return nil
	}
	return tea.Tick(m.opts.RefreshEvery, func(time.Time) tea.Msg { return refreshTickMsg{} })
}

// autoRefresh starts analysis for ids in order (daemon order is urgency
// order, so the top of the list is ready first).
func (m Model) autoRefresh(ids []string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		var errs []error
		for _, id := range ids {
			if err := c.Refresh(context.Background(), proto.ItemParams{ItemID: id}); err != nil {
				errs = append(errs, fmt.Errorf("refresh %s: %w", id, err))
			}
		}
		return autoRefreshedMsg{errs: errs}
	}
}

func (m Model) refreshItem(it model.Item) tea.Cmd {
	c, label := m.client, itemLabel(it)
	return func() tea.Msg {
		err := c.Refresh(context.Background(), proto.ItemParams{ItemID: it.ID, Force: true})
		return refreshedMsg{label: label, err: err}
	}
}

func (m Model) checkout(id string, force bool) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		res, err := c.Checkout(context.Background(), proto.CheckoutParams{ItemID: id, Force: force})
		return checkoutMsg{itemID: id, res: res, err: err}
	}
}

func (m Model) open(url string) tea.Cmd {
	open := m.opts.Open
	return func() tea.Msg { return openedMsg{url: url, err: open(url)} }
}

// ---- update ---------------------------------------------------------------

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case itemsMsg:
		return m.onItems(msg)
	case reloadedMsg:
		m.reloadPending = false
		var again tea.Cmd
		if m.reloadAgain {
			m.reloadAgain = false
			again = m.queueReload()
		}
		if msg.err != nil {
			m.note = "error: load items: " + msg.err.Error()
			return m, again
		}
		next, cmd := m.onItems(itemsMsg{items: msg.items})
		return next, tea.Batch(cmd, again)
	case detailMsg:
		if msg.seq == m.detailSeq {
			m.applyDetail(msg.d)
		}
		return m, nil
	case eventMsg:
		return m.onEvent(proto.Event(msg))
	case spinner.TickMsg:
		if !m.picker.busy() {
			m.spinning = false
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	case refreshTickMsg:
		return m, tea.Batch(m.discover(), m.scheduleRefresh())
	case checkoutMsg:
		return m.onCheckout(msg), nil
	case openedMsg:
		if msg.err != nil {
			m.note = "open failed: " + msg.err.Error()
		} else {
			m.note = "opened " + msg.url
		}
		return m, nil
	case refreshedMsg:
		if msg.err != nil {
			m.note = "refresh failed: " + msg.err.Error()
		}
		return m, nil
	case autoRefreshedMsg:
		if len(msg.errs) > 0 {
			m.note = msg.errs[0].Error()
		}
		return m, nil
	case errMsg:
		m.note = "error: " + msg.err.Error()
		return m, nil
	case closedMsg:
		m.logNote = "daemon connection lost; restart prt"
		return m, nil
	}
	return m, nil
}

// handleMouse implements the spec's mouse support: the wheel scrolls the
// pane under the pointer, a left click on a picker row selects that item.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.confirm != nil || m.showHelp {
		return m, nil
	}
	sw, _, bh := m.dims()
	inPicker := msg.X < sw && msg.Y < bh
	wheel := msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown
	switch {
	case inPicker && wheel:
		delta := 1
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		if m.picker.move(delta) {
			return m, m.selectionChanged()
		}
		return m, nil
	case inPicker && msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		i := m.picker.itemAt(msg.Y)
		if i < 0 {
			return m, nil
		}
		m.focus = FocusPicker
		if m.picker.moveTo(i) {
			return m, m.selectionChanged()
		}
		return m, nil
	case inPicker:
		return m, nil
	case m.tab == TabCode:
		return m, m.code.Update(msg)
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress && !m.overview.raw:
		// Row 0 of the main pane is the tab bar; a click on an anchored
		// Overview line (minimap row, headline, …) opens that unit in Code.
		// The main pane starts after the sidebar, its right border and a
		// one-cell gap (View).
		if id, ok := m.overview.clickAt(m.overview.vp.YOffset+msg.Y-1, msg.X-sw-2); ok && m.detail != nil {
			m.focus = FocusMain
			m.jumpToUnit(id)
		}
	case wheel && m.overview.raw:
		if msg.Button == tea.MouseButtonWheelUp {
			m.overview.rawVP.ScrollUp(3)
		} else {
			m.overview.rawVP.ScrollDown(3)
		}
	case wheel:
		if msg.Button == tea.MouseButtonWheelUp {
			m.overview.vp.ScrollUp(3)
		} else {
			m.overview.vp.ScrollDown(3)
		}
	}
	return m, nil
}

func (m Model) onItems(msg itemsMsg) (tea.Model, tea.Cmd) {
	changed := m.picker.setItems(msg.items)
	if msg.err != nil {
		m.note = msg.err.Error()
	}
	var cmds []tea.Cmd
	var ids []string
	for _, it := range m.picker.items {
		// Only never-analysed items start automatically; a stale item waits
		// for `r` (spec: "stale (new push since analysis; r refreshes)").
		if it.State != "" {
			continue
		}
		if prev, ok := m.requested[it.ID]; ok && prev == it.HeadSHA {
			continue
		}
		m.requested[it.ID] = it.HeadSHA
		ids = append(ids, it.ID)
	}
	if len(ids) > 0 {
		cmds = append(cmds, m.autoRefresh(ids))
	}
	if changed || (m.detail == nil && !m.detailPending) {
		cmds = append(cmds, m.selectionChanged())
	}
	cmds = append(cmds, m.ensureSpin())
	return m, tea.Batch(cmds...)
}

func (m Model) onEvent(e proto.Event) (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{m.listen()}
	switch e.Type {
	case proto.EventItem:
		if e.Item != nil {
			m.picker.patch(*e.Item)
		}
		cmds = append(cmds, m.queueReload(), m.ensureSpin())
	case proto.EventRemove:
		cmds = append(cmds, m.queueReload())
	case proto.EventStage:
		if e.Stage != "" {
			m.stages[e.ItemID] = e.Stage
		}
	case proto.EventLog:
		m.logNote = e.Note
	}
	if it, ok := m.picker.selected(); ok && it.ID == e.ItemID && e.Type != proto.EventRemove {
		m.detailSeq++
		cmds = append(cmds, m.loadDetail(it.ID, m.detailSeq))
	}
	return m, tea.Batch(cmds...)
}

func (m Model) onCheckout(msg checkoutMsg) Model {
	switch {
	case msg.err != nil:
		m.note = "checkout failed: " + msg.err.Error()
	case msg.res.Dirty:
		m.confirm = &confirmCheckout{itemID: msg.itemID, ref: msg.res.Ref}
	case msg.res.CheckedOut:
		m.note = "checked out " + msg.res.Ref
	default:
		m.note = "checkout: nothing done"
	}
	return m
}

// ensureSpin starts the spinner when an item is busy and it is not running.
func (m *Model) ensureSpin() tea.Cmd {
	if !m.picker.busy() || m.spinning {
		return nil
	}
	m.spinning = true
	return m.spin.Tick
}

// selectionChanged clears the main pane and loads the selected item's
// detail. Each load carries a sequence number; only the latest is applied,
// so fast cursor movement never shows a stale item.
func (m *Model) selectionChanged() tea.Cmd {
	m.detail = nil
	m.detailSeq++
	m.code.Clear()
	m.overview.setContent("", nil)
	m.overview.closeRaw()
	it, ok := m.picker.selected()
	if !ok {
		m.detailPending = false
		return nil
	}
	m.detailPending = true
	return m.loadDetail(it.ID, m.detailSeq)
}

// applyDetail shows d in the main pane. A reload of the same item keeps the
// Overview scroll position; the Code page keeps its own state (codeview
// SetDetail contract).
func (m *Model) applyDetail(d proto.Detail) {
	same := m.detail != nil && m.detail.Item.ID == d.Item.ID
	m.code.SetDetail(d)
	m.detail = &d
	m.detailPending = false
	y := m.overview.vp.YOffset
	m.renderOverview()
	if same {
		m.overview.vp.SetYOffset(y)
	}
}

func (m *Model) renderOverview() {
	if m.detail == nil {
		return
	}
	_, mw, _ := m.dims()
	m.overview.setPage(overviewPage(m.st, m.md, *m.detail, m.now(), mw-2))
}

// jumpToUnit switches to the Code tab and focuses unit id there.
func (m *Model) jumpToUnit(id string) {
	m.tab = TabCode
	if !m.code.FocusUnit(id) {
		m.note = "unit not found in Code"
	}
}

// ---- keys -----------------------------------------------------------------

func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirm != nil {
		return m.handleConfirm(k)
	}
	m.note = ""
	km := m.keys
	if k.String() == "ctrl+c" {
		return m, tea.Quit
	}
	if !m.showHelp && m.focus == FocusMain {
		if next, ok := m.routeMain(k); ok {
			return next, nil
		}
	}
	if key.Matches(k, km.Quit) {
		return m, tea.Quit
	}
	if m.showHelp {
		if key.Matches(k, km.Help) || key.Matches(k, km.Back) {
			m.showHelp = false
		}
		return m, nil
	}
	if next, cmd, ok := m.handleGlobalKey(k); ok {
		return next, cmd
	}
	if m.focus == FocusPicker {
		return m.handlePickerKey(k)
	}
	return m.handleMainKey(k), nil
}

// routeMain gives the focused main pane the first look at k. The Code page
// takes every key while it captures (filters bar, raw labels: so 1–9 toggle
// filters instead of switching tabs) and otherwise any key it handles; the
// Overview's raw-labels overlay captures every key. ok is false when k
// falls through to the global and pane keys.
func (m Model) routeMain(k tea.KeyMsg) (Model, bool) {
	switch {
	case m.tab == TabCode && m.code.Capturing():
		m.code.HandleKey(k)
		return m, true
	case m.tab == TabCode:
		return m, m.code.HandleKey(k)
	case m.overview.raw:
		m.handleRawKey(k)
		return m, true
	}
	return m, false
}

// handleRawKey drives the Overview's raw-labels overlay: L or esc closes it,
// scroll keys scroll it, anything else is ignored.
func (m *Model) handleRawKey(k tea.KeyMsg) {
	km, vp := m.keys, &m.overview.rawVP
	switch {
	case key.Matches(k, km.RawLabels), key.Matches(k, km.Back):
		m.overview.closeRaw()
	case key.Matches(k, km.Down):
		vp.ScrollDown(1)
	case key.Matches(k, km.Up):
		vp.ScrollUp(1)
	case key.Matches(k, km.PageDown):
		vp.PageDown()
	case key.Matches(k, km.PageUp):
		vp.PageUp()
	case key.Matches(k, km.Top):
		vp.GotoTop()
	case key.Matches(k, km.Bottom):
		vp.GotoBottom()
	}
}

// handleGlobalKey handles keys that work in any pane; ok is false when k is
// not one of them.
func (m Model) handleGlobalKey(k tea.KeyMsg) (Model, tea.Cmd, bool) {
	km := m.keys
	switch {
	case key.Matches(k, km.Help):
		m.showHelp = true
	case key.Matches(k, km.NextFocus), key.Matches(k, km.PrevFocus):
		// Two panes until the bottom strip exists, so both directions toggle.
		if m.focus == FocusPicker {
			m.focus = FocusMain
		} else {
			m.focus = FocusPicker
		}
	case key.Matches(k, km.Back):
		m.focus = FocusPicker
	case key.Matches(k, km.Overview):
		m.tab = TabOverview
	case key.Matches(k, km.Code):
		m.tab = TabCode
	case key.Matches(k, km.Refresh):
		it, ok := m.picker.selected()
		if !ok {
			return m, nil, true
		}
		m.note = "refreshing " + itemLabel(it)
		return m, m.refreshItem(it), true
	case key.Matches(k, km.Open):
		return m.handleOpen()
	case key.Matches(k, km.Checkout):
		it, ok := m.picker.selected()
		if !ok {
			return m, nil, true
		}
		m.note = "checking out " + orDash(it.Ref) + "…"
		return m, m.checkout(it.ID, false), true
	default:
		return m, nil, false
	}
	return m, nil, true
}

func (m Model) handleOpen() (Model, tea.Cmd, bool) {
	it, ok := m.picker.selected()
	switch {
	case !ok:
		return m, nil, true
	case it.URL == "":
		m.note = "no GitHub URL for " + itemLabel(it)
		return m, nil, true
	case m.opts.Open == nil:
		m.note = "no opener configured"
		return m, nil, true
	}
	return m, m.open(it.URL), true
}

func (m Model) handleConfirm(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	c := m.confirm
	m.confirm = nil
	if key.Matches(k, m.keys.Yes) {
		m.note = "checking out " + orDash(c.ref) + " (forced)…"
		return m, m.checkout(c.itemID, true)
	}
	m.note = "checkout cancelled"
	return m, nil
}

func (m Model) handlePickerKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	km := m.keys
	moved := false
	switch {
	case key.Matches(k, km.Down):
		moved = m.picker.move(1)
	case key.Matches(k, km.Up):
		moved = m.picker.move(-1)
	case key.Matches(k, km.Top):
		moved = m.picker.moveTo(0)
	case key.Matches(k, km.Bottom):
		moved = m.picker.moveTo(len(m.picker.items) - 1)
	case key.Matches(k, km.Focus):
		if _, ok := m.picker.selected(); ok {
			m.focus = FocusMain
		}
	}
	if moved {
		return m, m.selectionChanged()
	}
	return m, nil
}

// handleMainKey handles Overview keys (Code keys went to the component in
// routeMain).
func (m Model) handleMainKey(k tea.KeyMsg) Model {
	if m.tab == TabCode {
		return m
	}
	km, vp := m.keys, &m.overview.vp
	switch {
	case key.Matches(k, km.Focus):
		id, ok := m.overview.anchor()
		if !ok || m.detail == nil {
			m.note = "no unit here"
			return m
		}
		m.jumpToUnit(id)
	case key.Matches(k, km.RawLabels):
		id, ok := m.overview.anchor()
		if !ok || m.detail == nil {
			m.note = "no unit here"
			return m
		}
		_, mw, _ := m.dims()
		m.overview.openRaw(m.st.title.Render("Raw labels") + "  " + m.st.dim.Render("esc or L closes") + "\n\n" +
			rawLabelsFor(*m.detail, id, mw-2))
	case key.Matches(k, km.Down):
		vp.ScrollDown(1)
	case key.Matches(k, km.Up):
		vp.ScrollUp(1)
	case key.Matches(k, km.PageDown):
		vp.PageDown()
	case key.Matches(k, km.PageUp):
		vp.PageUp()
	case key.Matches(k, km.HalfPageDown):
		vp.HalfPageDown()
	case key.Matches(k, km.HalfPageUp):
		vp.HalfPageUp()
	case key.Matches(k, km.Top):
		vp.GotoTop()
	case key.Matches(k, km.Bottom):
		vp.GotoBottom()
	}
	return m
}

// ---- layout and view ------------------------------------------------------

// sidebarWidth adapts the picker to narrow terminals.
func sidebarWidth(total int) int {
	if total >= 100 {
		return 36
	}
	return max(16, min(36, total*2/5))
}

// dims returns the sidebar width, main pane width and body height (the
// terminal minus the status line). The sidebar has a one-cell right border
// and a one-cell gap.
func (m Model) dims() (side, main, body int) {
	side = sidebarWidth(m.width)
	return side, max(10, m.width-side-2), max(1, m.height-1)
}

func (m *Model) layout() {
	sw, mw, bh := m.dims()
	m.picker.setSize(sw, bh)
	m.code.SetSize(mw, bh-1)
	m.overview.setSize(mw, bh-1)
	m.renderOverview()
}

// View implements tea.Model.
func (m Model) View() string {
	sw, mw, bh := m.dims()
	rc := rowCtx{now: m.now(), spin: m.spin.View(), stages: m.stages}
	side := m.st.sidebar.Width(sw).Render(fit(m.picker.view(m.st, rc), sw, bh))
	main := fit(m.viewMain(mw), mw, bh)
	body := lipgloss.JoinHorizontal(lipgloss.Top, side, " ", main)
	return body + "\n" + m.viewStatus()
}

func (m Model) viewMain(w int) string {
	head := m.viewTabs()
	if m.showHelp {
		return head + "\n" + m.viewHelp(w)
	}
	it, ok := m.picker.selected()
	switch {
	case !ok:
		return head + "\n" + m.st.dim.Render("No items. Local branches with commits not on the default branch,\nand PRs that involve you, appear here.")
	case m.detail == nil:
		return head + "\n" + m.st.title.Render(itemLabel(it)) + "\n" + m.st.dim.Render("loading…")
	case m.tab == TabCode:
		return head + "\n" + m.code.View()
	}
	return head + "\n" + m.overview.view()
}

func (m Model) viewTabs() string {
	tab := func(t Tab, s string) string {
		if m.tab == t {
			return m.st.tabOn.Render(s)
		}
		return m.st.tabOff.Render(s)
	}
	focus := "  "
	if m.focus == FocusMain {
		focus = "▸ "
	}
	return focus + tab(TabOverview, "1 Overview") + "  " + tab(TabCode, "2 Code") + "  " +
		m.st.tabLater.Render("3 Architecture (later)")
}

func (m Model) viewStatus() string {
	right := m.statusCounts()
	avail := max(10, m.width-lipgloss.Width(right)-2)
	var left string
	switch {
	case m.confirm != nil:
		left = m.st.prompt.Render("work tree dirty — checkout " + orDash(m.confirm.ref) + " anyway? y/N")
	case m.note != "":
		left = m.note
	default:
		h := m.help
		h.Width = avail
		switch {
		case m.focus == FocusPicker:
			left = h.ShortHelpView(m.keys.pickerShort())
		case m.tab == TabOverview && m.overview.raw:
			left = m.st.dim.Render("raw labels · esc or L closes")
		default:
			left = h.ShortHelpView(m.keys.mainShort(m.tab, m.code.ShortBindings()))
		}
	}
	left = truncate(left, avail)
	gap := max(1, m.width-lipgloss.Width(left)-lipgloss.Width(right))
	return truncate(left+strings.Repeat(" ", gap)+right, m.width)
}

func (m Model) statusCounts() string {
	busy := 0
	for _, it := range m.picker.items {
		if it.State == model.StateFetching || it.State == model.StateAnalysing {
			busy++
		}
	}
	s := plural(len(m.picker.items), "item")
	if busy > 0 {
		s += fmt.Sprintf(" · %d busy", busy)
	}
	if m.logNote != "" {
		s = m.logNote + " · " + s
	}
	return m.st.dim.Render(s)
}
