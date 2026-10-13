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
	q      *queue
	wg     sync.WaitGroup

	evMu   sync.RWMutex
	closed bool
	events chan proto.Event

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
	return c
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

// onStage turns "stage started" rows into item states.
func (c *InProcess) onStage(rl model.RunLog) {
	if rl.Status != model.StageRunning {
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
	model.SectionNeedsYou: 0, model.SectionYours: 1, model.SectionBranches: 2, model.SectionAdded: 3,
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
	if it.HeadSHA != "" {
		if f, ok, err := s.Fetch(ctx, itemID, it.HeadSHA); err == nil && ok {
			det.Diff = f.Diff
		}
	}
	if det.Files, err = s.Files(ctx, itemID); err != nil {
		return det, err
	}
	if det.Units, err = s.Units(ctx, itemID); err != nil {
		return det, err
	}
	ids := make([]string, len(det.Units))
	for i, u := range det.Units {
		ids[i] = u.ID
	}
	if det.Labels, err = s.Labels(ctx, ids); err != nil {
		return det, err
	}
	if det.Links, err = s.Links(ctx, itemID); err != nil {
		return det, err
	}
	if det.Groups, err = s.Groups(ctx, itemID); err != nil {
		return det, err
	}
	if det.Reviews, err = s.Reviews(ctx, itemID); err != nil {
		return det, err
	}
	if det.Comments, err = s.Comments(ctx, itemID); err != nil {
		return det, err
	}
	if agg, ok, err := s.Agg(ctx, itemID); err == nil && ok {
		det.Agg = &agg
	}
	det.RunLogs, err = s.RunLogs(ctx, itemID)
	return det, err
}

// Refresh implements Client: queues the item for analysis (deduplicated);
// with p.Force the fetch cache is bypassed. p.FromStage is not used yet.
func (c *InProcess) Refresh(ctx context.Context, p proto.ItemParams) error {
	return c.Analyse(ctx, p.Force, p.ItemID)
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
	c.finish(it.ID, c.p.RunWith(c.ctx, it, pipeline.RunOptions{Force: j.force}))
}

func (c *InProcess) finish(id string, err error) {
	if err != nil {
		c.log.Warn("analysis failed", "item", id, "err", err)
		c.setState(id, model.StateError, err.Error())
		return
	}
	c.setState(id, model.StateReady, "")
}

// RunSync runs the pipeline for an item and waits (tests, --snapshot).
func (c *InProcess) RunSync(ctx context.Context, itemID string) error {
	it, err := c.d.Store.Item(ctx, itemID)
	if err != nil {
		return fmt.Errorf("daemon: run %s: %w", itemID, err)
	}
	err = c.p.Run(ctx, it)
	c.finish(itemID, err)
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
	c.setUrgency(&it)
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
	if err := c.d.Git.Checkout(ctx, it.Ref); err != nil {
		return res, fmt.Errorf("daemon: checkout %s: %w", it.Ref, err)
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
