package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitInit creates a throwaway local repository (no network involved).
func gitInit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "base")
	run("checkout", "-q", "-b", "feat/x")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("commit", "-q", "-am", "add two")
	return dir
}

func TestOpenOutsideWorkTree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	// Stop git's upward search at the temp dir, in case it sits in a repo.
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir))
	if _, err := Open(context.Background(), dir); !errors.Is(err, ErrNotWorkTree) {
		t.Fatalf("Open(non-repo) = %v, want ErrNotWorkTree", err)
	}
}

func TestExecBasics(t *testing.T) {
	dir := gitInit(t)
	ctx := context.Background()
	g, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	def, err := g.DefaultBranch(ctx)
	if err != nil || def != "main" {
		t.Fatalf("default branch = %q, %v", def, err)
	}
	refs, err := g.Refs(ctx, false)
	if err != nil || len(refs) != 2 {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
	mb, err := g.MergeBase(ctx, "main", "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	n, err := g.CountAhead(ctx, mb, "feat/x")
	if err != nil || n != 1 {
		t.Fatalf("ahead = %d, %v", n, err)
	}
	d, err := g.Diff(ctx, mb, "feat/x")
	if err != nil || !strings.Contains(d, "+two") {
		t.Fatalf("diff = %q, %v", d, err)
	}
	b, err := g.Show(ctx, "main", "a.txt")
	if err != nil || string(b) != "one\n" {
		t.Fatalf("show = %q, %v", b, err)
	}
	if _, err := g.Show(ctx, "main", "missing"); err == nil {
		t.Fatal("expected ErrNotFound")
	}
	dirty, err := g.IsDirty(ctx)
	if err != nil || dirty {
		t.Fatalf("dirty = %v, %v", dirty, err)
	}
	if got := CompareRef(ctx, g, "main"); got != "main" {
		t.Errorf("CompareRef without origin = %q", got)
	}
}

// ListFiles returns names git would C-quote in line mode (quote, backslash,
// tab) and non-ASCII names verbatim, and each can be read back with Show.
func TestExecListFilesSpecialNames(t *testing.T) {
	dir := gitInit(t)
	ctx := context.Background()
	names := []string{"web/café.ts", "web/a\"b.ts", "web/back\\slash.ts", "web/tab\there.ts", " lead.ts"}
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "names"}} {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	g, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	files, err := g.ListFiles(ctx, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, f := range files {
		have[f] = true
	}
	for _, n := range append(names, "a.txt") {
		if !have[n] {
			t.Errorf("ListFiles misses %q (got %q)", n, files)
			continue
		}
		if _, err := g.Show(ctx, "HEAD", n); err != nil {
			t.Errorf("Show %q: %v", n, err)
		}
	}
}

// A ref that names a tracked file must never be read as a pathspec, which
// would discard the user's unstaged edits to that file.
func TestExecCheckoutNeverPathspec(t *testing.T) {
	dir := gitInit(t)
	ctx := context.Background()
	g, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	edited := []byte("one\ntwo\nlocal edit\n")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := g.Checkout(ctx, "a.txt"); err == nil {
		t.Fatal("checkout of a path-named non-ref must fail")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != string(edited) {
		t.Fatalf("unstaged edit lost: %q", b)
	}
	if err := g.Checkout(ctx, "--orphan"); err == nil {
		t.Fatal("option-like ref must be refused")
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := g.Checkout(ctx, "main"); err != nil {
		t.Fatalf("checkout main: %v", err)
	}
}
