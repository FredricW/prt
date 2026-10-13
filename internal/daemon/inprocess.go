package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
)

// defaultConcurrency is used when Config.Pipeline.Concurrency is not positive.
const defaultConcurrency = 4

// InProcess implements Client by running discovery and the pipeline in this
// process. Analyses run on a worker pool limited by
// Config.Pipeline.Concurrency, most urgent first (or, in a workspace, on
// the Pool shared by every repo).
type InProcess struct {
	d   pipeline.Deps
	p   *pipeline.Pipeline
	log *log.Logger
	now func() time.Time

	// itemMu guards every read-modify-write of a stored Item (shared with
	// the pipeline through Deps.ItemLock).
	itemMu *sync.Mutex
	// discoverMu serialises Discover runs (see Discover).
	discoverMu sync.Mutex
	q          *queue
	wg         sync.WaitGroup

	evMu    sync.RWMutex
	closed  bool
	events  chan proto.Event
	ghNote  string // guarded by evMu
	srcNote string // guarded by evMu

	// Comment writes (see comments.go): commentLock serialises them per
	// item (shared with the pipeline through Deps.CommentLock), draftSeq
	// (from a random start) makes draft ids unique, viewer caches the GitHub
	// login used as a draft's author.
	commentLock func(itemID string) func()
	draftSeq    atomic.Uint64
	viewerMu    sync.Mutex
	viewer      string

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once

	// runs holds each item's running analysis (its cancel func and the
	// stage in flight), so removing the item (x, a discarded preview) stops
	// it before the paid stages run (see cancelRun) and a user stop knows
	// where it stopped (M5.1).
	runMu sync.Mutex
	runs  map[string]*runEntry

	// run executes one pipeline run (c.p.RunHead; tests substitute it to
	// see the options each job gets).
	run func(ctx context.Context, it model.Item, opt pipeline.RunOptions) (string, error)

	// snaps holds the last complete analysis of items being re-run
	// (stale-while-revalidate, M5.2; snapshot.go).
	snaps *snapshots
	// stopMu serialises read-modify-writes of stop records (resume.go).
	stopMu sync.Mutex
	// urgencyDays is, per item, the whole days of age and idle time its
	// stored urgency was last written for by discovery (guarded by itemMu;
	// discover.go): a pass where only the age moved within those days
	// writes nothing (M5.2).
	urgencyDays map[string]ageDays

	// settingsMu guards sdeps (M5.1, control.go).
	settingsMu sync.Mutex
	sdeps      SettingsDeps
	// saveMu serialises SaveSettings (read files, save, reload, apply).
	saveMu sync.Mutex

	// ws is the workspace part (M5.3, workspace.go): this client's repo
	// description, the shared worker pool and the cost ledger.
	ws wsState
}

// runEntry is one running analysis (guarded by InProcess.runMu).
type runEntry struct {
	cancel context.CancelCauseFunc
	stage  model.Stage // the last stage that started ("" before fetch)
}

var _ Client = (*InProcess)(nil)

// NewInProcess builds an in-process client with its own worker slots
// (Config.Pipeline.Concurrency) and its own cost ledger; it is
// NewInProcessWith(d, Options{}). d.Emit is wrapped so RunLog rows become
// events, drive item states and add each stage's cost to the ledger (a
// caller's Emit still runs), and d.ItemLock is set to the client's item
// lock.
func NewInProcess(d pipeline.Deps) *InProcess {
	return NewInProcessWith(d, Options{})
}

// NewInProcessWith builds an in-process client like NewInProcess, sharing
// o.Pool and o.Ledger with the other repos of a workspace (M5.3): with
// o.Pool set the queue runs on that pool from construction, before anything
// can queue a job, and never uses private slots; every stage's cost goes to
// o.Ledger (a private ledger when nil) under o.Repo.Key (spendMeter has the
// counting rule).
func NewInProcessWith(d pipeline.Deps, o Options) *InProcess {
	if o.Ledger == nil {
		o.Ledger = NewLedger()
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &InProcess{
		events: make(chan proto.Event, 256), ctx: ctx, cancel: cancel,
		itemMu: &sync.Mutex{}, commentLock: pipeline.NewKeyedLock(),
		runs: map[string]*runEntry{}, snaps: newSnapshots(maxSnapshots), urgencyDays: map[string]ageDays{},
		ws: wsState{repo: o.Repo, pool: o.Pool, ledger: o.Ledger},
	}
	// A random start keeps draft ids unique across processes too: with a
	// fixed clock (--now) every process would otherwise count from 1 and
	// a new draft could overwrite one stored by an earlier run.
	c.draftSeq.Store(rand.Uint64() >> 1)
	if d.Log == nil {
		d.Log = log.New(io.Discard)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	userEmit, meter := d.Emit, newSpendMeter()
	d.Emit = func(rl model.RunLog) {
		if aggregateLanded(rl) {
			// Before any event of the row: the reloads it triggers must see
			// the new analysis, not the snapshot.
			c.snaps.drop(rl.ItemID)
		}
		// Before the row's events, so a Workspace read they trigger sees it.
		c.addSpend(meter.spent(rl))
		c.send(proto.Event{Type: proto.EventStage, ItemID: rl.ItemID, Stage: rl.Stage, RunLog: &rl})
		c.onStage(rl)
		if userEmit != nil {
			userEmit(rl)
		}
	}
	d.ItemLock = c.itemMu
	d.CommentLock = c.commentLock
	c.d, c.log, c.now = d, d.Log, d.Now
	c.p = pipeline.New(d)
	c.run = c.p.RunHead
	c.q = newQueueOn(o.Pool, slotsFor(d.Config), &c.wg, c.analyse)
	c.recoverStates(ctx)
	c.sweepLabels(ctx)
	return c
}

// slotsFor is the worker-pool size of a configuration.
func slotsFor(cfg config.Config) int {
	if cfg.Pipeline.Concurrency <= 0 {
		return defaultConcurrency
	}
	return cfg.Pipeline.Concurrency
}

// cfg returns the live prt.toml configuration (the pipeline's, which
// SaveSettings replaces; M5.1). Read it per use, never cache it.
func (c *InProcess) cfg() config.Config { return c.p.Config() }

// strategy is the live auto-review strategy.
func (c *InProcess) strategy() model.AutoReview { return c.cfg().Pipeline.Strategy() }

// SetGitHubNote records why GitHub is disabled (no token, origin not on
// GitHub, --offline). With no GitHub client, each discovery reports it as an
// EventLog note so the status bar explains the missing PRs. With a client
// wired, the note (when set) describes where it came from (e.g. a
// --fixtures file) and the settings page shows it instead of "gh auth
// token ok".
func (c *InProcess) SetGitHubNote(note string) {
	c.evMu.Lock()
	c.ghNote = note
	c.evMu.Unlock()
}

// SetSourceNote records a standing note about where answers come from
// (e.g. "Jev: replay of testdata/recordings/jev (offline)"). Each discovery
// reports it as an EventLog note so the status bar says so; "" clears it.
func (c *InProcess) SetSourceNote(note string) {
	c.evMu.Lock()
	c.srcNote = note
	c.evMu.Unlock()
}

func (c *InProcess) sourceNote() string {
	c.evMu.RLock()
	defer c.evMu.RUnlock()
	return c.srcNote
}

func (c *InProcess) gitHubNote() string {
	c.evMu.RLock()
	defer c.evMu.RUnlock()
	if c.ghNote == "" {
		return "GitHub disabled; local branches only"
	}
	return c.ghNote
}

// send never blocks: the UI re-reads Detail on the next event anyway.
func (c *InProcess) send(e proto.Event) {
	c.evMu.RLock()
	defer c.evMu.RUnlock()
	if c.closed {
		return
	}
	select {
	case c.events <- e:
	default:
	}
}

// onStage turns "stage started" rows into item states and finished rows into
// patch events (the stage's output is stored: re-read Detail), so screens
// fill in as labels land. A progress row of a running stage
// (pipeline.IsProgress: the review stage's "k/n reviewed" after every
// routing-row change) is a patch event too, with no state change, so the
// Pipeline page's routing table updates live. When aggregate finishes, the
// stored item (with the picker fields the pipeline copied from the
// aggregate: urgency, minutes, LabelsUnavailable, Breaking, NeedsEyes) goes
// out as an item event.
func (c *InProcess) onStage(rl model.RunLog) {
	if pipeline.IsProgress(rl) {
		c.send(proto.Event{Type: proto.EventPatch, ItemID: rl.ItemID, Stage: rl.Stage, RunLog: &rl})
		return
	}
	switch rl.Status {
	case model.StageDone, model.StageCached, model.StageFailed:
		c.send(proto.Event{Type: proto.EventPatch, ItemID: rl.ItemID, Stage: rl.Stage})
		if rl.Stage == model.StageAggregate && rl.Status != model.StageFailed {
			c.sendItem(rl.ItemID)
		}
		return
	case model.StageRunning:
	case model.StageStopped:
		// A stop between stages records the stage about to start with no
		// Running row first: the item's "stopped at" note names it too.
		c.noteStage(rl.ItemID, rl.Stage)
		return
	default:
		return
	}
	c.noteStage(rl.ItemID, rl.Stage)
	if rl.Stage == model.StageFetch {
		c.setState(rl.ItemID, model.StateFetching, "")
		return
	}
	c.setState(rl.ItemID, model.StateAnalysing, string(rl.Stage))
}

// updateItem applies fn to the stored item under the item lock, stores it
// and emits an item event. It reports false if the item does not exist.
func (c *InProcess) updateItem(ctx context.Context, id string, fn func(*model.Item)) (model.Item, bool, error) {
	c.itemMu.Lock()
	it, err := c.d.Store.Item(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		c.itemMu.Unlock()
		return it, false, nil
	}
	if err != nil {
		c.itemMu.Unlock()
		return it, false, fmt.Errorf("daemon: load %s: %w", id, err)
	}
	fn(&it)
	err = c.d.Store.UpsertItem(ctx, it)
	c.itemMu.Unlock()
	if err != nil {
		return it, true, fmt.Errorf("daemon: store %s: %w", id, err)
	}
	c.send(proto.Event{Type: proto.EventItem, ItemID: it.ID, Item: &it})
	return it, true, nil
}

// sendItem emits an item event with the stored item (nothing if it is gone).
func (c *InProcess) sendItem(id string) {
	it, err := c.d.Store.Item(context.Background(), id)
	if err != nil {
		return
	}
	c.send(proto.Event{Type: proto.EventItem, ItemID: it.ID, Item: &it})
}

func (c *InProcess) setState(id string, st model.ItemState, note string) {
	if _, _, err := c.updateItem(context.Background(), id, func(it *model.Item) {
		it.State, it.StateNote = st, note
	}); err != nil {
		c.log.Warn("set item state", "item", id, "state", st, "err", err)
	}
}

// Items implements Client: items sorted by section, then urgency, then most
// recently updated, with UrgencyQuartile relative to all items. Previews
// (model.SourcePreview) are never listed.
func (c *InProcess) Items(ctx context.Context) ([]model.Item, error) {
	all, err := c.d.Store.Items(ctx)
	if err != nil {
		return nil, fmt.Errorf("daemon: items: %w", err)
	}
	items := all[:0]
	for _, it := range all {
		if it.Source != model.SourcePreview {
			items = append(items, it)
		}
	}
	top := 0.0
	for _, it := range items {
		top = max(top, it.Urgency)
	}
	for i := range items {
		items[i].UrgencyQuartile = quartile(items[i].Urgency, top)
	}
	sortItems(items)
	return items, nil
}

// quartile maps a score to 0–4 urgency-bar cells relative to the top score:
// the top item fills all four, a non-positive score none.
func quartile(u, top float64) int {
	if top <= 0 || u <= 0 {
		return 0
	}
	return min(4, max(1, int(math.Ceil(4*u/top-1e-9))))
}

var sectionOrder = map[model.Section]int{
	model.SectionNeedsYou: 0, model.SectionYours: 1, model.SectionBranches: 2, model.SectionInvolved: 3, model.SectionAdded: 4,
}

func sectionRank(s model.Section) int {
	if r, ok := sectionOrder[s]; ok {
		return r
	}
	return len(sectionOrder)
}

func sortItems(items []model.Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if ra, rb := sectionRank(a.Section), sectionRank(b.Section); ra != rb {
			return ra < rb
		}
		if a.Urgency != b.Urgency {
			return a.Urgency > b.Urgency
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID < b.ID
	})
}

// Refresh implements Client: queues the item for analysis (deduplicated).
// With p.Force (key `r`) the head is re-resolved first (local ref for
// branches, GitHub for PRs) and the fetch cache is bypassed, so new commits
// are analysed without waiting for the next discovery. p.FromStage (the
// Pipeline page's `r` on a stage) makes that stage and the later ones
// ignore their caches; such a job follows a run in flight. With p.User (a
// hand-made `r`) the job is explicit: it records Item.RequestedSHA and runs
// the model stages whatever the strategy; without it (the TUI's automatic
// first load, even when forced) the auto_review strategy gates it (M5.1).
func (c *InProcess) Refresh(ctx context.Context, p proto.ItemParams) error {
	if p.FromStage != "" && !pipeline.ValidStage(p.FromStage) {
		return fmt.Errorf("daemon: refresh %s: unknown stage %q", p.ItemID, p.FromStage)
	}
	if p.Force {
		if err := c.refreshHead(ctx, p.ItemID); err != nil {
			return err
		}
	}
	it, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return fmt.Errorf("daemon: refresh %s: %w", p.ItemID, err)
	}
	j := job{itemID: it.ID, urgency: it.Urgency, force: p.Force}
	if p.FromStage != "" {
		j.fromStage, j.rerun = p.FromStage, true
	}
	if p.User {
		return c.pushExplicit(ctx, it, j)
	}
	c.q.push(j)
	return nil
}

// pushExplicit queues j as an explicit request for it (job.explicit): it
// first records the item's current head as requested (Item.RequestedSHA),
// so the request survives a restart and the item's later automatic runs of
// this head are allowed too (model.AutoReview.Allows).
func (c *InProcess) pushExplicit(ctx context.Context, it model.Item, j job) error {
	j.explicit = true
	if err := c.markRequested(ctx, it); err != nil {
		return err
	}
	c.q.push(j)
	return nil
}

// markRequested sets it's RequestedSHA to its head (when known and not
// already set), emitting an item event.
func (c *InProcess) markRequested(ctx context.Context, it model.Item) error {
	if it.HeadSHA == "" || it.RequestedSHA == it.HeadSHA {
		return nil
	}
	_, _, err := c.updateItem(ctx, it.ID, func(s *model.Item) {
		if s.HeadSHA != "" {
			s.RequestedSHA = s.HeadSHA
		}
	})
	return err
}

// refreshHead re-reads one item's head (and, for PRs, its metadata). A
// lookup failure keeps the stored head: the refresh still re-runs.
func (c *InProcess) refreshHead(ctx context.Context, id string) error {
	it, err := c.d.Store.Item(ctx, id)
	if err != nil {
		return fmt.Errorf("daemon: refresh %s: %w", id, err)
	}
	switch {
	case it.Kind == model.KindPR && it.Number > 0 && c.d.GH != nil:
		pr, err := c.d.GH.PR(ctx, it.Number)
		if err != nil {
			c.log.Warn("refresh: PR lookup failed; keeping stored head", "item", id, "err", err)
			return nil
		}
		viewer, _ := c.d.GH.Viewer(ctx)
		head := c.prHead(ctx, pr)
		// No stale mark: the item is about to be re-analysed.
		return c.upsertDiscovered(ctx, id, func(it *model.Item) {
			st, note := it.State, it.StateNote
			applyPR(it, pr, head)
			it.State, it.StateNote = st, note
			if it.Source != model.SourceAdded && it.Source != model.SourcePreview {
				it.Section = prSection(pr, head, viewer)
			}
		})
	case it.Kind == model.KindBranch && it.Ref != "":
		sha, err := c.d.Git.ResolveRef(ctx, it.Ref)
		if err != nil || sha == it.HeadSHA {
			return nil
		}
		_, _, err = c.updateItem(ctx, id, func(it *model.Item) { it.HeadSHA = sha })
		return err
	}
	return nil
}

// EditGroups implements Client: validates and stores the edit, then queues
// the item (not forced: fetch, labels, embeddings and pairs come from the
// cache; the cluster stage applies the stored edits). The job is a rerun so
// it follows a run already in flight instead of being deduped away.
func (c *InProcess) EditGroups(ctx context.Context, p proto.GroupEditParams) error {
	e := p.Edit
	if e.ItemID == "" {
		e.ItemID = p.ItemID
	}
	if e.ItemID != p.ItemID {
		return fmt.Errorf("daemon: edit groups: edit for %q sent for item %q", e.ItemID, p.ItemID)
	}
	switch e.Op {
	case model.EditMerge, model.EditSplit:
		if len(e.Units) == 0 {
			return fmt.Errorf("daemon: edit groups %s: %s without units", p.ItemID, e.Op)
		}
	case model.EditReset:
	default:
		return fmt.Errorf("daemon: edit groups %s: unknown op %q", p.ItemID, e.Op)
	}
	it, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return fmt.Errorf("daemon: edit groups %s: %w", p.ItemID, err)
	}
	if e.At.IsZero() {
		e.At = c.now()
	}
	if err := c.d.Store.AppendGroupEdit(ctx, e); err != nil {
		return fmt.Errorf("daemon: edit groups %s: %w", p.ItemID, err)
	}
	// rerun: a run already in flight may have read the edits before this
	// one was stored, so another run must follow it. Explicit: the user
	// acted on this item.
	return c.pushExplicit(ctx, it, job{itemID: it.ID, urgency: it.Urgency, rerun: true})
}

// Analyse queues items for background analysis as one batch, so they start
// in urgency order. Unknown ids are an error; nothing is queued then. A
// direct call is an explicit request (tests, tools): the daemon's own
// automatic paths use queueAuto, which the auto_review strategy gates.
func (c *InProcess) Analyse(ctx context.Context, force bool, ids ...string) error {
	var items []model.Item
	jobs, err := c.jobsFor(ctx, ids, func(it model.Item) job {
		items = append(items, it)
		return job{itemID: it.ID, urgency: it.Urgency, force: force, explicit: true}
	})
	if err != nil {
		return err
	}
	for _, it := range items {
		if err := c.markRequested(ctx, it); err != nil {
			return err
		}
	}
	c.q.push(jobs...)
	return nil
}

// queueAuto queues automatic (gated) non-forced runs of ids as one batch,
// in urgency order. Unknown ids are an error; nothing is queued then.
func (c *InProcess) queueAuto(ctx context.Context, ids ...string) error {
	jobs, err := c.jobsFor(ctx, ids, func(it model.Item) job {
		return job{itemID: it.ID, urgency: it.Urgency}
	})
	if err != nil {
		return err
	}
	c.q.push(jobs...)
	return nil
}

// jobsFor loads ids and builds one job each with mk.
func (c *InProcess) jobsFor(ctx context.Context, ids []string, mk func(model.Item) job) ([]job, error) {
	jobs := make([]job, 0, len(ids))
	for _, id := range ids {
		it, err := c.d.Store.Item(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("daemon: refresh %s: %w", id, err)
		}
		jobs = append(jobs, mk(it))
	}
	return jobs, nil
}

// stopQueued is the StateNote of an item stopped before its run started.
const stopQueued = "queued"

// analyse runs one queued job on a worker. The run is registered in runs
// before anything else, so a user stop (Stop) either cancels it or is seen
// by the stop-mark check below. It is unregistered before the final state
// is written, so the reload that state's item event triggers sees no run in
// flight (Detail.Run nil).
func (c *InProcess) analyse(j job) {
	runCtx, cancel := context.WithCancelCause(c.ctx)
	entry := &runEntry{cancel: cancel}
	c.register(j.itemID, entry)
	defer func() {
		c.unregister(j.itemID, entry)
		cancel(nil)
	}()
	if c.q.stoppedBefore(j) {
		c.unregister(j.itemID, entry)
		c.rememberDropped(c.ctx, j)
		c.setStoppedUnlessAnalysed(j.itemID, stopQueued)
		return
	}
	it, err := c.d.Store.Item(c.ctx, j.itemID)
	if err != nil {
		c.log.Warn("analyse: item gone", "item", j.itemID, "err", err)
		return
	}
	pl, ok := c.gate(j, it)
	if !ok {
		c.log.Debug("analyse: automatic job for a stopped item skipped", "item", it.ID)
		return
	}
	if !pl.opts.AggregateOnly {
		if j.resume != nil {
			pl.opts = c.resumeOptions(c.ctx, it.ID, *j.resume, pl.opts)
		}
		// Before the pipeline writes anything, and before the item event
		// below makes screens reload.
		c.captureSnapshot(c.ctx, it)
	}
	c.setState(it.ID, model.StateFetching, "")
	head, err := c.run(runCtx, it, pl.opts)
	stage := c.runStage(entry)
	c.unregister(j.itemID, entry)
	c.finish(runCtx, it.ID, outcome{head: head, err: err, explicit: j.explicit, notAnalysed: pl.notAnalysed,
		opts: pl.opts, stage: stage})
}

// register records the item's running analysis.
func (c *InProcess) register(id string, e *runEntry) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	c.runs[id] = e
}

// unregister forgets the item's running analysis if it is still e.
func (c *InProcess) unregister(id string, e *runEntry) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if c.runs[id] == e {
		delete(c.runs, id)
	}
}

// plan is how a job runs once the strategy has been applied (gate).
type plan struct {
	opts pipeline.RunOptions
	// notAnalysed: a full free-only run of an item with no analysis of its
	// head; finish marks it model.StateNotAnalysed.
	notAnalysed bool
}

// gate applies the auto_review strategy when a job starts (so a strategy
// change applies to queued jobs; DECISIONS "M5.1 user override"). An
// explicit job, or one whose item the strategy allows (covered, or its head
// requested), runs as queued. Otherwise the run is FreeOnly: an item with a
// stored analysis (of its current head, or an older one when Stale) is only
// re-aggregated (never downgraded, its stored analysis never replaced);
// any other item runs the free stages and ends not analysed. An automatic job of a stopped item does not run at all
// (nothing restarts a stopped item automatically): ok is false.
func (c *InProcess) gate(j job, it model.Item) (p plan, ok bool) {
	opts := j.options()
	if j.explicit {
		return plan{opts: opts}, true
	}
	if it.State == model.StateStopped {
		return plan{}, false
	}
	if c.strategy().Allows(it) {
		return plan{opts: opts}, true
	}
	opts.FreeOnly = true
	if it.AnalysedSHA != "" {
		// Any stored analysis, of this head or an older one (Stale): only
		// re-aggregate it. A free-only full run would fetch and parse the
		// new head and replace the stored files and units, leaving them out
		// of step with the analysed head's groups, labels and fetch.
		// notAnalysed: AnalysedSHA never moves on a gated run.
		return plan{opts: pipeline.RunOptions{AggregateOnly: true, FreeOnly: true}, notAnalysed: true}, true
	}
	return plan{opts: opts, notAnalysed: true}, true
}

// runStage returns the stage e's run was in (fetch before the first row).
func (c *InProcess) runStage(e *runEntry) model.Stage {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if e.stage == "" {
		return model.StageFetch
	}
	return e.stage
}

// noteStage records the stage the item's running analysis entered.
func (c *InProcess) noteStage(id string, s model.Stage) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	if e := c.runs[id]; e != nil {
		e.stage = s
	}
}

// cancelRun stops the item's running analysis, if any: the item was
// removed, so its later stages (Jev, paid reviews) must not run. The queue
// never runs two jobs for one item at once, so the entry is that run's.
// The cause is nil: finish treats it as an interruption, not a user stop.
func (c *InProcess) cancelRun(id string) {
	c.cancelRunCause(id, nil)
}

// cancelRunCause cancels the item's running analysis with cause and
// reports whether one was running.
func (c *InProcess) cancelRunCause(id string, cause error) bool {
	c.runMu.Lock()
	e := c.runs[id]
	c.runMu.Unlock()
	if e == nil {
		return false
	}
	e.cancel(cause)
	return true
}

// outcome is what a run returned plus how it was started.
type outcome struct {
	head string
	err  error
	// explicit: the user asked for it; the analysed head becomes RequestedSHA.
	explicit bool
	// notAnalysed: a free-only run of an item without an analysis (plan).
	notAnalysed bool
	// opts are the options the run had: an aggregate-only run that is
	// stopped keeps a stored analysis; a stopped run's FromStage and Force
	// are remembered for Resume (M5.2).
	opts pipeline.RunOptions
	// stage is the stage in flight when the run ended (for a user stop).
	stage model.Stage
}

// stoppedByUser reports whether a run ended because the user stopped it
// (Stop: cancel cause pipeline.ErrStopped).
func stoppedByUser(runCtx context.Context, err error) bool {
	return errors.Is(err, pipeline.ErrStopped) || errors.Is(context.Cause(runCtx), pipeline.ErrStopped)
}

// finish records the outcome of a run that analysed o.head. If discovery
// moved the item's head while the run was in flight, the result describes
// an old commit: the item becomes Stale instead of Ready. A run the user
// stopped leaves the item StateStopped at the stage in flight (its options
// remembered for Resume). A run that failed because runCtx was cancelled
// otherwise (quit, daemon stop, the item removed) was interrupted, not
// broken: the item goes back to "not analysed" so it is queued again. A
// free-only run of an unanalysed item leaves it StateNotAnalysed (and
// AnalysedSHA unset). A completed run drops the item's snapshot; the run
// log keeps the item's last keepRuns runs whatever the outcome.
func (c *InProcess) finish(runCtx context.Context, id string, o outcome) {
	c.pruneRunLog(id)
	// A run that returned without error completed, even if a stop arrived
	// after it (before this point): its result is recorded.
	if o.err != nil && stoppedByUser(runCtx, o.err) {
		c.log.Info("analysis stopped", "item", id, "stage", o.stage)
		c.rememberStoppedRun(id, o.opts, o.stage)
		if o.opts.AggregateOnly {
			c.setStoppedUnlessAnalysed(id, string(o.stage))
		} else {
			c.setStopped(id, string(o.stage))
		}
		return
	}
	if o.err == nil {
		c.snaps.drop(id)
		if !o.opts.AggregateOnly {
			c.forgetStop(context.Background(), id)
		}
	}
	if o.err != nil && runCtx.Err() != nil {
		c.log.Info("analysis interrupted", "item", id, "err", o.err)
		c.resetState(id)
		return
	}
	if o.err != nil {
		c.log.Warn("analysis failed", "item", id, "err", o.err)
		c.setState(id, model.StateError, o.err.Error())
		return
	}
	if _, _, err := c.updateItem(context.Background(), id, func(it *model.Item) {
		if o.notAnalysed {
			markNotAnalysed(it)
			return
		}
		if o.head != "" {
			it.AnalysedSHA = o.head
			if o.explicit {
				it.RequestedSHA = o.head
			}
		}
		if o.head != "" && it.HeadSHA != "" && it.HeadSHA != o.head {
			it.State, it.StateNote = model.StateStale, staleNote
			return
		}
		it.State, it.StateNote = model.StateReady, ""
	}); err != nil {
		c.log.Warn("set item state", "item", id, "err", err)
	}
}

// markNotAnalysed sets the state after a free-only run: not analysed, or
// Stale (Ready) when an earlier analysis of another (this) head is stored,
// so an analysed item is never downgraded.
func markNotAnalysed(it *model.Item) {
	switch it.AnalysedSHA {
	case "":
		it.State, it.StateNote = model.StateNotAnalysed, ""
	case it.HeadSHA:
		it.State, it.StateNote = model.StateReady, ""
	default:
		it.State, it.StateNote = model.StateStale, staleNote
	}
}

// setStopped marks an item stopped by the user at stage ("queued" when it
// had not started).
func (c *InProcess) setStopped(id, stage string) {
	c.setState(id, model.StateStopped, stage)
}

// setStoppedUnlessAnalysed is setStopped for work that would not have
// produced the item's analysis: a dropped waiting job, or a stopped
// aggregate-only run. An item with a stored analysis keeps it and shows it
// as Ready (or Stale when its head moved), so later automatic jobs
// (re-aggregations, re-routes) still reach it; only an item with no
// analysis becomes StateStopped (DECISIONS "M5.1 review fixes 2").
func (c *InProcess) setStoppedUnlessAnalysed(id, stage string) {
	if _, _, err := c.updateItem(context.Background(), id, func(it *model.Item) {
		switch it.AnalysedSHA {
		case "":
			it.State, it.StateNote = model.StateStopped, stage
		case it.HeadSHA:
			it.State, it.StateNote = model.StateReady, ""
		default:
			it.State, it.StateNote = model.StateStale, staleNote
		}
	}); err != nil {
		c.log.Warn("set item state", "item", id, "err", err)
	}
}

// resetState marks an interrupted item as not analysed, or Stale when an
// earlier analysis of another head is still stored.
func (c *InProcess) resetState(id string) {
	if _, _, err := c.updateItem(context.Background(), id, func(it *model.Item) {
		if it.AnalysedSHA != "" {
			it.State, it.StateNote = model.StateStale, staleNote
			if it.AnalysedSHA == it.HeadSHA {
				it.State, it.StateNote = model.StateReady, ""
			}
			return
		}
		it.State, it.StateNote = "", ""
	}); err != nil {
		c.log.Warn("reset item state", "item", id, "err", err)
	}
}

// recoverStates resets items left Fetching or Analysing by a process that
// died mid-run (SIGKILL, closed terminal), so they are analysed again
// instead of spinning forever, and deletes leftover previews (the prompt
// that opened them is gone).
func (c *InProcess) recoverStates(ctx context.Context) {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		c.log.Warn("recover item states", "err", err)
		return
	}
	for _, it := range items {
		if it.Source == model.SourcePreview {
			if err := c.removePreview(ctx, it.ID); err != nil {
				c.log.Warn("remove leftover preview", "item", it.ID, "err", err)
			}
			continue
		}
		if it.State == model.StateFetching || it.State == model.StateAnalysing {
			c.resetState(it.ID)
		}
		c.recoverPending(ctx, it.ID)
	}
}

// recoverPending turns the item's local pending comments back into drafts.
// A comment is pending only while a review submission is in flight in this
// process, so one found at start-up was left by a process that died
// between marking it pending and recording the outcome; as a draft it can
// be edited, deleted or submitted again (DECISIONS "Pending comments are
// recovered as drafts at start-up").
func (c *InProcess) recoverPending(ctx context.Context, itemID string) {
	unlock := c.lockComments(itemID)
	defer unlock()
	cs, err := c.d.Store.Comments(ctx, itemID)
	if err != nil {
		c.log.Warn("recover pending comments", "item", itemID, "err", err)
		return
	}
	var stuck []model.Comment
	for _, cm := range cs {
		if cm.Source == model.CommentUser && cm.State == model.CommentPending {
			stuck = append(stuck, cm)
		}
	}
	if len(stuck) == 0 {
		return
	}
	c.log.Warn("pending comments from an interrupted submission are drafts again; check the PR on GitHub before submitting them again",
		"item", itemID, "comments", len(stuck))
	if err := c.putStates(ctx, stuck, model.CommentDraft); err != nil {
		c.log.Warn("recover pending comments", "item", itemID, "err", err)
	}
}

// AnalysePending queues, as automatic (gated) jobs in urgency order, every
// never-run item (State "") and every not-analysed item the strategy now
// allows, so a daemon with no TUI attached fills the cache for the items
// the auto_review strategy covers (daemon start, every discovery tick).
// Stopped, stale, failed and ready items are never queued here.
func (c *InProcess) AnalysePending(ctx context.Context) error {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		return fmt.Errorf("daemon: items: %w", err)
	}
	ids := c.pendingIDs(items)
	if len(ids) == 0 {
		return nil
	}
	return c.queueAuto(ctx, ids...)
}

// pendingIDs are the items AnalysePending queues.
func (c *InProcess) pendingIDs(items []model.Item) []string {
	auto := c.strategy()
	var ids []string
	for _, it := range items {
		if it.State == "" || (it.State == model.StateNotAnalysed && auto.Allows(it)) {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

// newlyAllowed are the not-analysed items the strategy now allows.
func (c *InProcess) newlyAllowed(items []model.Item) []string {
	auto := c.strategy()
	var ids []string
	for _, it := range items {
		if it.State == model.StateNotAnalysed && it.Source != model.SourcePreview && auto.Allows(it) {
			ids = append(ids, it.ID)
		}
	}
	return ids
}

// RunSync runs the pipeline for an item and waits (tests, --snapshot). A
// direct call: the strategy does not gate it and it records no request.
func (c *InProcess) RunSync(ctx context.Context, itemID string) error {
	it, err := c.d.Store.Item(ctx, itemID)
	if err != nil {
		return fmt.Errorf("daemon: run %s: %w", itemID, err)
	}
	head, err := c.p.RunHead(ctx, it, pipeline.RunOptions{})
	c.finish(ctx, itemID, outcome{head: head, err: err})
	return err
}

// Add implements Client. It accepts "#N"/"N" (needs GitHub), a local ref, or
// a free-text query matching an open PR's title (first hit). The item lands
// in the Added section and analysis starts immediately. An item already in
// the picker keeps its section and is just (re-)queued; a preview (Preview)
// is promoted to Added. The search hits' queries ("#N", a ref name) are
// accepted as they are, closed and merged PRs included.
func (c *InProcess) Add(ctx context.Context, query string) (model.Item, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return model.Item{}, errors.New("daemon: add: empty query")
	}
	it, err := c.resolveAdd(ctx, q, model.SourceAdded)
	if err != nil {
		return model.Item{}, err
	}
	// Adding is an explicit request (M5.1): it runs whatever the strategy.
	if err := c.pushExplicit(ctx, it, job{itemID: it.ID, urgency: it.Urgency, user: true}); err != nil {
		return it, err
	}
	if s, err := c.d.Store.Item(ctx, it.ID); err == nil {
		it = s
	}
	return it, nil
}

// analyseNow queues it ahead of every other job (job.user), as an
// automatic job: a tab preview starts in the next free slot, but the
// auto_review strategy decides whether its model stages run (previews are
// covered only by "all").
func (c *InProcess) analyseNow(it model.Item) {
	c.q.push(job{itemID: it.ID, urgency: it.Urgency, user: true})
}

// resolveAdd finds the item q names ("#N"/"N", a ref, or the first open PR
// matching free text) and stores it with source src (SourceAdded or
// SourcePreview) unless it exists; see addItem.
func (c *InProcess) resolveAdd(ctx context.Context, q string, src model.ItemSource) (model.Item, error) {
	verb := "add"
	if src == model.SourcePreview {
		verb = "preview"
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(q, "#")); err == nil {
		if c.d.GH == nil {
			return model.Item{}, fmt.Errorf("daemon: cannot %s #%d: GitHub unavailable", verb, n)
		}
		pr, err := c.d.GH.PR(ctx, n)
		if err != nil {
			return model.Item{}, fmt.Errorf("daemon: %s #%d: %w", verb, n, err)
		}
		return c.addPR(ctx, pr, src)
	}
	sha, refErr := c.d.Git.ResolveRef(ctx, q)
	if refErr == nil {
		return c.addBranch(ctx, q, sha, src)
	}
	if c.d.GH == nil {
		return model.Item{}, fmt.Errorf("daemon: %s %q: %w", verb, q, refErr)
	}
	prs, err := c.d.GH.SearchPRs(ctx, "is:pr is:open "+q, 1)
	if err != nil {
		return model.Item{}, fmt.Errorf("daemon: %s %q: %w", verb, q, errors.Join(refErr, err))
	}
	if len(prs) == 0 {
		return model.Item{}, fmt.Errorf("daemon: %s %q: no matching ref or open PR", verb, q)
	}
	return c.addPR(ctx, prs[0], src)
}

func (c *InProcess) addPR(ctx context.Context, pr gh.PR, src model.ItemSource) (model.Item, error) {
	head := c.prHead(ctx, pr)
	return c.addItem(ctx, model.PRItemID(pr.Number), src, func(it *model.Item) {
		applyPR(it, pr, head)
	})
}

// addBranch stores a ref as an item; the tip's subject, author, upstream
// and commit date come from the ref listing when ref is a branch.
func (c *InProcess) addBranch(ctx context.Context, ref, sha string, src model.ItemSource) (model.Item, error) {
	def, err := c.d.Git.DefaultBranch(ctx)
	if err != nil {
		return model.Item{}, fmt.Errorf("daemon: default branch: %w", err)
	}
	r, found := c.findRef(ctx, ref)
	return c.addItem(ctx, model.BranchItemID(ref), src, func(it *model.Item) {
		it.Kind, it.Ref, it.BaseRef, it.HeadSHA = model.KindBranch, ref, def, sha
		if found {
			it.Title, it.Author, it.Upstream = r.Subject, r.Author, r.Upstream
			it.LastCommitAt, it.UpdatedAt = r.CommitDate, r.CommitDate
			it.RemoteRef = r.Remote
		}
		if strings.HasPrefix(ref, "refs/remotes/") {
			it.RemoteRef = true
		}
		if it.Title == "" {
			it.Title = ref
		}
	})
}

// findRef returns the listed local or remote-tracking ref named name.
func (c *InProcess) findRef(ctx context.Context, name string) (gitx.Ref, bool) {
	refs, err := c.d.Git.Refs(ctx, true)
	if err != nil {
		return gitx.Ref{}, false
	}
	for _, r := range refs {
		if r.Name == name || r.FullName == name {
			return r, true
		}
	}
	return gitx.Ref{}, false
}

// addItem returns the stored item if it exists, else creates an item of
// source src filled by fill: an Added item in the Added section, or a
// preview with no section. Adding a stored preview promotes it to Added
// (with an item event); any other stored item is returned unchanged.
func (c *InProcess) addItem(ctx context.Context, id string, src model.ItemSource, fill func(*model.Item)) (model.Item, error) {
	c.itemMu.Lock()
	it, err := c.d.Store.Item(ctx, id)
	if err == nil {
		c.itemMu.Unlock()
		if it.Source == model.SourcePreview && src == model.SourceAdded {
			return c.promotePreview(ctx, id)
		}
		return it, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		c.itemMu.Unlock()
		return it, fmt.Errorf("daemon: add %s: %w", id, err)
	}
	it = model.Item{ID: id, Source: src}
	if src != model.SourcePreview {
		it.Section = model.SectionAdded
	}
	fill(&it)
	days := c.setUrgency(ctx, &it, false)
	err = c.d.Store.UpsertItem(ctx, it)
	if err == nil {
		c.urgencyDays[id] = days
	}
	c.itemMu.Unlock()
	if err != nil {
		return it, fmt.Errorf("daemon: add %s: %w", id, err)
	}
	c.send(proto.Event{Type: proto.EventItem, ItemID: it.ID, Item: &it})
	return it, nil
}

// promotePreview turns a preview into an Added item (source and section),
// emitting an item event. An item that stopped being a preview meanwhile
// is left as it is.
func (c *InProcess) promotePreview(ctx context.Context, id string) (model.Item, error) {
	it, ok, err := c.updateItem(ctx, id, func(it *model.Item) {
		if it.Source == model.SourcePreview {
			it.Source, it.Section = model.SourceAdded, model.SectionAdded
		}
	})
	if err != nil {
		return it, err
	}
	if !ok {
		return it, fmt.Errorf("daemon: add %s: %w", id, store.ErrNotFound)
	}
	return it, nil
}

// Remove implements Client. Removing a preview deletes it silently (no
// remove event: it was never listed). The labels of the removed item that
// no other stored item references are pruned (a preview's are kept until
// the start-up sweep, so tabbing back to it is free; see sweepLabels).
func (c *InProcess) Remove(ctx context.Context, itemID string) error {
	c.itemMu.Lock()
	it, err := c.d.Store.Item(ctx, itemID)
	preview := err == nil && it.Source == model.SourcePreview
	var subjects []string
	if !preview {
		subjects = c.removalSubjects(ctx, itemID)
	}
	err = c.d.Store.RemoveItem(ctx, itemID)
	c.itemMu.Unlock()
	if err != nil {
		return fmt.Errorf("daemon: remove %s: %w", itemID, err)
	}
	c.cancelRun(itemID)
	c.forgetItem(ctx, itemID)
	if !preview {
		c.send(proto.Event{Type: proto.EventRemove, ItemID: itemID})
		c.pruneLabels(ctx, itemID, subjects)
	}
	return nil
}

// forgetItem drops what the daemon keeps about a removed item: its
// snapshot and its stop record.
func (c *InProcess) forgetItem(ctx context.Context, itemID string) {
	c.snaps.drop(itemID)
	c.forgetStop(ctx, itemID)
}

// Checkout implements Client.
func (c *InProcess) Checkout(ctx context.Context, p proto.CheckoutParams) (proto.CheckoutResult, error) {
	it, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return proto.CheckoutResult{}, fmt.Errorf("daemon: checkout %s: %w", p.ItemID, err)
	}
	res := proto.CheckoutResult{Ref: it.Ref}
	if it.Ref == "" {
		return res, fmt.Errorf("daemon: %s has no head ref", it.ID)
	}
	if it.CrossRepo {
		// The head ref names a branch in the fork; a same-named local branch
		// is unrelated. Check out the head commit (detached) if it is local.
		if it.HeadSHA == "" {
			return res, fmt.Errorf("daemon: #%d is from a fork and its head is unknown; run `gh pr checkout %d`", it.Number, it.Number)
		}
		if _, err := c.d.Git.ResolveRef(ctx, it.HeadSHA); err != nil {
			return res, fmt.Errorf("daemon: #%d is from a fork and %.12s is not local; run `gh pr checkout %d`", it.Number, it.HeadSHA, it.Number)
		}
		res.Ref = it.HeadSHA
	}
	if !p.Force {
		dirty, err := c.d.Git.IsDirty(ctx)
		if err != nil {
			return res, fmt.Errorf("daemon: checkout: %w", err)
		}
		if dirty {
			res.Dirty = true
			return res, nil
		}
	}
	if err := c.d.Git.Checkout(ctx, res.Ref); err != nil {
		return res, fmt.Errorf("daemon: checkout %s: %w", res.Ref, err)
	}
	res.CheckedOut = true
	return res, nil
}

// Events implements Client.
func (c *InProcess) Events() <-chan proto.Event { return c.events }

// Close implements Client: drops queued work, cancels running work, waits
// for it and closes Events.
func (c *InProcess) Close() error {
	c.once.Do(func() {
		c.q.stop()
		c.cancel()
		c.wg.Wait()
		c.evMu.Lock()
		c.closed = true
		close(c.events)
		c.evMu.Unlock()
	})
	return nil
}
