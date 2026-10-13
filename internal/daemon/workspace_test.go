package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
)

// The workspace state dir is under $XDG_STATE_HOME/prt, the same for equal
// roots however they are spelled, and distinct for different roots.
func TestWorkspaceStateDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	root := t.TempDir()
	a := WorkspaceStateDir(root)
	if filepath.Dir(a) != filepath.Join(state, "prt") || !strings.HasPrefix(filepath.Base(a), "ws-") || len(filepath.Base(a)) != len("ws-")+16 {
		t.Errorf("state dir %q, want %s/prt/ws-<16 hex>", a, state)
	}
	for _, same := range []string{root + "/", filepath.Join(root, ".", "x", ".."), root + "//"} {
		if got := WorkspaceStateDir(same); got != a {
			t.Errorf("WorkspaceStateDir(%q) = %q, want %q", same, got, a)
		}
	}
	if WorkspaceStateDir(filepath.Join(root, "other")) == a {
		t.Error("different roots share a state dir")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if WorkspaceStateDir(".") != WorkspaceStateDir(cwd) {
		t.Error("a relative root is not resolved against the working directory")
	}
	other := t.TempDir()
	t.Setenv("XDG_STATE_HOME", other)
	if got := WorkspaceStateDir(root); filepath.Base(got) != filepath.Base(a) || !strings.HasPrefix(got, other) {
		t.Errorf("after moving XDG_STATE_HOME: %q, want %s/prt/%s", got, other, filepath.Base(a))
	}
}

// The socket sits in the state dir when that path is short enough, else in
// $TMPDIR/prt-ws-<hash>/, which stays within the Unix socket limit for a
// short TMPDIR and still differs per root.
func TestWorkspaceSocketPath(t *testing.T) {
	t.Setenv("TMPDIR", "/tmp")
	t.Setenv("XDG_STATE_HOME", "/s")
	short := WorkspaceSocketPath("/w/root")
	if short != filepath.Join(WorkspaceStateDir("/w/root"), "prtd.sock") {
		t.Errorf("short state dir: socket %q", short)
	}
	t.Setenv("XDG_STATE_HOME", "/"+strings.Repeat("long-state-dir/", 8))
	for _, root := range []string{"/w/root", "/" + strings.Repeat("deep/", 40) + "root"} {
		p := WorkspaceSocketPath(root)
		if !strings.HasPrefix(p, "/tmp/prt-ws-") || len(p) > maxSocketPath {
			t.Errorf("long state dir: socket %q (%d bytes), want /tmp/prt-ws-…, ≤ %d", p, len(p), maxSocketPath)
		}
		if filepath.Base(filepath.Dir(p)) != "prt-"+filepath.Base(WorkspaceStateDir(root)) {
			t.Errorf("fallback dir of %q is %q, want it named after the state dir", root, p)
		}
	}
	if WorkspaceSocketPath("/w/root") == WorkspaceSocketPath("/w/other") {
		t.Error("different roots share a fallback socket")
	}
}

// shortTemp returns a fresh directory with a short path (t.TempDir() can be
// too long for a Unix socket on macOS).
func shortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ws")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// A daemon serves on WorkspaceSocketPath, and Socket.Workspace round-trips
// what InProcess.Workspace answers: repo info with the role fallback, the
// pool size and the spend.
func TestServeOnWorkspaceSocketPath(t *testing.T) {
	ctx := context.Background()
	tmp := shortTemp(t)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("XDG_STATE_HOME", tmp)
	root := shortTemp(t)
	path := WorkspaceSocketPath(root)
	if len(path) > maxSocketPath {
		t.Fatalf("socket path %q is %d bytes", path, len(path))
	}
	led := NewLedger()
	repo := proto.RepoInfo{Key: "svc", Label: "acme/svc", Root: filepath.Join(root, "svc"), GitDir: filepath.Join(root, "svc", ".git"),
		GHRepo: "acme/svc", Status: proto.RepoGitHub}
	c := newPooledClient(t, Options{Pool: NewPool(3), Ledger: led, Repo: repo}, func(d *pipeline.Deps) {
		d.Config.User.Role = "backend"
	})
	led.Add("svc", 0.5)
	led.Add("other", 0.25)

	srvCtx, cancel := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() { errc <- Serve(srvCtx, c, path) }()
	defer func() {
		cancel()
		if err := <-errc; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	s := dialRetry(t, path)
	defer func() { _ = s.Close() }()

	got, err := s.Workspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := c.Workspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("over the socket %+v, in process %+v", got, want)
	}
	repo.Role, repo.SpentUSD = "backend", 0.5
	if got.Multi || got.Root != repo.Root || got.Concurrency != 3 || got.SpentUSD != 0.75 ||
		len(got.Repos) != 1 || !reflect.DeepEqual(got.Repos[0], repo) {
		t.Errorf("workspace = %+v, want repo %+v, concurrency 3, spent 0.75", got, repo)
	}
}

// RepoInfo reports the role set with SetRepoInfo, else the live [user]
// role (which follows a settings change).
func TestRepoInfoRoleFallback(t *testing.T) {
	c := newPooledClient(t, Options{Repo: proto.RepoInfo{Key: "k", Label: "acme/k"}}, func(d *pipeline.Deps) {
		d.Config.User.Role = "backend"
	})
	if r := c.RepoInfo(); r.Role != "backend" || r.Key != "k" || r.Label != "acme/k" {
		t.Errorf("repo info %+v, want role backend from the config", r)
	}
	cfg := c.cfg()
	cfg.User.Role = "ops"
	c.p.SetConfig(cfg)
	if r := c.RepoInfo(); r.Role != "ops" {
		t.Errorf("role %q after a config change, want ops", r.Role)
	}
	c.SetRepoInfo(proto.RepoInfo{Key: "k", Role: "frontend", Status: proto.RepoNotGitHub})
	if r := c.RepoInfo(); r.Role != "frontend" || r.Status != proto.RepoNotGitHub || r.Label != "" {
		t.Errorf("repo info %+v after SetRepoInfo", r)
	}
	ws, err := c.Workspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ws.Repos[0].Role != "frontend" {
		t.Errorf("Workspace role %q, want frontend", ws.Repos[0].Role)
	}
}

// Quartile and SortItems are the rules Items uses.
func TestExportedRankingHelpers(t *testing.T) {
	if Quartile(10, 10) != 4 || Quartile(0, 10) != 0 || Quartile(1, 10) != 1 || Quartile(5, 0) != 0 {
		t.Error("Quartile does not match the urgency bar rule")
	}
	items := []model.Item{branchItem("low", 1), branchItem("high", 3)}
	SortItems(items)
	if items[0].Ref != "high" {
		t.Errorf("SortItems order %v", []string{items[0].Ref, items[1].Ref})
	}
}
