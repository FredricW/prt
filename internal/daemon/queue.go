package daemon

import (
	"sort"
	"sync"

	"github.com/fredricw/prt/internal/model"
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
	seq          int // enqueue order, breaks urgency ties
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
// queued twice; an item already running is queued again only when forced or
// marked rerun.
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
	inflight map[string]bool
}

func newQueue(slots int, wg *sync.WaitGroup, run func(job)) *queue {
	if slots <= 0 {
		slots = 1
	}
	return &queue{slots: slots, run: run, wg: wg, queued: map[string]int{}, inflight: map[string]bool{}}
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
			continue
		}
		if q.inflight[j.itemID] && !j.force && !j.rerun {
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
		if q.inflight[j.itemID] {
			i++
			continue
		}
		q.waiting = append(q.waiting[:i], q.waiting[i+1:]...)
		delete(q.queued, j.itemID)
		q.running++
		q.inflight[j.itemID] = true
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
