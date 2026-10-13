// Command prt is the PR triage TUI; `prt daemon` runs the analysis daemon.
//
// Usage:
//
//	prt [--repo DIR] [--no-daemon] [--offline] [--fixtures FILE] [--jev-replay DIR] [--embed-replay DIR] [--review-replay DIR] [--drafts FILE] [--snapshot [--width N --height N --now TIME]]
//	prt daemon [--repo DIR] [--offline] [--fixtures FILE] [--jev-replay DIR] [--embed-replay DIR] [--review-replay DIR] [--drafts FILE]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/log"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/questions"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/store"
	"github.com/fredricw/prt/internal/tui"
)

var version = "dev"

// ghToken returns the GitHub token (`gh auth token`); tests replace it.
var ghToken = gh.TokenFromGH

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

type options struct {
	repo     string
	fixtures string
	now      string
	noDaemon bool
	offline  bool
	snapshot bool
	width    int
	height   int
	maxItems int
	// jevReplay / embedReplay replace Jev and the embedder with recorded
	// answers (make demo / make snapshot); see replayDeps.
	jevReplay    string
	embedReplay  string
	reviewReplay string
	// drafts seeds local user drafts (testdata/fixtures/drafts.json) into
	// the store before anything renders; see seedDrafts.
	drafts string
	// nowFn is the parsed --now clock (nil: wall clock). setup hands it to
	// the pipeline too, so urgency (age term), quartiles and order use the
	// same clock as the TUI and snapshots do not drift.
	nowFn func() time.Time
}

func run(args []string, stdout, stderr io.Writer) int {
	daemonMode := len(args) > 0 && args[0] == "daemon"
	if daemonMode {
		args = args[1:]
	}
	fs := flag.NewFlagSet("prt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.StringVar(&o.repo, "repo", ".", "path inside the git repository to triage")
	fs.BoolVar(&o.noDaemon, "no-daemon", false, "run the pipeline in-process instead of connecting to prt daemon")
	fs.BoolVar(&o.offline, "offline", false, "do not contact GitHub, Jev, LLMs or embedding servers")
	fs.BoolVar(&o.snapshot, "snapshot", false, "render a few screens to stdout without a TTY and exit")
	fs.IntVar(&o.width, "width", 120, "snapshot width")
	fs.IntVar(&o.height, "height", 30, "snapshot height")
	fs.IntVar(&o.maxItems, "max-items", 4, "snapshot: number of items to render")
	fs.StringVar(&o.fixtures, "fixtures", "", "use a fake GitHub client loaded from this JSON fixture (works with --offline)")
	fs.StringVar(&o.now, "now", "", "snapshot: fixed current time (RFC 3339) so ages are stable")
	fs.StringVar(&o.jevReplay, "jev-replay", "", "answer Jev from recordings in this directory (works with --offline; a miss means labels unavailable)")
	fs.StringVar(&o.embedReplay, "embed-replay", "", "embed from recordings in this directory (works with --offline; a miss skips embeddings)")
	fs.StringVar(&o.reviewReplay, "review-replay", "", "answer the fast/deep reviewers from recordings in this directory (works with --offline; a miss leaves the group unreviewed)")
	fs.StringVar(&o.drafts, "drafts", "", "seed local draft comments from this JSON file (demo and snapshot); drafts already stored are kept as they are")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, "prt", version)
		return 0
	}

	var nowFn func() time.Time
	if o.now != "" {
		t, err := time.Parse(time.RFC3339, o.now)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "prt: --now:", err)
			return 2
		}
		nowFn = func() time.Time { return t }
		o.nowFn = nowFn
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env, err := setup(ctx, o)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "prt:", err)
		return 1
	}
	defer env.close()
	if o.drafts != "" {
		n, err := seedDrafts(ctx, env.deps.Store, o.drafts)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "prt: --drafts:", err)
			return 1
		}
		env.deps.Log.Info("seeded drafts", "path", o.drafts, "new", n)
	}
	newInProcess := func() *daemon.InProcess {
		c := daemon.NewInProcess(env.deps)
		c.SetGitHubNote(env.ghNote)
		c.SetSourceNote(env.srcNote)
		return c
	}

	if daemonMode {
		inproc := newInProcess()
		defer func() { _ = inproc.Close() }()
		go discoverLoop(ctx, inproc, time.Duration(env.cfg.Pipeline.RefreshSeconds)*time.Second, env.deps.Log)
		if err := daemon.Serve(ctx, inproc, daemon.SocketPath(env.gitDir)); err != nil {
			_, _ = fmt.Fprintln(stderr, "prt daemon:", err)
			return 1
		}
		return 0
	}

	var client daemon.Client
	if !o.noDaemon && !o.snapshot {
		if c, err := daemon.Dial(ctx, daemon.SocketPath(env.gitDir)); err == nil {
			client = c
		} else {
			env.deps.Log.Info("no daemon; running in-process", "err", err)
		}
	}
	if client == nil {
		client = newInProcess()
	}
	defer func() { _ = client.Close() }()

	if o.snapshot {
		sopts := tui.Options{Now: nowFn, Repo: env.repoLabel, Role: env.cfg.User.Role, Root: env.root}
		if err := tui.SnapshotWithOptions(ctx, client, stdout, o.width, o.height, o.maxItems, sopts); err != nil {
			_, _ = fmt.Fprintln(stderr, "prt:", err)
			return 1
		}
		return 0
	}
	topts := tui.Options{
		Open:         openURL,
		RefreshEvery: time.Duration(env.cfg.Pipeline.RefreshSeconds) * time.Second,
		Now:          nowFn,
		Repo:         env.repoLabel,
		Role:         env.cfg.User.Role,
		Root:         env.root,
	}
	if km, err := tui.KeyMapFromConfig(env.cfg.Keys); err != nil {
		env.deps.Log.Warn("bad [keys] in prt.toml; using defaults", "err", err)
	} else {
		topts.Keys = &km
	}
	p := tea.NewProgram(tui.NewWithOptions(client, nil, topts), tea.WithAltScreen(), tea.WithContext(ctx), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil && !errors.Is(err, tea.ErrProgramKilled) {
		_, _ = fmt.Fprintln(stderr, "prt:", err)
		return 1
	}
	return 0
}

type environment struct {
	deps   pipeline.Deps
	cfg    config.Config
	gitDir string
	// ghNote says why GitHub is disabled ("" when a client is wired).
	ghNote string
	// srcNote names replayed sources ("" when none), shown in the status bar.
	srcNote string
	// repoLabel ("owner/name", else the work tree's directory name) and
	// root (the work tree) feed the TUI header and the Code page's `e`.
	repoLabel string
	root      string
	closers   []func()
}

// repoLabel names the repo for the header: github.repo from config, else
// the origin remote when it is on GitHub, else the work tree's base name.
func repoLabel(ctx context.Context, g gitx.Git, cfg config.Config) string {
	if cfg.GitHub.Repo != "" {
		return cfg.GitHub.Repo
	}
	if url, err := g.RemoteURL(ctx, "origin"); err == nil {
		if r, ok := gh.ParseRemote(url); ok {
			return r.String()
		}
	}
	if g.Root() == "" {
		return ""
	}
	return filepath.Base(g.Root())
}

func (e *environment) close() {
	for i := len(e.closers) - 1; i >= 0; i-- {
		e.closers[i]()
	}
}

// setup opens git, the store and the log, loads config, and wires external
// clients from the environment. Missing keys or tools degrade, never fail.
func setup(ctx context.Context, o options) (*environment, error) {
	g, err := gitx.Open(ctx, o.repo)
	if err != nil {
		return nil, err
	}
	gitDir, err := g.GitDir(ctx)
	if err != nil {
		return nil, err
	}
	env := &environment{gitDir: gitDir}
	prtDir := filepath.Join(gitDir, "prt")
	if err := os.MkdirAll(prtDir, 0o755); err != nil {
		return nil, err
	}
	logf, err := os.OpenFile(filepath.Join(prtDir, "prt.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	env.closers = append(env.closers, func() { _ = logf.Close() })
	logger := log.NewWithOptions(logf, log.Options{ReportTimestamp: true, Prefix: "prt"})

	st, err := store.Open(ctx, store.DefaultPath(gitDir))
	if err != nil {
		env.close()
		return nil, err
	}
	env.closers = append(env.closers, func() { _ = st.Close() })

	dirs := config.Dirs(gitDir)
	if o.jevReplay != "" || o.embedReplay != "" || o.reviewReplay != "" {
		// Recorded request keys depend on config and questions: read only
		// the repo's own .git/prt (as the recorder does), never XDG.
		dirs = []string{prtDir}
	}
	cfg, err := config.Load(dirs)
	if err != nil {
		env.close()
		return nil, err
	}
	routing, err := config.LoadRouting(dirs)
	if err != nil {
		env.close()
		return nil, err
	}
	qraw, qsrc, err := config.ReadQuestions(dirs)
	if err != nil {
		env.close()
		return nil, err
	}
	cat, err := questions.Load(qraw, qsrc)
	if err != nil {
		env.close()
		return nil, err
	}
	for _, w := range append(append([]string(nil), cfg.Warnings...), routing.Warnings...) {
		logger.Warn("config", "warning", w)
	}

	d := pipeline.Deps{Git: g, Store: st, Config: cfg, Routing: routing, Questions: cat, Log: logger, Now: o.nowFn, ConfigDirs: dirs}
	if o.fixtures != "" {
		fake, err := gh.LoadFixture(o.fixtures)
		if err != nil {
			env.close()
			return nil, fmt.Errorf("--fixtures: %w", err)
		}
		d.GH = fake
		logger.Info("using GitHub fixture", "path", o.fixtures)
	}
	if o.offline && d.GH == nil {
		env.ghNote = "offline: GitHub disabled; local branches only"
	}
	if !o.offline {
		if d.GH == nil {
			var reason string
			d.GH, reason = githubClient(ctx, g, cfg, logger)
			if d.GH == nil {
				env.ghNote = "GitHub disabled (" + reason + "); local branches only"
			}
		}
		jevURL := os.Getenv("JEV_BASE_URL")
		if jevURL == "" {
			jevURL = cfg.Models.JevBaseURL
		}
		jc := jev.NewHTTP(jevURL, os.Getenv("JEV_API_KEY"))
		if !jc.HasKey() {
			logger.Warn("JEV_API_KEY not set; labels unavailable")
		}
		d.Jev = jc
		if e, err := embed.FromSpec(cfg.Models.Embed, ""); err == nil {
			d.Embedder = e
		} else {
			logger.Warn("embeddings disabled", "err", err)
		}
		key := review.AnthropicKeyFromEnv()
		if fast, err := review.FromSpec(cfg.Models.Fast, key); err == nil {
			d.Reviewers.Fast = fast
		} else {
			logger.Warn("fast review tier disabled", "spec", cfg.Models.Fast, "err", err)
		}
		if deep, err := review.FromSpec(cfg.Models.Deep, key); err == nil {
			d.Reviewers.Deep = deep
		} else {
			logger.Warn("deep review tier disabled", "spec", cfg.Models.Deep, "err", err)
		}
	}
	env.srcNote = replayDeps(&d, o)
	env.deps = d
	env.cfg = cfg
	env.repoLabel = repoLabel(ctx, g, cfg)
	env.root = g.Root()
	return env, nil
}

// replayDeps swaps in recorded Jev answers and embeddings when asked (they
// win over live clients) and returns the status note naming them. A Jev
// replay miss is reported as unavailable, so a request that was never
// recorded degrades to "labels unavailable" instead of failing the item.
func replayDeps(d *pipeline.Deps, o options) string {
	var notes []string
	if o.jevReplay != "" {
		d.Jev = &jev.Replay{Dir: o.jevReplay, MissUnavailable: true}
		notes = append(notes, "Jev: replay of "+o.jevReplay)
	}
	if o.embedReplay != "" {
		d.Embedder = &embed.Replay{Dir: o.embedReplay}
		notes = append(notes, "embeddings: replay of "+o.embedReplay)
	}
	if o.reviewReplay != "" {
		// One Replay per tier: recording keys include the model spec. A miss
		// is unavailable, so an unrecorded group degrades instead of failing loudly.
		d.Reviewers.Fast = &review.Replay{Name: d.Config.Models.Fast, Dir: o.reviewReplay, MissUnavailable: true}
		d.Reviewers.Deep = &review.Replay{Name: d.Config.Models.Deep, Dir: o.reviewReplay, MissUnavailable: true}
		notes = append(notes, "reviews: replay of "+o.reviewReplay)
	}
	if len(notes) == 0 {
		return ""
	}
	note := strings.Join(notes, " · ")
	if o.offline {
		note += " (offline)"
	}
	return note
}

// seedDrafts stores the user drafts in the JSON file at path ([]model.Comment)
// and returns how many were new. A draft whose id is already stored for its
// item is left alone, so edits made in the demo survive the next start (a
// deleted fixture draft does come back). Only local user drafts are
// accepted; the pipeline anchors them to units on the next analysis.
func seedDrafts(ctx context.Context, st store.Store, path string) (int, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // path is a CLI flag
	if err != nil {
		return 0, err
	}
	var drafts []model.Comment
	if err := json.Unmarshal(raw, &drafts); err != nil {
		return 0, fmt.Errorf("%s: %w", path, err)
	}
	have := map[string]map[string]bool{}
	n := 0
	for _, c := range drafts {
		if c.ID == "" || c.ItemID == "" || c.Path == "" || c.Line <= 0 {
			return n, fmt.Errorf("%s: draft %q needs id, itemId, path and line", path, c.ID)
		}
		if c.Source != model.CommentUser || c.State != model.CommentDraft {
			return n, fmt.Errorf("%s: %s is not a user draft (source %q, state %q)", path, c.ID, c.Source, c.State)
		}
		ids, ok := have[c.ItemID]
		if !ok {
			stored, err := st.Comments(ctx, c.ItemID)
			if err != nil {
				return n, err
			}
			ids = map[string]bool{}
			for _, s := range stored {
				ids[s.ID] = true
			}
			have[c.ItemID] = ids
		}
		if ids[c.ID] {
			continue
		}
		if err := st.PutComment(ctx, c); err != nil {
			return n, err
		}
		ids[c.ID] = true
		n++
	}
	return n, nil
}

// daemonWork is what the daemon loop drives: discovery, then analysis of
// every never-analysed item (urgency order), so the cache fills with no TUI
// attached (spec: Architecture).
type daemonWork interface {
	Discover(ctx context.Context) error
	AnalysePending(ctx context.Context) error
}

// discoverLoop runs discovery and queues pending analysis now and then
// every interval until ctx ends. interval <= 0 runs one pass.
func discoverLoop(ctx context.Context, c daemonWork, interval time.Duration, logger *log.Logger) {
	discover := func() {
		if err := c.Discover(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("discover", "err", err)
		}
		if err := c.AnalysePending(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("queue analysis", "err", err)
		}
	}
	discover()
	if interval <= 0 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			discover()
		}
	}
}

// openURL opens url in the default browser (key `o`).
func openURL(url string) error {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	// Background context: the browser must outlive any request. url is a
	// single argv entry, never interpreted by a shell.
	cmd := exec.CommandContext(context.Background(), name, url) //nolint:gosec // see above
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// githubClient returns nil and a short user-facing reason when the repo is
// not on GitHub or gh has no token.
func githubClient(ctx context.Context, g gitx.Git, cfg config.Config, logger *log.Logger) (gh.Client, string) {
	var repo gh.Repo
	if cfg.GitHub.Repo != "" {
		r, ok := gh.ParseRemote("github.com/" + cfg.GitHub.Repo)
		if !ok {
			logger.Warn("bad github.repo in config", "repo", cfg.GitHub.Repo)
			return nil, "bad github.repo in prt.toml"
		}
		repo = r
	} else {
		url, err := g.RemoteURL(ctx, "origin")
		if err != nil {
			logger.Info("no origin remote; GitHub disabled")
			return nil, "no origin remote"
		}
		r, ok := gh.ParseRemote(url)
		if !ok {
			logger.Info("origin is not GitHub; GitHub disabled")
			return nil, "origin is not on GitHub"
		}
		repo = r
	}
	tok, err := ghToken(ctx)
	if err != nil {
		logger.Warn("GitHub disabled", "err", err)
		return nil, "no token from `gh auth token`"
	}
	return gh.NewAPI(ctx, repo, tok), ""
}
