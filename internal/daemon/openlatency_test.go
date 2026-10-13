package daemon

// Open-latency measurements behind docs/m52-open-latency.md (M5.2 user
// report: "opening a PR that has already been analysed still shows a loading
// bar and sometimes takes quite some time"). Everything is offline: the
// generated fixture repo, the GitHub fixture and the recorded Jev, embedding
// and review answers, over a file-backed store opened exactly as `prt` opens
// it (WAL; one write connection and, since the M5.2 fixes, a read pool).
//
// Nothing here asserts a time budget (timings depend on the machine), so the
// report test only runs when asked (the -perf test flag or PRT_PERF=1):
//
//	go test ./internal/daemon -run TestOpenLatency -count=1 -v -args -perf
//	go test ./internal/daemon -run '^$' -bench Detail -benchtime 100x
//
// The numbers are logged as "perf:" lines. "full" is Detail (every part),
// "lean" is DetailOf with NoLabels (what screens load on open since M5.2).

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
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

// perfEnvVar and perfFlag enable TestOpenLatency.
const perfEnvVar = "PRT_PERF"

var perfFlag = flag.Bool("perf", false, "run the open-latency measurements (TestOpenLatency)")

// perfSelected is the item the measurements treat as the one open in the TUI.
var perfSelected = model.PRItemID(101)

// perfClock is a settable clock: discovery's urgency has an age term, so
// advancing it by a refresh interval shows what one 60 s tick rewrites.
type perfClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *perfClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *perfClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// perfGate holds review calls while armed, so a run can be measured
// mid-flight (the snapshot section).
type perfGate struct {
	mu      sync.Mutex
	ch      chan struct{}
	entered chan struct{}
}

// arm makes review calls block until the returned release is called.
func (g *perfGate) arm() (release func()) {
	ch := make(chan struct{})
	g.mu.Lock()
	g.ch = ch
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.ch = nil
			g.mu.Unlock()
			close(ch)
		})
	}
}

// wait blocks a review call while the gate is armed.
func (g *perfGate) wait(ctx context.Context) error {
	g.mu.Lock()
	ch := g.ch
	g.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case g.entered <- struct{}{}:
	default:
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

// gatedTier is a reviewer whose review calls pass a perfGate.
type gatedTier struct {
	review.Reviewer
	gate *perfGate
}

func (r gatedTier) Review(ctx context.Context, req review.Request) (review.Result, error) {
	if req.Task == review.TaskReview {
		if err := r.gate.wait(ctx); err != nil {
			return review.Result{}, err
		}
	}
	return r.Reviewer.Review(ctx, req)
}

// perfEnv is a daemon over the fixture repo with every item analysed.
type perfEnv struct {
	c     *InProcess
	deps  pipeline.Deps
	path  string // the store file
	clock *perfClock
	gate  *perfGate
	ids   []string
}

// newPerfEnv discovers the fixture items and analyses each once (auto_review
// "all", replayed models), so every Detail afterwards is of an analysed item.
func newPerfEnv(tb testing.TB) *perfEnv {
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
	// The repo-local config only, as the replay flags do (recorded request
	// keys depend on it).
	cfg, err := config.Load([]string{filepath.Join(gitDir, "prt")})
	if err != nil {
		tb.Fatal(err)
	}
	cfg.Pipeline.AutoReview = string(model.AutoAll)
	path := filepath.Join(tb.TempDir(), "prt.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = st.Close() })
	rec := testutil.Testdata(tb, "recordings")
	clock := &perfClock{t: testNow}
	gate := &perfGate{entered: make(chan struct{}, 1)}
	d := pipeline.Deps{
		Git: g, GH: fake, Store: st, Config: cfg, Questions: questions.Default(), Now: clock.now,
		Jev:      &jev.Replay{Dir: filepath.Join(rec, "jev"), MissUnavailable: true},
		Embedder: &embed.Replay{Dir: filepath.Join(rec, "embed")},
		Reviewers: review.Tiers{
			Fast: gatedTier{&review.Replay{Name: cfg.Models.Fast, Dir: filepath.Join(rec, "review"), MissUnavailable: true}, gate},
			Deep: gatedTier{&review.Replay{Name: cfg.Models.Deep, Dir: filepath.Join(rec, "review"), MissUnavailable: true}, gate},
		},
	}
	c := NewInProcess(d)
	tb.Cleanup(func() { _ = c.Close() })
	if err := c.Discover(ctx); err != nil {
		tb.Fatal(err)
	}
	items, err := c.Items(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	e := &perfEnv{c: c, deps: d, path: path, clock: clock, gate: gate}
	for _, it := range items {
		if err := c.RunSync(ctx, it.ID); err != nil {
			tb.Fatalf("analyse %s: %v", it.ID, err)
		}
		e.ids = append(e.ids, it.ID)
	}
	sort.Strings(e.ids)
	return e
}

// others are the analysed items other than the selected one.
func (e *perfEnv) others() []string {
	var out []string
	for _, id := range e.ids {
		if id != perfSelected {
			out = append(out, id)
		}
	}
	return out
}

// churn re-analyses each of ids in its own loop until the returned stop is
// called, as the worker pool does: c.analyse runs an explicit forced job
// whose fetch and later stages ignore their caches (Pipeline page `r` on
// fetch), so every stage writes again (fetch, files, units, links, groups,
// routing rows, reviews, call logs, aggregate, run log, item states). Jev
// labels and embeddings stay content-keyed cache hits (reads). stop returns
// the number of runs finished.
func (e *perfEnv) churn(ids []string) (stop func() int) {
	var wg sync.WaitGroup
	var runs atomic.Int64
	done := make(chan struct{})
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				// seq above every stop mark (0 counts as queued before a stop).
				e.c.analyse(job{itemID: id, force: true, fromStage: model.StageFetch, explicit: true, seq: math.MaxInt32})
				runs.Add(1)
			}
		}()
	}
	return func() int {
		close(done)
		wg.Wait()
		return int(runs.Load())
	}
}

// ---- timing helpers --------------------------------------------------------

type lat struct {
	n             int
	p50, p95, max time.Duration
}

func summarise(ds []time.Duration) lat {
	if len(ds) == 0 {
		return lat{}
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	at := func(p float64) time.Duration {
		i := int(math.Ceil(p*float64(len(s)))) - 1
		return s[min(max(i, 0), len(s)-1)]
	}
	return lat{n: len(s), p50: at(0.50), p95: at(0.95), max: s[len(s)-1]}
}

func (l lat) String() string {
	return fmt.Sprintf("p50 %s · p95 %s · max %s (n=%d)", ms(l.p50), ms(l.p95), ms(l.max), l.n)
}

func ms(d time.Duration) string { return strconv.FormatFloat(float64(d)/1e6, 'f', 2, 64) + " ms" }

func kb(n int) string { return strconv.FormatFloat(float64(n)/1024, 'f', 1, 64) + " KB" }

// timeDetail times n sequential full Detail calls.
func timeDetail(tb testing.TB, c Client, id string, n int) []time.Duration {
	return timeDetailOf(tb, c, id, false, n)
}

// timeDetailOf times n sequential DetailOf calls (lean: NoLabels).
func timeDetailOf(tb testing.TB, c Client, id string, lean bool, n int) []time.Duration {
	tb.Helper()
	ctx := context.Background()
	out := make([]time.Duration, 0, n)
	for range n {
		t := time.Now()
		if _, err := c.DetailOf(ctx, proto.DetailParams{ItemID: id, NoLabels: lean}); err != nil {
			tb.Fatalf("detail %s: %v", id, err)
		}
		out = append(out, time.Since(t))
	}
	return out
}

// timeLabels times n sequential Labels calls.
func timeLabels(tb testing.TB, c Client, id string, units []string, n int) []time.Duration {
	tb.Helper()
	ctx := context.Background()
	out := make([]time.Duration, 0, n)
	for range n {
		t := time.Now()
		if _, err := c.Labels(ctx, proto.LabelsParams{ItemID: id, UnitIDs: units}); err != nil {
			tb.Fatalf("labels %s: %v", id, err)
		}
		out = append(out, time.Since(t))
	}
	return out
}

// detailJSON is the JSON size of a DetailOf and its encode+decode time.
func detailJSON(tb testing.TB, c Client, id string, lean bool) (proto.Detail, int, time.Duration) {
	tb.Helper()
	d, err := c.DetailOf(context.Background(), proto.DetailParams{ItemID: id, NoLabels: lean})
	if err != nil {
		tb.Fatal(err)
	}
	b, err := json.Marshal(d)
	if err != nil {
		tb.Fatal(err)
	}
	t := time.Now()
	for range 20 {
		bb, _ := json.Marshal(d)
		var back proto.Detail
		if err := json.Unmarshal(bb, &back); err != nil {
			tb.Fatal(err)
		}
	}
	return d, len(b), time.Since(t) / 20
}

// burst issues n concurrent DetailOf calls, as the TUI did before M5.2 when
// n events for the selected item arrived while earlier loads were in
// flight (each event started a load; none was cancelled), and returns how
// long the last-issued one (the only one the TUI applies) took and how
// long all took.
func burst(tb testing.TB, c Client, id string, lean bool, n int) (last, all time.Duration) {
	tb.Helper()
	ctx := context.Background()
	var wg sync.WaitGroup
	var lastD atomic.Int64
	start := time.Now()
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.Now()
			if _, err := c.DetailOf(ctx, proto.DetailParams{ItemID: id, NoLabels: lean}); err != nil {
				tb.Errorf("detail %s: %v", id, err)
			}
			if i == n-1 {
				lastD.Store(int64(time.Since(t)))
			}
		}()
		time.Sleep(200 * time.Microsecond) // issue order = event order
	}
	wg.Wait()
	return time.Duration(lastD.Load()), time.Since(start)
}

// step is one store read of Detail.
type step struct {
	name  string
	rows  int
	d     time.Duration
	label bool // a label or override read (left out by the lean Detail)
}

// detailSteps replays InProcess.DetailOf's store reads of the live path in
// its order, timing each (the routing info and the branch ageing are
// in-memory and left out).
func detailSteps(ctx context.Context, s store.Store, id string) ([]step, error) {
	var out []step
	t := time.Now()
	lap := func(name string, rows int, label bool) {
		out = append(out, step{name, rows, time.Since(t), label})
		t = time.Now()
	}
	it, err := s.Item(ctx, id)
	if err != nil {
		return nil, err
	}
	lap("Item", 1, false)
	diff := 0
	for _, sha := range []string{it.UnitsSHA, it.AnalysedSHA, it.HeadSHA} {
		if sha == "" {
			continue
		}
		if f, ok, err := s.Fetch(ctx, id, sha); err == nil && ok {
			diff = len(f.Diff)
			break
		}
	}
	lap("Fetch (diff "+kb(diff)+")", 1, false)
	files, err := s.Files(ctx, id)
	if err != nil {
		return nil, err
	}
	lap("Files", len(files), false)
	units, err := s.Units(ctx, id)
	if err != nil {
		return nil, err
	}
	lap("Units", len(units), false)
	subjects := labelSubjects(id, files, units)
	ls, err := s.Labels(ctx, subjects)
	if err != nil {
		return nil, err
	}
	lap(fmt.Sprintf("Labels (IN over %d subjects)", len(subjects)), len(ls), true)
	ovs, err := s.Overrides(ctx, subjects)
	if err != nil {
		return nil, err
	}
	lap("Overrides", len(ovs), true)
	links, err := s.Links(ctx, id)
	if err != nil {
		return nil, err
	}
	lap("Links", len(links), false)
	groups, err := s.Groups(ctx, id)
	if err != nil {
		return nil, err
	}
	lap("Groups", len(groups), false)
	rs, err := s.ItemReviews(ctx, id)
	if err != nil {
		return nil, err
	}
	lap("ItemReviews", len(rs), false)
	if _, _, err := s.Agg(ctx, id); err != nil {
		return nil, err
	}
	lap("Agg", 1, false)
	cs, err := s.Comments(ctx, id)
	if err != nil {
		return nil, err
	}
	lap("Comments", len(cs), false)
	fm, err := s.FindingMarks(ctx, id)
	if err != nil {
		return nil, err
	}
	lap("FindingMarks", len(fm), false)
	rls, err := s.CurrentRunLogs(ctx, id)
	if err != nil {
		return nil, err
	}
	lap("RunLogs (current run)", len(rls), false)
	return out, nil
}

// medianSteps runs detailSteps n times and keeps each step's median.
func medianSteps(tb testing.TB, s store.Store, id string, n int) []step {
	tb.Helper()
	var runs [][]step
	for range n {
		st, err := detailSteps(context.Background(), s, id)
		if err != nil {
			tb.Fatal(err)
		}
		runs = append(runs, st)
	}
	out := slices.Clone(runs[0])
	for i := range out {
		ds := make([]time.Duration, 0, n)
		for _, r := range runs {
			ds = append(ds, r[i].d)
		}
		out[i].d = summarise(ds).p50
	}
	return out
}

// stepTime is the median time of the step whose name starts with prefix.
func stepTime(steps []step, prefix string) time.Duration {
	for _, s := range steps {
		if strings.HasPrefix(s.name, prefix) {
			return s.d
		}
	}
	return 0
}

func logSteps(tb testing.TB, label string, steps []step) {
	tb.Helper()
	var total, lean time.Duration
	for _, s := range steps {
		total += s.d
		if !s.label {
			lean += s.d
		}
	}
	tb.Logf("perf: %s: store reads, median per step (sum %s; lean, without labels and overrides, %s)", label, ms(total), ms(lean))
	for _, s := range steps {
		tb.Logf("perf:   %-34s %6d rows  %s", s.name, s.rows, ms(s.d))
	}
}

// ---- socket -----------------------------------------------------------------

// perfSocket serves c on a fresh short socket path and dials it. stop closes
// the client and the server (Serve owns c's event stream while it runs).
func perfSocket(tb testing.TB, c Client) (*Socket, func()) {
	tb.Helper()
	dir, err := os.MkdirTemp("", "prtperf")
	if err != nil {
		tb.Fatal(err)
	}
	path := filepath.Join(dir, "s", "prtd.sock")
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, c, path) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := Dial(context.Background(), path)
		if err == nil {
			return s, func() {
				_ = s.Close()
				cancel()
				<-errc
				_ = os.RemoveAll(dir)
			}
		}
		if time.Now().After(deadline) {
			cancel()
			tb.Fatalf("dial: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ---- events -------------------------------------------------------------------

// evRec records the client's events with arrival times.
type evRec struct {
	mu   sync.Mutex
	evs  []proto.Event
	at   []time.Time
	done chan struct{}
	wg   sync.WaitGroup
}

// record drains what is buffered and records every later event until stop.
func record(c *InProcess) *evRec {
	drain(c.Events())
	r := &evRec{done: make(chan struct{})}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			select {
			case <-r.done:
				return
			case e, ok := <-c.Events():
				if !ok {
					return
				}
				r.mu.Lock()
				r.evs, r.at = append(r.evs, e), append(r.at, time.Now())
				r.mu.Unlock()
			}
		}
	}()
	return r
}

func (r *evRec) snapshot() ([]proto.Event, []time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.evs), slices.Clone(r.at)
}

func (r *evRec) stop() ([]proto.Event, []time.Time) {
	close(r.done)
	r.wg.Wait()
	return r.snapshot()
}

// waitSettled waits until id has gone through Fetching (or Analysing) and
// back to a resting state, then lets trailing events land.
func (r *evRec) waitSettled(tb testing.TB, id string) {
	tb.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		evs, _ := r.snapshot()
		busy := false
		for _, e := range evs {
			if e.Type != proto.EventItem || e.ItemID != id || e.Item == nil {
				continue
			}
			switch e.Item.State {
			case model.StateFetching, model.StateAnalysing:
				busy = true
			case model.StateReady, model.StateStale, model.StateError, model.StateNotAnalysed:
				if busy {
					time.Sleep(150 * time.Millisecond)
					return
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	tb.Fatalf("%s did not settle", id)
}

// reloadCounts applies the pre-M5.2 TUI's rules to an event stream for a
// selected item: every event naming it except a remove starts a full
// Detail load (tui.Model.onEvent), and every item/remove event asks for a
// list reload (queueReload, coalesced while one is in flight).
func reloadCounts(evs []proto.Event, selected string) (detail, list int, byType map[proto.EventType]int) {
	byType = map[proto.EventType]int{}
	for _, e := range evs {
		if e.ItemID == selected {
			byType[e.Type]++
			if e.Type != proto.EventRemove {
				detail++
			}
		}
		if e.Type == proto.EventItem || e.Type == proto.EventRemove {
			list++
		}
	}
	return detail, list, byType
}

func fmtTypes(m map[proto.EventType]int) string {
	var parts []string
	for _, t := range []proto.EventType{proto.EventItem, proto.EventStage, proto.EventPatch, proto.EventLog, proto.EventRemove} {
		if m[t] > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", t, m[t]))
		}
	}
	return strings.Join(parts, ", ")
}

// closer counts the gaps between consecutive events shorter than l: each
// such event starts a load while the previous one is (at least) still in
// flight, and the earlier load's result is dropped (detailSeq moved on).
func closer(at []time.Time, l time.Duration) int {
	n := 0
	for i := 1; i < len(at); i++ {
		if at[i].Sub(at[i-1]) < l {
			n++
		}
	}
	return n
}

// selectedTimes are the arrival times of the events naming id (no removes).
func selectedTimes(evs []proto.Event, at []time.Time, id string) []time.Time {
	var out []time.Time
	for i, e := range evs {
		if e.ItemID == id && e.Type != proto.EventRemove {
			out = append(out, at[i])
		}
	}
	return out
}

// gaps summarises the time between consecutive events.
func gaps(at []time.Time) lat {
	var ds []time.Duration
	for i := 1; i < len(at); i++ {
		ds = append(ds, at[i].Sub(at[i-1]))
	}
	return summarise(ds)
}

// ---- synthetic large item --------------------------------------------------------

// scaled stores k copies of item src's analysis (diff, files, units, their
// labels, groups, routing rows, aggregate) under a new item id, as a stand-in
// for a PR k times the size of a fixture PR. Units and files get distinct ids
// and paths per copy; Detail does not validate references, so the copy
// serves only to size the reads.
func scaled(tb testing.TB, s store.Store, src string, k int) string {
	tb.Helper()
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			tb.Fatal(err)
		}
	}
	it, err := s.Item(ctx, src)
	must(err)
	f, ok, err := s.Fetch(ctx, src, it.AnalysedSHA)
	must(err)
	if !ok {
		tb.Fatalf("%s: no fetch for %s", src, it.AnalysedSHA)
	}
	files, err := s.Files(ctx, src)
	must(err)
	units, err := s.Units(ctx, src)
	must(err)
	ls, err := s.Labels(ctx, labelSubjects(src, files, units))
	must(err)
	groups, err := s.Groups(ctx, src)
	must(err)
	rows, err := s.ItemReviews(ctx, src)
	must(err)
	agg, ok, err := s.Agg(ctx, src)
	must(err)
	if !ok {
		tb.Fatalf("%s: no aggregate", src)
	}

	id := model.PRItemID(9000 + k)
	it.ID, it.Number = id, 9000+k
	must(s.UpsertItem(ctx, it))
	f.ItemID, f.Diff = id, strings.Repeat(f.Diff, k)
	must(s.PutFetch(ctx, f))
	unitID := func(u string, i int) string { return u + "~" + strconv.Itoa(i) }
	path := func(p string, i int) string { return "copy" + strconv.Itoa(i) + "/" + p }
	var nf []model.File
	var nu []model.ChangeUnit
	var nl []model.Label
	var ng []model.Group
	var nr []model.Review
	fileSub := map[string]string{}
	for _, fl := range files {
		fileSub[model.FileSubject(src, fl.Path)] = fl.Path
	}
	unitSet := map[string]bool{}
	for _, u := range units {
		unitSet[u.ID] = true
	}
	for i := range k {
		for _, fl := range files {
			fl.ItemID, fl.Path = id, path(fl.Path, i)
			nf = append(nf, fl)
		}
		for _, u := range units {
			u.ItemID, u.ID, u.Path, u.FileID = id, unitID(u.ID, i), path(u.Path, i), path(u.FileID, i)
			u.Order += i * len(units)
			nu = append(nu, u)
		}
		for _, l := range ls {
			switch {
			case unitSet[l.UnitID]:
				l.UnitID = unitID(l.UnitID, i)
			case fileSub[l.UnitID] != "":
				l.UnitID = model.FileSubject(id, path(fileSub[l.UnitID], i))
			case i == 0 && l.UnitID == model.ItemSubject(src):
				l.UnitID = model.ItemSubject(id)
			default:
				continue
			}
			nl = append(nl, l)
		}
		for _, g := range groups {
			g.ItemID, g.ID = id, unitID(g.ID, i)
			ids := make([]string, len(g.UnitIDs))
			for j, u := range g.UnitIDs {
				ids[j] = unitID(u, i)
			}
			g.UnitIDs = ids
			g.Order += i * len(groups)
			ng = append(ng, g)
		}
		for _, r := range rows {
			r.ItemID, r.GroupID = id, unitID(r.GroupID, i)
			nr = append(nr, r)
		}
	}
	must(s.PutFiles(ctx, id, nf))
	must(s.PutUnits(ctx, id, nu))
	must(s.PutLabels(ctx, nl))
	must(s.PutGroups(ctx, id, ng))
	must(s.PutItemReviews(ctx, id, nr))
	agg.ItemID = id
	must(s.PutAgg(ctx, agg))
	return id
}

// ---- the report --------------------------------------------------------------------

// TestOpenLatency measures what opening an analysed item costs and what
// keeps re-loading it. See docs/m52-open-latency.md for the analysis.
func TestOpenLatency(t *testing.T) {
	if !*perfFlag && os.Getenv(perfEnvVar) == "" {
		t.Skip("measurement only: pass -args -perf or set " + perfEnvVar + "=1 (see docs/m52-open-latency.md)")
	}
	t0 := time.Now()
	e := newPerfEnv(t)
	t.Logf("perf: setup (fixture repo, discovery, %d analyses): %s", len(e.ids), time.Since(t0).Round(time.Millisecond))
	largest := perfIdle(t, e)
	perfSocketIdle(t, e)
	perfBusy(t, e)
	perfBursts(t, e)
	perfSnapshot(t, e)
	perfEvents(t, e)
	perfSizes(t, e, largest)
	perfRunLog(t, e)
}

// perfN is the number of calls per latency figure.
const perfN = 60

// perfIdle is section 1: one Detail per item, nothing else running. It
// returns the item with the most units.
func perfIdle(t *testing.T, e *perfEnv) string {
	ctx := context.Background()
	t.Log("perf: == 1. idle Detail, in-process (file store, WAL, one write connection + read pool) ==")
	largest, largestUnits := "", -1
	for _, id := range e.ids {
		d, size, enc := detailJSON(t, e.c, id, false)
		_, leanSize, leanEnc := detailJSON(t, e.c, id, true)
		full := summarise(timeDetail(t, e.c, id, perfN))
		lean := summarise(timeDetailOf(t, e.c, id, true, perfN))
		t.Logf("perf: %-26s files %3d units %3d labels %5d runlogs %3d", id, len(d.Files), len(d.Units), len(d.Labels), len(d.RunLogs))
		t.Logf("perf:   full json %9s (encode+decode %s) | %s", kb(size), ms(enc), full)
		t.Logf("perf:   lean json %9s (encode+decode %s) | %s", kb(leanSize), ms(leanEnc), lean)
		if len(d.Units) > 0 {
			one := summarise(timeLabels(t, e.c, id, []string{d.Units[0].ID}, perfN))
			all := summarise(timeLabels(t, e.c, id, nil, perfN))
			t.Logf("perf:   Labels: one unit %s | all subjects %s", one, all)
		}
		if len(d.Units) > largestUnits {
			largest, largestUnits = id, len(d.Units)
		}
	}
	logSteps(t, "idle "+largest, medianSteps(t, e.deps.Store, largest, 30))
	itemsLat := make([]time.Duration, 0, perfN)
	routing := make([]time.Duration, 0, perfN)
	for range perfN {
		tt := time.Now()
		if _, err := e.c.Items(ctx); err != nil {
			t.Fatal(err)
		}
		itemsLat = append(itemsLat, time.Since(tt))
		tt = time.Now()
		_ = e.c.routingInfo()
		routing = append(routing, time.Since(tt))
	}
	t.Logf("perf: Items() (list reload after each item event): %s", summarise(itemsLat))
	t.Logf("perf: routing info built per Detail: %s", summarise(routing))
	return largest
}

// perfSocketIdle is section 2: idle Detail over the daemon socket.
func perfSocketIdle(t *testing.T, e *perfEnv) {
	t.Log("perf: == 2. idle Detail over the Unix socket (NDJSON) ==")
	sock, stop := perfSocket(t, e.c)
	defer stop()
	for _, id := range e.ids {
		t.Logf("perf: socket %-26s full %s", id, summarise(timeDetail(t, sock, id, perfN)))
		t.Logf("perf: socket %-26s lean %s", id, summarise(timeDetailOf(t, sock, id, true, perfN)))
	}
}

// perfBusy is section 3: Detail of the selected item while the other items
// re-analyse (the worker pool runs 4 at once).
func perfBusy(t *testing.T, e *perfEnv) {
	idle := summarise(timeDetail(t, e.c, perfSelected, perfN))
	idleLean := summarise(timeDetailOf(t, e.c, perfSelected, true, perfN))
	t.Logf("perf: == 3. Detail(%s) while other items' analyses write (reads on the read pool) ==", perfSelected)
	t.Logf("perf: idle in-process full %s", idle)
	t.Logf("perf: idle in-process lean %s", idleLean)
	for _, writers := range []int{1, len(e.others())} {
		stop := e.churn(e.others()[:writers])
		time.Sleep(200 * time.Millisecond)
		tb := time.Now()
		busy := summarise(timeDetail(t, e.c, perfSelected, perfN))
		busyLean := summarise(timeDetailOf(t, e.c, perfSelected, true, perfN))
		el := time.Since(tb)
		runs := stop()
		t.Logf("perf: %d writer(s) in-process full %s (×%.1f p50, ×%.1f p95)", writers, busy, ratio(busy.p50, idle.p50), ratio(busy.p95, idle.p95))
		t.Logf("perf: %d writer(s) in-process lean %s (%d runs in %s)", writers, busyLean, runs, el.Round(time.Millisecond))
	}
	stop := e.churn(e.others())
	time.Sleep(200 * time.Millisecond)
	sock, stopSock := perfSocket(t, e.c)
	busySock := summarise(timeDetail(t, sock, perfSelected, perfN))
	busySockLean := summarise(timeDetailOf(t, sock, perfSelected, true, perfN))
	stopSock()
	stop()
	t.Logf("perf: %d writers over the socket full %s", len(e.others()), busySock)
	t.Logf("perf: %d writers over the socket lean %s", len(e.others()), busySockLean)
}

// perfBursts is section 4: concurrent loads, as the pre-M5.2 TUI started
// one per event and cancelled none.
func perfBursts(t *testing.T, e *perfEnv) {
	t.Log("perf: == 4. concurrent Detail bursts (one load per event, none cancelled) ==")
	logBursts := func(state string) {
		for _, b := range []int{1, 10, 30} {
			last, all := burst(t, e.c, perfSelected, false, b)
			lastLean, allLean := burst(t, e.c, perfSelected, true, b)
			t.Logf("perf: burst %2d %s: full last-issued %s, all done %s | lean last-issued %s, all done %s",
				b, state, ms(last), ms(all), ms(lastLean), ms(allLean))
		}
	}
	logBursts("idle")
	stop := e.churn(e.others())
	time.Sleep(200 * time.Millisecond)
	logBursts("busy")
	stop()
}

// perfSnapshot measures Detail of the selected item while its own re-run
// is in flight (held in review), when the analysis parts come from the
// snapshot (M5.2).
func perfSnapshot(t *testing.T, e *perfEnv) {
	ctx := context.Background()
	t.Logf("perf: == S. Detail(%s) while its own re-run is in flight (stale-while-revalidate) ==", perfSelected)
	idle := summarise(timeDetail(t, e.c, perfSelected, perfN))
	idleLean := summarise(timeDetailOf(t, e.c, perfSelected, true, perfN))
	release := e.gate.arm()
	defer release()
	if err := e.c.Refresh(ctx, proto.ItemParams{ItemID: perfSelected, FromStage: model.StageReview, User: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.gate.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the re-run never reached review")
	}
	d, err := e.c.Detail(ctx, perfSelected)
	if err != nil {
		t.Fatal(err)
	}
	if d.Run == nil || !d.Run.Snapshot {
		t.Fatalf("no snapshot served during the re-run: run %+v", d.Run)
	}
	full := summarise(timeDetail(t, e.c, perfSelected, perfN))
	lean := summarise(timeDetailOf(t, e.c, perfSelected, true, perfN))
	sock, stopSock := perfSocket(t, e.c)
	sockLean := summarise(timeDetailOf(t, sock, perfSelected, true, perfN))
	stopSock()
	t.Logf("perf: idle (store):          full %s | lean %s", idle, idleLean)
	t.Logf("perf: during the re-run:     full %s | lean %s (Run.Snapshot, stage %s)", full, lean, d.Run.Stage)
	t.Logf("perf: during, over the socket lean %s", sockLean)
	release()
	e.c.wg.Wait()
	if d, err := e.c.Detail(ctx, perfSelected); err != nil || d.Run != nil {
		t.Fatalf("after the re-run: run %+v, %v", d.Run, err)
	}
}

// perfEvents is section 5: the events (and so pre-M5.2 TUI reloads) each
// trigger sends with the selected item open.
func perfEvents(t *testing.T, e *perfEnv) {
	ctx := context.Background()
	t.Logf("perf: == 5. events and TUI reloads per trigger (selected %s) ==", perfSelected)
	busyLat := summarise(timeDetail(t, e.c, perfSelected, 20)).p50
	rec := record(e.c)
	if err := e.c.Refresh(ctx, proto.ItemParams{ItemID: perfSelected, Force: true, User: true}); err != nil {
		t.Fatal(err)
	}
	rec.waitSettled(t, perfSelected)
	evs, at := rec.stop()
	logTrigger(t, "r (forced re-run) of the selected item", evs, at, busyLat)

	rec = record(e.c)
	e.c.analyse(job{itemID: perfSelected, aggOnly: true, seq: math.MaxInt32})
	time.Sleep(150 * time.Millisecond)
	evs, at = rec.stop()
	logTrigger(t, "aggregate-only run (override, gated re-aggregation)", evs, at, busyLat)

	rec = record(e.c)
	other := e.others()[0]
	if err := e.c.Refresh(ctx, proto.ItemParams{ItemID: other, Force: true, User: true}); err != nil {
		t.Fatal(err)
	}
	rec.waitSettled(t, other)
	evs, at = rec.stop()
	logTrigger(t, "r of another item ("+other+")", evs, at, busyLat)

	for _, step := range []struct {
		name    string
		advance time.Duration
	}{
		{"discovery pass, clock unchanged", 0},
		{"discovery pass, clock +60 s (one refresh tick)", 60 * time.Second},
		{"discovery pass, clock +1 h", time.Hour},
		{"discovery pass, clock +1 day", 24 * time.Hour},
	} {
		e.clock.advance(step.advance)
		rec = record(e.c)
		if err := e.c.Discover(ctx); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		evs, at = rec.stop()
		logTrigger(t, step.name, evs, at, busyLat)
	}
}

// perfSizes is section 6: the label query plan, the selected item's Labels
// read as the store grows, Details of PRs k times a fixture PR, and the
// label pruning.
func perfSizes(t *testing.T, e *perfEnv, largest string) {
	ctx := context.Background()
	st := e.deps.Store
	t.Log("perf: == 6. Detail of larger PRs (fixture item copied k times) ==")
	for _, n := range []int{1, 63, 2801} {
		t.Logf("perf: listDocs query plan, parent IN %d value(s): %s", n, labelsPlan(t, e.path, n))
	}
	logStoreLabels(t, e, "before the copies")
	var copies []string
	for _, k := range []int{10, 40} {
		id := scaled(t, st, largest, k)
		copies = append(copies, id)
		logStoreLabels(t, e, fmt.Sprintf("after the ×%d copy", k))
		d, size, _ := detailJSON(t, e.c, id, false)
		_, leanSize, _ := detailJSON(t, e.c, id, true)
		full := summarise(timeDetail(t, e.c, id, 20))
		lean := summarise(timeDetailOf(t, e.c, id, true, 20))
		one := summarise(timeLabels(t, e.c, id, []string{d.Units[0].ID}, 20))
		t.Logf("perf: ×%-3d files %4d units %5d labels %6d", k, len(d.Files), len(d.Units), len(d.Labels))
		t.Logf("perf:   full json %9s in-process %s", kb(size), full)
		t.Logf("perf:   lean json %9s in-process %s | Labels of one unit %s", kb(leanSize), lean, one)
		logSteps(t, fmt.Sprintf("×%d", k), medianSteps(t, st, id, 10))
		sock, stopSock := perfSocket(t, e.c)
		t.Logf("perf: ×%-3d over the socket full %s", k, summarise(timeDetail(t, sock, id, 20)))
		t.Logf("perf: ×%-3d over the socket lean %s", k, summarise(timeDetailOf(t, sock, id, true, 20)))
		stopSock()
		for _, b := range []int{10, 30} {
			last, all := burst(t, e.c, id, false, b)
			lastLean, allLean := burst(t, e.c, id, true, b)
			t.Logf("perf: ×%-3d burst %2d idle: full last-issued %s, all done %s | lean last-issued %s, all done %s",
				k, b, ms(last), ms(all), ms(lastLean), ms(allLean))
		}
		stop := e.churn(e.others())
		time.Sleep(200 * time.Millisecond)
		t.Logf("perf: ×%-3d with %d writers full %s | lean %s", k, len(e.others()),
			summarise(timeDetail(t, e.c, id, 20)), summarise(timeDetailOf(t, e.c, id, true, 20)))
		stop()
	}
	tt := time.Now()
	n, err := st.PruneLabels(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("perf: start-up label sweep over the grown store: %d pruned in %s", n, ms(time.Since(tt)))
	for i := len(copies) - 1; i >= 0; i-- {
		tt = time.Now()
		if err := e.c.Remove(ctx, copies[i]); err != nil {
			t.Fatal(err)
		}
		el := time.Since(tt)
		logStoreLabels(t, e, fmt.Sprintf("after removing %s (Remove with label pruning %s)", copies[i], ms(el)))
	}
}

// perfRunLog is section 7: run-log growth. Detail returns only the current
// run; finishing a run prunes the log to keepRuns runs.
func perfRunLog(t *testing.T, e *perfEnv) {
	ctx := context.Background()
	st := e.deps.Store
	rls, err := st.CurrentRunLogs(ctx, perfSelected)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("perf: == 7. run-log growth (%d rows per run; Detail returns the current run, finish keeps %d runs) ==", len(rls), keepRuns)
	added := 0
	for _, runs := range []int{100, 1000} {
		for ; added < runs; added++ {
			for _, rl := range rls {
				if err := st.AppendRunLog(ctx, rl); err != nil {
					t.Fatal(err)
				}
			}
		}
		_, size, _ := detailJSON(t, e.c, perfSelected, false)
		d, err := e.c.Detail(ctx, perfSelected)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("perf: +%4d runs unpruned: Detail run-log rows %d, json %9s | %s; current-run read %s", runs, len(d.RunLogs), kb(size),
			summarise(timeDetail(t, e.c, perfSelected, 20)), ms(stepTime(medianSteps(t, st, perfSelected, 10), "RunLogs")))
	}
	tt := time.Now()
	n, err := st.PruneRunLogs(ctx, perfSelected, keepRuns)
	if err != nil {
		t.Fatal(err)
	}
	left, err := st.RunLogs(ctx, perfSelected)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("perf: prune to %d runs: %d rows deleted in %s, %d left", keepRuns, n, ms(time.Since(tt)), len(left))
}

// logStoreLabels logs how many labels the store holds and the selected
// item's Detail and Labels read at that size.
func logStoreLabels(tb testing.TB, e *perfEnv, when string) {
	tb.Helper()
	ctx := context.Background()
	counts, err := e.deps.Store.LabelCounts(ctx)
	if err != nil {
		tb.Fatal(err)
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	labels := stepTime(medianSteps(tb, e.deps.Store, perfSelected, 10), "Labels")
	tb.Logf("perf: %s: %6d labels in the store → Detail(%s) %s, its Labels read %s",
		when, total, perfSelected, summarise(timeDetail(tb, e.c, perfSelected, 20)), ms(labels))
}

// labelsPlan is SQLite's plan for Store.Labels' query (listDocs: kind
// "label", parent IN n subjects; no ORDER BY since M5.2) on the store file.
func labelsPlan(tb testing.TB, path string, n int) string {
	tb.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	q := `EXPLAIN QUERY PLAN SELECT id, body FROM docs WHERE kind=? AND parent IN (` +
		strings.TrimSuffix(strings.Repeat("?,", n), ",") + `)`
	args := []any{"label"}
	for i := range n {
		args = append(args, "subject"+strconv.Itoa(i))
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		tb.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var parts []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			tb.Fatal(err)
		}
		parts = append(parts, detail)
	}
	if err := rows.Err(); err != nil {
		tb.Fatal(err)
	}
	return strings.Join(parts, "; ")
}

func ratio(a, b time.Duration) float64 {
	if b <= 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// logTrigger logs one trigger's events and what the pre-M5.2 TUI would do
// with them.
func logTrigger(tb testing.TB, name string, evs []proto.Event, at []time.Time, detailLat time.Duration) {
	tb.Helper()
	detail, list, byType := reloadCounts(evs, perfSelected)
	sel := selectedTimes(evs, at, perfSelected)
	span := time.Duration(0)
	if len(sel) > 1 {
		span = sel[len(sel)-1].Sub(sel[0])
	}
	tb.Logf("perf: %s: %d events (%d for the selected: %s) → %d full Detail loads, %d list reloads requested",
		name, len(evs), detail, fmtTypes(byType), detail, list)
	if detail > 1 {
		tb.Logf("perf:   selected-item events span %s, gap %s; %d of %d gaps shorter than one idle load (%s)",
			ms(span), gaps(sel), closer(sel, detailLat), detail-1, ms(detailLat))
	}
}

// ---- benchmarks ------------------------------------------------------------------

// BenchmarkDetail times Detail (full) and DetailOf with NoLabels (lean) of
// each analysed fixture item, Labels of one unit, and the selected item
// with the other items re-analysing and over the socket.
func BenchmarkDetail(b *testing.B) {
	e := newPerfEnv(b)
	ctx := context.Background()
	for _, id := range e.ids {
		b.Run("idle/"+id, func(b *testing.B) {
			for b.Loop() {
				if _, err := e.c.Detail(ctx, id); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("lean/"+id, func(b *testing.B) {
			for b.Loop() {
				if _, err := e.c.DetailOf(ctx, proto.DetailParams{ItemID: id, NoLabels: true}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	d, err := e.c.Detail(ctx, perfSelected)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("labels1/"+perfSelected, func(b *testing.B) {
		for b.Loop() {
			if _, err := e.c.Labels(ctx, proto.LabelsParams{ItemID: perfSelected, UnitIDs: []string{d.Units[0].ID}}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("busy/"+perfSelected, func(b *testing.B) {
		stop := e.churn(e.others())
		defer stop()
		time.Sleep(200 * time.Millisecond)
		for b.Loop() {
			if _, err := e.c.Detail(ctx, perfSelected); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("busy-lean/"+perfSelected, func(b *testing.B) {
		stop := e.churn(e.others())
		defer stop()
		time.Sleep(200 * time.Millisecond)
		for b.Loop() {
			if _, err := e.c.DetailOf(ctx, proto.DetailParams{ItemID: perfSelected, NoLabels: true}); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("socket/"+perfSelected, func(b *testing.B) {
		sock, stop := perfSocket(b, e.c)
		defer stop()
		for b.Loop() {
			if _, err := sock.Detail(ctx, perfSelected); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("socket-lean/"+perfSelected, func(b *testing.B) {
		sock, stop := perfSocket(b, e.c)
		defer stop()
		for b.Loop() {
			if _, err := sock.DetailOf(ctx, proto.DetailParams{ItemID: perfSelected, NoLabels: true}); err != nil {
				b.Fatal(err)
			}
		}
	})
}
