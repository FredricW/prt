package daemon

import (
	"sort"
	"sync"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
)

// job is one queued analysis.
type job struct {
	itemID  string
	urgency float64
	force   bool
	// fromStage makes that stage and every later one ignore its cache
	// (pipeline.RunOptions.FromStage); "" for none.
	fromStage model.Stage
	// rerun queues the job even while the item is running (it starts after
	// that run): used after a group edit, a review action, a cap change or
	// a routing reload, which the running run may have read too early.
	rerun bool
	// keepReviewed is set by a routing reload
	// (pipeline.RunOptions.KeepReviewed). A merged job keeps it only when
	// both jobs had it: any other job routes every group by the rules.
	keepReviewed bool
	// aggOnly re-runs aggregate alone (pipeline.RunOptions.AggregateOnly),
	// after a label override. A merged job keeps it only when both jobs had
	// it: any other job is a full run, which aggregates too.
	aggOnly bool
	// user marks a job the user asked for by hand (search-and-add enter,
	// tab preview, :add): it starts before every automatic job, whatever
	// the urgencies (spec: "starts analysis immediately"). A merged job
	// keeps it when either job had it.
	user bool
	// explicit marks a job the user asked for on this item (r, :resume,
	// :add, a group edit, a review action, a cap, an override): it runs the
	// model-backed stages whatever the auto_review strategy. Any other job is
	// gated by the strategy when it starts (InProcess.gate). Separate from
	// user, which is only queue priority (a tab preview is user but not
	// explicit). A merged job keeps it when either job had it.
	explicit bool
	// resume is the stop record of the stopped run a Resume continues
	// (M5.2): when the job starts, resumeOptions serves the stages that run
	// redid from their caches. A merged job keeps the first non-nil one.
	resume *stopRecord
	seq    int // enqueue order, breaks urgency ties
}

// options are the pipeline run options of the job.
func (j job) options() pipeline.RunOptions {
	return pipeline.RunOptions{Force: j.force, FromStage: j.fromStage, KeepReviewed: j.keepReviewed, AggregateOnly: j.aggOnly}
}

// earlierStage returns the earlier of two FromStage values ("" means
// none, so the other wins).
func earlierStage(a, b model.Stage) model.Stage {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	for _, s := range model.Stages {
		if s == a || s == b {
			return s
		}
	}
	return a
}

// queue is a priority work queue: at most slots jobs run at once, and the
// next job started is always the most urgent waiting one. An item is never
// queued twice; an item already running a full run is queued again only
// when forced or marked rerun (any job follows an aggregate-only run).
type queue struct {
	slots int
	run   func(job) // executes a job; called on its own goroutine
	wg    *sync.WaitGroup

	mu       sync.Mutex
	stopped  bool
	seq      int
	running  int
	waiting  []job
	queued   map[string]int // itemID -> index in waiting (rebuilt on sort)
	inflight map[string]job // itemID -> the job running for it

	// Stop marks (M5.1): a job of item id whose seq is ≤ stopSeq[id] (or ≤
	// stopAllSeq) was queued before the user stopped it. drop and dropAll
	// set them; stoppedBefore lets a job that had already left the waiting
	// list when the stop came (started, not yet registered as a run) see it.
	stopSeq    map[string]int
	stopAllSeq int
}

func newQueue(slots int, wg *sync.WaitGroup, run func(job)) *queue {
	if slots <= 0 {
		slots = 1
	}
	return &queue{slots: slots, run: run, wg: wg, queued: map[string]int{}, inflight: map[string]job{}, stopSeq: map[string]int{}}
}

// coveredLocked reports whether the run in flight for j's item already does
// j's work, so j is dropped: true for a plain job (not forced, not a
// rerun) behind a full run. An aggregate-only run covers nothing but
// another aggregate-only job, which is a rerun anyway. An explicit job is
// covered only by an explicit run: an automatic run may be gated to the
// free stages, so the user's request (Add, :resume) must run after it. A
// run the user stopped (still unwinding) covers nothing.
func (q *queue) coveredLocked(j job) bool {
	cur, ok := q.inflight[j.itemID]
	if !ok || cur.aggOnly || j.force || j.rerun {
		return false
	}
	if j.explicit && !cur.explicit {
		return false
	}
	return !q.stoppedLocked(cur)
}

// covers reports whether j would be dropped as covered by the run in flight.
func (q *queue) covers(j job) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.coveredLocked(j)
}

// push adds jobs atomically (so a batch is ordered by urgency even with one
// slot) and starts as many as slots allow.
func (q *queue) push(jobs ...job) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return
	}
	for _, j := range jobs {
		if i, ok := q.queued[j.itemID]; ok {
			q.waiting[i].force = q.waiting[i].force || j.force
			q.waiting[i].fromStage = earlierStage(q.waiting[i].fromStage, j.fromStage)
			q.waiting[i].urgency = j.urgency
			q.waiting[i].keepReviewed = q.waiting[i].keepReviewed && j.keepReviewed
			q.waiting[i].aggOnly = q.waiting[i].aggOnly && j.aggOnly
			q.waiting[i].user = q.waiting[i].user || j.user
			q.waiting[i].explicit = q.waiting[i].explicit || j.explicit
			if q.waiting[i].resume == nil {
				q.waiting[i].resume = j.resume
			}
			continue
		}
		if q.coveredLocked(j) {
			continue
		}
		q.seq++
		j.seq = q.seq
		q.waiting = append(q.waiting, j)
		q.queued[j.itemID] = len(q.waiting) - 1
	}
	q.sortLocked()
	q.startLocked()
}

func (q *queue) sortLocked() {
	sort.SliceStable(q.waiting, func(a, b int) bool {
		if q.waiting[a].user != q.waiting[b].user {
			return q.waiting[a].user
		}
		if q.waiting[a].urgency != q.waiting[b].urgency {
			return q.waiting[a].urgency > q.waiting[b].urgency
		}
		return q.waiting[a].seq < q.waiting[b].seq
	})
	for i, j := range q.waiting {
		q.queued[j.itemID] = i
	}
}

// startLocked starts waiting jobs while slots are free. A job whose item is
// still running stays queued until that run finishes.
func (q *queue) startLocked() {
	for i := 0; i < len(q.waiting) && q.running < q.slots; {
		j := q.waiting[i]
		if _, busy := q.inflight[j.itemID]; busy {
			i++
			continue
		}
		q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
		delete(q.queued, j.itemID)
		q.running++
		q.inflight[j.itemID] = j
		q.wg.Add(1)
		go q.exec(j)
	}
	for i, j := range q.waiting {
		q.queued[j.itemID] = i
	}
}

func (q *queue) exec(j job) {
	defer q.wg.Done()
	q.run(j)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.running--
	delete(q.inflight, j.itemID)
	if !q.stopped {
		q.startLocked()
	}
}

// drop removes id's waiting job (a user stop) and marks every job of id
// queued so far as stopped (see stoppedBefore). It returns the job that
// was waiting (nil when none was) and whether one is running.
func (q *queue) drop(id string) (dropped *job, busy bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopSeq[id] = q.seq
	_, busy = q.inflight[id]
	i, waiting := q.queued[id]
	if !waiting {
		return nil, busy
	}
	j := q.waiting[i]
	q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
	delete(q.queued, id)
	q.reindexLocked()
	return &j, busy
}

// dropAll removes every waiting job (stop all) and marks every job queued
// so far as stopped. It returns the jobs that were waiting and the items
// running (sorted).
func (q *queue) dropAll() (dropped []job, busy []string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopAllSeq = q.seq
	for id := range q.inflight {
		busy = append(busy, id)
	}
	dropped = q.waiting
	q.waiting = nil
	q.queued = map[string]int{}
	sort.Strings(busy)
	return dropped, busy
}

// stoppedBefore reports whether j was queued before a stop of its item (or
// a stop all), so it must not run.
func (q *queue) stoppedBefore(j job) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.stoppedLocked(j)
}

func (q *queue) stoppedLocked(j job) bool {
	return j.seq <= q.stopAllSeq || j.seq <= q.stopSeq[j.itemID]
}

// setSlots changes how many jobs may run at once (the settings page's
// concurrency). Growing starts waiting jobs; shrinking never kills a
// running job, it only delays the next starts.
func (q *queue) setSlots(n int) {
	if n <= 0 {
		n = 1
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.slots = n
	if !q.stopped {
		q.startLocked()
	}
}

// slotCount returns the current number of slots.
func (q *queue) slotCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.slots
}

func (q *queue) reindexLocked() {
	for i, j := range q.waiting {
		q.queued[j.itemID] = i
	}
}

// stop drops waiting jobs and refuses new ones; running jobs finish.
func (q *queue) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopped = true
	q.waiting = nil
	q.queued = map[string]int{}
}
