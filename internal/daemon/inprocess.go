package daemon

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
)

// InProcess implements Client by running the pipeline in this process.
type InProcess struct {
	d      pipeline.Deps
	p      *pipeline.Pipeline
	events chan proto.Event
	wg     sync.WaitGroup
	cancel context.CancelFunc
	ctx    context.Context
	once   sync.Once
}

var _ Client = (*InProcess)(nil)

// NewInProcess builds an in-process client. d.Emit is replaced so RunLog rows
// become events.
func NewInProcess(d pipeline.Deps) *InProcess {
	ctx, cancel := context.WithCancel(context.Background())
	c := &InProcess{events: make(chan proto.Event, 256), ctx: ctx, cancel: cancel}
	d.Emit = func(rl model.RunLog) {
		c.send(proto.Event{Type: proto.EventStage, ItemID: rl.ItemID, Stage: rl.Stage, RunLog: &rl})
	}
	c.d = d
	c.p = pipeline.New(d)
	return c
}

// send never blocks: the UI re-reads Detail on the next event anyway.
func (c *InProcess) send(e proto.Event) {
	select {
	case c.events <- e:
	default:
	}
}

// Discover implements Client: local branches with commits not on the default
// branch become branch items. TODO(M0): GitHub sources (involves:@me, owned paths).
func (c *InProcess) Discover(ctx context.Context) error {
	def, err := c.d.Git.DefaultBranch(ctx)
	if err != nil {
		return err
	}
	refs, err := c.d.Git.Refs(ctx, false)
	if err != nil {
		return err
	}
	for _, r := range refs {
		if r.Name == def {
			continue
		}
		ahead, err := c.d.Git.CountAhead(ctx, def, r.Name)
		if err != nil || ahead == 0 {
			continue
		}
		id := model.BranchItemID(r.Name)
		it, err := c.d.Store.Item(ctx, id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if errors.Is(err, store.ErrNotFound) {
			it = model.Item{ID: id, Kind: model.KindBranch, Source: model.SourceRanked, Section: model.SectionBranches}
		}
		it.Ref, it.BaseRef, it.Title, it.Author = r.Name, def, r.Subject, r.Author
		if it.HeadSHA != "" && it.HeadSHA != r.SHA {
			it.State = model.StateStale
		}
		it.HeadSHA, it.Ahead, it.UpdatedAt = r.SHA, ahead, r.CommitDate
		if err := c.d.Store.UpsertItem(ctx, it); err != nil {
			return err
		}
		c.send(proto.Event{Type: proto.EventItem, ItemID: id, Item: &it})
	}
	return nil
}

// Items implements Client.
func (c *InProcess) Items(ctx context.Context) ([]model.Item, error) { return c.d.Store.Items(ctx) }

// Detail implements Client.
func (c *InProcess) Detail(ctx context.Context, itemID string) (proto.Detail, error) {
	s := c.d.Store
	it, err := s.Item(ctx, itemID)
	if err != nil {
		return proto.Detail{}, err
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

// Refresh implements Client; the pipeline runs in a goroutine.
func (c *InProcess) Refresh(ctx context.Context, p proto.ItemParams) error {
	it, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return err
	}
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.setState(it, model.StateAnalysing, "")
		if err := c.p.Run(c.ctx, it); err != nil {
			c.setState(it, model.StateError, err.Error())
			return
		}
		c.setState(it, model.StateReady, "")
	}()
	return nil
}

// RunSync runs the pipeline for an item and waits (tests, --snapshot).
func (c *InProcess) RunSync(ctx context.Context, itemID string) error {
	it, err := c.d.Store.Item(ctx, itemID)
	if err != nil {
		return err
	}
	if err := c.p.Run(ctx, it); err != nil {
		c.setState(it, model.StateError, err.Error())
		return err
	}
	c.setState(it, model.StateReady, "")
	return nil
}

func (c *InProcess) setState(it model.Item, st model.ItemState, note string) {
	ctx := context.Background()
	if cur, err := c.d.Store.Item(ctx, it.ID); err == nil {
		it = cur
	}
	it.State, it.StateNote = st, note
	_ = c.d.Store.UpsertItem(ctx, it)
	c.send(proto.Event{Type: proto.EventItem, ItemID: it.ID, Item: &it})
}

// Add implements Client. Supports "#N"/"N" (needs GitHub) and local refs.
func (c *InProcess) Add(ctx context.Context, query string) (model.Item, error) {
	q := strings.TrimSpace(query)
	if n, err := strconv.Atoi(strings.TrimPrefix(q, "#")); err == nil {
		if c.d.GH == nil {
			return model.Item{}, fmt.Errorf("daemon: cannot add #%d: GitHub unavailable", n)
		}
		pr, err := c.d.GH.PR(ctx, n)
		if err != nil {
			return model.Item{}, err
		}
		it := model.Item{
			ID: model.PRItemID(n), Kind: model.KindPR, Number: n, Ref: pr.HeadRef, BaseRef: pr.BaseRef,
			HeadSHA: pr.HeadSHA, Title: pr.Title, Body: pr.Body, Author: pr.Author, URL: pr.URL,
			Source: model.SourceAdded, Section: model.SectionAdded, CI: pr.CI,
		}
		return it, c.d.Store.UpsertItem(ctx, it)
	}
	sha, err := c.d.Git.ResolveRef(ctx, q)
	if err != nil {
		return model.Item{}, err
	}
	it := model.Item{ID: model.BranchItemID(q), Kind: model.KindBranch, Ref: q, HeadSHA: sha, Title: q,
		Source: model.SourceAdded, Section: model.SectionAdded}
	return it, c.d.Store.UpsertItem(ctx, it)
}

// Remove implements Client.
func (c *InProcess) Remove(ctx context.Context, itemID string) error {
	if err := c.d.Store.RemoveItem(ctx, itemID); err != nil {
		return err
	}
	c.send(proto.Event{Type: proto.EventRemove, ItemID: itemID})
	return nil
}

// Events implements Client.
func (c *InProcess) Events() <-chan proto.Event { return c.events }

// Close implements Client: cancels running work and waits for it.
func (c *InProcess) Close() error {
	c.once.Do(func() {
		c.cancel()
		c.wg.Wait()
		close(c.events)
	})
	return nil
}
