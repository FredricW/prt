package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunBuildsBranchPerPatch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustWrite(t, filepath.Join(src, "base", "a.txt"), "one\n")
	mustWrite(t, filepath.Join(src, "prs", "01-add-two.patch"),
		"diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1,2 @@\n one\n+two\n")
	out := filepath.Join(root, "testdata", "repo")

	if err := run(src, out); err != nil {
		t.Fatal(err)
	}
	first := revParse(t, out, "01-add-two")
	if err := run(src, out); err != nil {
		t.Fatal(err)
	}
	if again := revParse(t, out, "01-add-two"); again != first {
		t.Fatalf("SHAs must be deterministic: %s vs %s", first, again)
	}
	if err := run(src, filepath.Join(root, "elsewhere")); err == nil {
		t.Fatal("must refuse output outside testdata")
	}
}

func mustWrite(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func revParse(t *testing.T, dir, rev string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", "-C", dir, "rev-parse", rev).Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
