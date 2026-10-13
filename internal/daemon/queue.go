package daemon

import (
	"cmp"
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
	// seq is the enqueue order, which breaks urgency ties: per queue, or
	// pool-wide for a pooled queue (so the order is comparable across the
	// repos of a workspace).
	seq int
}

// jobKey is what orders waiting jobs: user jobs first, then higher urgency,
// then earlier enqueue. A pooled queue publishes its best startable job's
// key to the pool, which compares the keys of every repo's queue.
type jobKey struct {
	user    bool
	urgency float64
	seq     int
}

// key returns j's ordering key.
func (j job) key() jobKey { return jobKey{user: j.user, urgency: j.urgency, seq: j.seq} }

// before reports whether a job keyed a starts before one keyed b. It is a
// strict total order (cmp.Compare places a NaN urgency last), so the pool's
// choice among queues never depends on map order.
func (a jobKey) before(b jobKey) bool {
	if a.user != b.user {
		return a.user
	}
	if c := cmp.Compare(a.urgency, b.urgency); c != 0 {
		return c > 0
	}
	return a.seq < b.seq
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
//
// A pooled queue (pool set, M5.3) has no slots of its own: a job starts
// when the shared Pool grants it a slot, which it does for the best waiting
// job of every queue registered with it (see Pool). Every other rule is the
// same. Lock order: queue.mu, then Pool.mu.
type queue struct {
	slots int
	pool  *Pool     // shared worker pool; nil: private slots (fixed at construction)
	run   func(job) // executes a job; called on its own goroutine
	wg    *sync.WaitGroup

	mu      sync.Mutex
	stopped bool
	// seq is the last enqueue number given to a job of this queue (from the
	// pool's counter when pooled), so stop marks taken from it cover every
	// job queued here so far.
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

// newQueue returns a queue with slots private slots (≤ 0: one).
func newQueue(slots int, wg *sync.WaitGroup, run func(job)) *queue {
	return newQueueOn(nil, slots, wg, run)
}

// newQueueOn returns a queue on pool (registered with it), or with slots
// private slots (≤ 0: one) when pool is nil.
func newQueueOn(pool *Pool, slots int, wg *sync.WaitGroup, run func(job)) *queue {
	if slots <= 0 {
		slots = 1
	}
	q := &queue{slots: slots, pool: pool, run: run, wg: wg, queued: map[string]int{}, inflight: map[string]job{}, stopSeq: map[string]int{}}
	if pool != nil {
		pool.register(q)
	}
	return q
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
		q.seq = q.nextSeqLocked()
		j.seq = q.seq
		q.waiting = append(q.waiting, j)
		q.queued[j.itemID] = len(q.waiting) - 1
	}
	q.sortLocked()
	q.startLocked()
}

// nextSeqLocked returns the enqueue number of a new job: the queue's own
// counter, or the pool's (comparable across queues) for a pooled queue.
func (q *queue) nextSeqLocked() int {
	if q.pool != nil {
		return q.pool.nextSeq()
	}
	return q.seq + 1
}

func (q *queue) sortLocked() {
	sort.SliceStable(q.waiting, func(a, b int) bool {
		return q.waiting[a].key().before(q.waiting[b].key())
	})
	for i, j := range q.waiting {
		q.queued[j.itemID] = i
	}
}

// startLocked starts waiting jobs while slots are free. A job whose item is
// still running stays queued until that run finishes. A pooled queue offers
// its best startable job to the pool, which leaves it published as this
// queue's head when it grants no slot (and the head is withdrawn when the
// queue has no startable job).
func (q *queue) startLocked() {
	offered := false
	for i := 0; i < len(q.waiting); {
		j := q.waiting[i]
		if _, busy := q.inflight[j.itemID]; busy {
			i++
			continue
		}
		if !q.acquireLocked(j) {
			offered = true
			break
		}
		q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
		delete(q.queued, j.itemID)
		q.running++
		q.inflight[j.itemID] = j
		q.wg.Add(1)
		go q.exec(j)
	}
	if !offered && q.pool != nil {
		q.pool.withdraw(q)
	}
	q.reindexLocked()
}

// acquireLocked takes a slot for j: a private one while any is free, else
// the pool's when j is the best waiting job of every pooled queue.
func (q *queue) acquireLocked(j job) bool {
	if q.pool != nil {
		return q.pool.offer(q, j.key())
	}
	return q.running < q.slots
}

func (q *queue) exec(j job) {
	defer q.wg.Done()
	q.run(j)
	q.end(j)
	if q.pool != nil {
		// Outside q.mu: the freed slot may go to another repo's queue.
		q.pool.kick()
	}
}

// end records the end of j's run and starts what the freed slot allows.
func (q *queue) end(j job) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.running--
	delete(q.inflight, j.itemID)
	if q.pool != nil {
		q.pool.release()
	}
	if !q.stopped {
		q.startLocked()
	}
}

// headChangedLocked re-offers a pooled queue's best startable job after
// waiting jobs were removed, so the pool never holds a stale head.
func (q *queue) headChangedLocked() {
	if q.pool != nil && !q.stopped {
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
	q.headChangedLocked()
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
	if q.pool != nil {
		q.pool.withdraw(q)
	}
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
// running job, it only delays the next starts. A pooled queue resizes the
// shared pool, so every repo of the workspace gets the new size.
func (q *queue) setSlots(n int) {
	if q.pool != nil {
		q.pool.SetSlots(n) // not under q.mu: growing starts jobs of any queue
		return
	}
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

// slotCount returns the current number of slots (the pool size for a
// pooled queue).
func (q *queue) slotCount() int {
	if q.pool != nil {
		return q.pool.Slots()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.slots
}

func (q *queue) reindexLocked() {
	for i, j := range q.waiting {
		q.queued[j.itemID] = i
	}
}

// stop drops waiting jobs and refuses new ones; running jobs finish (a
// pooled queue's then release their slots to the other queues). A pooled
// queue leaves its pool.
func (q *queue) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopped = true
	q.waiting = nil
	q.queued = map[string]int{}
	if q.pool != nil {
		q.pool.unregister(q)
	}
}
