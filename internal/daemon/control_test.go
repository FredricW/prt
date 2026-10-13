package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/settings"
)

// stopClient is a client whose runs block in the review stage until
// stopped (recordRuns with block), one or more slots.
func stopClient(t *testing.T, slots int, names ...string) (*InProcess, *runRec) {
	t.Helper()
	st := newTestStore(t)
	for i, n := range names {
		put(t, st, branchItem(n, float64(len(names)-i)))
	}
	c := newClient(t, st, branchGit(names...), nil, func(d *pipeline.Deps) {
		d.Config.Pipeline.Concurrency = slots
	})
	rec := recordRuns(c)
	rec.setBlock(true)
	return c, rec
}

func TestStopItem(t *testing.T) {
	ctx := context.Background()
	c, rec := stopClient(t, 1, "a", "b")
	a, b := model.BranchItemID("a"), model.BranchItemID("b")
	if err := c.Analyse(ctx, false, a, b); err != nil {
		t.Fatal(err)
	}
	rec.waitStarted(t, a)
	// A rerun of a is waiting behind its run, b behind the single slot.
	c.q.push(job{itemID: a, rerun: true, explicit: true})
	res, err := c.Stop(ctx, proto.StopParams{ItemID: a})
	if err != nil || !reflect.DeepEqual(res.Stopped, []string{a}) {
		t.Fatalf("stop = %+v, %v", res, err)
	}
	rec.waitStarted(t, b) // the slot is free again
	if it := waitState(t, c.d.Store, a, model.StateStopped); it.StateNote != "review" {
		t.Errorf("stopped note %q, want the stage in flight (review)", it.StateNote)
	}
	if n := len(rec.of(a)); n != 1 {
		t.Errorf("a ran %d times; its queued rerun should have been dropped", n)
	}
	// Nothing to stop is not an error; an unknown item is.
	if res, err := c.Stop(ctx, proto.StopParams{ItemID: a}); err != nil || len(res.Stopped) != 0 {
		t.Errorf("second stop = %+v, %v", res, err)
	}
	if _, err := c.Stop(ctx, proto.StopParams{ItemID: "branch:missing"}); err == nil {
		t.Error("stop of an unknown item: want error")
	}
	if _, err := c.Stop(ctx, proto.StopParams{}); err == nil {
		t.Error("stop without an item: want error")
	}
	// Stopped items are not re-queued automatically.
	if _, err := c.Stop(ctx, proto.StopParams{All: true}); err != nil {
		t.Fatal(err)
	}
	waitState(t, c.d.Store, b, model.StateStopped)
	if err := c.AnalysePending(ctx); err != nil {
		t.Fatal(err)
	}
	c.q.push(job{itemID: a}, job{itemID: b}) // automatic follow-ups (reroute, override)
	c.wg.Wait()
	if na, nb := len(rec.of(a)), len(rec.of(b)); na != 1 || nb != 1 {
		t.Errorf("runs after stop: a %d, b %d; want no restart", na, nb)
	}
}

func TestStopAll(t *testing.T) {
	ctx := context.Background()
	c, rec := stopClient(t, 2, "a", "b", "q")
	a, b, q := model.BranchItemID("a"), model.BranchItemID("b"), model.BranchItemID("q")
	if err := c.Analyse(ctx, false, a, b, q); err != nil {
		t.Fatal(err)
	}
	rec.waitStarted(t, a, b)
	res, err := c.Stop(ctx, proto.StopParams{All: true})
	if err != nil || !reflect.DeepEqual(res.Stopped, []string{a, b, q}) {
		t.Fatalf("stop all = %+v, %v", res, err)
	}
	c.wg.Wait()
	for id, note := range map[string]string{a: "review", b: "review", q: stopQueued} {
		if it := stored(t, c.d.Store, id); it.State != model.StateStopped || it.StateNote != note {
			t.Errorf("%s: state %q note %q, want stopped at %s", id, it.State, it.StateNote, note)
		}
	}
	if n := len(rec.of(q)); n != 0 {
		t.Errorf("queued item ran %d times after stop all", n)
	}
	if got := c.strategy(); got != model.AutoAll {
		t.Errorf("stop all changed the strategy to %q", got)
	}
	if res, err := c.Stop(ctx, proto.StopParams{All: true}); err != nil || len(res.Stopped) != 0 {
		t.Errorf("stop all with nothing running = %+v, %v", res, err)
	}
}

// A job already taken off the queue but not yet registered as a run sees
// the stop through the queue's stop marks.
func TestStopBeforeRunRegisters(t *testing.T) {
	c, rec := stopClient(t, 1, "a")
	a := model.BranchItemID("a")
	j := job{itemID: a, explicit: true, seq: 1}
	c.q.seq = 1
	if _, err := c.Stop(context.Background(), proto.StopParams{ItemID: a}); err != nil {
		t.Fatal(err)
	}
	c.analyse(j)
	if it := stored(t, c.d.Store, a); it.State != model.StateStopped || it.StateNote != stopQueued {
		t.Errorf("state %q note %q, want stopped (queued)", it.State, it.StateNote)
	}
	if len(rec.of(a)) != 0 {
		t.Error("stopped job ran")
	}
}

// Removing an item cancels its run without a cause: an interruption, not a
// user stop, so the old reset applies.
func TestCancelWithoutCauseResets(t *testing.T) {
	ctx := context.Background()
	c, rec := stopClient(t, 1, "a")
	a := model.BranchItemID("a")
	if err := c.Analyse(ctx, false, a); err != nil {
		t.Fatal(err)
	}
	rec.waitStarted(t, a)
	c.cancelRun(a)
	c.wg.Wait()
	if it := stored(t, c.d.Store, a); it.State != "" {
		t.Errorf("state %q after an interruption, want reset", it.State)
	}
}

func TestResume(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mk := func(name string, state model.ItemState) model.Item {
		it := branchItem(name, 1)
		it.State, it.StateNote = state, "jev"
		return it
	}
	put(t, st, mk("s1", model.StateStopped), mk("s2", model.StateStopped), mk("na", model.StateNotAnalysed))
	c := newClient(t, st, branchGit("s1", "s2", "na"), nil, withStrategy(model.AutoManual))
	rec := recordRuns(c)

	if _, err := c.Resume(ctx, proto.ResumeParams{ItemID: "branch:missing"}); err == nil {
		t.Error("resume of an unknown item: want error")
	}
	res, err := c.Resume(ctx, proto.ResumeParams{ItemID: model.BranchItemID("na")})
	if err != nil || !reflect.DeepEqual(res.Resumed, []string{model.BranchItemID("na")}) {
		t.Fatalf("resume na = %+v, %v", res, err)
	}
	res, err = c.Resume(ctx, proto.ResumeParams{All: true})
	want := []string{model.BranchItemID("s1"), model.BranchItemID("s2")}
	slices.Sort(res.Resumed)
	if err != nil || !reflect.DeepEqual(res.Resumed, want) {
		t.Fatalf("resume all = %+v, %v; want %v", res, err, want)
	}
	c.wg.Wait()
	for _, id := range append(want, model.BranchItemID("na")) {
		if got := rec.onlyRun(t, id); got != (pipeline.RunOptions{}) {
			t.Errorf("%s resumed with %+v, want a plain explicit run", id, got)
		}
		if it := stored(t, st, id); it.State != model.StateReady || it.RequestedSHA != it.HeadSHA {
			t.Errorf("%s: state %q requested %q", id, it.State, it.RequestedSHA)
		}
	}

	// Resuming a running item is a no-op.
	rec.setBlock(true)
	if _, err := c.Resume(ctx, proto.ResumeParams{ItemID: model.BranchItemID("s1")}); err != nil {
		t.Fatal(err)
	}
	rec.waitStarted(t, want...) // drain the earlier starts
	rec.waitStarted(t, model.BranchItemID("s1"))
	res, err = c.Resume(ctx, proto.ResumeParams{ItemID: model.BranchItemID("s1")})
	if err != nil || len(res.Resumed) != 0 {
		t.Errorf("resume while running = %+v, %v", res, err)
	}
}

// blockingReviewer blocks every review call until its context ends, until
// released; then it answers like review.FakeRespond.
type blockingReviewer struct {
	name     string
	released atomic.Bool
	entered  chan struct{}
	once     sync.Once
	calls    atomic.Int64
}

func (r *blockingReviewer) Model() string { return r.name }

func (r *blockingReviewer) Review(ctx context.Context, req review.Request) (review.Result, error) {
	r.calls.Add(1)
	if !r.released.Load() && req.Task == review.TaskReview {
		r.once.Do(func() { close(r.entered) })
		<-ctx.Done()
		return review.Result{}, context.Cause(ctx)
	}
	return review.FakeRespond(0.9)(req)
}

// End to end (real pipeline): stopping during the review stage leaves the
// item stopped at review; resume re-runs it with the earlier stages served
// from their caches (no new Jev call). Fully green once the pipeline marks
// stopped rows (pipeline.ErrStopped handling).
func TestStopResumeEndToEnd(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, branchItem("x", 1))
	m := newCountingModels()
	br := &blockingReviewer{name: "fake:block", entered: make(chan struct{})}
	c := newClient(t, st, branchGit("x"), nil, func(d *pipeline.Deps) {
		m.deps(model.AutoManual)(d)
		d.Reviewers = review.Tiers{Fast: br, Deep: br}
	})
	id := model.BranchItemID("x")
	if err := c.Refresh(ctx, proto.ItemParams{ItemID: id, User: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-br.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("review never started")
	}
	if res, err := c.Stop(ctx, proto.StopParams{ItemID: id}); err != nil || len(res.Stopped) != 1 {
		t.Fatalf("stop = %+v, %v", res, err)
	}
	c.wg.Wait()
	it := waitState(t, st, id, model.StateStopped)
	if it.StateNote != string(model.StageReview) {
		t.Errorf("stopped at %q, want review", it.StateNote)
	}
	jevBefore := m.jevCalls()
	br.released.Store(true)
	if _, err := c.Resume(ctx, proto.ResumeParams{ItemID: id}); err != nil {
		t.Fatal(err)
	}
	c.wg.Wait()
	it = waitState(t, st, id, model.StateReady)
	if it.RequestedSHA != "h-x" {
		t.Errorf("requested %q after resume", it.RequestedSHA)
	}
	// Questions asked before the stop (labels, pairs) are cache hits; only
	// the stages after review (branch state) ask Jev for the first time.
	asked := map[string]bool{}
	for _, req := range m.jev.Calls[:jevBefore] {
		for q := range req.Questions {
			asked[q] = true
		}
	}
	if len(asked) == 0 {
		t.Fatal("no Jev call before the stop")
	}
	for _, req := range m.jev.Calls[jevBefore:] {
		for q := range req.Questions {
			if asked[q] {
				t.Errorf("Jev asked %q again on resume; want a cache hit", q)
			}
		}
	}
}

// fakeProber reports a fixed Ollama status.
type fakeProber struct{ p proto.Probe }

func (f fakeProber) Ollama(context.Context) proto.Probe { return f.p }

// settingsEnv is a client with a repo config dir and a global dir in temp
// dirs (XDG_CONFIG_HOME points at the global one's parent).
type settingsEnv struct {
	c         *InProcess
	repo, glb string
}

func newSettingsEnv(t *testing.T, ghc gh.Client, mutate func(*pipeline.Deps)) *settingsEnv {
	t.Helper()
	root := t.TempDir()
	xdg := filepath.Join(root, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	e := &settingsEnv{repo: config.RepoDir(filepath.Join(root, ".git")), glb: filepath.Join(xdg, "prt")}
	e.c = newClient(t, newTestStore(t), branchGit("x"), ghc, func(d *pipeline.Deps) {
		d.ConfigDirs = []string{e.repo, e.glb}
		if mutate != nil {
			mutate(d)
		}
	})
	return e
}

func TestSettingsInfo(t *testing.T) {
	ctx := context.Background()
	secrets := map[string]string{
		"JEV_API_KEY": "sk-jev-SECRET-1234", "PRT_ANTHROPIC_API_KEY": "sk-ant-SECRET-5678",
		"OLLAMA_HOST": "http://secret-host:1",
	}
	e := newSettingsEnv(t, nil, nil)
	e.c.SetGitHubNote("GitHub disabled (no token); local branches only")
	e.c.SetSettingsDeps(SettingsDeps{
		Getenv: func(k string) string { return secrets[k] },
		Prober: fakeProber{proto.Probe{OK: false, Note: "unreachable at 127.0.0.1:11434"}},
	})
	info, err := e.c.Settings(ctx, proto.SettingsParams{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range secrets {
		if strings.Contains(string(b), v) || strings.Contains(string(b), "SECRET") {
			t.Fatalf("settings info leaks an env value: %s", b)
		}
	}
	if info.RepoDir != e.repo || info.GlobalDir != e.glb {
		t.Errorf("dirs = %q / %q, want %q / %q", info.RepoDir, info.GlobalDir, e.repo, e.glb)
	}
	if info.RoutingPath != filepath.Join(e.repo, config.RoutingFile) || info.QuestionsPath != filepath.Join(e.repo, config.QuestionsFile) {
		t.Errorf("paths = %q / %q", info.RoutingPath, info.QuestionsPath)
	}
	if info.Source != config.Defaults().Source || info.RoutingSource != "builtin" {
		t.Errorf("sources = %q / %q", info.Source, info.RoutingSource)
	}
	if want := settingsValues(e.c); !reflect.DeepEqual(info.Values, want) {
		t.Errorf("values = %+v, want %+v", info.Values, want)
	}
	if info.Env.GitHub.OK || !strings.Contains(info.Env.GitHub.Note, "no token") {
		t.Errorf("github probe = %+v", info.Env.GitHub)
	}
	if info.Applied != "" {
		t.Errorf("Applied = %q on a read", info.Applied)
	}
	if _, err := e.c.Settings(ctx, proto.SettingsParams{Ensure: "bogus"}); err == nil {
		t.Error("unknown ensure: want error")
	}

	withGH := newSettingsEnv(t, &gh.Fake{Login: "me"}, nil)
	info, err = withGH.c.Settings(ctx, proto.SettingsParams{})
	if err != nil || !info.Env.GitHub.OK || info.Env.GitHub.Note != "gh auth token ok" {
		t.Errorf("github probe with a client = %+v, %v", info.Env.GitHub, err)
	}
}

// Needs settings.Env (settings work package): presence lines and the
// prober's answer.
func TestSettingsEnvPresence(t *testing.T) {
	e := newSettingsEnv(t, nil, nil)
	e.c.SetSettingsDeps(SettingsDeps{
		Getenv: func(k string) string {
			if k == "JEV_API_KEY" {
				return "sk-jev-SECRET"
			}
			return ""
		},
		Prober: fakeProber{proto.Probe{OK: true, Note: "ok"}},
	})
	info, err := e.c.Settings(context.Background(), proto.SettingsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if !info.Env.JevKey.OK || info.Env.AnthropicKey.OK || !info.Env.Ollama.OK {
		t.Errorf("env = %+v", info.Env)
	}
}

// settingsValues are the client's live settings.
func settingsValues(c *InProcess) proto.SettingsValues {
	return settings.FromConfig(c.cfg(), c.p.Routing().Config)
}

func TestSettingsEnsureRouting(t *testing.T) {
	e := newSettingsEnv(t, nil, nil)
	info, err := e.c.Settings(context.Background(), proto.SettingsParams{Ensure: "routing"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(info.RoutingPath); err != nil || filepath.Dir(info.RoutingPath) != e.repo {
		t.Errorf("routing file %q not created: %v", info.RoutingPath, err)
	}
}

// Needs settings.EnsureQuestions (settings work package).
func TestSettingsEnsureQuestions(t *testing.T) {
	e := newSettingsEnv(t, nil, nil)
	info, err := e.c.Settings(context.Background(), proto.SettingsParams{Ensure: "questions"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(info.QuestionsPath); err != nil || filepath.Dir(info.QuestionsPath) != e.repo {
		t.Errorf("questions file %q not created: %v", info.QuestionsPath, err)
	}
}

func TestSettingsWithoutConfigDirs(t *testing.T) {
	c := newClient(t, newTestStore(t), branchGit(), nil, nil)
	info, err := c.Settings(context.Background(), proto.SettingsParams{})
	if err != nil || info.RepoDir != "" || info.QuestionsPath != "" || info.RoutingPath != "" {
		t.Errorf("info = %+v, %v", info, err)
	}
	if _, err := c.Settings(context.Background(), proto.SettingsParams{Ensure: "questions"}); err == nil {
		t.Error("ensure questions without a config dir: want error")
	}
}

func TestSaveSettingsRejects(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, nil, nil)
	v := settingsValues(e.c)
	bad := v
	bad.KeepPairP = 2
	if _, err := e.c.SaveSettings(ctx, proto.SaveSettingsParams{Values: bad}); err == nil {
		t.Error("invalid values saved")
	}
	if _, err := e.c.SaveSettings(ctx, proto.SaveSettingsParams{Scope: "team", Values: v}); err == nil {
		t.Error("unknown scope: want error")
	}
	if _, err := os.Stat(filepath.Join(e.repo, config.MainFile)); err == nil {
		t.Error("a rejected save wrote prt.toml")
	}
}

// Needs settings.Validate and settings.Save (settings work package).
func TestSaveSettingsRepoAppliesLive(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, nil, nil)
	st := e.c.d.Store
	analysed := analysedItem(branchItem("x", 1))
	put(t, st, analysed)
	rec := recordRuns(e.c)
	drain(e.c.Events())
	v := settingsValues(e.c)
	v.AutoReview = string(model.AutoNeedsYou)
	v.Concurrency = 2
	v.Rank.WAge = 0.25
	info, err := e.c.SaveSettings(ctx, proto.SaveSettingsParams{Values: v})
	if err != nil {
		t.Fatal(err)
	}
	if info.Source != filepath.Join(e.repo, config.MainFile) || info.Values.Concurrency != 2 || info.Values.Rank.WAge != 0.25 {
		t.Errorf("info after save = %+v", info)
	}
	if e.c.q.slotCount() != 2 || e.c.cfg().Rank.WAge != 0.25 || e.c.strategy() != model.AutoNeedsYou {
		t.Errorf("not applied live: slots %d, w_age %v, strategy %q", e.c.q.slotCount(), e.c.cfg().Rank.WAge, e.c.strategy())
	}
	for _, want := range []string{"concurrency 2", "re-aggregated 1 item"} {
		if !strings.Contains(info.Applied, want) {
			t.Errorf("Applied = %q, want %q", info.Applied, want)
		}
	}
	e.c.wg.Wait()
	if got := rec.onlyRun(t, analysed.ID); got != (pipeline.RunOptions{AggregateOnly: true, FreeOnly: true}) {
		t.Errorf("weight change ran %+v, want aggregate-only free-only", got)
	}
	if !hasEvent(drain(e.c.Events()), proto.EventSettings, "") {
		t.Error("no EventSettings")
	}
}

// Needs settings.Validate and settings.Save: a strategy change queues the
// newly covered not-analysed items.
func TestSaveSettingsStrategyQueuesCovered(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, nil, func(d *pipeline.Deps) { d.Config.Pipeline.AutoReview = string(model.AutoNeedsYou) })
	na := branchItem("x", 1)
	na.State = model.StateNotAnalysed
	put(t, e.c.d.Store, na)
	rec := recordRuns(e.c)
	v := settingsValues(e.c)
	v.AutoReview = string(model.AutoAll)
	info, err := e.c.SaveSettings(ctx, proto.SaveSettingsParams{Values: v})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info.Applied, "1 item queued") {
		t.Errorf("Applied = %q", info.Applied)
	}
	e.c.wg.Wait()
	if got := rec.onlyRun(t, na.ID); got.FreeOnly {
		t.Errorf("newly covered item ran %+v", got)
	}
}

// Needs settings.Validate and settings.Save: a global save under a repo
// prt.toml warns that the repo file shadows it.
func TestSaveSettingsGlobalShadowed(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, nil, nil)
	e.c.SetSettingsDeps(SettingsDeps{GlobalDir: e.glb})
	writeFile(t, e.repo, config.MainFile, "[user]\nrole = \"frontend\"\n")
	v := settingsValues(e.c)
	v.Role = "backend"
	info, err := e.c.SaveSettings(ctx, proto.SaveSettingsParams{Scope: proto.ScopeGlobal, Values: v})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.glb, config.MainFile)); err != nil {
		t.Errorf("global prt.toml not written: %v", err)
	}
	found := false
	for _, w := range info.Warnings {
		if strings.Contains(w, "global prt.toml is shadowed by "+filepath.Join(e.repo, config.MainFile)) {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %q, want the shadow warning", info.Warnings)
	}
}

// Without a model factory a model change is saved but applies after a
// restart (with a warning); with one, the new clients are installed.
func TestSaveSettingsModels(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, nil, nil)
	v := settingsValues(e.c)
	v.Fast = "anthropic:claude-haiku-9"
	info, err := e.c.SaveSettings(ctx, proto.SaveSettingsParams{Values: v})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(info.Warnings, "model changes apply after restart") {
		t.Errorf("warnings = %q", info.Warnings)
	}
	built := 0
	fast := &review.Fake{Name: "fake:new"}
	e.c.SetSettingsDeps(SettingsDeps{Models: func(cfg config.Config) (pipeline.Models, []string) {
		built++
		return pipeline.Models{Reviewers: review.Tiers{Fast: fast}}, []string{"deep review tier disabled"}
	}})
	v.Fast = "anthropic:claude-haiku-10"
	info, err = e.c.SaveSettings(ctx, proto.SaveSettingsParams{Values: v})
	if err != nil {
		t.Fatal(err)
	}
	if built != 1 || e.c.p.CurrentModels().Reviewers.Fast != fast || !strings.Contains(info.Applied, "models") {
		t.Errorf("factory calls %d, applied %q", built, info.Applied)
	}
	// The factory's notes (a tier it disabled) reach the page.
	if !slices.Contains(info.Warnings, "deep review tier disabled") {
		t.Errorf("warnings = %q, want the factory note", info.Warnings)
	}
}

// Growing the slots starts waiting jobs; shrinking never kills a run.
func TestQueueSetSlots(t *testing.T) {
	var wg sync.WaitGroup
	release := make(chan struct{})
	var running atomic.Int64
	started := make(chan string, 8)
	q := newQueue(1, &wg, func(j job) {
		running.Add(1)
		started <- j.itemID
		<-release
		running.Add(-1)
	})
	q.push(job{itemID: "a"}, job{itemID: "b"}, job{itemID: "c"})
	<-started
	q.setSlots(3)
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("growing the slots did not start the waiting jobs")
		}
	}
	q.setSlots(1)
	if n := running.Load(); n != 3 {
		t.Errorf("running = %d after shrinking, want 3 (no run killed)", n)
	}
	close(release)
	wg.Wait()
	if q.slotCount() != 1 {
		t.Errorf("slots = %d", q.slotCount())
	}
}

// Stop, Resume, Settings and SaveSettings answer the same over the socket.
func TestSocketControlMethods(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, nil, nil)
	put(t, e.c.d.Store, branchItem("x", 1))
	rec := recordRuns(e.c)
	rec.setBlock(true)
	s, _, _ := startServer(t, e.c)
	id := model.BranchItemID("x")

	if err := s.Refresh(ctx, proto.ItemParams{ItemID: id, User: true}); err != nil {
		t.Fatal(err)
	}
	rec.waitStarted(t, id)
	res, err := s.Stop(ctx, proto.StopParams{ItemID: id})
	if err != nil || !reflect.DeepEqual(res.Stopped, []string{id}) {
		t.Fatalf("stop over the socket = %+v, %v", res, err)
	}
	waitState(t, e.c.d.Store, id, model.StateStopped)
	if _, err := s.Stop(ctx, proto.StopParams{ItemID: "branch:missing"}); err == nil {
		t.Error("stop of an unknown item over the socket: want error")
	}
	rec.setBlock(false)
	rr, err := s.Resume(ctx, proto.ResumeParams{All: true})
	if err != nil || !reflect.DeepEqual(rr.Resumed, []string{id}) {
		t.Fatalf("resume over the socket = %+v, %v", rr, err)
	}
	waitState(t, e.c.d.Store, id, model.StateReady)

	viaSocket, err := s.Settings(ctx, proto.SettingsParams{})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := e.c.Settings(ctx, proto.SettingsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normInfo(viaSocket), normInfo(direct)) {
		t.Errorf("settings over the socket = %+v\nin process = %+v", viaSocket, direct)
	}
	if _, err := s.SaveSettings(ctx, proto.SaveSettingsParams{Scope: "team", Values: direct.Values}); err == nil {
		t.Error("save with an unknown scope over the socket: want error")
	}
}

// normInfo makes nil and empty slices compare equal (JSON round trip).
func normInfo(i proto.SettingsInfo) proto.SettingsInfo {
	if len(i.Warnings) == 0 {
		i.Warnings = nil
	}
	if len(i.Values.Owns) == 0 {
		i.Values.Owns = nil
	}
	return i
}

// Needs settings.Validate and settings.Save: a save over the socket
// returns the reloaded settings and an EventSettings reaches subscribers.
func TestSocketSaveSettings(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, nil, nil)
	s, _, _ := startServer(t, e.c)
	v := settingsValues(e.c)
	v.Concurrency = 3
	info, err := s.SaveSettings(ctx, proto.SaveSettingsParams{Values: v})
	if err != nil {
		t.Fatal(err)
	}
	if info.Values.Concurrency != 3 || !strings.Contains(info.Applied, "concurrency 3") {
		t.Errorf("save over the socket = %+v", info)
	}
	waitEvent(t, s.Events(), func(ev proto.Event) bool { return ev.Type == proto.EventSettings })
}
