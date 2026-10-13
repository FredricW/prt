package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
)

// gatedRun replaces a client's pipeline run: the first run blocks until
// release is closed (also after its context ends, so a stopped run is still
// unwinding meanwhile); later runs return at once. It records every run's
// options.
type gatedRun struct {
	mu      sync.Mutex
	runs    []pipeline.RunOptions
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

func gateRuns(c *InProcess) *gatedRun {
	g := &gatedRun{release: make(chan struct{}), started: make(chan struct{})}
	c.run = func(ctx context.Context, it model.Item, opt pipeline.RunOptions) (string, error) {
		g.mu.Lock()
		g.runs = append(g.runs, opt)
		first := len(g.runs) == 1
		g.mu.Unlock()
		if !first {
			return it.HeadSHA, nil
		}
		g.once.Do(func() { close(g.started) })
		<-g.release
		if err := context.Cause(ctx); err != nil {
			return "", fmt.Errorf("run: %w", err)
		}
		return it.HeadSHA, nil
	}
	return g
}

func (g *gatedRun) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-g.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first run not started")
	}
}

func (g *gatedRun) all() []pipeline.RunOptions {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.runs)
}

// An explicit request for an item whose automatic (gated, FreeOnly) run is
// in flight runs after it: Add during a tab preview, :resume during an
// automatic run, and :resume while a stopped run is still unwinding. No
// discovery tick or AnalysePending is needed (--no-daemon never has one).
func TestExplicitRequestNotCoveredByAutomaticRun(t *testing.T) {
	ctx := context.Background()
	id := model.BranchItemID("x")
	cases := []struct {
		name  string
		start func(c *InProcess) error
		then  func(c *InProcess) error
	}{
		{"add during a tab preview", func(c *InProcess) error {
			_, err := c.Preview(ctx, "x")
			return err
		}, func(c *InProcess) error {
			_, err := c.Add(ctx, "x")
			return err
		}},
		{"resume during an automatic run", func(c *InProcess) error {
			return c.Refresh(ctx, proto.ItemParams{ItemID: id})
		}, func(c *InProcess) error {
			res, err := c.Resume(ctx, proto.ResumeParams{ItemID: id})
			if err == nil && !slices.Contains(res.Resumed, id) {
				err = fmt.Errorf("resume reported %v", res.Resumed)
			}
			return err
		}},
		{"resume right after a stop", func(c *InProcess) error {
			return c.Refresh(ctx, proto.ItemParams{ItemID: id, User: true})
		}, func(c *InProcess) error {
			if _, err := c.Stop(ctx, proto.StopParams{ItemID: id}); err != nil {
				return err
			}
			res, err := c.Resume(ctx, proto.ResumeParams{ItemID: id})
			if err == nil && !slices.Contains(res.Resumed, id) {
				err = fmt.Errorf("resume reported %v", res.Resumed)
			}
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			if !strings.HasPrefix(tc.name, "add") {
				put(t, st, branchItem("x", 1))
			}
			c := newClient(t, st, branchGit("x"), nil, withStrategy(model.AutoNeedsYou))
			g := gateRuns(c)
			if err := tc.start(c); err != nil {
				t.Fatal(err)
			}
			g.waitStarted(t)
			if err := tc.then(c); err != nil {
				t.Fatal(err)
			}
			close(g.release)
			c.wg.Wait()
			runs := g.all()
			if len(runs) != 2 || runs[1].FreeOnly {
				t.Fatalf("runs = %+v, want a second, model-backed run", runs)
			}
			it := stored(t, st, id)
			if it.State != model.StateReady || it.AnalysedSHA != "h-x" || it.RequestedSHA != "h-x" {
				t.Errorf("item state %q analysed %q requested %q, want ready at h-x", it.State, it.AnalysedSHA, it.RequestedSHA)
			}
		})
	}
}

// :resume while an explicit run of the item is going is still a no-op.
func TestResumeCoveredByExplicitRun(t *testing.T) {
	ctx := context.Background()
	id := model.BranchItemID("x")
	st := newTestStore(t)
	put(t, st, branchItem("x", 1))
	c := newClient(t, st, branchGit("x"), nil, withStrategy(model.AutoNeedsYou))
	g := gateRuns(c)
	if err := c.Refresh(ctx, proto.ItemParams{ItemID: id, User: true}); err != nil {
		t.Fatal(err)
	}
	g.waitStarted(t)
	res, err := c.Resume(ctx, proto.ResumeParams{ItemID: id})
	if err != nil || len(res.Resumed) != 0 {
		t.Errorf("resume = %+v, %v; want nothing resumed", res, err)
	}
	close(g.release)
	c.wg.Wait()
	if runs := g.all(); len(runs) != 1 {
		t.Errorf("runs = %+v, want one", runs)
	}
}

// An automatic run of a Stale item outside the strategy only re-aggregates
// the stored analysis: the files and units of the analysed head are never
// replaced by the new head's (real pipeline, no model call).
func TestGatedStaleRunKeepsStoredAnalysis(t *testing.T) {
	ctx := context.Background()
	id := model.BranchItemID("x")
	st := analysedStore(t, nil, "x")
	before, err := st.Units(ctx, id)
	if err != nil || len(before) == 0 {
		t.Fatalf("units before: %v, %v", before, err)
	}
	beforeFiles, err := st.Files(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	it := stored(t, st, id)
	it.HeadSHA, it.State, it.StateNote = "h-x2", model.StateStale, staleNote
	put(t, st, it)
	g := branchGit("x")
	g.RefList[0].SHA = "h-x2"
	g.MergeBases["main..h-x2"] = "b1"
	g.Diffs["b1..h-x2"] = oneLineDiff("other.txt")
	w := &tripwire{t: t}
	c := newClient(t, st, g, nil, tripDeps(w, model.AutoNeedsYou))
	c.q.push(job{itemID: id, rerun: true, keepReviewed: true}) // as reroute queues it
	c.wg.Wait()
	after, err := st.Units(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	afterFiles, err := st.Files(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !sameUnits(before, after) || len(afterFiles) != len(beforeFiles) || (len(afterFiles) > 0 && afterFiles[0].Path != beforeFiles[0].Path) {
		t.Errorf("stored analysis replaced: units %+v -> %+v, files %+v -> %+v", before, after, beforeFiles, afterFiles)
	}
	got := stored(t, st, id)
	if got.State != model.StateStale || got.AnalysedSHA != "h-x" || got.RequestedSHA != "" {
		t.Errorf("item state %q analysed %q requested %q, want stale at h-x", got.State, got.AnalysedSHA, got.RequestedSHA)
	}
}

func sameUnits(a, b []model.ChangeUnit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Path != b[i].Path {
			return false
		}
	}
	return true
}

// A stop that arrives after the run returned successfully (before finish)
// does not throw the finished analysis away.
func TestFinishIgnoresLateStop(t *testing.T) {
	st := newTestStore(t)
	put(t, st, branchItem("x", 1))
	c := newClient(t, st, branchGit("x"), nil, nil)
	id := model.BranchItemID("x")
	runCtx, cancel := context.WithCancelCause(context.Background())
	cancel(pipeline.ErrStopped)
	c.finish(runCtx, id, outcome{head: "h-x", explicit: true, stage: model.StageAggregate})
	it := stored(t, st, id)
	if it.State != model.StateReady || it.AnalysedSHA != "h-x" || it.RequestedSHA != "h-x" {
		t.Errorf("item state %q (%q) analysed %q requested %q, want ready at h-x", it.State, it.StateNote, it.AnalysedSHA, it.RequestedSHA)
	}
	c.finish(runCtx, id, outcome{err: fmt.Errorf("jev: %w", context.Cause(runCtx)), stage: model.StageJev})
	if it := stored(t, st, id); it.State != model.StateStopped {
		t.Errorf("a stopped run that failed: state %q, want stopped", it.State)
	}
}

// A skip on a Stale item does not request (or pay for) an analysis of its
// new head; on an item analysed at its head it stays explicit.
func TestSkipOnStaleItemIsAutomatic(t *testing.T) {
	ctx := context.Background()
	id := model.BranchItemID("x")
	for _, tc := range []struct {
		name     string
		analysed string
		explicit bool
	}{
		{"stale", "h-old", false},
		{"analysed at head", "h-x", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newTestStore(t)
			it := branchItem("x", 1)
			it.AnalysedSHA, it.State = tc.analysed, model.StateReady
			if tc.analysed != it.HeadSHA {
				it.State = model.StateStale
			}
			put(t, st, it)
			c := newClient(t, st, branchGit("x"), nil, withStrategy(model.AutoNeedsYou))
			rec := recordRuns(c)
			if err := c.ReviewAction(ctx, proto.ReviewActionParams{ItemID: id, GroupID: "g1", Action: proto.ActionSkip}); err != nil {
				t.Fatal(err)
			}
			c.wg.Wait()
			got := rec.onlyRun(t, id)
			if got.FreeOnly == tc.explicit {
				t.Errorf("run %+v, explicit %v", got, tc.explicit)
			}
			if req := stored(t, st, id).RequestedSHA; (req == "h-x") != tc.explicit {
				t.Errorf("RequestedSHA = %q", req)
			}
		})
	}
}

// A repo save when only a global prt.toml exists starts the repo file as a
// copy of the global one, so its other keys keep applying.
func TestSaveSettingsRepoSeedsFromGlobal(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, nil, func(d *pipeline.Deps) {
		glb := d.ConfigDirs[1]
		writeFile(t, glb, config.MainFile, "[github]\nrelease_branches = [\"rel/*\"]\n\n[keys]\nquit = \"Q\"\n\n[pipeline]\nline_batch = 7\n")
		cfg, err := config.Load(d.ConfigDirs)
		if err != nil {
			t.Fatal(err)
		}
		d.Config = cfg
	})
	v := settingsValues(e.c)
	v.AutoReview = string(model.AutoManual) // what :auto manual saves
	if _, err := e.c.SaveSettings(ctx, proto.SaveSettingsParams{Scope: proto.ScopeRepo, Values: v}); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load([]string{e.repo, e.glb})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Source != filepath.Join(e.repo, config.MainFile) {
		t.Fatalf("source %q, want the repo file", cfg.Source)
	}
	if cfg.Keys["quit"] != "Q" || !slices.Equal(cfg.GitHub.ReleaseBranches, []string{"rel/*"}) || cfg.Pipeline.LineBatch != 7 {
		t.Errorf("global keys lost: keys %v release %v line_batch %d", cfg.Keys, cfg.GitHub.ReleaseBranches, cfg.Pipeline.LineBatch)
	}
	if cfg.Pipeline.Strategy() != model.AutoManual {
		t.Errorf("strategy %q not saved", cfg.Pipeline.AutoReview)
	}
	if got := e.c.cfg(); !slices.Equal(got.GitHub.ReleaseBranches, []string{"rel/*"}) {
		t.Errorf("live config release branches %v", got.GitHub.ReleaseBranches)
	}
}

// A global save is refused when the global dir is not read (replay mode
// reads only .git/prt), so the user's real config is never written.
func TestSaveSettingsGlobalRefusedWhenNotRead(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	glb := filepath.Join(root, "global")
	c := newClient(t, newTestStore(t), branchGit(), nil, func(d *pipeline.Deps) { d.ConfigDirs = []string{repo} })
	c.SetSettingsDeps(SettingsDeps{GlobalDir: glb})
	_, err := c.SaveSettings(ctx, proto.SaveSettingsParams{Scope: proto.ScopeGlobal, Values: settingsValues(c)})
	if err == nil || !strings.Contains(err.Error(), "not read") {
		t.Errorf("global save in replay mode: err %v", err)
	}
	if _, err := os.Stat(filepath.Join(glb, config.MainFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("global prt.toml written: %v", err)
	}
}

// With a GitHub client from a fixture, the settings page says so instead
// of claiming gh auth is fine.
func TestSettingsGitHubFixtureNote(t *testing.T) {
	e := newSettingsEnv(t, &gh.Fake{Login: "me"}, nil)
	e.c.SetGitHubNote("GitHub fixture prs.json (gh auth not checked)")
	info, err := e.c.Settings(context.Background(), proto.SettingsParams{})
	if err != nil || !info.Env.GitHub.OK || !strings.Contains(info.Env.GitHub.Note, "not checked") {
		t.Errorf("github probe = %+v, %v", info.Env.GitHub, err)
	}
}
