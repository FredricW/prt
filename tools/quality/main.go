// Command quality prints the label-quality scoreboard: it runs the full
// pipeline offline over the fixture items from the committed recordings
// and scores the aggregates against testdata/groundtruth (see
// internal/quality and docs/QUALITY.md). No network, no spend.
//
//	go run ./tools/quality                 markdown table
//	go run ./tools/quality -json           the same board as JSON
//
// Flags: -groundtruth (default testdata/groundtruth), -recordings
// (testdata/recordings), -fixtures (testdata/fixtures; the fixture repo is
// generated from it into a temp dir), -review-misses (let a review request
// without a recording fail its row instead of the run; reviews feed no
// metric), -explain (a per-item diagnostic listing before the board).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/fredricw/prt/internal/fixturegen"
	"github.com/fredricw/prt/internal/quality"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

// options are the parsed flags.
type options struct {
	groundTruth, recordings, fixtures string
	json, reviewMisses, explain       bool
}

// run is main without process globals; it returns the exit code.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	o, err := parseFlags(args, stderr)
	if err != nil {
		return 2
	}
	if err := execute(ctx, o, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// parseFlags parses args; defaults are relative to the module root.
func parseFlags(args []string, stderr io.Writer) (options, error) {
	root := moduleRoot()
	fs := flag.NewFlagSet("quality", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	fs.StringVar(&o.groundTruth, "groundtruth", filepath.Join(root, "testdata", "groundtruth"), "ground-truth directory (*.toml)")
	fs.StringVar(&o.recordings, "recordings", filepath.Join(root, "testdata", "recordings"), "recordings directory (jev/, embed/, review/)")
	fs.StringVar(&o.fixtures, "fixtures", filepath.Join(root, "testdata", "fixtures"), "fixture sources (base/, prs/, prs.json, prt.toml)")
	fs.BoolVar(&o.json, "json", false, "print the board as JSON")
	fs.BoolVar(&o.reviewMisses, "review-misses", false, "tolerate review requests without a recording (the row fails; reviews feed no metric)")
	fs.BoolVar(&o.explain, "explain", false, "print a per-item diagnostic listing (units, headlines, hot spots, misses) before the board")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	return o, nil
}

// execute loads the ground truth, generates the fixture repo into a temp
// dir, evaluates and prints the board.
func execute(ctx context.Context, o options, stdout io.Writer) error {
	truths, err := quality.LoadDir(o.groundTruth)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "prt-quality-*")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	// fixturegen only writes under a "testdata" directory.
	repo := filepath.Join(tmp, "testdata", "fixture-repo")
	if err := fixturegen.Generate(ctx, o.fixtures, repo); err != nil {
		return fmt.Errorf("generate fixture repo: %w", err)
	}
	b, explained, err := quality.EvaluateExplain(ctx, quality.Env{
		RepoDir: repo, PRsJSON: filepath.Join(o.fixtures, "prs.json"),
		Recordings: o.recordings, ReviewMisses: o.reviewMisses,
	}, truths)
	if err != nil {
		return err
	}
	if o.explain {
		for _, e := range explained {
			if _, err := io.WriteString(stdout, e); err != nil {
				return err
			}
		}
	}
	if o.json {
		out, err := b.JSON()
		if err != nil {
			return err
		}
		_, err = stdout.Write(out)
		return err
	}
	_, err = io.WriteString(stdout, b.Markdown())
	return err
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod ("." if none).
func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
