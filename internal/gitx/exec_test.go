package gitx

import (
	"context"
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
}
