package daemon

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
)

// Resume keeps a stopped run's FromStage and Force (M5.2; deferred in
// DECISIONS "M5.1 review fixes 2"). A user stop remembers the options of
// the jobs it caught, running or waiting, in a stop record; Resume queues
// the item with them, so a stopped Pipeline-page `r` on a stage, or a
// forced `r`, still ignores the caches it asked to ignore.

// stopNS is the store cache namespace of stop records (key: item id). They
// are stored, not kept in memory, so they survive a restart like the
// item's StateStopped.
const stopNS = "daemon:stop"

// stopRecord is what Resume needs of the jobs a user stop caught.
type stopRecord struct {
	// FromStage and Force merge every caught job with the queue's rules
	// (earlier stage, Force OR).
	FromStage model.Stage `json:"fromStage,omitempty"`
	Force     bool        `json:"force,omitempty"`
	// RunFrom is the stopped run's own FromStage and RunStage the stage it
	// stopped in; both empty when the stop only dropped waiting jobs.
	RunFrom  model.Stage `json:"runFrom,omitempty"`
	RunStage model.Stage `json:"runStage,omitempty"`
}

// carriesOptions reports whether a resumed job needs the record.
func (r stopRecord) carriesOptions() bool { return r.FromStage != "" || r.Force }

// merge adds o to r; o's stopped run, when it has one, replaces r's.
func (r stopRecord) merge(o stopRecord) stopRecord {
	r.FromStage = earlierStage(r.FromStage, o.FromStage)
	r.Force = r.Force || o.Force
	if o.RunStage != "" {
		r.RunFrom, r.RunStage = o.RunFrom, o.RunStage
	}
	return r
}

// rememberDropped records the options of a job a stop dropped before it ran.
func (c *InProcess) rememberDropped(ctx context.Context, j job) {
	add := stopRecord{FromStage: j.fromStage, Force: j.force}
	if j.resume != nil {
		// A resumed job stopped again before it ran: keep its stopped run.
		add.RunFrom, add.RunStage = j.resume.RunFrom, j.resume.RunStage
	}
	c.remember(ctx, j.itemID, add)
}

// rememberStoppedRun records the options of a run the user stopped and the
// stage it stopped in.
func (c *InProcess) rememberStoppedRun(id string, opts pipeline.RunOptions, stage model.Stage) {
	c.remember(context.Background(), id, stopRecord{FromStage: opts.FromStage, Force: opts.Force, RunFrom: opts.FromStage, RunStage: stage})
}

// remember merges add into the item's stop record; a record without
// options is not stored. Errors are logged: Resume then runs from the
// caches, as before M5.2.
func (c *InProcess) remember(ctx context.Context, id string, add stopRecord) {
	c.stopMu.Lock()
	defer c.stopMu.Unlock()
	rec, _ := c.loadStop(ctx, id)
	rec = rec.merge(add)
	if !rec.carriesOptions() {
		return
	}
	b, err := json.Marshal(rec)
	if err == nil {
		err = c.d.Store.CachePut(ctx, stopNS, id, b)
	}
	if err != nil {
		c.log.Warn("remember stopped options", "item", id, "err", err)
	}
}

// loadStop returns the item's stop record (ok false when none is stored).
func (c *InProcess) loadStop(ctx context.Context, id string) (stopRecord, bool) {
	b, ok, err := c.d.Store.CacheGet(ctx, stopNS, id)
	if err != nil {
		c.log.Warn("load stop record", "item", id, "err", err)
		return stopRecord{}, false
	}
	if !ok {
		return stopRecord{}, false
	}
	var rec stopRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		c.log.Warn("decode stop record", "item", id, "err", err)
		return stopRecord{}, false
	}
	return rec, true
}

// forgetStop deletes the item's stop record: Resume consumed it, a full run
// of the item completed, or the item was removed.
func (c *InProcess) forgetStop(ctx context.Context, id string) {
	c.stopMu.Lock()
	defer c.stopMu.Unlock()
	if _, ok := c.loadStop(ctx, id); !ok {
		return
	}
	if err := c.d.Store.CacheDelete(ctx, stopNS, id); err != nil {
		c.log.Warn("forget stop record", "item", id, "err", err)
	}
}

// resumeJob is the explicit job Resume queues for it: its stop record's
// FromStage and Force, and the stopped run (job.resume) when it had one.
func (c *InProcess) resumeJob(ctx context.Context, it model.Item) job {
	j := job{itemID: it.ID, urgency: it.Urgency, user: true, explicit: true}
	rec, ok := c.loadStop(ctx, it.ID)
	if !ok {
		return j
	}
	j.fromStage, j.force = rec.FromStage, rec.Force
	if rec.RunStage != "" {
		j.resume = &rec
	}
	return j
}

// resumeOptions adapts a resumed run's options to what the stopped run
// already redid. The pipeline's FromStage ignores a stage's whole cache, so
// resuming a FromStage-review run as it was queued would review again, and
// pay again for, every group the stopped run had already re-reviewed.
// When the resumed job still asks for the stopped run's FromStage and the
// stop came at or after that stage, the stages before the stopped one are
// served from the caches the stopped run refreshed: FromStage moves to the
// stopped stage. For review, the item's routing rows say which groups the
// stopped run finished; the cached reviews of the others (left from before
// the run) are deleted and the run continues from aggregate, so those
// groups are reviewed and the finished ones hit their new cache rows. A
// job merged with an earlier request (another FromStage) runs as queued.
func (c *InProcess) resumeOptions(ctx context.Context, itemID string, rec stopRecord, opt pipeline.RunOptions) pipeline.RunOptions {
	if opt.FromStage == "" || opt.FromStage != rec.RunFrom || stageIndex(rec.RunStage) < stageIndex(opt.FromStage) {
		return opt
	}
	from := rec.RunStage
	if from == model.StageReview {
		c.dropUnreviewed(ctx, itemID)
		from = model.StageAggregate
	}
	opt.FromStage = from
	return opt
}

// dropUnreviewed deletes the cached review of every routed group whose
// routing row the stopped run did not finish (stopped, queued, failed): it
// predates the run the user asked for. Errors are logged; such a group is
// then served from its old review.
func (c *InProcess) dropUnreviewed(ctx context.Context, itemID string) {
	rows, err := c.d.Store.ItemReviews(ctx, itemID)
	if err != nil {
		c.log.Warn("resume: routing rows", "item", itemID, "err", err)
		return
	}
	for _, r := range rows {
		if r.Tier == model.TierNone || r.Tier == "" || r.Status == model.ReviewDone {
			continue
		}
		if err := c.d.Store.DeleteReview(ctx, r.GroupID, r.Tier); err != nil {
			c.log.Warn("resume: drop old review", "item", itemID, "group", r.GroupID, "err", err)
		}
	}
}

// stageIndex is s's position in model.Stages (len(model.Stages) when
// unknown, e.g. "queued").
func stageIndex(s model.Stage) int {
	if i := slices.Index(model.Stages, s); i >= 0 {
		return i
	}
	return len(model.Stages)
}
