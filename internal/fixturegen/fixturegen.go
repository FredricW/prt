// Package fixturegen builds testdata/fixture-repo deterministically from the
// committed sources in testdata/fixtures (used by tools/fixturegen and by
// tests, which generate a fresh copy into a temp dir):
//
//	testdata/fixtures/base/**             tree of the initial commit on main
//	testdata/fixtures/prs/NN-name.patch   one branch per patch, off main
//	testdata/fixtures/prt.toml            copied to <out>/.git/prt/prt.toml
//
// A patch in `git format-patch` mbox form (starts with "From ") is applied
// with `git am` (keeps its messages, authors and Date headers; the committer
// date is set to the author date). A plain diff is applied with
// `git apply --index` and committed with the file name as message. Committer
// identity, base commit and plain-diff dates are fixed, so SHAs are stable
// across machines and runs.
//
// The generated repo gets an `origin` remote pointing at the fictional
// GitHub repo acme/portal so remote parsing works; nothing ever contacts it.
//
// Usage: go run ./tools/fixturegen -src testdata/fixtures -out testdata/fixture-repo
package fixturegen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// fixedDate is the author and committer date of the base commit and of
	// commits made from plain diffs.
	fixedDate = "2026-10-01T09:00:00Z"
	// OriginURL is the fictional remote of the fixture repo.
	OriginURL = "https://github.com/acme/portal.git"
	// baseMessage is the commit message of the base commit on main.
	baseMessage = "chore: import portal monorepo"
)

// Generate recreates the repository at out from the fixture sources in src.
// out must be inside a directory named testdata (it is deleted first).
func Generate(ctx context.Context, src, out string) error {
	absOut, err := outputDir(out)
	if err != nil {
		return err
	}
	if err := resetDir(absOut); err != nil {
		return err
	}
	r := repo{dir: absOut}
	if err := r.git(ctx, "init", "-q", "-b", "main"); err != nil {
		return err
	}
	if err := commitBase(ctx, r, filepath.Join(src, "base")); err != nil {
		return err
	}
	if err := applyPatches(ctx, r, filepath.Join(src, "prs")); err != nil {
		return err
	}
	if err := r.git(ctx, "checkout", "-q", "main"); err != nil {
		return err
	}
	if err := r.git(ctx, "remote", "add", "origin", OriginURL); err != nil {
		return err
	}
	return installConfig(filepath.Join(src, "prt.toml"), absOut)
}

// outputDir resolves out and refuses anything not under a testdata directory,
// because the directory is deleted before generation.
func outputDir(out string) (string, error) {
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", fmt.Errorf("resolve output %s: %w", out, err)
	}
	if !strings.Contains(filepath.ToSlash(abs)+"/", "/testdata/") || filepath.Base(abs) == "testdata" {
		return "", fmt.Errorf("refusing to recreate %s: output must be inside a testdata directory", abs)
	}
	return abs, nil
}

func resetDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove %s: %w", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}

// commitBase copies the base tree (or a stub README when absent) and commits it on main.
func commitBase(ctx context.Context, r repo, base string) error {
	if _, err := os.Stat(base); err == nil {
		if err := copyTree(base, r.dir); err != nil {
			return fmt.Errorf("copy base: %w", err)
		}
	} else if err := os.WriteFile(filepath.Join(r.dir, "README.md"), []byte("# fixture repo\n"), 0o644); err != nil {
		return fmt.Errorf("write stub README: %w", err)
	}
	if err := r.git(ctx, "add", "-A"); err != nil {
		return err
	}
	return r.git(ctx, "commit", "-q", "-m", baseMessage)
}

// applyPatches creates one branch off main per patch file, in file-name order.
func applyPatches(ctx context.Context, r repo, dir string) error {
	patches, err := filepath.Glob(filepath.Join(dir, "*.patch"))
	if err != nil {
		return fmt.Errorf("list patches: %w", err)
	}
	sort.Strings(patches)
	for _, p := range patches {
		name := strings.TrimSuffix(filepath.Base(p), ".patch")
		if err := applyPatch(ctx, r, name, p); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func applyPatch(ctx context.Context, r repo, branch, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve patch: %w", err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return fmt.Errorf("read patch: %w", err)
	}
	if err := r.git(ctx, "checkout", "-q", "-b", branch, "main"); err != nil {
		return err
	}
	if bytes.HasPrefix(body, []byte("From ")) {
		return r.git(ctx, "am", "-q", "--committer-date-is-author-date", abs)
	}
	if err := r.git(ctx, "apply", "--index", abs); err != nil {
		return err
	}
	return r.git(ctx, "commit", "-q", "-m", branch)
}

// installConfig copies the fixture prt.toml into <repo>/.git/prt/ if it exists.
func installConfig(src, repoDir string) error {
	b, err := os.ReadFile(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	dir := filepath.Join(repoDir, ".git", "prt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prt.toml"), b, 0o644); err != nil {
		return fmt.Errorf("write prt.toml: %w", err)
	}
	return nil
}

// repo runs git in a fixed directory with an isolated, deterministic environment.
type repo struct{ dir string }

func (r repo) git(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.dir}, args...)...)
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
