// Command prt is the PR triage TUI; `prt daemon` runs the analysis daemon.
//
// Usage:
//
//	prt [--repo DIR] [--no-daemon] [--offline] [--fixtures FILE] [--snapshot [--width N --height N --now TIME]]
//	prt daemon [--repo DIR] [--offline] [--fixtures FILE]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
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
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/questions"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/store"
	"github.com/fredricw/prt/internal/tui"
)

var version = "dev"

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
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env, err := setup(ctx, o)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "prt:", err)
		return 1
	}
	defer env.close()

	if daemonMode {
		inproc := daemon.NewInProcess(env.deps)
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
		client = daemon.NewInProcess(env.deps)
	}
	defer func() { _ = client.Close() }()

	if o.snapshot {
		if err := tui.SnapshotWithOptions(ctx, client, stdout, o.width, o.height, o.maxItems, tui.Options{Now: nowFn}); err != nil {
			_, _ = fmt.Fprintln(stderr, "prt:", err)
			return 1
		}
		return 0
	}
	topts := tui.Options{
		Open:         openURL,
		RefreshEvery: time.Duration(env.cfg.Pipeline.RefreshSeconds) * time.Second,
		Now:          nowFn,
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
	deps    pipeline.Deps
	cfg     config.Config
	gitDir  string
	closers []func()
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
	cat, err := questions.Parse(qraw, qsrc)
	if err != nil {
		env.close()
		return nil, err
	}

	d := pipeline.Deps{Git: g, Store: st, Config: cfg, Routing: routing, Questions: cat, Log: logger}
	if o.fixtures != "" {
		fake, err := gh.LoadFixture(o.fixtures)
		if err != nil {
			env.close()
			return nil, fmt.Errorf("--fixtures: %w", err)
		}
		d.GH = fake
		logger.Info("using GitHub fixture", "path", o.fixtures)
	}
	if !o.offline {
		if d.GH == nil {
			d.GH = githubClient(ctx, g, cfg, logger)
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
		}
		if deep, err := review.FromSpec(cfg.Models.Deep, key); err == nil {
			d.Reviewers.Deep = deep
		}
	}
	env.deps = d
	env.cfg = cfg
	return env, nil
}

// discoverLoop runs discovery now and then every interval until ctx ends, so
// a daemon keeps the picker fresh with no TUI attached. interval <= 0 runs
// discovery once.
func discoverLoop(ctx context.Context, c daemon.Client, interval time.Duration, logger *log.Logger) {
	discover := func() {
		if err := c.Discover(ctx); err != nil && ctx.Err() == nil {
			logger.Warn("discover", "err", err)
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

// githubClient returns nil when the repo is not on GitHub or gh has no token.
func githubClient(ctx context.Context, g gitx.Git, cfg config.Config, logger *log.Logger) gh.Client {
	var repo gh.Repo
	if cfg.GitHub.Repo != "" {
		r, ok := gh.ParseRemote("github.com/" + cfg.GitHub.Repo)
		if !ok {
			logger.Warn("bad github.repo in config", "repo", cfg.GitHub.Repo)
			return nil
		}
		repo = r
	} else {
		url, err := g.RemoteURL(ctx, "origin")
		if err != nil {
			logger.Info("no origin remote; GitHub disabled")
			return nil
		}
		r, ok := gh.ParseRemote(url)
		if !ok {
			logger.Info("origin is not GitHub; GitHub disabled")
			return nil
		}
		repo = r
	}
	tok, err := gh.TokenFromGH(ctx)
	if err != nil {
		logger.Warn("GitHub disabled", "err", err)
		return nil
	}
	return gh.NewAPI(ctx, repo, tok)
}
