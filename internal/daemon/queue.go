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
	seq     int // enqueue order, breaks urgency ties
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
}

func newQueue(slots int, wg *sync.WaitGroup, run func(job)) *queue {
	if slots <= 0 {
		slots = 1
	}
	return &queue{slots: slots, run: run, wg: wg, queued: map[string]int{}, inflight: map[string]job{}}
}

// coveredLocked reports whether the run in flight for j's item already does
// j's work, so j is dropped: true for a plain job (not forced, not a
// rerun) behind a full run. An aggregate-only run covers nothing but
// another aggregate-only job, which is a rerun anyway.
func (q *queue) coveredLocked(j job) bool {
	cur, ok := q.inflight[j.itemID]
	return ok && !cur.aggOnly && !j.force && !j.rerun
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

// stop drops waiting jobs and refuses new ones; running jobs finish.
func (q *queue) stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.stopped = true
	q.waiting = nil
	q.queued = map[string]int{}
}
