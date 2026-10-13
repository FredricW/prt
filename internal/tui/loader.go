package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// Detail loading (M5.2, docs/m52-open-latency.md fixes 1, 2 and the TUI
// parts of 4 and 5): show what is known at once, then revalidate.
//
//   - A small LRU keeps the last Detail per item. Selecting an item shows
//     its cached detail at once and loads it again in the background; only
//     an item not loaded this session shows "loading…".
//   - Details are loaded without labels (DetailParams.NoLabels); labels are
//     fetched per unit on demand (labels.go).
//   - At most one Detail call per item is in flight; a reload asked for
//     meanwhile sets a dirty flag and runs once when it lands. Every call
//     has its own context, cancelled when the selection moves on, and a
//     result that is not the shown item's latest is dropped.
//   - Event-triggered reloads wait for a trailing debounce, and only events
//     that change what the page shows trigger one (reloadFor).

const (
	// detailCacheSize is how many items' details the LRU keeps.
	detailCacheSize = 16
	// reloadDebounce is the trailing debounce of event-triggered reloads;
	// reloadMaxWait bounds how long a steady stream of events can hold one
	// back.
	reloadDebounce = 150 * time.Millisecond
	reloadMaxWait  = time.Second
)

// detailCache is an LRU of the last Detail per item id.
type detailCache struct {
	max   int
	order []string // least recently used first
	byID  map[string]proto.Detail
}

func newDetailCache(n int) *detailCache {
	return &detailCache{max: max(1, n), byID: map[string]proto.Detail{}}
}

// get returns id's detail and marks it most recently used.
func (c *detailCache) get(id string) (proto.Detail, bool) {
	d, ok := c.byID[id]
	if ok {
		c.touch(id)
	}
	return d, ok
}

// peek returns id's detail without touching it.
func (c *detailCache) peek(id string) (proto.Detail, bool) {
	d, ok := c.byID[id]
	return d, ok
}

// put stores d under its item id, evicting the least recently used entry
// when full.
func (c *detailCache) put(d proto.Detail) {
	id := d.Item.ID
	if id == "" {
		return
	}
	if _, ok := c.byID[id]; !ok && len(c.order) >= c.max {
		delete(c.byID, c.order[0])
		c.order = c.order[1:]
	}
	c.byID[id] = d
	c.touch(id)
}

// set replaces id's stored detail in place (no LRU move) when it has one.
func (c *detailCache) set(d proto.Detail) {
	if _, ok := c.byID[d.Item.ID]; ok {
		c.byID[d.Item.ID] = d
	}
}

// drop forgets id.
func (c *detailCache) drop(id string) {
	if _, ok := c.byID[id]; !ok {
		return
	}
	delete(c.byID, id)
	c.remove(id)
}

func (c *detailCache) touch(id string) {
	c.remove(id)
	c.order = append(c.order, id)
}

func (c *detailCache) remove(id string) {
	for i, x := range c.order {
		if x == id {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

// liveLoad is a Detail call in flight: its sequence number and the cancel
// func of its context.
type liveLoad struct {
	seq    int
	cancel context.CancelFunc
}

// loadState is the bookkeeping of Detail calls, shared by the copies of the
// root model (Bubble Tea only uses the latest copy).
type loadState struct {
	next  int                 // the last sequence number handed out
	live  map[string]liveLoad // item id → its call in flight
	dirty map[string]bool     // item id → load once more when the call lands
	// gen is bumped by every event that asks for a reload; due holds the
	// items waiting for the debounce and since when the first one waits.
	gen   int
	due   map[string]bool
	since time.Time
	// started, applied and dropped count calls (tests and perf).
	started, applied, dropped int
}

func newLoadState() *loadState {
	return &loadState{live: map[string]liveLoad{}, dirty: map[string]bool{}, due: map[string]bool{}}
}

// reloadTickMsg ends a debounce wait; gen is the generation it was set for
// and at when it fired.
type reloadTickMsg struct {
	gen int
	at  time.Time
}

// selectionChanged shows the selected item: its cached detail at once (the
// Overview, Code, Pipeline and Architecture pages keep their state for the
// same item), else "loading…" for an item not loaded this session; then it
// loads the item again. Loads of other items are cancelled.
func (m *Model) selectionChanged() tea.Cmd {
	m.overview.closeRaw()
	it, ok := m.picker.selected()
	if !ok {
		m.cancelLoads("")
		m.clearDetail()
		m.detailPending = false
		return nil
	}
	if m.detail == nil || m.detail.Item.ID != it.ID {
		m.overview.focus = ""
		if d, ok := m.cache.get(it.ID); ok {
			m.applyDetail(d)
		} else {
			m.clearDetail()
		}
	}
	m.cancelLoads("")
	m.detailPending = true
	return batch(m.startLoad(it.ID), m.codeLabels())
}

// clearDetail empties the main pane (nothing to show for the selection).
func (m *Model) clearDetail() {
	m.detail = nil
	m.code.Clear()
	m.pipe.Clear()
	m.arch.Clear()
	m.overview.setContent("", nil)
	m.overview.closeRaw()
	m.overview.focus = ""
}

// cancelLoads cancels and forgets every Detail call in flight except
// keep's and the preview's (their results will be dropped as stale).
func (m *Model) cancelLoads(keep string) {
	for id, l := range m.loads.live {
		if id == keep || (id == m.previewID && m.previewID != "") {
			continue
		}
		l.cancel()
		delete(m.loads.live, id)
		delete(m.loads.dirty, id)
	}
}

// startLoad starts a Detail call (without labels) for id, the shown item
// or the preview; its sequence number becomes detailSeq (or previewSeq).
func (m *Model) startLoad(id string) tea.Cmd {
	if l, ok := m.loads.live[id]; ok {
		l.cancel()
	}
	m.loads.next++
	seq := m.loads.next
	ctx, cancel := context.WithCancel(context.Background())
	m.loads.live[id] = liveLoad{seq: seq, cancel: cancel}
	delete(m.loads.dirty, id)
	m.loads.started++
	c := m.client
	p := proto.DetailParams{ItemID: id, NoLabels: true}
	if id == m.previewID && m.previewID != "" {
		m.previewSeq = seq
		return func() tea.Msg {
			d, err := c.DetailOf(ctx, p)
			return previewDetailMsg{seq: seq, d: d, err: err}
		}
	}
	m.detailSeq = seq
	return func() tea.Msg {
		d, err := c.DetailOf(ctx, p)
		return detailMsg{seq: seq, id: id, d: d, err: err}
	}
}

// requestLoad loads id again unless a call for it is in flight; then it
// marks id dirty, so one more call runs when that one lands.
func (m *Model) requestLoad(id string) tea.Cmd {
	if _, ok := m.loads.live[id]; ok {
		m.loads.dirty[id] = true
		return nil
	}
	return m.startLoad(id)
}

// landed records that the call seq for id answered: it is no longer in
// flight. It reports whether id was dirty (asked for again meanwhile).
func (m *Model) landed(id string, seq int) bool {
	l, ok := m.loads.live[id]
	if !ok || l.seq != seq {
		return false
	}
	l.cancel()
	delete(m.loads.live, id)
	dirty := m.loads.dirty[id]
	delete(m.loads.dirty, id)
	return dirty
}

// scheduleReload asks for an event-triggered reload of id (the shown item
// or the preview) after the trailing debounce.
func (m *Model) scheduleReload(id string) tea.Cmd {
	if id == "" {
		return nil
	}
	if len(m.loads.due) == 0 {
		m.loads.since = time.Now()
	}
	m.loads.due[id] = true
	m.loads.gen++
	gen := m.loads.gen
	if m.debounce <= 0 {
		return func() tea.Msg { return reloadTickMsg{gen: gen, at: time.Now()} }
	}
	return tea.Tick(m.debounce, func(t time.Time) tea.Msg { return reloadTickMsg{gen: gen, at: t} })
}

// onReloadTick runs the reloads waiting for the debounce when no event
// came after this tick's (or when they waited reloadMaxWait).
func (m Model) onReloadTick(msg reloadTickMsg) (tea.Model, tea.Cmd) {
	if len(m.loads.due) == 0 {
		return m, nil
	}
	if msg.gen != m.loads.gen && msg.at.Sub(m.loads.since) < reloadMaxWait {
		return m, nil
	}
	var cmds []tea.Cmd
	for id := range m.loads.due {
		if m.shows(id) {
			cmds = append(cmds, m.requestLoad(id))
		}
	}
	clear(m.loads.due)
	return m, batch(cmds...)
}

// shows reports whether id is the selected item or the open preview.
func (m Model) shows(id string) bool {
	if id == "" {
		return false
	}
	if id == m.previewID {
		return true
	}
	it, ok := m.picker.selected()
	return ok && it.ID == id
}

// onDetail applies the shown item's latest detail: cached, drawn, and the
// follow-up load started when the item was asked for again meanwhile. Any
// other answer is dropped.
func (m Model) onDetail(msg detailMsg) (tea.Model, tea.Cmd) {
	id := msg.id
	if id == "" {
		id = msg.d.Item.ID
	}
	dirty := m.landed(id, msg.seq)
	sel, ok := m.picker.selected()
	if msg.seq != m.detailSeq || !ok || sel.ID != id {
		m.loads.dropped++
		return m, nil
	}
	m.detailPending = false
	var cmds []tea.Cmd
	switch {
	case msg.err != nil && !errors.Is(msg.err, context.Canceled):
		m.note = fmt.Sprintf("error: load %s: %v", id, msg.err)
	case msg.err == nil:
		m.loads.applied++
		m.applyLoaded(msg.d)
		m.syncFocus()
		cmds = append(cmds, m.markViewed(), m.codeLabels())
	}
	if dirty {
		cmds = append(cmds, m.startLoad(id))
	}
	return m, batch(cmds...)
}

// applyLoaded shows a freshly loaded detail and caches it. While a run is
// in flight and the new detail has no aggregate yet, the page keeps the
// previous analysis (stale-while-revalidate) with the new item, comments
// and run rows.
func (m *Model) applyLoaded(d proto.Detail) {
	if prev, ok := m.cache.peek(d.Item.ID); ok {
		d = keepAnalysis(prev, d)
	}
	m.cache.put(d)
	m.applyDetail(d)
}

// keepAnalysis returns d, or d over prev's analysis when d has no
// aggregate while its item is busy and prev had one.
func keepAnalysis(prev, d proto.Detail) proto.Detail {
	running := busy(d.Item) || d.Run != nil
	if d.Agg != nil || prev.Agg == nil || !running {
		return d
	}
	out := prev
	out.Item, out.Comments, out.FindingMarks, out.RunLogs, out.Routing =
		d.Item, d.Comments, d.FindingMarks, d.RunLogs, d.Routing
	run := proto.RunStatus{Stage: model.Stage(d.Item.StateNote)}
	if d.Run != nil {
		run = *d.Run
	}
	run.Snapshot = true
	if run.SnapshotSHA == "" {
		run.SnapshotSHA = prev.Item.AnalysedSHA
	}
	out.Run = &run
	out.LabelsOmitted = d.LabelsOmitted
	return out
}

// busy reports whether an item is fetching or analysing.
func busy(it model.Item) bool {
	return it.State == model.StateFetching || it.State == model.StateAnalysing
}

// applyDetail shows d in the main pane. A reload of the same item keeps the
// Overview scroll position and block focus; the Code and Architecture pages
// keep their own state (SetDetail contracts).
func (m *Model) applyDetail(d proto.Detail) {
	same := m.detail != nil && m.detail.Item.ID == d.Item.ID
	if !same {
		m.overview.focus = ""
		m.overview.closeRaw()
	}
	m.code.SetDetail(d)
	m.pipe.SetDetail(d)
	m.arch.SetDetail(d)
	m.detail = &d
	m.detailPending = false
	if d.Agg != nil && d.Agg.Branch != nil && d.Agg.Branch.SupersededBy > 0 {
		m.superseded[d.Item.ID] = d.Agg.Branch.SupersededBy
	}
	m.supplyLabels(d)
	y := m.overview.vp.YOffset
	m.renderOverview()
	if same {
		m.overview.vp.SetYOffset(y)
	}
}

// onEvent handles a daemon event: the picker, its stage strip, the shown
// detail's item, and the event-triggered reloads (reloadFor).
func (m Model) onEvent(e proto.Event) (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{m.listen()}
	reload := false
	switch e.Type {
	case proto.EventItem:
		if e.Item != nil {
			reload = m.onItemEvent(*e.Item)
			m.picker.patch(*e.Item)
		} else {
			reload = true
		}
		cmds = append(cmds, m.queueReload(), m.ensureSpin())
	case proto.EventRemove:
		m.cache.drop(e.ItemID)
		m.dropLabels(e.ItemID)
		cmds = append(cmds, m.queueReload())
	case proto.EventStage:
		m.onStageEvent(e)
	case proto.EventPatch:
		reload = m.patchReloads(e)
	case proto.EventLog:
		m.logNote = e.Note
	case proto.EventSettings:
		cmds = append(cmds, m.onSettingsEvent())
	}
	if reload && m.shows(e.ItemID) {
		cmds = append(cmds, m.scheduleReload(e.ItemID))
	}
	return m, tea.Batch(cmds...)
}

// patchReloads reports whether a patch event changes what the page shows:
// a finished stage row or a comment change (no row, or a done, cached or
// failed one); a review progress row only while the Pipeline page shows
// the item (its routing table updates live).
func (m Model) patchReloads(e proto.Event) bool {
	if e.RunLog == nil {
		return true
	}
	switch e.RunLog.Status {
	case model.StageDone, model.StageCached, model.StageFailed:
		return true
	}
	return m.tab == TabPipeline && m.detail != nil && m.detail.Item.ID == e.ItemID
}

// onStageEvent records a stage row for the picker's stage strip and the
// stage map; it loads nothing. On the Pipeline page the shown item's run
// rows take the row too, so its stage strip moves without a reload.
func (m *Model) onStageEvent(e proto.Event) {
	if e.Stage != "" {
		m.stages[e.ItemID] = e.Stage
	}
	if e.RunLog == nil {
		return
	}
	m.runLogs[e.ItemID] = upsertRunLog(m.runLogs[e.ItemID], *e.RunLog)
	if m.tab == TabPipeline && m.detail != nil && m.detail.Item.ID == e.ItemID {
		d := *m.detail
		d.RunLogs = append(append([]model.RunLog(nil), d.RunLogs...), *e.RunLog)
		m.detail = &d
		m.pipe.SetDetail(d)
	}
}

// onItemEvent takes a changed item into the shown, previewed and cached
// details (the title line and the "updating" note follow it without a
// Detail call) and reports whether the item's analysis changed, which
// needs a reload: a run finished (or failed or stopped), the analysed or
// head commit moved, or the item became Stale or Ready.
func (m *Model) onItemEvent(it model.Item) bool {
	old, known := m.knownItem(it.ID)
	changed := !known || analysisChanged(old, it)
	if changed {
		m.dropLabels(it.ID)
	}
	if d, ok := m.cache.peek(it.ID); ok {
		d.Item = it
		m.cache.set(d)
	}
	if m.preview != nil && m.preview.Item.ID == it.ID {
		p := *m.preview
		p.Item = it
		m.preview = &p
		m.renderPreview()
	}
	if m.detail != nil && m.detail.Item.ID == it.ID {
		d := *m.detail
		d.Item = it
		m.detail = &d
		y := m.overview.vp.YOffset
		m.renderOverview()
		m.overview.vp.SetYOffset(y)
		if m.tab == TabPipeline {
			m.pipe.SetDetail(d)
		}
	}
	return changed
}

// knownItem is the last item seen for id in a shown, previewed or cached
// detail.
func (m Model) knownItem(id string) (model.Item, bool) {
	switch {
	case m.detail != nil && m.detail.Item.ID == id:
		return m.detail.Item, true
	case m.preview != nil && m.preview.Item.ID == id:
		return m.preview.Item, true
	}
	d, ok := m.cache.peek(id)
	return d.Item, ok
}

// analysisChanged reports whether cur changes the analysis old was shown
// with (see onItemEvent).
func analysisChanged(old, cur model.Item) bool {
	switch {
	case old.AnalysedSHA != cur.AnalysedSHA, old.HeadSHA != cur.HeadSHA:
		return true
	case busy(old) && !busy(cur):
		return true
	case old.State != cur.State && (cur.State == model.StateStale || cur.State == model.StateReady):
		return true
	}
	return false
}

// updatingNote is the dim "↻ updating review · <stage>" shown on the
// Overview's title line and in the tab bar's context while the item is
// fetching or analysing or a run is in flight (Detail.Run), plus "· showing
// <short sha>" when the page shows the last complete analysis of an older
// head; "" otherwise.
func updatingNote(d proto.Detail) string {
	it := d.Item
	if !busy(it) && d.Run == nil {
		return ""
	}
	stage := ""
	switch {
	case d.Run != nil && d.Run.Stage != "":
		stage = string(d.Run.Stage)
	case it.State == model.StateAnalysing && it.StateNote != "":
		stage = it.StateNote
	case it.State == model.StateFetching:
		stage = string(model.StageFetch)
	}
	s := "↻ updating review"
	if stage != "" {
		s += " · " + stage
	}
	if d.Run != nil && d.Run.Snapshot && d.Run.SnapshotSHA != "" {
		s += " · showing " + shortSHA(d.Run.SnapshotSHA)
	}
	return s
}

// tickCmd is the refresh tick's work: with a daemon that discovers by
// itself (Options.ReloadOnTick) it only re-reads the items; otherwise it
// runs discovery as before.
func (m Model) tickCmd() tea.Cmd {
	if m.opts.ReloadOnTick {
		return m.loadItems()
	}
	return m.discover()
}
