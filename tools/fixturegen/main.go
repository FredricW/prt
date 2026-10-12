// Command fixturegen builds testdata/fixture-repo deterministically from the
// committed sources in testdata/fixtures:
//
//	testdata/fixtures/base/**        tree of the initial commit on main
//	testdata/fixtures/prs/NN-name.patch   one branch per patch, off main
//
// A patch in `git format-patch` mbox form (starts with "From ") is applied
// with `git am` (keeps its message and author); a plain diff is applied with
// `git apply --index` and committed with the file name as message. Authors,
// committers and dates are fixed so SHAs are stable across machines.
//
// Usage: go run ./tools/fixturegen -src testdata/fixtures -out testdata/fixture-repo
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const fixedDate = "2026-01-01T00:00:00Z"

func main() {
	src := flag.String("src", "testdata/fixtures", "fixture sources")
	out := flag.String("out", "testdata/fixture-repo", "repository to (re)create")
	flag.Parse()
	if err := run(*src, *out); err != nil {
		fmt.Fprintln(os.Stderr, "fixturegen:", err)
		os.Exit(1)
	}
}

func run(src, out string) error {
	absOut, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	if !strings.Contains(filepath.ToSlash(absOut), "/testdata/") {
		return fmt.Errorf("refusing to recreate %s: output must be under a testdata directory", absOut)
	}
	if err := os.RemoveAll(absOut); err != nil {
		return err
	}
	if err := os.MkdirAll(absOut, 0o755); err != nil {
		return err
	}
	g := func(args ...string) error { return git(absOut, args...) }
	if err := g("init", "-q", "-b", "main"); err != nil {
		return err
	}
	base := filepath.Join(src, "base")
	if _, err := os.Stat(base); err == nil {
		if err := copyTree(base, absOut); err != nil {
			return err
		}
	} else if err := os.WriteFile(filepath.Join(absOut, "README.md"), []byte("# fixture repo\n"), 0o644); err != nil {
		return err
	}
	if err := g("add", "-A"); err != nil {
		return err
	}
	if err := g("commit", "-q", "-m", "base"); err != nil {
		return err
	}
	patches, _ := filepath.Glob(filepath.Join(src, "prs", "*.patch"))
	sort.Strings(patches)
	for _, p := range patches {
		name := strings.TrimSuffix(filepath.Base(p), ".patch")
		absP, err := filepath.Abs(p)
		if err != nil {
			return err
		}
		if err := g("checkout", "-q", "-b", name, "main"); err != nil {
			return err
		}
		body, err := os.ReadFile(absP)
		if err != nil {
			return err
		}
		if bytes.HasPrefix(body, []byte("From ")) {
			err = g("am", "-q", "--committer-date-is-author-date", absP)
		} else if err = g("apply", "--index", absP); err == nil {
			err = g("commit", "-q", "-m", name)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return g("checkout", "-q", "main")
}

func git(dir string, args ...string) error {
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Fixture Author", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture Author", "GIT_COMMITTER_EMAIL=fixture@example.com",
		"GIT_AUTHOR_DATE="+fixedDate, "GIT_COMMITTER_DATE="+fixedDate,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
}
