package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
)

// allTOML keeps the test clients' strategy ("all") in a prt.toml, so a
// reload sees no strategy change.
const allTOML = "[pipeline]\nauto_review = \"all\"\n"

// A changed [rank] weight in the repo prt.toml is applied by a reload: the
// analysed item is re-aggregated, Applied says so and an EventSettings
// follows. Nothing is written.
func TestReloadSettingsAppliesChangedWeights(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, nil, nil)
	analysed := analysedItem(branchItem("x", 1))
	put(t, e.c.d.Store, analysed)
	rec := recordRuns(e.c)
	path := writeFile(t, e.repo, config.MainFile, allTOML+"[rank]\nw_age = 0.375\n")
	before := readFile(t, path)
	drain(e.c.Events())
	info, err := e.c.ReloadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info.Applied, "re-aggregated 1 item") || e.c.cfg().Rank.WAge != 0.375 || info.Values.Rank.WAge != 0.375 {
		t.Errorf("Applied %q, w_age %v", info.Applied, e.c.cfg().Rank.WAge)
	}
	if info.Source != path {
		t.Errorf("Source = %q, want %q", info.Source, path)
	}
	e.c.wg.Wait()
	if got := rec.onlyRun(t, analysed.ID); !got.AggregateOnly {
		t.Errorf("weight change ran %+v, want aggregate-only", got)
	}
	if !hasEvent(drain(e.c.Events()), proto.EventSettings, "") {
		t.Error("no EventSettings")
	}
	if readFile(t, path) != before {
		t.Error("ReloadSettings rewrote prt.toml")
	}
	if _, err := os.Stat(filepath.Join(e.repo, config.RoutingFile)); err == nil {
		t.Error("ReloadSettings wrote a routing file")
	}
}

// Unchanged files (or none, for a client loaded from the defaults) change
// nothing; a client without config dirs has nothing to reload.
func TestReloadSettingsNothingChanged(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		setup func(t *testing.T) *InProcess
	}{
		{"same values in prt.toml", func(t *testing.T) *InProcess {
			e := newSettingsEnv(t, nil, nil)
			writeFile(t, e.repo, config.MainFile, allTOML)
			return e.c
		}},
		{"no files, default config", func(t *testing.T) *InProcess {
			return newSettingsEnv(t, nil, func(d *pipeline.Deps) { d.Config = config.Defaults() }).c
		}},
		{"no config dirs", func(t *testing.T) *InProcess {
			return newClient(t, newTestStore(t), branchGit(), nil, nil)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.setup(t)
			for range 2 {
				info, err := c.ReloadSettings(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if info.Applied != nothingChanged {
					t.Errorf("Applied = %q, want %q", info.Applied, nothingChanged)
				}
			}
		})
	}
}

// A file that does not parse is an error and the live settings stay.
func TestReloadSettingsSyntaxErrorKeepsOld(t *testing.T) {
	ctx := context.Background()
	e := newSettingsEnv(t, nil, nil)
	old := e.c.cfg().Rank.WAge
	writeFile(t, e.repo, config.MainFile, "[rank\nw_age = 0.375\n")
	drain(e.c.Events())
	if _, err := e.c.ReloadSettings(ctx); err == nil || !strings.Contains(err.Error(), "previous settings kept") {
		t.Fatalf("err = %v, want a reload error", err)
	}
	if e.c.cfg().Rank.WAge != old {
		t.Errorf("w_age %v after a failed reload, want %v", e.c.cfg().Rank.WAge, old)
	}
	if hasEvent(drain(e.c.Events()), proto.EventSettings, "") {
		t.Error("a failed reload sent EventSettings")
	}
	writeFile(t, e.repo, config.RoutingFile, "cost_cap_usd = \n")
	writeFile(t, e.repo, config.MainFile, allTOML)
	if _, err := e.c.ReloadSettings(ctx); err == nil || !strings.Contains(err.Error(), "prt.routing.toml") {
		t.Fatalf("err = %v, want a routing reload error", err)
	}
}

// A concurrency in the file of a pooled client resizes the shared pool.
func TestReloadSettingsConcurrencyResizesPool(t *testing.T) {
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
	writeFile(t, repo, config.MainFile, allTOML+"concurrency = 5\n")
	info, err := a.ReloadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := b.Workspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info.Applied, "concurrency 5") || pool.Slots() != 5 || ws.Concurrency != 5 {
		t.Errorf("Applied %q, pool %d, B's concurrency %d; want 5", info.Applied, pool.Slots(), ws.Concurrency)
	}
}

// A global save made through one repo of a workspace is applied to another
// repo by its ReloadSettings.
func TestReloadSettingsAppliesGlobalSaveOfAnotherRepo(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	xdg := filepath.Join(root, "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	glb := filepath.Join(xdg, "prt")
	led, pool := NewLedger(), NewPool(2)
	client := func(name string) *InProcess {
		repo := config.RepoDir(filepath.Join(root, name, ".git"))
		return newPooledClient(t, Options{Pool: pool, Ledger: led, Repo: proto.RepoInfo{Key: name}}, func(d *pipeline.Deps) {
			d.ConfigDirs = []string{repo, glb}
		})
	}
	a, b := client("a"), client("b")
	v := settingsValues(a)
	v.Rank.WAge = 0.375
	if _, err := a.SaveSettings(ctx, proto.SaveSettingsParams{Scope: proto.ScopeGlobal, Values: v}); err != nil {
		t.Fatal(err)
	}
	if b.cfg().Rank.WAge == 0.375 {
		t.Fatal("B changed before its reload")
	}
	drain(b.Events())
	info, err := b.ReloadSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b.cfg().Rank.WAge != 0.375 || info.Source != filepath.Join(glb, config.MainFile) {
		t.Errorf("B after reload: w_age %v, source %q", b.cfg().Rank.WAge, info.Source)
	}
	if !hasEvent(drain(b.Events()), proto.EventSettings, "") {
		t.Error("no EventSettings from B")
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
