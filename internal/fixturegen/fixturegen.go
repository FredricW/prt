// Package fixturegen builds testdata/fixture-repo deterministically from the
// committed sources in testdata/fixtures (used by tools/fixturegen and by
// tests, which generate a fresh copy into a temp dir):
//
//	testdata/fixtures/base/**             tree of the initial commit on main
//	testdata/fixtures/base-message.txt    optional message of that commit
//	testdata/fixtures/prs/NN-name.patch   one branch per patch, off main
//	testdata/fixtures/history/<branch>.<label>.patch
//	                                      an earlier head of <branch> (M4)
//	testdata/fixtures/prt.toml            copied to <out>/.git/prt/prt.toml
//
// A history patch is a plain diff applied on top of <branch>'s head; the
// resulting tree is committed with main as its only parent (like a head
// that was later force-pushed over) and stored at
// refs/fixture/<branch>/<label>, outside refs/heads so it is not a local
// branch. "Since you looked" fixtures use it as the head the viewer last
// reviewed (prs.json viewerReviewedSha).
//
// A patch in `git format-patch` mbox form (starts with "From ") is applied
// with `git am` (keeps its messages, authors and Date headers; the committer
// date is set to the author date). A plain diff is applied with
// `git apply --index` and committed with the file name as message. Committer
// identity, base commit and plain-diff dates are fixed, so SHAs are stable
// across machines and runs.
//
// The generated repo gets an `origin` remote pointing at a fictional GitHub
// repo (acme/portal unless Options.OriginURL says otherwise) so remote
// parsing works; nothing ever contacts it.
//
// GenerateWorkspace builds a multi-repo fixture workspace (M5.3) from a
// manifest; see Manifest.
//
// Usage:
//
//	go run ./tools/fixturegen -src testdata/fixtures -out testdata/fixture-repo
//	go run ./tools/fixturegen -workspace testdata/fixtures-workspace/manifest.json
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
	// baseMessage is the commit message of the base commit on main when the
	// sources have no base-message.txt.
	baseMessage = "chore: import portal monorepo"
	// baseMessageFile names the optional file in the sources that holds
	// the base commit's message.
	baseMessageFile = "base-message.txt"
)

// Options tunes GenerateWith.
type Options struct {
	// OriginURL is the generated repo's origin remote; "" means OriginURL.
	OriginURL string
}

// Generate recreates the repository at out from the fixture sources in src
// with the default Options. out must be inside a directory named testdata
// (it is deleted first).
func Generate(ctx context.Context, src, out string) error {
	return GenerateWith(ctx, src, out, Options{})
}

// GenerateWith recreates the repository at out from the fixture sources in
// src. out must be inside a directory named testdata (it is deleted first).
func GenerateWith(ctx context.Context, src, out string, o Options) error {
	absOut, err := outputDir(out)
	if err != nil {
		return err
	}
	msg, err := readBaseMessage(src)
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
	if err := commitBase(ctx, r, filepath.Join(src, "base"), msg); err != nil {
		return err
	}
	if err := applyPatches(ctx, r, filepath.Join(src, "prs")); err != nil {
		return err
	}
	if err := applyHistory(ctx, r, filepath.Join(src, "history")); err != nil {
		return err
	}
	if err := r.git(ctx, "checkout", "-q", "main"); err != nil {
		return err
	}
	origin := o.OriginURL
	if origin == "" {
		origin = OriginURL
	}
	if err := r.git(ctx, "remote", "add", "origin", origin); err != nil {
		return err
	}
	return installConfig(filepath.Join(src, "prt.toml"), absOut)
}

// readBaseMessage returns the base commit's message: the trimmed contents
// of <src>/base-message.txt, or baseMessage when the file is absent or
// blank.
func readBaseMessage(src string) (string, error) {
	b, err := os.ReadFile(filepath.Join(src, baseMessageFile))
	if errors.Is(err, fs.ErrNotExist) {
		return baseMessage, nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", baseMessageFile, err)
	}
	if msg := strings.TrimSpace(string(b)); msg != "" {
		return msg, nil
	}
	return baseMessage, nil
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

// commitBase copies the base tree (or a stub README when absent) and
// commits it on main with message msg.
func commitBase(ctx context.Context, r repo, base, msg string) error {
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
	return r.git(ctx, "commit", "-q", "-m", msg)
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

// HistoryRef is the ref an earlier head of branch is stored at.
func HistoryRef(branch, label string) string { return "refs/fixture/" + branch + "/" + label }

// applyHistory creates one earlier head per history patch, in file-name
// order (see the package doc). A missing directory is not an error.
func applyHistory(ctx context.Context, r repo, dir string) error {
	patches, err := filepath.Glob(filepath.Join(dir, "*.patch"))
	if err != nil {
		return fmt.Errorf("list history patches: %w", err)
	}
	sort.Strings(patches)
	for _, p := range patches {
		name := strings.TrimSuffix(filepath.Base(p), ".patch")
		branch, label, ok := strings.Cut(name, ".")
		if !ok || branch == "" || label == "" {
			return fmt.Errorf("history patch %s: want <branch>.<label>.patch", filepath.Base(p))
		}
		if err := applyEarlierHead(ctx, r, branch, label, p); err != nil {
			return fmt.Errorf("history %s: %w", name, err)
		}
	}
	return nil
}

func applyEarlierHead(ctx context.Context, r repo, branch, label, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve patch: %w", err)
	}
	if err := r.git(ctx, "checkout", "-q", branch); err != nil {
		return err
	}
	if err := r.git(ctx, "apply", "--index", abs); err != nil {
		return err
	}
	tree, err := r.gitOut(ctx, "write-tree")
	if err != nil {
		return err
	}
	sha, err := r.gitOut(ctx, "commit-tree", tree, "-p", "main", "-m", branch+" ("+label+")")
	if err != nil {
		return err
	}
	if err := r.git(ctx, "update-ref", HistoryRef(branch, label), sha); err != nil {
		return err
	}
	// Drop the applied changes from the index and work tree; the branch
	// itself never moved.
	return r.git(ctx, "checkout", "-q", "-f", "main")
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
	_, err := r.gitOut(ctx, args...)
	return err
}

// gitOut runs git and returns its trimmed standard output.
func (r repo) gitOut(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", r.dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Fixture Author", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture Author", "GIT_COMMITTER_EMAIL=fixture@example.com",
		"GIT_AUTHOR_DATE="+fixedDate, "GIT_COMMITTER_DATE="+fixedDate,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
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
