package tui

// TUI half of the open-latency measurements behind docs/m52-open-latency.md
// (the daemon half is internal/daemon/openlatency_test.go). The real root
// Model is driven the way Bubble Tea drives it (every command on its own
// goroutine, messages applied one at a time) over a real daemon client on
// the generated fixture repo, with every item analysed from the recorded
// Jev, embedding and review answers. Offline; nothing asserts a budget, so
// it only runs when asked:
//
//	go test ./internal/tui -run TestOpenLatencyTUI -count=1 -v -args -perf
//
// The numbers are logged as "perf:" lines.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/questions"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/store"
	"github.com/fredricw/prt/internal/testutil"
)

var perfFlag = flag.Bool("perf", false, "run the open-latency measurements (TestOpenLatencyTUI)")

// ---- environment ------------------------------------------------------------

// perfDaemon is an in-process daemon over the fixture repo with every item
// analysed (auto_review "all", replayed models, file-backed store).
func perfDaemon(tb testing.TB) *daemon.InProcess {
	tb.Helper()
	ctx := context.Background()
	repo := testutil.FixtureRepo(tb)
	fake, err := gh.LoadFixture(testutil.FixturePRs(tb))
	if err != nil {
		tb.Fatal(err)
	}
	g, err := gitx.Open(ctx, repo)
	if err != nil {
		tb.Fatal(err)
	}
	gitDir, err := g.GitDir(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	cfg, err := config.Load([]string{filepath.Join(gitDir, "prt")})
	if err != nil {
		tb.Fatal(err)
	}
	cfg.Pipeline.AutoReview = string(model.AutoAll)
	st, err := store.Open(ctx, filepath.Join(tb.TempDir(), "prt.db"))
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = st.Close() })
	rec := testutil.Testdata(tb, "recordings")
	d := pipeline.Deps{
		Git: g, GH: fake, Store: st, Config: cfg, Questions: questions.Default(),
		Now:      func() time.Time { return fixedNow },
		Jev:      &jev.Replay{Dir: filepath.Join(rec, "jev"), MissUnavailable: true},
		Embedder: &embed.Replay{Dir: filepath.Join(rec, "embed")},
		Reviewers: review.Tiers{
			Fast: &review.Replay{Name: cfg.Models.Fast, Dir: filepath.Join(rec, "review"), MissUnavailable: true},
			Deep: &review.Replay{Name: cfg.Models.Deep, Dir: filepath.Join(rec, "review"), MissUnavailable: true},
		},
	}
	c := daemon.NewInProcess(d)
	tb.Cleanup(func() { _ = c.Close() })
	if err := c.Discover(ctx); err != nil {
		tb.Fatal(err)
	}
	items, err := c.Items(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	for _, it := range items {
		if err := c.RunSync(ctx, it.ID); err != nil {
			tb.Fatalf("analyse %s: %v", it.ID, err)
		}
	}
	return c
}

// perfServe serves c on a short socket path and returns a dialled client.
func perfServe(tb testing.TB, c daemon.Client) daemon.Client {
	tb.Helper()
	dir, err := os.MkdirTemp("", "prtperf")
	if err != nil {
		tb.Fatal(err)
	}
	path := filepath.Join(dir, "s", "prtd.sock")
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- daemon.Serve(ctx, c, path) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := daemon.Dial(context.Background(), path)
		if err == nil {
			tb.Cleanup(func() {
				_ = s.Close()
				cancel()
				<-errc
				_ = os.RemoveAll(dir)
			})
			return s
		}
		if time.Now().After(deadline) {
			cancel()
			tb.Fatalf("dial: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// timedClient counts and times the client calls the TUI makes.
type timedClient struct {
	daemon.Client
	mu       sync.Mutex
	details  []time.Duration
	items    atomic.Int64
	inflight atomic.Int64
	maxIn    atomic.Int64
}

func (c *timedClient) Detail(ctx context.Context, id string) (proto.Detail, error) {
	n := c.inflight.Add(1)
	for {
		m := c.maxIn.Load()
		if n <= m || c.maxIn.CompareAndSwap(m, n) {
			break
		}
	}
	t := time.Now()
	d, err := c.Client.Detail(ctx, id)
	el := time.Since(t)
	c.inflight.Add(-1)
	c.mu.Lock()
	c.details = append(c.details, el)
	c.mu.Unlock()
	return d, err
}

func (c *timedClient) Items(ctx context.Context) ([]model.Item, error) {
	c.items.Add(1)
	return c.Client.Items(ctx)
}

// reset clears the counters and returns what they held.
func (c *timedClient) reset() (details []time.Duration, items int, maxIn int) {
	c.mu.Lock()
	details, c.details = c.details, nil
	c.mu.Unlock()
	return details, int(c.items.Swap(0)), int(c.maxIn.Swap(c.inflight.Load()))
}

// ---- a Bubble Tea stand-in ------------------------------------------------------

// perfRT runs a Model like tea.Program: each command on its own goroutine,
// messages applied one at a time on the caller's goroutine.
type perfRT struct {
	tb   testing.TB
	m    Model
	msgs chan tea.Msg
	done chan struct{}
	// applied and dropped count detail messages the Model used or threw
	// away (a newer load was started meanwhile: detailSeq moved on); paint
	// is the Update time of each applied one (Overview, Code, Pipeline and
	// Architecture rebuilt on the UI goroutine).
	applied, dropped int
	paint            []time.Duration
}

func newPerfRT(tb testing.TB, m Model) *perfRT {
	r := &perfRT{tb: tb, m: m, msgs: make(chan tea.Msg, 4096), done: make(chan struct{})}
	tb.Cleanup(func() { close(r.done) })
	return r
}

func (r *perfRT) exec(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			for _, c := range b {
				r.exec(c)
			}
			return
		}
		if msg == nil {
			return
		}
		select {
		case r.msgs <- msg:
		case <-r.done:
		}
	}()
}

func (r *perfRT) apply(msg tea.Msg) {
	dm, isDetail := msg.(detailMsg)
	fresh := isDetail && dm.seq == r.m.detailSeq
	t := time.Now()
	next, cmd := r.m.Update(msg)
	el := time.Since(t)
	r.m = next.(Model)
	switch {
	case fresh:
		r.applied++
		r.paint = append(r.paint, el)
	case isDetail:
		r.dropped++
	}
	r.exec(cmd)
}

// until applies messages until pred holds; it reports the time taken and
// whether pred held before the timeout.
func (r *perfRT) until(pred func(Model) bool, timeout time.Duration) (time.Duration, bool) {
	start := time.Now()
	deadline := time.After(timeout)
	for !pred(r.m) {
		select {
		case msg := <-r.msgs:
			r.apply(msg)
		case <-deadline:
			return time.Since(start), false
		}
	}
	return time.Since(start), true
}

// pump applies messages for d.
func (r *perfRT) pump(d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case msg := <-r.msgs:
			r.apply(msg)
		case <-deadline:
			return
		}
	}
}

// selectID moves the picker cursor to id as a click on its row does
// (pickerMouse: moveTo, then selectionChanged).
func (r *perfRT) selectID(id string) {
	r.tb.Helper()
	i := r.m.picker.indexOf(id)
	if i < 0 {
		r.tb.Fatalf("%s not in the picker", id)
	}
	r.m.focus = FocusPicker
	if r.m.picker.moveTo(i) {
		r.exec(r.m.selectionChanged())
	}
}

func (r *perfRT) resetCounts() ([]time.Duration, int, int) {
	p, a, d := r.paint, r.applied, r.dropped
	r.paint, r.applied, r.dropped = nil, 0, 0
	return p, a, d
}

func painted(id string) func(Model) bool {
	return func(m Model) bool { return m.detail != nil && m.detail.Item.ID == id }
}

// ---- stats ----------------------------------------------------------------------

type latStats struct {
	n             int
	p50, p95, max time.Duration
}

func summarise(ds []time.Duration) latStats {
	if len(ds) == 0 {
		return latStats{}
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	at := func(p float64) time.Duration {
		i := int(math.Ceil(p*float64(len(s)))) - 1
		return s[min(max(i, 0), len(s)-1)]
	}
	return latStats{n: len(s), p50: at(0.5), p95: at(0.95), max: s[len(s)-1]}
}

func (l latStats) String() string {
	return fmt.Sprintf("p50 %s · p95 %s · max %s (n=%d)", msf(l.p50), msf(l.p95), msf(l.max), l.n)
}

func msf(d time.Duration) string { return strconv.FormatFloat(float64(d)/1e6, 'f', 2, 64) + " ms" }

func sum(ds []time.Duration) time.Duration {
	var t time.Duration
	for _, d := range ds {
		t += d
	}
	return t
}

// scaleDetail copies d's files, units, labels, groups and routing rows k
// times (distinct ids and paths), as a stand-in for a PR k times the size.
func scaleDetail(d proto.Detail, k int) proto.Detail {
	out := d
	out.Files, out.Units, out.Labels, out.Groups, out.Reviews = nil, nil, nil, nil, nil
	out.Diff = strings.Repeat(d.Diff, k)
	suffix := func(s string, i int) string { return s + "~" + strconv.Itoa(i) }
	path := func(p string, i int) string { return "copy" + strconv.Itoa(i) + "/" + p }
	units := map[string]bool{}
	for _, u := range d.Units {
		units[u.ID] = true
	}
	for i := range k {
		for _, f := range d.Files {
			f.Path = path(f.Path, i)
			out.Files = append(out.Files, f)
		}
		for _, u := range d.Units {
			u.ID, u.Path, u.FileID, u.Order = suffix(u.ID, i), path(u.Path, i), path(u.FileID, i), u.Order+i*len(d.Units)
			out.Units = append(out.Units, u)
		}
		for _, l := range d.Labels {
			if units[l.UnitID] {
				l.UnitID = suffix(l.UnitID, i)
				out.Labels = append(out.Labels, l)
			}
		}
		for _, g := range d.Groups {
			ids := make([]string, len(g.UnitIDs))
			for j, u := range g.UnitIDs {
				ids[j] = suffix(u, i)
			}
			g.ID, g.UnitIDs, g.Order = suffix(g.ID, i), ids, g.Order+i*len(d.Groups)
			out.Groups = append(out.Groups, g)
		}
		for _, rv := range d.Reviews {
			rv.GroupID = suffix(rv.GroupID, i)
			out.Reviews = append(out.Reviews, rv)
		}
	}
	return out
}

// ---- the report --------------------------------------------------------------------

// TestOpenLatencyTUI measures, through the real Model, how long "loading…"
// shows when an analysed item is opened, idle and while other items
// analyse, and what one re-run of the open item costs the TUI.
func TestOpenLatencyTUI(t *testing.T) {
	if !*perfFlag && os.Getenv("PRT_PERF") == "" {
		t.Skip("measurement only: pass -args -perf or set PRT_PERF=1 (see docs/m52-open-latency.md)")
	}
	for _, transport := range []string{"in-process", "socket"} {
		t.Run(transport, func(t *testing.T) { openLatencyTUI(t, transport) })
	}
}

func openLatencyTUI(t *testing.T, transport string) {
	ctx := context.Background()
	d := perfDaemon(t)
	// The set-up's analyses left up to 256 events buffered; a TUI started
	// now would turn each one naming the selected item into a Detail load
	// (a start-up storm a real session does not have). Serve drains them
	// itself.
	for drained := false; !drained; {
		select {
		case <-d.Events():
		default:
			drained = true
		}
	}
	var base daemon.Client = d
	if transport == "socket" {
		base = perfServe(t, d)
	}
	c := &timedClient{Client: base}
	// A colour renderer, as in a terminal (styles cost more than plain).
	rr := lipgloss.NewRenderer(io.Discard)
	rr.SetColorProfile(termenv.TrueColor)
	m := NewWithOptions(c, rr, Options{Now: func() time.Time { return fixedNow }})
	r := newPerfRT(t, m)
	r.apply(tea.WindowSizeMsg{Width: 160, Height: 45})
	r.exec(r.m.Init())
	first, ok := r.until(func(m Model) bool { return m.detail != nil }, 30*time.Second)
	if !ok {
		t.Fatal("first item never loaded")
	}
	sel, _ := r.m.picker.selected()
	firstPaint, firstDetail := time.Duration(0), time.Duration(0)
	if len(r.paint) > 0 {
		firstPaint = r.paint[0]
	}
	c.mu.Lock()
	if len(c.details) > 0 {
		firstDetail = c.details[0]
	}
	c.mu.Unlock()
	r.pump(300 * time.Millisecond) // the start-up discovery's events
	t.Logf("perf: [%s] start-up: first item (%s) painted after %s (its Detail %s, applying it %s)",
		transport, sel.ID, msf(first), msf(firstDetail), msf(firstPaint))

	ids := make([]string, 0, len(r.m.picker.items))
	for _, it := range r.m.picker.items {
		ids = append(ids, it.ID)
	}

	// 1. Idle: open each analysed item in turn (as a click on its row),
	// starting from the last one so every open is a real move.
	r.selectID(ids[len(ids)-1])
	if _, ok := r.until(painted(ids[len(ids)-1]), 30*time.Second); !ok {
		t.Fatal("never painted")
	}
	c.reset()
	r.resetCounts()
	var loading []time.Duration
	per := map[string][]time.Duration{}
	for range 5 {
		for _, id := range ids {
			r.selectID(id)
			if r.m.detail == nil && !strings.Contains(r.m.View(), "loading…") {
				t.Fatal("expected the loading placeholder while the detail loads")
			}
			el, ok := r.until(painted(id), 30*time.Second)
			if !ok {
				t.Fatalf("%s never painted", id)
			}
			loading = append(loading, el)
			per[id] = append(per[id], el)
		}
	}
	paint, applied, dropped := r.resetCounts()
	dets, items, _ := c.reset()
	t.Logf("perf: [%s] 1. idle open (\"loading…\" shown until painted): %s", transport, summarise(loading))
	for _, id := range ids {
		t.Logf("perf: [%s]    %-26s %s", transport, id, summarise(per[id]))
	}
	t.Logf("perf: [%s]    Detail calls %d (%s), applied %d, dropped %d, list reloads %d; paint (Update of the applied detail) %s",
		transport, len(dets), summarise(dets), applied, dropped, items, summarise(paint))

	// 2. Busy: the same while two other items re-analyse continuously
	// through the daemon's worker pool (forced `r`, re-pushed as soon as
	// the queue accepts it).
	churn := []string{model.PRItemID(102), model.PRItemID(103)}
	browse := []string{}
	for _, id := range ids {
		if !slices.Contains(churn, id) {
			browse = append(browse, id)
		}
	}
	stopChurn := make(chan struct{})
	var wg sync.WaitGroup
	for _, id := range churn {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopChurn:
					return
				case <-time.After(25 * time.Millisecond):
				}
				_ = d.Refresh(ctx, proto.ItemParams{ItemID: id, Force: true, User: true})
			}
		}()
	}
	r.pump(500 * time.Millisecond)
	c.reset()
	r.resetCounts()
	loading = nil
	for range 5 {
		for _, id := range browse {
			r.selectID(id)
			el, ok := r.until(painted(id), 60*time.Second)
			if !ok {
				t.Fatalf("%s never painted", id)
			}
			loading = append(loading, el)
		}
	}
	paint, applied, dropped = r.resetCounts()
	dets, items, maxIn := c.reset()
	t.Logf("perf: [%s] 2. open while %v re-analyse: %s", transport, churn, summarise(loading))
	t.Logf("perf: [%s]    Detail calls %d (%s), max %d in flight, applied %d, dropped %d, list reloads %d; paint %s",
		transport, len(dets), summarise(dets), maxIn, applied, dropped, items, summarise(paint))
	close(stopChurn)
	wg.Wait()
	settled := func(m Model) bool {
		for _, it := range m.picker.items {
			if it.State == model.StateFetching || it.State == model.StateAnalysing {
				return false
			}
		}
		return c.inflight.Load() == 0
	}
	if _, ok := r.until(settled, 60*time.Second); !ok {
		t.Fatal("churn did not settle")
	}
	r.pump(300 * time.Millisecond)

	// 3. `r` on the open item: every event for it starts a full Detail load.
	target := model.PRItemID(101)
	r.selectID(target)
	if _, ok := r.until(painted(target), 30*time.Second); !ok {
		t.Fatal("target never painted")
	}
	r.pump(200 * time.Millisecond)
	c.reset()
	r.resetCounts()
	sawBusy := false
	r.apply(keyMsg("r"))
	done := func(m Model) bool {
		it, _ := m.picker.selected()
		if it.State == model.StateFetching || it.State == model.StateAnalysing {
			sawBusy = true
		}
		return sawBusy && it.State == model.StateReady && c.inflight.Load() == 0 &&
			m.detail != nil && m.detail.Item.State == model.StateReady
	}
	runFor, ok := r.until(done, 60*time.Second)
	if !ok {
		t.Fatal("re-run did not finish")
	}
	r.pump(300 * time.Millisecond)
	paint, applied, dropped = r.resetCounts()
	dets, items, maxIn = c.reset()
	t.Logf("perf: [%s] 3. r on the open item %s: settled after %s", transport, target, msf(runFor))
	t.Logf("perf: [%s]    Detail calls %d (sum %s; %s), max %d in flight, applied %d, dropped %d, list reloads %d; paint %s (sum %s)",
		transport, len(dets), msf(sum(dets)), summarise(dets), maxIn, applied, dropped, items, summarise(paint), msf(sum(paint)))

	// 4. Open an analysed item while it re-runs (`r`, a routing reload or a
	// cap change landed on it): "loading…" lasts until a load finishes with
	// no newer event for the item in between.
	other := browse[len(browse)-1]
	if other == target {
		other = browse[0]
	}
	var opens []time.Duration
	for range 3 {
		r.apply(keyMsg("r"))
		if _, ok := r.until(func(m Model) bool {
			it, _ := m.picker.selected()
			return it.State == model.StateAnalysing
		}, 30*time.Second); !ok {
			t.Fatal("re-run never started")
		}
		sawBusy = true
		r.selectID(other)
		r.selectID(target)
		el, ok := r.until(painted(target), 60*time.Second)
		if !ok {
			t.Fatal("target never painted")
		}
		opens = append(opens, el)
		if _, ok := r.until(done, 60*time.Second); !ok {
			t.Fatal("re-run did not finish")
		}
		r.pump(200 * time.Millisecond)
	}
	_, _, dropped = r.resetCounts()
	t.Logf("perf: [%s] 4. open %s while it re-runs: \"loading…\" for %s (%d stale loads dropped over the 3 runs)",
		transport, target, summarise(opens), dropped)

	// 5. Paint cost of large items (the Update that applies a detail).
	det, err := c.Detail(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []int{1, 10, 40} {
		big := scaleDetail(det, k)
		var first, reload []time.Duration
		for range 5 {
			// As a newly selected item: nothing shown yet.
			r.m.detail = nil
			r.m.detailSeq++
			tt := time.Now()
			next, _ := r.m.Update(detailMsg{seq: r.m.detailSeq, d: big})
			first = append(first, time.Since(tt))
			r.m = next.(Model)
			tt = time.Now()
			next, _ = r.m.Update(detailMsg{seq: r.m.detailSeq, d: big})
			reload = append(reload, time.Since(tt))
			r.m = next.(Model)
		}
		t.Logf("perf: [%s] 5. paint ×%-2d (%d units, %d labels): first %s · reload of the same item %s",
			transport, k, len(big.Units), len(big.Labels), summarise(first), summarise(reload))
	}
}
