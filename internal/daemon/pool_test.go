package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
)

// newPooledClient builds an InProcess over fakes with NewInProcessWith and
// the items in its own store (strategy "all", like newClient). Its git
// knows the branch feat/u (for Add).
func newPooledClient(t *testing.T, o Options, mutate func(*pipeline.Deps), items ...model.Item) *InProcess {
	t.Helper()
	st := newTestStore(t)
	put(t, st, items...)
	d := pipeline.Deps{Git: branchGit("feat/u"), Store: st, Config: config.Defaults(), Now: func() time.Time { return testNow }}
	d.Config.Pipeline.AutoReview = string(model.AutoAll)
	if mutate != nil {
		mutate(&d)
	}
	c := NewInProcessWith(d, o)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// poolRuns replaces the pipeline run of the clients sharing a pool. Each
// run is named "<client>/<ref>"; it records its start, counts the runs in
// flight (peak) and waits until released, or until its context ends. With
// auto set, runs return after a short pause instead.
type poolRuns struct {
	auto    bool
	started chan string

	cur, peak atomic.Int64

	mu    sync.Mutex
	gates map[string]chan struct{}
	order []string
}

func newPoolRuns(auto bool) *poolRuns {
	return &poolRuns{auto: auto, started: make(chan string, 64), gates: map[string]chan struct{}{}}
}

// attach makes r run c's jobs under the client name key.
func (r *poolRuns) attach(c *InProcess, key string) {
	c.run = func(ctx context.Context, it model.Item, _ pipeline.RunOptions) (string, error) {
		name := key + "/" + it.Ref
		n := r.cur.Add(1)
		for p := r.peak.Load(); n > p && !r.peak.CompareAndSwap(p, n); p = r.peak.Load() {
		}
		r.mu.Lock()
		r.order = append(r.order, name)
		gate := r.gateLocked(name)
		r.mu.Unlock()
		r.started <- name
		if r.auto {
			time.Sleep(2 * time.Millisecond)
		} else {
			select {
			case <-gate:
			case <-ctx.Done():
			}
		}
		r.cur.Add(-1)
		if err := context.Cause(ctx); err != nil {
			return "", fmt.Errorf("run: %w", err)
		}
		return it.HeadSHA, nil
	}
}

func (r *poolRuns) gateLocked(name string) chan struct{} {
	g, ok := r.gates[name]
	if !ok {
		g = make(chan struct{})
		r.gates[name] = g
	}
	return g
}

// release lets name's run return.
func (r *poolRuns) release(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	close(r.gateLocked(name))
}

// next waits for the next run to start and returns its name.
func (r *poolRuns) next(t *testing.T) string {
	t.Helper()
	select {
	case name := <-r.started:
		return name
	case <-time.After(5 * time.Second):
		t.Fatal("no run started")
		return ""
	}
}

// none fails when a run starts within a short wait.
func (r *poolRuns) none(t *testing.T) {
	t.Helper()
	select {
	case name := <-r.started:
		t.Fatalf("%s started; want no run", name)
	case <-time.After(50 * time.Millisecond):
	}
}

func (r *poolRuns) ran() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.order)
}

// poolPair is two clients ("A" and "B") on one pool of one slot, whose
// runs wait for release. A runs "block" first, so every later job waits.
type poolPair struct {
	pool *Pool
	a, b *InProcess
	runs *poolRuns
}

func newPoolPair(t *testing.T, itemsA, itemsB []model.Item) *poolPair {
	t.Helper()
	ctx := context.Background()
	p := &poolPair{pool: NewPool(1), runs: newPoolRuns(false)}
	led := NewLedger()
	p.a = newPooledClient(t, Options{Pool: p.pool, Ledger: led, Repo: proto.RepoInfo{Key: "a"}}, nil,
		append([]model.Item{branchItem("block", 0)}, itemsA...)...)
	p.b = newPooledClient(t, Options{Pool: p.pool, Ledger: led, Repo: proto.RepoInfo{Key: "b"}}, nil, itemsB...)
	p.runs.attach(p.a, "A")
	p.runs.attach(p.b, "B")
	if err := p.a.queueAuto(ctx, model.BranchItemID("block")); err != nil {
		t.Fatal(err)
	}
	if got := p.runs.next(t); got != "A/block" {
		t.Fatalf("first run %q, want A/block", got)
	}
	return p
}

// startOrder releases the running job and each next one, n times, and
// returns the runs started meanwhile in order.
func (p *poolPair) startOrder(t *testing.T, running string, n int) []string {
	t.Helper()
	var order []string
	for range n {
		p.runs.release(running)
		running = p.runs.next(t)
		order = append(order, running)
	}
	p.runs.release(running)
	return order
}

// itemIDs returns the items' ids.
func itemIDs(items ...model.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

// With one slot for two clients, the next job is the best waiting one of
// either client: user jobs first, then urgency, then enqueue order (which
// is pool-wide).
func TestPoolStartsBestJobOfAnyClient(t *testing.T) {
	ctx := context.Background()
	a1, a2 := branchItem("a1", 2), branchItem("a2", 7)
	b1, b2 := branchItem("b1", 5), branchItem("b2", 1)
	same1, same2 := branchItem("s1", 3), branchItem("s2", 3)
	preview, hot := branchItem("preview", 1), branchItem("hot", 9)
	cases := []struct {
		name   string
		itemsA []model.Item
		itemsB []model.Item
		queue  func(p *poolPair) error
		want   []string
	}{
		{
			name:   "more urgent job of the other client first",
			itemsA: []model.Item{a1}, itemsB: []model.Item{b1, b2},
			queue: func(p *poolPair) error {
				if err := p.a.queueAuto(ctx, a1.ID); err != nil {
					return err
				}
				return p.b.queueAuto(ctx, itemIDs(b1, b2)...)
			},
			want: []string{"B/b1", "A/a1", "B/b2"},
		},
		{
			name:   "more urgent job of the same client first",
			itemsA: []model.Item{a2}, itemsB: []model.Item{b1},
			queue: func(p *poolPair) error {
				if err := p.b.queueAuto(ctx, b1.ID); err != nil {
					return err
				}
				return p.a.queueAuto(ctx, a2.ID)
			},
			want: []string{"A/a2", "B/b1"},
		},
		{
			name:   "equal urgency: enqueue order across clients (B first)",
			itemsA: []model.Item{same1}, itemsB: []model.Item{same2},
			queue: func(p *poolPair) error {
				if err := p.b.queueAuto(ctx, same2.ID); err != nil {
					return err
				}
				return p.a.queueAuto(ctx, same1.ID)
			},
			want: []string{"B/s2", "A/s1"},
		},
		{
			name:   "equal urgency: enqueue order across clients (A first)",
			itemsA: []model.Item{same1}, itemsB: []model.Item{same2},
			queue: func(p *poolPair) error {
				if err := p.a.queueAuto(ctx, same1.ID); err != nil {
					return err
				}
				return p.b.queueAuto(ctx, same2.ID)
			},
			want: []string{"A/s1", "B/s2"},
		},
		{
			name:   "a user job beats a more urgent automatic job of the other client",
			itemsA: []model.Item{preview}, itemsB: []model.Item{hot},
			queue: func(p *poolPair) error {
				if err := p.b.queueAuto(ctx, hot.ID); err != nil {
					return err
				}
				p.a.analyseNow(preview) // a tab preview: user priority, automatic
				return nil
			},
			want: []string{"A/preview", "B/hot"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPoolPair(t, tc.itemsA, tc.itemsB)
			if err := tc.queue(p); err != nil {
				t.Fatal(err)
			}
			p.runs.none(t) // the one slot is A/block's
			if got := p.startOrder(t, "A/block", len(tc.want)); !slices.Equal(got, tc.want) {
				t.Errorf("start order %v, want %v", got, tc.want)
			}
			if n := p.runs.peak.Load(); n != 1 {
				t.Errorf("peak concurrent runs %d, want 1", n)
			}
		})
	}
}

// Add (search-and-add, :add) is a user job: it starts before a more urgent
// automatic job queued earlier by another client.
func TestPoolAddBeatsOtherClientsAutomaticJob(t *testing.T) {
	ctx := context.Background()
	hot := branchItem("hot", 9)
	p := newPoolPair(t, nil, []model.Item{hot})
	if err := p.b.queueAuto(ctx, hot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.a.Add(ctx, "feat/u"); err != nil {
		t.Fatal(err)
	}
	if got, want := p.startOrder(t, "A/block", 2), []string{"A/feat/u", "B/hot"}; !slices.Equal(got, want) {
		t.Errorf("start order %v, want %v", got, want)
	}
}

// Every job of two clients runs, never more at once than the pool has
// slots (counted with an atomic across both clients; run with -race).
func TestPoolLimitsConcurrencyAcrossClients(t *testing.T) {
	for _, slots := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d slots", slots), func(t *testing.T) {
			ctx := context.Background()
			pool, runs := NewPool(slots), newPoolRuns(true)
			var clients []*InProcess
			var want []string
			for _, key := range []string{"A", "B"} {
				var items []model.Item
				for i := range 4 {
					it := branchItem(fmt.Sprintf("%s%d", strings.ToLower(key), i), float64(i))
					items = append(items, it)
					want = append(want, key+"/"+it.Ref)
				}
				c := newPooledClient(t, Options{Pool: pool, Repo: proto.RepoInfo{Key: key}}, nil, items...)
				runs.attach(c, key)
				clients = append(clients, c)
				if err := c.queueAuto(ctx, itemIDs(items...)...); err != nil {
					t.Fatal(err)
				}
			}
			for range want {
				runs.next(t)
			}
			for _, c := range clients {
				c.wg.Wait()
			}
			got := runs.ran()
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("ran %v, want %v", got, want)
			}
			if n := runs.peak.Load(); n < 1 || n > int64(slots) {
				t.Errorf("peak concurrent runs %d, want 1..%d", n, slots)
			}
		})
	}
}

// Growing the pool starts a waiting job of another client at once;
// shrinking it never stops a running one.
func TestPoolSetSlotsStartsWaitingJobs(t *testing.T) {
	ctx := context.Background()
	b1 := branchItem("b1", 1)
	p := newPoolPair(t, nil, []model.Item{b1})
	if err := p.b.queueAuto(ctx, b1.ID); err != nil {
		t.Fatal(err)
	}
	p.runs.none(t)
	p.pool.SetSlots(2)
	if got := p.runs.next(t); got != "B/b1" {
		t.Fatalf("after growing: %q started, want B/b1", got)
	}
	p.pool.SetSlots(1)
	if n := p.runs.cur.Load(); n != 2 {
		t.Errorf("%d runs after shrinking, want 2 (none stopped)", n)
	}
	if p.pool.Slots() != 1 || p.a.q.slotCount() != 1 || p.b.q.slotCount() != 1 {
		t.Errorf("slots: pool %d, A %d, B %d; want 1", p.pool.Slots(), p.a.q.slotCount(), p.b.q.slotCount())
	}
	p.runs.release("A/block")
	p.runs.release("B/b1")
}

// The settings page's concurrency on one pooled client resizes the shared
// pool: the other client's Workspace reports the new size.
func TestPoolSaveSettingsConcurrencyResizesPool(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	xdg := filepath.Join(root, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	repo := config.RepoDir(filepath.Join(root, ".git"))
	pool := NewPool(2)
	a := newPooledClient(t, Options{Pool: pool, Repo: proto.RepoInfo{Key: "a"}}, func(d *pipeline.Deps) {
		d.ConfigDirs = []string{repo, filepath.Join(xdg, "prt")}
	})
	b := newPooledClient(t, Options{Pool: pool, Repo: proto.RepoInfo{Key: "b"}}, nil)
	v := settingsValues(a)
	v.Concurrency = 3
	info, err := a.SaveSettings(ctx, proto.SaveSettingsParams{Values: v})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info.Applied, "concurrency 3") {
		t.Errorf("Applied = %q, want concurrency 3", info.Applied)
	}
	ws, err := b.Workspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pool.Slots() != 3 || ws.Concurrency != 3 {
		t.Errorf("pool %d, B's Workspace().Concurrency %d; want 3", pool.Slots(), ws.Concurrency)
	}
}

// Closing one client unregisters its queue: its running job ends and frees
// the slot, and the other client's waiting jobs run.
func TestPoolCloseFreesSlotsForOtherClient(t *testing.T) {
	ctx := context.Background()
	b1, b2 := branchItem("b1", 2), branchItem("b2", 1)
	p := newPoolPair(t, []model.Item{branchItem("a1", 9)}, []model.Item{b1, b2})
	if err := p.a.queueAuto(ctx, model.BranchItemID("a1")); err != nil {
		t.Fatal(err)
	}
	if err := p.b.queueAuto(ctx, b1.ID, b2.ID); err != nil {
		t.Fatal(err)
	}
	p.runs.none(t)
	if err := p.a.Close(); err != nil { // cancels A/block, drops A/a1
		t.Fatal(err)
	}
	if got, want := p.startOrder(t, "", 2), []string{"B/b1", "B/b2"}; !slices.Equal(got, want) {
		t.Errorf("after closing A: %v, want %v", got, want)
	}
	p.b.wg.Wait()
	if slices.Contains(p.runs.ran(), "A/a1") {
		t.Error("the closed client's waiting job ran")
	}
	p.pool.mu.Lock()
	_, registered := p.pool.queues[p.a.q]
	running := p.pool.running
	p.pool.mu.Unlock()
	if registered || running != 0 {
		t.Errorf("after close: A registered %v, %d running; want unregistered, 0", registered, running)
	}
}

// Stop all on one client stops its own running and waiting jobs only.
func TestPoolStopAllKeepsOtherClientsJobs(t *testing.T) {
	ctx := context.Background()
	a1, b1 := branchItem("a1", 9), branchItem("b1", 1)
	p := newPoolPair(t, []model.Item{a1}, []model.Item{b1})
	if err := p.a.queueAuto(ctx, a1.ID); err != nil {
		t.Fatal(err)
	}
	if err := p.b.queueAuto(ctx, b1.ID); err != nil {
		t.Fatal(err)
	}
	res, err := p.a.Stop(ctx, proto.StopParams{All: true})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{a1.ID, model.BranchItemID("block")}; !slices.Equal(res.Stopped, want) {
		t.Errorf("stopped %v, want %v", res.Stopped, want)
	}
	if got := p.runs.next(t); got != "B/b1" {
		t.Fatalf("after stop all on A: %q started, want B/b1", got)
	}
	p.runs.release("B/b1")
	p.b.wg.Wait()
	p.a.wg.Wait()
	if slices.Contains(p.runs.ran(), "A/a1") {
		t.Error("a job dropped by stop all ran")
	}
	if st := stored(t, p.b.d.Store, b1.ID).State; st != model.StateReady {
		t.Errorf("B's item state %q, want ready", st)
	}
}

// An unpooled client keeps its private slots; a pooled one reports and
// resizes the pool.
func TestPoolSlotCount(t *testing.T) {
	solo := newPooledClient(t, Options{}, func(d *pipeline.Deps) { d.Config.Pipeline.Concurrency = 3 })
	if solo.q.pool != nil || solo.q.slotCount() != 3 {
		t.Errorf("unpooled: pool %v, slots %d; want nil, 3", solo.q.pool, solo.q.slotCount())
	}
	pool := NewPool(0)
	c := newPooledClient(t, Options{Pool: pool}, func(d *pipeline.Deps) { d.Config.Pipeline.Concurrency = 7 })
	if c.q.slotCount() != defaultConcurrency {
		t.Errorf("pooled slots %d, want the pool's %d", c.q.slotCount(), defaultConcurrency)
	}
	c.q.setSlots(5)
	if pool.Slots() != 5 {
		t.Errorf("setSlots on a pooled queue: pool %d, want 5", pool.Slots())
	}
	if (&Pool{}).Slots() != defaultConcurrency {
		t.Error("a zero Pool should have defaultConcurrency slots")
	}
}
