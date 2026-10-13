// Package tui is the Bubble Tea front end. The root Model draws a header
// line, the picker (the "Queue · ranked" sidebar box, always visible on the
// left, with the `/` search prompt at its bottom), the main pane (the tab
// bar, then the label-driven Overview, the Code tab wrapping
// internal/codeview, the Architecture tab wrapping internal/archview, or
// the Pipeline page) and a bottom line with the key hints, notes, prompts
// or the `:` command line. Colours and drawing pieces come from
// internal/theme. It talks to the analysis side only through
// daemon.Client; long operations are tea.Cmds returning messages and never
// run in Update.
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
	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/archview"
	"github.com/fredricw/prt/internal/cmdline"
	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeview"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/searchview"
	"github.com/fredricw/prt/internal/settingsview"
	"github.com/fredricw/prt/internal/theme"
)

// Focus is which pane has keyboard focus.
type Focus int

// Focus targets (spec: tab cycles picker → main pane → bottom strip).
// FocusStrip is the Code page's Comments strip; it is only reachable while
// the Code tab shows a strip, and its keys go to the Code page.
const (
	FocusPicker Focus = iota
	FocusMain
	FocusStrip
)

// Tab is the main-pane tab.
type Tab int

// Main-pane tabs. TabPipeline is the Pipeline page: not a numbered tab but
// a page `P` opens over the main pane and `P`/esc closes, back to the
// previous tab. TabArch is the Architecture tab (3, M5). TabSettings is the
// settings page (M5.1): like the Pipeline page it is opened over the main
// pane (`,` or `:settings`) and closed back to the previous tab; it is not
// tied to the selected item.
const (
	TabOverview Tab = iota
	TabCode
	TabPipeline
	TabArch
	TabSettings
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
	// Edit opens the routing rules file for the Pipeline page's `e`; nil
	// runs $EDITOR (fallback vi) through tea.ExecProcess. Tests inject a
	// recording stand-in.
	Edit EditFunc
	// Repo ("owner/name", else the repo directory's name) and Role (the
	// configured role, "frontend") fill the header line; "" hides the part
	// (M5).
	Repo, Role string
	// Root is the repository's work tree, so the Code page's `e` opens
	// <Root>/<path>; "" uses the path as is (relative to the working
	// directory) (M5).
	Root string
	// EditFile opens a file at a line for the Code page's `e`; nil runs
	// `$EDITOR +<line> <path>` (fallback vi) through tea.ExecProcess (M5).
	EditFile EditFileFunc
	// ReloadOnTick makes the refresh tick only re-read the items instead of
	// running discovery (M5.2): set it when a daemon is attached, since the
	// daemon runs discovery itself. The default discovers on every tick
	// (--no-daemon).
	ReloadOnTick bool
}

// Messages.
type (
	itemsMsg struct {
		items []model.Item
		err   error // discovery error; items are still the stored ones
	}
	// detailMsg answers a Detail call for item id ("" means d.Item.ID);
	// only the shown item's latest (seq == detailSeq) is applied.
	detailMsg struct {
		seq int
		id  string
		d   proto.Detail
		err error
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
	// cache keeps the last Detail per item, loads the Detail calls in
	// flight and the debounce of event-triggered reloads, labels the labels
	// fetched on demand (M5.2; loader.go, labels.go). debounce is
	// reloadDebounce (tests may shorten it).
	cache    *detailCache
	loads    *loadState
	labels   *labelState
	debounce time.Duration
	// reloadPending is set while an event-triggered list reload is in
	// flight, so a burst of item events causes one reload, not one each.
	reloadPending, reloadAgain bool
	overview                   overview
	code                       codePane
	pipe                       pipePane
	arch                       archPane
	// prevTab is the tab the Pipeline page returns to.
	prevTab Tab
	// settings is the settings page (M5.1); settingsPrev the tab it returns
	// to. strategy is the daemon's auto_review strategy ("" until known),
	// named on the Overview of a not-analysed or stopped item.
	settings     settingsPane
	settingsPrev Tab
	strategy     string

	// search is the `/` prompt; cmd the `:` command line (M5).
	search searchPane
	cmd    cmdPane
	// previewID is the search result shown with tab (a hidden preview
	// item), preview its loaded detail, previewOV its Overview.
	previewID  string
	preview    *proto.Detail
	previewSeq int
	previewOV  overview
	// previewBusy is set while a Preview call is in flight; previewNext is
	// the latest tab'd query waiting for it (hasNext). Preview calls run
	// one at a time, so the daemon's "one preview" cleanup never races a
	// newer preview (M5 review finding).
	previewBusy bool
	previewNext string
	hasNext     bool
	// marked are the multi-selected items (space); markNoted is set once
	// the "overlap view comes in M6" note was shown.
	marked    map[string]bool
	markNoted bool
	// superseded maps a branch item id to the PR superseding it (from its
	// loaded detail), for the picker line.
	superseded map[string]int

	requested map[string]string      // item id → head SHA auto-refreshed
	stages    map[string]model.Stage // item id → latest stage seen
	// runLogs are the stage rows received as stage events, per item, for
	// the picker's one-line stage strip while the item analyses.
	runLogs map[string][]model.RunLog
	// strip renders the picker's stage strip (pipeview.StageStrip; tests
	// substitute a stand-in).
	strip func(rls []model.RunLog, width int) string

	note    string // transient; cleared on the next key press
	logNote string // latest daemon log note ("GitHub unavailable")
	confirm *confirmCheckout

	// form is the open submit-review or create-PR form (nil when none); it
	// captures every key. newDialog builds it (nil: Huh); tests script it.
	form      *openForm
	newDialog func(formSpec, *formValues) dialog
	// viewed holds "item@analysedSHA" keys already sent to MarkViewed this
	// session.
	viewed map[string]bool
	// selectAfter is the id of a just-created PR item (or an added one) the
	// picker moves to once it is listed.
	selectAfter string

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
	now := o.Now
	if now == nil {
		now = time.Now
	}
	h := help.New()
	h.Styles = helpStyles(r)
	pipe := pipeview.New(r)
	arch := archview.New(r)
	cl := cmdline.New(r)
	sv := settingsview.New(r)
	m := Model{
		settings:   &sv,
		client:     c,
		opts:       o,
		keys:       km,
		r:          r,
		st:         newStyles(r),
		md:         newMarkdown(r),
		help:       h,
		spin:       spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(r.NewStyle())),
		overview:   newOverview(),
		previewOV:  newOverview(),
		code:       newCodePane(r),
		pipe:       &pipe,
		arch:       &arch,
		search:     newSearchPane(r, c, now),
		cmd:        &cl,
		marked:     map[string]bool{},
		superseded: map[string]int{},
		requested:  map[string]string{},
		stages:     map[string]model.Stage{},
		runLogs:    map[string][]model.RunLog{},
		viewed:     map[string]bool{},
		cache:      newDetailCache(detailCacheSize),
		loads:      newLoadState(),
		labels:     newLabelState(),
		debounce:   reloadDebounce,
		strip:      pipeview.StageStrip,
		width:      120,
		height:     40,
	}
	m.code.SetKeys(km.codeKeys())
	m.pipe.SetKeys(km.pipeKeys())
	m.arch.SetKeys(km.archKeys())
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
// against the cache), then discover, listen for events, schedule refresh
// and read the auto-review strategy (M5.1).
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.loadItems(), m.discover(), m.listen(), m.scheduleRefresh(), m.loadStrategy())
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
// order, so the top of the list is ready first). It is an automatic run:
// User is false, so the daemon's auto_review strategy decides whether the
// model-backed stages run (M5.1; an uncovered item stops after the free
// stages as not analysed).
func (m Model) autoRefresh(ids []string) tea.Cmd {
	c := m.client
	return func() tea.Msg {
		var errs []error
		for _, id := range ids {
			if err := c.Refresh(context.Background(), proto.ItemParams{ItemID: id, User: false}); err != nil {
				errs = append(errs, fmt.Errorf("refresh %s: %w", id, err))
			}
		}
		return autoRefreshedMsg{errs: errs}
	}
}

// refreshItem is `r`: a forced re-run the user asked for (User: the
// strategy does not gate it, M5.1).
func (m Model) refreshItem(it model.Item) tea.Cmd {
	c, label := m.client, itemLabel(it)
	return func() tea.Msg {
		err := c.Refresh(context.Background(), proto.ItemParams{ItemID: it.ID, Force: true, User: true})
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
		m.sizeForm()
		return m, nil
	case tea.KeyMsg:
		return afterInput(m.handleKey(msg))
	case tea.MouseMsg:
		return afterInput(m.handleMouse(msg))
	case itemsMsg:
		return m.onItems(msg)
	case reloadedMsg:
		return m.onReloaded(msg)
	case detailMsg:
		return m.onDetail(msg)
	case reloadTickMsg:
		return m.onReloadTick(msg)
	case labelsMsg:
		return m.onLabels(msg)
	case eventMsg:
		return m.onEvent(proto.Event(msg))
	case spinner.TickMsg:
		return m.onSpin(msg)
	case refreshTickMsg:
		return m, tea.Batch(m.tickCmd(), m.scheduleRefresh())
	case checkoutMsg:
		return m.onCheckout(msg), nil
	case groupEditedMsg:
		return m.onGroupEdited(msg), nil
	case tierDoneMsg:
		return m.onTierDone(msg), nil
	}
	if next, ok := m.onNoteMsg(msg); ok {
		return next, nil
	}
	if next, ok := m.onControlMsg(msg); ok {
		return next, nil
	}
	if next, cmd, ok := m.onSettingsMsg(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.onPipeMsg(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.onCommentMsg(msg); ok {
		return next, cmd
	}
	if next, cmd, ok := m.onSearchMsg(msg); ok {
		return next, cmd
	}
	return m.routeOther(msg)
}

// routeOther hands a message no case claimed to the open form, command
// line or search prompt (their cursor blinks, debounce ticks), and search
// results to the prompt.
func (m Model) routeOther(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch {
	case m.form != nil:
		return m.updateForm(msg)
	case m.cmd.Active():
		return m.updateCmdline(msg)
	case m.tab == TabSettings:
		return m.updateSettings(msg)
	}
	if _, ok := msg.(searchview.ResultMsg); ok || m.search.Active() {
		return m.updateSearch(msg)
	}
	return m, nil
}

// onNoteMsg handles the messages that only set the status note; ok is false
// for any other message.
func (m Model) onNoteMsg(msg tea.Msg) (Model, bool) {
	switch msg := msg.(type) {
	case openedMsg:
		if msg.err != nil {
			m.note = "open failed: " + msg.err.Error()
		} else {
			m.note = "opened " + msg.url
		}
	case unitOpenedMsg:
		if msg.err != nil {
			m.note = "open failed: " + msg.err.Error()
		} else {
			m.note = "opened " + msg.where + " on GitHub"
		}
	case fileEditedMsg:
		if msg.err != nil {
			m.note = "editor: " + msg.err.Error()
		}
	case refreshedMsg:
		if msg.err != nil {
			m.note = "refresh failed: " + msg.err.Error()
		}
	case autoRefreshedMsg:
		if len(msg.errs) > 0 {
			m.note = msg.errs[0].Error()
		}
	case errMsg:
		m.note = "error: " + msg.err.Error()
	case closedMsg:
		m.logNote = "daemon connection lost; restart prt"
	default:
		return m, false
	}
	return m, true
}

func (m Model) onReloaded(msg reloadedMsg) (tea.Model, tea.Cmd) {
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
}

func (m Model) onSpin(msg spinner.TickMsg) (tea.Model, tea.Cmd) {
	if m.tab == TabPipeline {
		m.pipe.SetNow(m.now()) // a running review's elapsed seconds
	}
	if !m.picker.busy() {
		m.spinning = false
		return m, nil
	}
	var cmd tea.Cmd
	m.spin, cmd = m.spin.Update(msg)
	return m, cmd
}

// afterInput runs after every key and mouse message: it keeps the strip
// focus in step and marks the shown item viewed when the main pane has
// focus.
func afterInput(tm tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m := tm.(Model)
	m.syncFocus()
	return m, batch(cmd, m.markViewed())
}

// handleMouse implements the spec's mouse support: the wheel scrolls the
// pane under the pointer, a left click on a picker row selects that item,
// a click on a tab selects that tab (tabbar.go; the tab bar never passes a
// click on), clicks in the search results, the Code, Pipeline and
// Architecture pages go to those components, and on the Overview a click
// opens a row's unit, focuses a box or opens a "+N more" list. Row 0 is
// the header line; the sidebar box spans rows 1..body with its list
// starting at row 2; row 1 of the main pane is the tab bar.
func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// The settings page is keyboard-only (a Huh form); the mouse is ignored
	// while it is open.
	if m.confirm != nil || m.showHelp || m.form != nil || m.cmd.Active() || m.tab == TabSettings {
		return m, nil
	}
	sw, _, bh := m.dims()
	inPicker := msg.X <= sw && msg.Y >= 1 && msg.Y <= bh
	row := msg.Y - 2
	wheel := msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown
	switch {
	case inPicker && m.search.Active() && row >= m.picker.height:
		listRows, pad, _ := m.searchLayout()
		local := msg
		local.X, local.Y = msg.X-2, row-listRows-pad
		cmd := m.search.Update(local)
		return m, batch(cmd, m.afterSearch())
	case inPicker:
		return m.pickerMouse(msg, row, wheel)
	case msg.Y < 1:
		return m, nil
	case msg.Y == 1 && !wheel:
		// The main pane starts after the sidebar box and a gap.
		return m.tabBarMouse(msg, msg.X-sw-2)
	case m.previewing():
		wheelOverview(&m.previewOV, msg, wheel)
	case m.tab == TabCode:
		// A click on a group may ask for its units' labels (M5.2).
		cmd := m.code.Update(msg)
		return m, batch(cmd, m.codeLabels())
	case m.tab == TabPipeline:
		return m, m.pipe.Update(msg)
	case m.tab == TabArch:
		cmd := m.arch.Update(msg)
		return m, batch(cmd, m.archJump())
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		// Rows 0 and 1 are the header and the tab bar.
		return m.overviewClick(msg.X-sw-2, msg.Y-2)
	default:
		wheelOverview(&m.overview, msg, wheel)
	}
	return m, nil
}

// pickerMouse is the wheel (moves the cursor) and a click (selects the row)
// over the picker list.
func (m Model) pickerMouse(msg tea.MouseMsg, row int, wheel bool) (tea.Model, tea.Cmd) {
	switch {
	case wheel:
		delta := 1
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		if m.picker.move(delta) {
			return m, m.selectionChanged()
		}
	case msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress:
		i := m.picker.itemAt(row)
		if i < 0 {
			return m, nil
		}
		m.focus = FocusPicker
		if m.picker.moveTo(i) {
			return m, m.selectionChanged()
		}
	}
	return m, nil
}

// wheelOverview scrolls an Overview (or its open overlay) by three lines.
func wheelOverview(o *overview, msg tea.MouseMsg, wheel bool) {
	if !wheel {
		return
	}
	vp := &o.vp
	if o.raw {
		vp = &o.rawVP
	}
	if msg.Button == tea.MouseButtonWheelUp {
		vp.ScrollUp(3)
	} else {
		vp.ScrollDown(3)
	}
}

func (m Model) onItems(msg itemsMsg) (tea.Model, tea.Cmd) {
	changed := m.picker.setItems(msg.items)
	changed = m.takeSelectAfter() || changed
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

// jumpToUnit switches to the Code tab and focuses unit id there.
func (m *Model) jumpToUnit(id string) {
	m.tab = TabCode
	if !m.code.FocusUnit(id) {
		m.note = "unit not found in Code"
	}
}

// previewing reports whether the main pane shows a previewed search result.
func (m Model) previewing() bool { return m.previewID != "" && m.search.Active() }

// ---- keys -----------------------------------------------------------------

func (m Model) handleKey(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirm != nil {
		return m.handleConfirm(k)
	}
	m.note = ""
	km := m.keys
	switch {
	case k.String() == "ctrl+c":
		return m.quit()
	case m.form != nil:
		return m.updateForm(k)
	case m.cmd.Active():
		return m.updateCmdline(k)
	case m.search.Active():
		return m.updateSearch(k)
	case m.tab == TabSettings:
		return m.settingsKey(k)
	}
	if !m.showHelp && m.focus != FocusPicker {
		capturing := m.tab == TabCode && m.code.Capturing()
		if next, ok := m.routeMain(k); ok {
			if next.tab == TabCode && !capturing && key.Matches(k, km.SideBySide) {
				next.layoutNote()
			}
			cmd := batch(next.sendGroupEdit(), next.sendReReview(), next.pipeActionCmd(), next.sendComment(),
				next.codeOpenCmd(), next.archJump(), next.codeLabels())
			return next, cmd
		}
	}
	if key.Matches(k, km.Quit) {
		return m.quit()
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
	return m.handleMainKey(k)
}

// routeMain gives the focused main pane the first look at k. The Code page
// takes every key while it captures (filters bar, raw labels: so 1–9 toggle
// filters instead of switching tabs) and otherwise any key it handles; the
// Pipeline page (cap prompt, call log, stage log) and the Architecture tab
// (a module's file list) likewise; the Overview's overlays (raw labels,
// urgency weights) capture every key. ok is false when k falls through to
// the global and pane keys.
func (m Model) routeMain(k tea.KeyMsg) (Model, bool) {
	switch {
	case m.tab == TabCode && m.code.Capturing():
		m.code.HandleKey(k)
		return m, true
	case m.tab == TabCode:
		return m, m.code.HandleKey(k)
	case m.tab == TabPipeline && m.pipe.Capturing():
		m.pipe.HandleKey(k)
		return m, true
	case m.tab == TabPipeline:
		return m, m.pipe.HandleKey(k)
	case m.tab == TabArch && m.arch.Capturing():
		m.arch.HandleKey(k)
		return m, true
	case m.tab == TabArch:
		return m, m.arch.HandleKey(k)
	case m.overview.raw:
		m.handleRawKey(k)
		return m, true
	case m.tab == TabOverview && m.overview.focus != "" && key.Matches(k, m.keys.Back):
		// esc first clears the block focus, then returns to the picker.
		m.overview.focus = ""
		m.redrawOverview()
		return m, true
	}
	return m, false
}

// handleGlobalKey handles keys that work in any pane; ok is false when k is
// not one of them.
func (m Model) handleGlobalKey(k tea.KeyMsg) (Model, tea.Cmd, bool) {
	km := m.keys
	switch {
	case key.Matches(k, km.Help):
		m.showHelp = true
	case key.Matches(k, km.Command):
		return m, m.cmd.Open(), true
	case key.Matches(k, km.NextFocus):
		m.cycleFocus(1)
	case key.Matches(k, km.PrevFocus):
		m.cycleFocus(-1)
	case key.Matches(k, km.Approve):
		return m, m.openSubmit(proto.EventApprove), true
	case key.Matches(k, km.RequestChanges):
		return m, m.openSubmit(proto.EventRequestChanges), true
	case key.Matches(k, km.Submit):
		return m, m.openSubmit(proto.EventComment), true
	case key.Matches(k, km.Pipeline):
		m.togglePipeline()
	case key.Matches(k, km.Back) && m.tab == TabPipeline && m.focus == FocusMain:
		m.closePipeline()
	case key.Matches(k, km.Back) && m.focus == FocusPicker && len(m.marked) > 0:
		m.clearMarks()
	case key.Matches(k, km.Back):
		m.focus = FocusPicker
	case key.Matches(k, km.Overview):
		m.tab = TabOverview
	case key.Matches(k, km.Code):
		m.tab = TabCode
	case key.Matches(k, km.Architecture):
		m.tab = TabArch
	case key.Matches(k, km.Refresh):
		it, ok := m.picker.selected()
		if !ok {
			return m, nil, true
		}
		if resumable(it) {
			// r on a stopped or not-analysed item resumes or runs it (not
			// forced, DECISIONS "M5.1 stop / resume").
			return m, m.resumeSelected(it), true
		}
		m.note = "refreshing " + itemLabel(it)
		return m, m.refreshItem(it), true
	case key.Matches(k, km.Stop), key.Matches(k, km.StopAll), key.Matches(k, km.Resume):
		return m, m.controlKey(k), true
	case key.Matches(k, km.Settings):
		return m, m.openSettings(), true
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
	case key.Matches(k, km.Sort):
		m.picker.cycleSort()
		m.note = "sort: " + m.picker.sort.String()
	case key.Matches(k, km.Search):
		return m, m.openSearch()
	case key.Matches(k, km.Remove):
		return m, m.removeSelected()
	case key.Matches(k, km.Select):
		m.toggleMark()
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

// handleMainKey handles Overview keys (Code, Pipeline and Architecture keys
// went to their components in routeMain): the block keys first (blocks.go),
// then the overlays, the jump to the unit at the viewport top and
// scrolling.
func (m Model) handleMainKey(k tea.KeyMsg) (Model, tea.Cmd) {
	if m.tab != TabOverview {
		return m, nil
	}
	if m.detail != nil {
		if cmd, ok := m.overviewBlockKey(k); ok {
			return m, cmd
		}
	}
	km, vp := m.keys, &m.overview.vp
	switch {
	case key.Matches(k, km.Weights):
		if m.detail == nil {
			m.note = "no item loaded"
			return m, nil
		}
		_, mw, _ := m.dims()
		m.overview.openWeights(m.st.title.Render("Urgency weights") + "  " +
			m.st.dim.Render("esc or "+km.Weights.Help().Key+" closes") + "\n\n" + weightsText(*m.detail, mw-2))
	case key.Matches(k, km.Focus):
		id, ok := m.overview.anchor()
		if !ok || m.detail == nil {
			m.note = "no unit here"
			return m, nil
		}
		m.jumpToUnit(id)
	case key.Matches(k, km.RawLabels):
		id, ok := m.overview.anchor()
		if !ok || m.detail == nil {
			m.note = "no unit here"
			return m, nil
		}
		return m, m.openRawLabels(id)
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
	return m, nil
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
// terminal minus the header line and the bottom line). The sidebar box is
// sidebar+1 cells wide and followed by a one-cell gap, so the main pane
// starts at column sidebar+2.
func (m Model) dims() (side, main, body int) {
	side = sidebarWidth(m.width)
	return side, max(10, m.width-side-2), max(3, m.height-2)
}

// sidebarInner is the rows inside the sidebar box's borders.
func (m Model) sidebarInner() int {
	_, _, bh := m.dims()
	return max(1, bh-2)
}

// sizeSidebar sizes the picker list (and the open search prompt) inside
// the sidebar box: the bottom line is the footer, or the prompt's rows.
func (m *Model) sizeSidebar() {
	sw, _, _ := m.dims()
	inner := m.sidebarInner()
	w := max(4, sw-3)
	rows := inner - 1
	if m.search.Active() {
		sr := searchRows(inner)
		rows = inner - sr
		m.search.SetSize(w, sr)
	}
	m.picker.setSize(w, max(1, rows))
}

func (m *Model) layout() {
	sw, mw, bh := m.dims()
	m.sizeSidebar()
	m.code.SetSize(mw, bh-1)
	m.pipe.SetSize(mw, bh-1)
	m.arch.SetSize(mw, bh-1)
	m.settings.SetSize(mw, bh-1)
	// The main pane starts after the sidebar box and a gap, below the
	// header and the tab bar; components map mouse cells from there.
	for _, c := range []any{m.code, m.pipe, m.arch} {
		if o, ok := c.(originSetter); ok {
			o.SetOrigin(sw+2, 2)
		}
	}
	m.overview.setSize(mw, bh-1)
	m.previewOV.setSize(mw, bh-2)
	m.cmd.SetWidth(m.width)
	m.renderOverview()
	m.renderPreview()
}

// View implements tea.Model.
func (m Model) View() string {
	sw, mw, bh := m.dims()
	side := fit(m.viewSidebar(), sw+1, bh)
	main := fit(m.viewMain(mw), mw, bh)
	body := lipgloss.JoinHorizontal(lipgloss.Top, side, " ", main)
	return m.viewHeader() + "\n" + body + "\n" + m.viewStatus()
}

// rowCtx is the picker's rendering context.
func (m Model) rowCtx() rowCtx {
	return rowCtx{now: m.now(), spin: m.spin.View(), stages: m.stages, runLogs: m.runLogs, strip: m.strip,
		marked: m.marked, superseded: m.superseded}
}

// viewSidebar draws the "Queue · ranked" box: the list, then the footer
// ("/ search  s sort: urgency ▾") or the open search prompt.
func (m Model) viewSidebar() string {
	sw, _, bh := m.dims()
	bottom := []string{m.st.dim.Render(m.keys.Search.Help().Key + " search  " + m.keys.Sort.Help().Key + " " + m.picker.sortLabel())}
	if m.search.Active() {
		_, pad, lines := m.searchLayout()
		bottom = append(make([]string, pad), lines...)
	}
	return renderSidebar(m.st, m.picker, m.rowCtx(), bottom, sw+1, bh, m.focus == FocusPicker)
}

func (m Model) viewMain(w int) string {
	head := m.viewTabs(w)
	if m.showHelp {
		return head + "\n" + m.viewHelp(w)
	}
	if m.form != nil {
		return head + "\n" + m.viewForm()
	}
	if m.tab == TabSettings {
		return head + "\n" + m.settings.View()
	}
	if m.previewing() {
		return head + "\n" + m.viewPreview()
	}
	it, ok := m.picker.selected()
	switch {
	case !ok:
		return head + "\n" + m.st.dim.Render("No items. Local branches with commits not on the default branch,\nand PRs that involve you, appear here.")
	case m.detail == nil:
		return head + "\n" + m.st.title.Render(itemLabel(it)) + "\n" + m.st.dim.Render("loading…")
	case m.tab == TabCode:
		return head + "\n" + m.code.View()
	case m.tab == TabPipeline:
		return head + "\n" + m.pipe.View()
	case m.tab == TabArch:
		return head + "\n" + m.arch.View()
	}
	return head + "\n" + m.overview.view()
}

// spread is theme.Spread that leaves no trailing blanks when right is
// empty.
func spread(left, right string, w int) string {
	if ansi.StringWidth(right) == 0 {
		return truncate(left, w)
	}
	return theme.Spread(left, right, w)
}

// viewHeader is the top line: "prt · <repo> · N PRs · N branches · role:
// <role>" on the left; the sort mode (when not urgency), the multi-select
// count, busy items, drafts and the daemon's log note dim on the right.
func (m Model) viewHeader() string {
	st := m.st
	sep := st.dim.Render(" · ")
	left := st.accent.Bold(true).Render("prt")
	if m.opts.Repo != "" {
		left += sep + st.bold.Render(m.opts.Repo)
	}
	prs, branches := 0, 0
	for _, it := range m.picker.items {
		if it.Kind == model.KindBranch {
			branches++
		} else {
			prs++
		}
	}
	nb := fmt.Sprintf("%d branches", branches)
	if branches == 1 {
		nb = "1 branch"
	}
	left += sep + st.dim.Render(plural(prs, "PR")) + sep + st.dim.Render(nb)
	if m.opts.Role != "" {
		left += sep + st.dim.Render("role: ") + m.opts.Role
	}
	return spread(left, st.dim.Render(strings.Join(m.headerFacts(), " · ")), m.width)
}

// headerFacts are the header's right-hand facts.
func (m Model) headerFacts() []string {
	var parts []string
	if m.picker.sort != sortUrgency {
		parts = append(parts, "sort: "+m.picker.sort.String())
	}
	if n := len(m.marked); n > 0 {
		parts = append(parts, fmt.Sprintf("%d selected", n))
	}
	if n := m.picker.busyCount(); n > 0 {
		parts = append(parts, fmt.Sprintf("%d busy", n))
	}
	if m.detail != nil {
		if n := countDrafts(m.detail.Comments).total(); n > 0 {
			parts = append(parts, plural(n, "draft"))
		}
	}
	if m.logNote != "" {
		parts = append(parts, m.logNote)
	}
	return parts
}

// viewStatus is the bottom line: the checkout prompt, the form hint, the
// command line or a note when there is one, else the key hint bar.
func (m Model) viewStatus() string {
	var s string
	switch {
	case m.confirm != nil:
		s = m.st.prompt.Render("work tree dirty — checkout " + orDash(m.confirm.ref) + " anyway? y/N")
	case m.form != nil:
		s = m.st.dim.Render("enter next · enter on the last field submits · esc cancels")
	case m.cmd.Active():
		s = m.cmd.View()
	case m.note != "":
		s = m.note
	case m.tab == TabOverview && m.focus != FocusPicker && m.overview.list != nil:
		s = m.st.th.Hints(m.keys.listHints(), m.width)
	case m.tab == TabOverview && m.focus != FocusPicker && m.overview.raw && m.overview.weights:
		s = m.st.dim.Render("urgency weights · esc or " + m.keys.Weights.Help().Key + " closes")
	case m.tab == TabOverview && m.focus != FocusPicker && m.overview.raw:
		s = m.st.dim.Render("raw labels · esc or " + m.keys.RawLabels.Help().Key + " closes")
	default:
		s = m.st.th.Hints(m.hints(), m.width)
	}
	return truncate(s, m.width)
}

// hints is the key hint bar for the focus and tab.
func (m Model) hints() []theme.Hint {
	switch {
	case m.search.Active():
		sk := searchview.DefaultKeys()
		return append([]theme.Hint{pairHint(sk.Up, sk.Down, "results")}, theme.HintsOf(sk.Add, sk.Preview, sk.Close)...)
	case m.tab == TabSettings:
		return m.keys.mainHints(m.tab, flatten(m.settings.HelpBindings()))
	case m.focus == FocusPicker:
		return m.keys.pickerHints()
	case m.tab == TabPipeline:
		return m.keys.mainHints(m.tab, m.pipe.ShortBindings())
	case m.tab == TabArch:
		return m.keys.mainHints(m.tab, m.arch.ShortBindings())
	case m.tab == TabCode:
		return m.keys.mainHints(m.tab, m.code.ShortBindings())
	case m.overview.focus != "":
		return m.keys.blockHints()
	}
	return m.keys.mainHints(TabOverview, nil)
}
