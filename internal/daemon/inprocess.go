package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
)

// defaultConcurrency is used when Config.Pipeline.Concurrency is not positive.
const defaultConcurrency = 4

// InProcess implements Client by running discovery and the pipeline in this
// process. Analyses run on a worker pool limited by
// Config.Pipeline.Concurrency, most urgent first.
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

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
}

var _ Client = (*InProcess)(nil)

// NewInProcess builds an in-process client. d.Emit is wrapped so RunLog rows
// become events and drive item states (a caller's Emit still runs), and
// d.ItemLock is set to the client's item lock.
func NewInProcess(d pipeline.Deps) *InProcess {
	ctx, cancel := context.WithCancel(context.Background())
	c := &InProcess{events: make(chan proto.Event, 256), ctx: ctx, cancel: cancel, itemMu: &sync.Mutex{}}
	if d.Log == nil {
		d.Log = log.New(io.Discard)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	userEmit := d.Emit
	d.Emit = func(rl model.RunLog) {
		c.send(proto.Event{Type: proto.EventStage, ItemID: rl.ItemID, Stage: rl.Stage, RunLog: &rl})
		c.onStage(rl)
		if userEmit != nil {
			userEmit(rl)
		}
	}
	d.ItemLock = c.itemMu
	c.d, c.log, c.now = d, d.Log, d.Now
	c.p = pipeline.New(d)
	slots := d.Config.Pipeline.Concurrency
	if slots <= 0 {
		slots = defaultConcurrency
	}
	c.q = newQueue(slots, &c.wg, c.analyse)
	c.recoverStates(ctx)
	return c
}

// SetGitHubNote records why GitHub is disabled (no token, origin not on
// GitHub, --offline). With no GitHub client, each discovery reports it as an
// EventLog note so the status bar explains the missing PRs.
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
	default:
		return
	}
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
// recently updated, with UrgencyQuartile relative to all items.
func (c *InProcess) Items(ctx context.Context) ([]model.Item, error) {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		return nil, fmt.Errorf("daemon: items: %w", err)
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

// Detail implements Client.
func (c *InProcess) Detail(ctx context.Context, itemID string) (proto.Detail, error) {
	s := c.d.Store
	it, err := s.Item(ctx, itemID)
	if err != nil {
		return proto.Detail{}, fmt.Errorf("daemon: detail %s: %w", itemID, err)
	}
	det := proto.Detail{Item: it}
	// The diff must match the stored files and units, which describe the
	// analysed head; a Stale item's new head has no analysis (or fetch) yet.
	for _, sha := range []string{it.AnalysedSHA, it.HeadSHA} {
		if sha == "" {
			continue
		}
		if f, ok, err := s.Fetch(ctx, itemID, sha); err == nil && ok {
			det.Diff = f.Diff
			break
		}
	}
	if det.Files, err = s.Files(ctx, itemID); err != nil {
		return det, err
	}
	if det.Units, err = s.Units(ctx, itemID); err != nil {
		return det, err
	}
	if det.Labels, err = s.Labels(ctx, labelSubjects(itemID, det.Files, det.Units)); err != nil {
		return det, err
	}
	if det.Links, err = s.Links(ctx, itemID); err != nil {
		return det, err
	}
	if det.Groups, err = s.Groups(ctx, itemID); err != nil {
		return det, err
	}
	if det.Reviews, err = s.ItemReviews(ctx, itemID); err != nil {
		return det, err
	}
	if det.Comments, err = s.Comments(ctx, itemID); err != nil {
		return det, err
	}
	if agg, ok, err := s.Agg(ctx, itemID); err == nil && ok {
		det.Agg = &agg
	}
	info := c.routingInfo()
	det.Routing = &info
	det.RunLogs, err = s.RunLogs(ctx, itemID)
	return det, err
}

// labelSubjects lists every Label.UnitID an item's labels can carry: its
// units, its files (model.FileSubject) and the item itself
// (model.ItemSubject).
func labelSubjects(itemID string, files []model.File, units []model.ChangeUnit) []string {
	ids := make([]string, 0, len(units)+len(files)+1)
	for _, u := range units {
		ids = append(ids, u.ID)
	}
	for _, f := range files {
		ids = append(ids, model.FileSubject(itemID, f.Path))
	}
	return append(ids, model.ItemSubject(itemID))
}

// Refresh implements Client: queues the item for analysis (deduplicated).
// With p.Force (key `r`) the head is re-resolved first (local ref for
// branches, GitHub for PRs) and the fetch cache is bypassed, so new commits
// are analysed without waiting for the next discovery. p.FromStage (the
// Pipeline page's `r` on a stage) makes that stage and the later ones
// ignore their caches; such a job follows a run in flight.
func (c *InProcess) Refresh(ctx context.Context, p proto.ItemParams) error {
	if p.FromStage != "" && !pipeline.ValidStage(p.FromStage) {
		return fmt.Errorf("daemon: refresh %s: unknown stage %q", p.ItemID, p.FromStage)
	}
	if p.Force {
		if err := c.refreshHead(ctx, p.ItemID); err != nil {
			return err
		}
	}
	if p.FromStage == "" {
		return c.Analyse(ctx, p.Force, p.ItemID)
	}
	it, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return fmt.Errorf("daemon: refresh %s: %w", p.ItemID, err)
	}
	c.q.push(job{itemID: it.ID, urgency: it.Urgency, force: p.Force, fromStage: p.FromStage, rerun: true})
	return nil
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
			if it.Source != model.SourceAdded {
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
	// one was stored, so another run must follow it.
	c.q.push(job{itemID: it.ID, urgency: it.Urgency, rerun: true})
	return nil
}

// Analyse queues items for background analysis as one batch, so they start
// in urgency order. Unknown ids are an error; nothing is queued then.
func (c *InProcess) Analyse(ctx context.Context, force bool, ids ...string) error {
	jobs := make([]job, 0, len(ids))
	for _, id := range ids {
		it, err := c.d.Store.Item(ctx, id)
		if err != nil {
			return fmt.Errorf("daemon: refresh %s: %w", id, err)
		}
		jobs = append(jobs, job{itemID: it.ID, urgency: it.Urgency, force: force})
	}
	c.q.push(jobs...)
	return nil
}

// analyse runs one queued job on a worker.
func (c *InProcess) analyse(j job) {
	it, err := c.d.Store.Item(c.ctx, j.itemID)
	if err != nil {
		c.log.Warn("analyse: item gone", "item", j.itemID, "err", err)
		return
	}
	c.setState(it.ID, model.StateFetching, "")
	head, err := c.p.RunHead(c.ctx, it, pipeline.RunOptions{Force: j.force, FromStage: j.fromStage})
	c.finish(c.ctx, it.ID, head, err)
}

// finish records the outcome of a run that analysed head. If discovery
// moved the item's head while the run was in flight, the result describes
// an old commit: the item becomes Stale instead of Ready. A run that failed
// because runCtx was cancelled (quit, daemon stop) was interrupted, not
// broken: the item goes back to "not analysed" so it is queued again.
func (c *InProcess) finish(runCtx context.Context, id, head string, err error) {
	if err != nil && runCtx.Err() != nil {
		c.log.Info("analysis interrupted", "item", id, "err", err)
		c.resetState(id)
		return
	}
	if err != nil {
		c.log.Warn("analysis failed", "item", id, "err", err)
		c.setState(id, model.StateError, err.Error())
		return
	}
	if _, _, err := c.updateItem(context.Background(), id, func(it *model.Item) {
		if head != "" {
			it.AnalysedSHA = head
		}
		if head != "" && it.HeadSHA != "" && it.HeadSHA != head {
			it.State, it.StateNote = model.StateStale, staleNote
			return
		}
		it.State, it.StateNote = model.StateReady, ""
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
// instead of spinning forever.
func (c *InProcess) recoverStates(ctx context.Context) {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		c.log.Warn("recover item states", "err", err)
		return
	}
	for _, it := range items {
		if it.State == model.StateFetching || it.State == model.StateAnalysing {
			c.resetState(it.ID)
		}
	}
}

// AnalysePending queues every never-analysed item (State "") in urgency
// order, so a daemon with no TUI attached fills the cache.
func (c *InProcess) AnalysePending(ctx context.Context) error {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		return fmt.Errorf("daemon: items: %w", err)
	}
	var ids []string
	for _, it := range items {
		if it.State == "" {
			ids = append(ids, it.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return c.Analyse(ctx, false, ids...)
}

// RunSync runs the pipeline for an item and waits (tests, --snapshot).
func (c *InProcess) RunSync(ctx context.Context, itemID string) error {
	it, err := c.d.Store.Item(ctx, itemID)
	if err != nil {
		return fmt.Errorf("daemon: run %s: %w", itemID, err)
	}
	head, err := c.p.RunHead(ctx, it, pipeline.RunOptions{})
	c.finish(ctx, itemID, head, err)
	return err
}

// Add implements Client. It accepts "#N"/"N" (needs GitHub), a local ref, or
// a free-text query matching an open PR's title (first hit). The item lands
// in the Added section and analysis starts immediately. An item already in
// the picker keeps its section and is just (re-)queued.
func (c *InProcess) Add(ctx context.Context, query string) (model.Item, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return model.Item{}, errors.New("daemon: add: empty query")
	}
	it, err := c.resolveAdd(ctx, q)
	if err != nil {
		return model.Item{}, err
	}
	if err := c.Analyse(ctx, false, it.ID); err != nil {
		return it, err
	}
	return it, nil
}

func (c *InProcess) resolveAdd(ctx context.Context, q string) (model.Item, error) {
	if n, err := strconv.Atoi(strings.TrimPrefix(q, "#")); err == nil {
		if c.d.GH == nil {
			return model.Item{}, fmt.Errorf("daemon: cannot add #%d: GitHub unavailable", n)
		}
		pr, err := c.d.GH.PR(ctx, n)
		if err != nil {
			return model.Item{}, fmt.Errorf("daemon: add #%d: %w", n, err)
		}
		return c.addPR(ctx, pr)
	}
	sha, refErr := c.d.Git.ResolveRef(ctx, q)
	if refErr == nil {
		return c.addBranch(ctx, q, sha)
	}
	if c.d.GH == nil {
		return model.Item{}, fmt.Errorf("daemon: add %q: %w", q, refErr)
	}
	prs, err := c.d.GH.SearchPRs(ctx, "is:pr is:open "+q, 1)
	if err != nil {
		return model.Item{}, fmt.Errorf("daemon: add %q: %w", q, errors.Join(refErr, err))
	}
	if len(prs) == 0 {
		return model.Item{}, fmt.Errorf("daemon: add %q: no matching ref or open PR", q)
	}
	return c.addPR(ctx, prs[0])
}

func (c *InProcess) addPR(ctx context.Context, pr gh.PR) (model.Item, error) {
	head := c.prHead(ctx, pr)
	return c.addItem(ctx, model.PRItemID(pr.Number), func(it *model.Item) {
		applyPR(it, pr, head)
	})
}

func (c *InProcess) addBranch(ctx context.Context, ref, sha string) (model.Item, error) {
	def, err := c.d.Git.DefaultBranch(ctx)
	if err != nil {
		return model.Item{}, fmt.Errorf("daemon: default branch: %w", err)
	}
	return c.addItem(ctx, model.BranchItemID(ref), func(it *model.Item) {
		it.Kind, it.Ref, it.BaseRef, it.HeadSHA = model.KindBranch, ref, def, sha
		if it.Title == "" {
			it.Title = ref
		}
	})
}

// addItem returns the stored item if it exists, else creates an Added item
// filled by fill.
func (c *InProcess) addItem(ctx context.Context, id string, fill func(*model.Item)) (model.Item, error) {
	c.itemMu.Lock()
	it, err := c.d.Store.Item(ctx, id)
	if err == nil {
		c.itemMu.Unlock()
		return it, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		c.itemMu.Unlock()
		return it, fmt.Errorf("daemon: add %s: %w", id, err)
	}
	it = model.Item{ID: id, Source: model.SourceAdded, Section: model.SectionAdded}
	fill(&it)
	c.setUrgency(ctx, &it, false)
	err = c.d.Store.UpsertItem(ctx, it)
	c.itemMu.Unlock()
	if err != nil {
		return it, fmt.Errorf("daemon: add %s: %w", id, err)
	}
	c.send(proto.Event{Type: proto.EventItem, ItemID: it.ID, Item: &it})
	return it, nil
}

// Remove implements Client.
func (c *InProcess) Remove(ctx context.Context, itemID string) error {
	c.itemMu.Lock()
	err := c.d.Store.RemoveItem(ctx, itemID)
	c.itemMu.Unlock()
	if err != nil {
		return fmt.Errorf("daemon: remove %s: %w", itemID, err)
	}
	c.send(proto.Event{Type: proto.EventRemove, ItemID: itemID})
	return nil
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
