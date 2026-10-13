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

	ws, err := openWorkspace(ctx, options{}, workspaceRoot(root), config.Defaults(), quiet)
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
	ws, err = openWorkspace(ctx, options{fixtures: testutil.FixtureWorkspaceManifest(t)}, workspaceRoot(root), config.Defaults(), quiet)
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

// A repo whose setup fails (malformed prt.toml) is skipped with a
// workspace note; the others are served.
func TestOpenWorkspaceSkipsBrokenRepo(t *testing.T) {
	isolate(t)
	root := t.TempDir()
	gitRepoAt(t, filepath.Join(root, "good"), "")
	writePrtToml(t, gitRepoAt(t, filepath.Join(root, "bad"), ""), "[rank\n")
	ctx := context.Background()
	ws, err := openWorkspace(ctx, options{offline: true}, workspaceRoot(root), config.Defaults(), log.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	info, _ := ws.client.Workspace(ctx)
	_ = ws.client.Close()
	ws.close()
	if len(info.Repos) != 1 || info.Repos[0].Key != "good" {
		t.Errorf("repos = %+v, want only good", info.Repos)
	}
	if !slices.ContainsFunc(info.Notes, func(n string) bool { return strings.HasPrefix(n, "skipped bad: ") }) {
		t.Errorf("notes = %q, want the broken repo noted", info.Notes)
	}
}

// One daemon serves a workspace on its state-dir socket: a client dialling
// WorkspaceSocketPath(root) sees every repo, with prefixed ids.
func TestWorkspaceDaemon(t *testing.T) {
	isolate(t)
	root := workspaceRoot(testutil.FixtureWorkspace(t))
	manifest := testutil.FixtureWorkspaceManifest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws, err := openWorkspace(ctx, options{offline: true, fixtures: manifest}, root, config.Defaults(), log.New(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	defer ws.close()
	defer func() { _ = ws.client.Close() }()
	sock := daemon.WorkspaceSocketPath(root)
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
