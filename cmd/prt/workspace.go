package main

// Workspace mode (M5.3): `prt --root DIR`, or prt started in a directory
// that is not inside a git repository, serves every repository under the
// directory in one picker. Each repository is set up exactly as in
// single-repo mode (its own store, log and config under its own .git/prt,
// see setup) and gets an in-process client; the clients share one worker
// pool and one cost ledger and are multiplexed by workspace.Client. One
// daemon serves a workspace, on daemon.WorkspaceSocketPath(root), with the
// workspace log beside it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/charmbracelet/log"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/fixturegen"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/tui"
	"github.com/fredricw/prt/internal/workspace"
)

// runWorkspace is run for a workspace: the daemon (`prt daemon`), a
// snapshot, or the TUI attached to the workspace's daemon when one is
// running, else in-process.
func runWorkspace(ctx context.Context, o options, daemonMode bool, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		prefix := "prt:"
		if daemonMode {
			prefix = "prt daemon:"
		}
		_, _ = fmt.Fprintln(stderr, prefix, err)
		return 1
	}
	root := workspaceRoot(o.root)
	wcfg, err := workspaceConfig(o)
	if err != nil {
		return fail(err)
	}
	logger, closeLog, err := openWorkspaceLog(root)
	if err != nil {
		return fail(err)
	}
	defer closeLog()
	for _, w := range wcfg.Warnings {
		logger.Warn("config", "warning", w)
	}
	refresh := time.Duration(wcfg.Pipeline.RefreshSeconds) * time.Second
	sock := daemon.WorkspaceSocketPath(root)

	var client daemon.Client
	// attached: the TUI talks to the workspace daemon, which runs discovery
	// itself, so the TUI's refresh tick only re-reads the items.
	attached := false
	if !daemonMode && !o.noDaemon && !o.snapshot {
		if c, err := daemon.Dial(ctx, sock); err == nil {
			client, attached = c, true
		} else {
			logger.Info("no workspace daemon; running in-process", "err", err)
		}
	}
	if client == nil {
		owner := daemon.LockOwner{What: "prt --root " + root + " (no daemon)", Root: root}
		if daemonMode {
			owner = daemonOwner(root, sock)
		}
		ws, err := openWorkspace(ctx, o, root, wcfg, owner, logger)
		if err != nil {
			return fail(err)
		}
		defer ws.close() // after the client: its members use the stores
		client = ws.client
		if daemonMode {
			defer func() { _ = ws.client.Close() }()
			if err := serveWorkspace(ctx, ws.client, sock, refresh, logger); err != nil {
				return fail(err)
			}
			return 0
		}
	}
	defer func() { _ = client.Close() }()

	if o.snapshot {
		if err := tui.SnapshotWithOptions(ctx, client, stdout, o.width, o.height, o.maxItems, tui.Options{Now: o.nowFn}); err != nil {
			return fail(err)
		}
		return 0
	}
	// Repo, Role and Root stay empty: in a workspace the header and the
	// Code page's `e` use the daemon's Workspace answer.
	topts := tui.Options{Open: openURL, RefreshEvery: refresh, Now: o.nowFn, ReloadOnTick: attached}
	return runTUI(ctx, client, topts, wcfg.Keys, logger, stderr)
}

// serveWorkspace runs the workspace daemon until ctx ends: discovery and
// pending analysis of every repo now and every refresh interval, and the
// workspace client on the socket.
func serveWorkspace(ctx context.Context, c *workspace.Client, sock string, refresh time.Duration, logger *log.Logger) error {
	go discoverLoop(ctx, c, refresh, logger)
	return daemon.Serve(ctx, c, sock)
}

// workspaceRoot is dir as an absolute path with symlinks resolved (the
// absolute path when it cannot be resolved), so the TUI and the daemon
// derive the same state dir and socket however the root was named.
func workspaceRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	return abs
}

// workspaceConfig is the configuration of the workspace as a whole, read
// from the global prt.toml only: [workspace] depth and exclude, the shared
// pool's [pipeline] concurrency, the refresh interval and [keys]. Every repo
// still reads its own config (setup). With a replay flag it is the built-in
// defaults, since replay never reads the XDG config (demo and snapshot must
// not depend on the user's files).
func workspaceConfig(o options) (config.Config, error) {
	dir := config.GlobalDir()
	if o.replaying() || dir == "" {
		return config.Defaults(), nil
	}
	return config.Load([]string{dir})
}

// openWorkspaceLog opens the workspace log, prt.log in the workspace's
// state dir (each repo logs to its own .git/prt/prt.log as well).
func openWorkspaceLog(root string) (*log.Logger, func(), error) {
	dir := daemon.WorkspaceStateDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, fmt.Errorf("workspace state dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "prt.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // our own state dir
	if err != nil {
		return nil, nil, err
	}
	logger := log.NewWithOptions(f, log.Options{ReportTimestamp: true, Prefix: "prt"})
	return logger, func() { _ = f.Close() }, nil
}

// workspaceEnv is an opened workspace: the client and every repo's setup
// (closed after the client).
type workspaceEnv struct {
	client *workspace.Client
	envs   []*environment
}

func (w *workspaceEnv) close() {
	for i := len(w.envs) - 1; i >= 0; i-- {
		w.envs[i].close()
	}
}

// openWorkspace discovers the repositories under root and sets each one up
// as single-repo mode would, on one shared pool (the global [pipeline]
// concurrency) and ledger, with `gh auth token` read at most once, taking
// each repo's pipeline lock for owner. With --fixtures the file is a
// workspace manifest (fixturegen.Manifest) and each repo gets its own
// fixture; a repo the manifest has none for keeps GitHub disabled. A repo
// whose setup fails, or whose lock another process holds, is listed as a
// RepoError repo with the reason (and logged); finding no repository, or
// none that opens, is an error.
func openWorkspace(ctx context.Context, o options, root string, wcfg config.Config, owner daemon.LockOwner, logger *log.Logger) (*workspaceEnv, error) {
	depth := o.depth
	if depth <= 0 {
		depth = wcfg.Workspace.Depth
	}
	if depth <= 0 {
		depth = config.DefaultWorkspaceDepth
	}
	res, err := workspace.Discover(root, workspace.Options{Depth: depth, Exclude: wcfg.Workspace.Exclude})
	if err != nil {
		return nil, err
	}
	for _, n := range res.Notes {
		logger.Info("discovery", "note", n)
	}
	if len(res.Repos) == 0 {
		return nil, fmt.Errorf("no git repositories under %s (depth %d)", res.Root, depth)
	}
	var manifest *fixturegen.Manifest
	if o.fixtures != "" {
		m, err := fixturegen.LoadManifest(o.fixtures)
		if err != nil {
			return nil, fmt.Errorf("--fixtures: a workspace takes a fixture manifest: %w", err)
		}
		manifest = &m
	}

	pool, ledger := daemon.NewPool(wcfg.Pipeline.Concurrency), daemon.NewLedger()
	token := onceToken(o.tokenFunc())
	we := &workspaceEnv{}
	var (
		members  []workspace.Member
		unopened []proto.RepoInfo
	)
	for _, r := range res.Repos {
		ro := o
		ro.root, ro.repo, ro.token, ro.fixtures = "", r.Root, token, ""
		if manifest != nil {
			if ro.fixtures = manifest.PRsFor(r.Key); ro.fixtures == "" {
				ro.noGitHub = "no GitHub fixture for " + r.Key + " in " + filepath.Base(o.fixtures) + "; local branches only"
			}
		}
		env, err := setup(ctx, ro)
		if err == nil {
			if err = lockRepo(env, owner); err != nil {
				env.close()
			}
		}
		if err != nil {
			unopened = append(unopened, proto.RepoInfo{Key: r.Key, Label: r.Key, Root: r.Root, GitDir: r.GitDir,
				Status: proto.RepoError, Note: err.Error()})
			logger.Warn("repo not opened", "repo", r.Key, "err", err)
			continue
		}
		we.envs = append(we.envs, env)
		info := workspace.Describe(r, env.cfg.GitHub.Repo, env.origin, env.deps.GH != nil, env.ghNote)
		c := daemon.NewInProcessWith(env.deps, daemon.Options{Pool: pool, Ledger: ledger, Repo: info})
		c.SetGitHubNote(env.ghNote)
		c.SetSourceNote(env.srcNote)
		c.SetSettingsDeps(settingsDeps(o))
		members = append(members, workspace.Member{Info: info, Client: c})
		logger.Info("repo", "key", r.Key, "label", info.Label, "status", info.Status, "root", r.Root)
	}
	if len(members) == 0 {
		we.close()
		return nil, errors.Join(append([]error{fmt.Errorf("no repository under %s could be opened", res.Root)}, unopenedErrs(unopened)...)...)
	}
	we.client = workspace.New(res.Root, members, workspace.ClientOptions{Pool: pool, Ledger: ledger, Notes: res.Notes, Unopened: unopened, Log: logger})
	return we, nil
}

// unopenedErrs are the reasons repos could not be opened, as errors (for
// the "none could be opened" error).
func unopenedErrs(repos []proto.RepoInfo) []error {
	out := make([]error, 0, len(repos))
	for _, r := range repos {
		out = append(out, fmt.Errorf("%s: %s", r.Key, r.Note))
	}
	return out
}

// onceToken wraps a token reader so it runs at most once (one `gh auth
// token` for every repo of a workspace); every caller gets its result.
func onceToken(f func(context.Context) (string, error)) func(context.Context) (string, error) {
	var (
		once sync.Once
		tok  string
		err  error
	)
	return func(ctx context.Context) (string, error) {
		once.Do(func() { tok, err = f(ctx) })
		return tok, err
	}
}
