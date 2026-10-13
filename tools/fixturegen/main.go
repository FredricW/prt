// Command fixturegen builds testdata/fixture-repo deterministically from the
// committed sources in testdata/fixtures; see package internal/fixturegen.
//
// Usage: go run ./tools/fixturegen -src testdata/fixtures -out testdata/fixture-repo
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/fredricw/prt/internal/fixturegen"
)

// originURL is the fictional remote of the fixture repo.
const originURL = fixturegen.OriginURL

func main() {
	src := flag.String("src", "testdata/fixtures", "fixture sources")
	out := flag.String("out", "testdata/fixture-repo", "repository to (re)create")
	flag.Parse()
	if err := run(context.Background(), *src, *out); err != nil {
		fmt.Fprintln(os.Stderr, "fixturegen:", err)
		os.Exit(1)
	}
}

// run recreates the repository at out from the fixture sources in src.
func run(ctx context.Context, src, out string) error { return fixturegen.Generate(ctx, src, out) }
