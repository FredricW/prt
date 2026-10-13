package daemon

import "sync"

// Pool is a worker pool shared by the InProcess clients of one workspace:
// at most Slots analyses run at once over every repo together, the most
// urgent waiting job of any repo first (user jobs before automatic ones,
// then urgency, then enqueue order, as in one repo's queue). A nil *Pool
// means each client keeps its own queue slots (the single-repo behaviour).
//
// Design (M5.3): every queue registered with the pool publishes the key of
// its best startable waiting job (its head) under its own lock, then the
// pool's: the lock order is queue.mu, then Pool.mu, and the pool never calls
// into a queue while holding mu. A queue gets a slot (offer) when fewer than
// Slots jobs run and it holds the best published head. When a slot frees, or
// the pool grows, kick asks the queue holding the best head to start it,
// outside mu, while slots remain. Enqueue numbers are pool-wide, so the keys
// of different queues compare like the jobs of one queue.
type Pool struct {
	mu      sync.Mutex
	slots   int
	running int               // jobs running over every queue
	seq     int               // pool-wide enqueue order (job.seq)
	queues  map[*queue]bool   // registered queues
	heads   map[*queue]jobKey // each queue's best startable waiting job
}

// NewPool returns a pool with slots workers (≤ 0: defaultConcurrency).
func NewPool(slots int) *Pool {
	if slots <= 0 {
		slots = defaultConcurrency
	}
	return &Pool{slots: slots, queues: map[*queue]bool{}, heads: map[*queue]jobKey{}}
}

// Slots returns the pool size.
func (p *Pool) Slots() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sizeLocked()
}

// SetSlots resizes the pool (the settings page's concurrency in a
// workspace). Growing starts waiting jobs of any repo; shrinking never
// kills a running job.
func (p *Pool) SetSlots(n int) {
	if n <= 0 {
		n = 1
	}
	p.mu.Lock()
	p.slots = n
	p.mu.Unlock()
	p.kick()
}

// sizeLocked is the pool size (a zero Pool gets defaultConcurrency).
func (p *Pool) sizeLocked() int {
	if p.slots <= 0 {
		return defaultConcurrency
	}
	return p.slots
}

// register adds q to the pool (newQueueOn).
func (p *Pool) register(q *queue) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.queues == nil {
		p.queues, p.heads = map[*queue]bool{}, map[*queue]jobKey{}
	}
	p.queues[q] = true
}

// unregister removes q (its client closed): it gets no slot again. Its
// running jobs still release theirs.
func (p *Pool) unregister(q *queue) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.queues, q)
	delete(p.heads, q)
}

// nextSeq returns the next pool-wide enqueue number.
func (p *Pool) nextSeq() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	return p.seq
}

// offer publishes k as q's head and grants q a slot when one is free and k
// is the best head of every queue. A granted head is no longer published
// (q offers its next one). Called with q.mu held.
func (p *Pool) offer(q *queue, k jobKey) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.queues[q] {
		return false
	}
	p.heads[q] = k
	if p.running >= p.sizeLocked() || p.bestLocked() != q {
		return false
	}
	delete(p.heads, q)
	p.running++
	return true
}

// withdraw forgets q's head (q has no startable waiting job).
func (p *Pool) withdraw(q *queue) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.heads, q)
}

// release frees the slot of a finished job. The caller kicks the pool
// once it holds no queue lock.
func (p *Pool) release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running > 0 {
		p.running--
	}
}

// bestLocked returns the queue holding the best published head, or nil.
// Keys are unique (pool-wide enqueue numbers), so the choice is too.
func (p *Pool) bestLocked() *queue {
	var best *queue
	var bk jobKey
	for q, k := range p.heads {
		if best == nil || k.before(bk) {
			best, bk = q, k
		}
	}
	return best
}

// kick starts waiting jobs while slots are free, best head first. It holds
// no lock while a queue starts its job (startFromPool takes that queue's
// lock, then the pool's, through offer). Every pass either starts a job or
// sees a head another goroutine changed meanwhile, so it ends once the
// slots are full or nothing waits.
func (p *Pool) kick() {
	for {
		p.mu.Lock()
		var q *queue
		if p.running < p.sizeLocked() {
			q = p.bestLocked()
		}
		p.mu.Unlock()
		if q == nil {
			return
		}
		q.startFromPool()
	}
}

// startFromPool starts what the pool grants q (kick).
func (q *queue) startFromPool() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		q.pool.withdraw(q)
		return
	}
	q.startLocked()
}
