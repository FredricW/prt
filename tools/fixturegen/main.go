// Command fixturegen builds testdata/fixture-repo deterministically from the
// committed sources in testdata/fixtures, or with -workspace the M5.3
// fixture workspace testdata/fixture-workspace from a manifest; see package
// internal/fixturegen.
//
// Usage:
//
//	go run ./tools/fixturegen -src testdata/fixtures -out testdata/fixture-repo
//	go run ./tools/fixturegen -workspace testdata/fixtures-workspace/manifest.json -out testdata/fixture-workspace
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/fredricw/prt/internal/fixturegen"
)

const (
	// originURL is the fictional remote of the fixture repo.
	originURL = fixturegen.OriginURL
	// defaultRepoOut is where the fixture repo is generated.
	defaultRepoOut = "testdata/fixture-repo"
	// defaultWorkspaceOut is where the fixture workspace is generated.
	defaultWorkspaceOut = "testdata/fixture-workspace"
)

// config is what one invocation generates.
type config struct {
	src       string // fixture sources (single-repo mode)
	out       string // directory to (re)create
	workspace string // workspace manifest; "" for single-repo mode
}

func main() {
	src := flag.String("src", "testdata/fixtures", "fixture sources (not with -workspace)")
	out := flag.String("out", "", "directory to (re)create (default "+defaultRepoOut+", or "+defaultWorkspaceOut+" with -workspace)")
	workspace := flag.String("workspace", "", "workspace manifest: build a fixture workspace instead of one repo")
	flag.Parse()
	srcSet := false
	flag.Visit(func(f *flag.Flag) { srcSet = srcSet || f.Name == "src" })
	cfg, err := newConfig(*src, *out, *workspace, srcSet)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixturegen:", err)
		os.Exit(2)
	}
	if err := cfg.generate(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "fixturegen:", err)
		os.Exit(1)
	}
}

// newConfig checks the flags and fills the default output directory.
// srcSet tells whether -src was given explicitly.
func newConfig(src, out, workspace string, srcSet bool) (config, error) {
	if workspace != "" && srcSet {
		return config{}, errors.New("-src and -workspace cannot be combined (the manifest names every repo's sources)")
	}
	if out == "" {
		out = defaultRepoOut
		if workspace != "" {
			out = defaultWorkspaceOut
		}
	}
	return config{src: src, out: out, workspace: workspace}, nil
}

// generate builds the workspace when one is configured, else the repo.
func (c config) generate(ctx context.Context) error {
	if c.workspace != "" {
		return fixturegen.GenerateWorkspace(ctx, c.workspace, c.out)
	}
	return run(ctx, c.src, c.out)
}

// run recreates the repository at out from the fixture sources in src.
func run(ctx context.Context, src, out string) error { return fixturegen.Generate(ctx, src, out) }
