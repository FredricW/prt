package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/log"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/testutil"
	"github.com/fredricw/prt/internal/workspace"
)

// The generated fixture workspace holds exactly two repositories at the
// default depth: portal and billing-svc. Discovery skips the node_modules
// and vendor decoys, the depth-3 decoy under archive/, the plain notes/
// directory and the links/loop symlink back to the root.
func TestFixtureWorkspaceDiscovery(t *testing.T) {
	root := testutil.FixtureWorkspace(t)
	res, err := workspace.Discover(root, workspace.Options{Depth: config.DefaultWorkspaceDepth})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range res.Repos {
		keys = append(keys, r.Key)
	}
	if !slices.Equal(keys, []string{"billing-svc", "portal"}) {
		t.Fatalf("repos = %v (notes %q), want billing-svc and portal", keys, res.Notes)
	}
	if !slices.ContainsFunc(res.Notes, func(n string) bool { return strings.Contains(n, "symlink loop at links/loop") }) {
		t.Errorf("notes = %q, want the links/loop symlink loop noted", res.Notes)
	}
	// One level deeper the archive decoy is a repository too; node_modules
	// and vendor never are.
	deep, err := workspace.Discover(root, workspace.Options{Depth: 3})
	if err != nil {
		t.Fatal(err)
	}
	keys = keys[:0]
	for _, r := range deep.Repos {
		keys = append(keys, r.Key)
	}
	if !slices.Equal(keys, []string{"archive/2019/legacy-app", "billing-svc", "portal"}) {
		t.Errorf("depth 3 repos = %v", keys)
	}
}

// `prt --root` renders the workspace snapshot from the fixture manifest:
// both repos grouped in one picker with prefixed ids, then the folded and
// filtered workspace screens; --repo on a directory outside any repository
// does the same.
func TestWorkspaceSnapshot(t *testing.T) {
	isolate(t)
	root := testutil.FixtureWorkspace(t)
	for _, flag := range []string{"--root", "--repo"} {
		code, out, errOut := runPrt("--offline", "--snapshot", "--now", "2026-10-13T12:00:00Z",
			flag, root, "--fixtures", testutil.FixtureWorkspaceManifest(t), "--max-items", "1")
		if code != 0 {
			t.Fatalf("%s: code %d err %q", flag, code, errOut)
		}
		// Without recordings labels are unavailable, so the order inside a
		// repo is git facts only; the temp root is too long for the header's
		// counts, which the tui tests cover.
		for _, want := range []string{
			"▾ acme/portal 6", "▾ acme/billing-svc 3",
			"=== screen: picker + overview · portal::pr:",
			"=== screen: picker + overview · billing-svc::pr:7 ===",
			"=== screen: workspace · folded ===",
			"=== screen: workspace · filter portal ===",
			"labels unavailable",
		} {
			if !strings.Contains(out, want) {
				first, _, _ := strings.Cut(strings.TrimPrefix(out, "=== screen:"), "=== screen:")
				t.Errorf("%s: snapshot lacks %q; first screen:\n%s", flag, want, first)
			}
		}
		if strings.Contains(out, "left-pad") || strings.Contains(out, "old-lib") || strings.Contains(out, "legacy-app") {
			t.Errorf("%s: a decoy repository was served", flag)
		}
	}
}

func TestWorkspaceFlags(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--root", dir, "--repo", dir}, "--root and --repo"},
		{[]string{"--root", dir, "--depth", "-1"}, "--depth"},
		{[]string{"--root", dir, "--drafts", "d.json"}, "--drafts needs a single repository"},
	} {
		if code, _, errOut := runPrt(append([]string{"--offline", "--snapshot"}, tc.args...)...); code != 2 || !strings.Contains(errOut, tc.want) {
			t.Errorf("%v: code %d err %q, want usage error %q", tc.args, code, errOut, tc.want)
		}
	}
	if code, _, errOut := runPrt("--offline", "--snapshot", "--root", dir, "--depth", "3"); code != 1 || !strings.Contains(errOut, "no git repositories under") || !strings.Contains(errOut, "(depth 3)") {
		t.Errorf("empty root: code %d err %q", code, errOut)
	}
	// In a workspace --fixtures names a manifest, not one repo's prs.json.
	gitRepoAt(t, filepath.Join(dir, "a"), "")
	if code, _, errOut := runPrt("--offline", "--snapshot", "--root", dir, "--fixtures", testutil.FixturePRs(t)); code != 1 || !strings.Contains(errOut, "fixture manifest") {
		t.Errorf("prs.json as a workspace fixture: code %d err %q", code, errOut)
	}
}

// Every repo of a workspace is set up like a single repo, with one `gh auth
// token` read for all of them; a repo the fixture manifest has no fixture
// for keeps GitHub disabled (never gh auth) and says why.
func TestOpenWorkspace(t *testing.T) {
	isolate(t)
	var reads atomic.Int32
	ghToken = func(context.Context) (string, error) { reads.Add(1); return "token", nil }
	root := t.TempDir()
	gitRepoAt(t, filepath.Join(root, "a"), "https://github.com/acme/a.git")
	gitRepoAt(t, filepath.Join(root, "team", "b"), "https://github.com/acme/b.git")
	gitRepoAt(t, filepath.Join(root, "c"), "")
	ctx := context.Background()
	quiet := log.New(io.Discard)

	ws, err := openWorkspace(ctx, options{}, workspaceRoot(root), config.Defaults(), testOwner, quiet)
	if err != nil {
		t.Fatal(err)
	}
	info, err := ws.client.Workspace(ctx)
	_ = ws.client.Close()
	ws.close()
	if err != nil {
		t.Fatal(err)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("gh auth token read %d times, want once for the workspace", n)
	}
	got := map[string]proto.RepoInfo{}
	for _, r := range info.Repos {
		got[r.Key] = r
	}
	if !info.Multi || len(info.Repos) != 3 || info.Concurrency != config.Defaults().Pipeline.Concurrency {
		t.Fatalf("workspace = %+v", info)
	}
	if r := got["team/b"]; r.Label != "acme/b" || r.Status != proto.RepoGitHub {
		t.Errorf("team/b = %+v, want acme/b on GitHub", r)
	}
	if r := got["c"]; r.Label != "c" || r.Status != proto.RepoNoRemote {
		t.Errorf("c = %+v, want no remote", r)
	}

	reads.Store(0)
	ws, err = openWorkspace(ctx, options{fixtures: testutil.FixtureWorkspaceManifest(t)}, workspaceRoot(root), config.Defaults(), testOwner, quiet)
	if err != nil {
		t.Fatal(err)
	}
	info, _ = ws.client.Workspace(ctx)
	_ = ws.client.Close()
	ws.close()
	if n := reads.Load(); n != 0 {
		t.Errorf("with a fixture manifest gh auth was read %d times", n)
	}
	for _, r := range info.Repos {
		if r.Key == "a" && (r.Status != proto.RepoGitHubDisabled || !strings.Contains(r.Note, "no GitHub fixture for a in manifest.json")) {
			t.Errorf("a without a fixture = %+v", r)
		}
	}
}

// testOwner is the pipeline-lock owner the tests open workspaces as.
var testOwner = daemon.LockOwner{What: "prt --root (test)"}

// A repo whose setup fails (malformed prt.toml) or whose pipeline another
// process runs is listed with the error status and the reason, so the
// picker shows it; the others are served.
func TestOpenWorkspaceListsUnopenedRepos(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	gitRepoAt(t, filepath.Join(root, "good"), "")
	writePrtToml(t, gitRepoAt(t, filepath.Join(root, "bad"), ""), "[rank\n")
	busy := gitRepoAt(t, filepath.Join(root, "busy"), "")
	release, err := daemon.LockRepo(filepath.Join(busy, ".git"), daemon.LockOwner{What: "prt (no daemon)"})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx := context.Background()
	ws, err := openWorkspace(ctx, options{offline: true}, workspaceRoot(root), config.Defaults(), testOwner, log.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := ws.client.Workspace(ctx)
	_ = ws.client.Close()
	ws.close()
	var keys []string
	for _, r := range info.Repos {
		keys = append(keys, r.Key)
	}
	if !slices.Equal(keys, []string{"bad", "busy", "good"}) || len(ws.client.Members()) != 1 {
		t.Fatalf("repos = %+v, members %d; want bad, busy and good, only good opened", info.Repos, len(ws.client.Members()))
	}
	if r := info.Repos[0]; r.Status != proto.RepoError || !strings.Contains(r.Note, "config") {
		t.Errorf("bad = %+v, want the config error", r)
	}
	if r := info.Repos[1]; r.Status != proto.RepoError || !strings.Contains(r.Note, "already analysed by pid") || !strings.Contains(r.Note, "prt (no daemon)") {
		t.Errorf("busy = %+v, want the lock owner named", r)
	}
	// Closing the workspace released its locks.
	again, err := daemon.LockRepo(filepath.Join(root, "good", ".git"), testOwner)
	if err != nil {
		t.Fatalf("lock after close: %v", err)
	}
	again()
}

// Only one process runs a repo's pipeline: a single-repo prt that must run
// in-process (--snapshot) refuses a repo another prt analyses, naming it.
func TestSingleRepoRefusesLockedRepo(t *testing.T) {
	isolate(t)
	repo := gitRepo(t, "")
	release, err := daemon.LockRepo(filepath.Join(repo, ".git"), daemon.LockOwner{What: "prt daemon --root /w", Socket: "/nowhere/prtd.sock", Root: "/w"})
	if err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runPrt("--offline", "--snapshot", "--repo", repo)
	release()
	if code != 1 || !strings.Contains(errOut, "already analysed by pid") || !strings.Contains(errOut, "prt daemon --root /w") || !strings.Contains(errOut, "--snapshot") {
		t.Errorf("code %d err %q", code, errOut)
	}
	if code, _, errOut := runPrt("--offline", "--snapshot", "--repo", repo); code != 0 {
		t.Errorf("after release: code %d err %q", code, errOut)
	}
}

// shortTemp returns a fresh directory with a short path, removed after the
// test: t.TempDir() is too long for a Unix socket on macOS, and the socket's
// temp-dir fallback must not land in the user's real $TMPDIR.
func shortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ws")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// One daemon serves a workspace on its state-dir socket: a client dialling
// WorkspaceSocketPath(root) sees every repo, with prefixed ids. A
// single-repo prt started in one of its repos finds the daemon through the
// repo's pipeline lock and attaches to it instead of analysing the repo a
// second time.
func TestWorkspaceDaemon(t *testing.T) {
	isolate(t)
	tmp := shortTemp(t)
	t.Setenv("TMPDIR", tmp)
	t.Setenv("XDG_STATE_HOME", tmp)
	root := workspaceRoot(testutil.FixtureWorkspace(t))
	manifest := testutil.FixtureWorkspaceManifest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sock := daemon.WorkspaceSocketPath(root)
	if !strings.HasPrefix(sock, tmp) {
		t.Fatalf("socket %s is outside the test's temp dir %s", sock, tmp)
	}
	owner := daemon.LockOwner{What: "prt daemon --root " + root, Socket: sock, Root: root}
	ws, err := openWorkspace(ctx, options{offline: true, fixtures: manifest}, root, config.Defaults(), owner, log.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.close()
	defer func() { _ = ws.client.Close() }()
	served := make(chan error, 1)
	go func() { served <- serveWorkspace(ctx, ws.client, sock, 0, log.New(io.Discard)) }()

	var c *daemon.Socket
	deadline := time.Now().Add(10 * time.Second)
	for {
		if c, err = daemon.Dial(ctx, sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", sock, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer func() { _ = c.Close() }()
	info, err := c.Workspace(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Multi || len(info.Repos) != 2 || info.Root != root {
		t.Fatalf("workspace over the socket = %+v", info)
	}
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := c.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, it := range items {
		ids[it.ID] = true
		if repo, _ := model.SplitRepoItemID(it.ID); repo == "" || it.Repo != repo {
			t.Errorf("item %s has repo %q", it.ID, it.Repo)
		}
	}
	for _, want := range []string{"portal::pr:101", "billing-svc::pr:7", "billing-svc::pr:8", "billing-svc::branch:09-spike-usage-export"} {
		if !ids[want] {
			t.Errorf("items lack %s: %v", want, ids)
		}
	}

	// prt in portal: the lock names the workspace daemon, which it attaches to.
	_, lockErr := daemon.LockRepo(filepath.Join(root, "portal", ".git"), daemon.LockOwner{What: "prt (no daemon)"})
	if lockErr == nil {
		t.Fatal("portal's pipeline lock was free while the workspace daemon serves it")
	}
	att, err := attachOwner(ctx, lockErr, options{})
	if err != nil {
		t.Fatal(err)
	}
	if info, err := att.Workspace(ctx); err != nil || !info.Multi || info.Root != root {
		t.Errorf("attached to %+v, %v; want the workspace daemon", info, err)
	}
	_ = att.Close()
	if _, err := attachOwner(ctx, lockErr, options{noDaemon: true}); err == nil || !strings.Contains(err.Error(), "already analysed by pid") {
		t.Errorf("--no-daemon on a served repo: %v", err)
	}
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

func TestOnceToken(t *testing.T) {
	n := 0
	tok := onceToken(func(context.Context) (string, error) { n++; return "t", nil })
	for range 3 {
		if got, err := tok(context.Background()); got != "t" || err != nil {
			t.Fatalf("token = %q, %v", got, err)
		}
	}
	if n != 1 {
		t.Errorf("read %d times", n)
	}
}

// The workspace root is resolved, so a symlinked path and the real one
// share a state dir and socket.
func TestWorkspaceRootResolvesSymlinks(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if a, b := daemon.WorkspaceSocketPath(workspaceRoot(link)), daemon.WorkspaceSocketPath(workspaceRoot(dir)); a != b {
		t.Errorf("socket via link %s != via real path %s", a, b)
	}
}
