// Package testutil wires fixtures and fakes for tests across packages. It
// must only be imported from _test.go files.
package testutil

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/fredricw/prt/internal/fixturegen"
)

// RepoRoot returns the module root (the directory holding go.mod).
func RepoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("testutil: go.mod not found")
		}
		dir = parent
	}
}

// Testdata returns an absolute path under <root>/testdata.
func Testdata(t testing.TB, elem ...string) string {
	t.Helper()
	return filepath.Join(append([]string{RepoRoot(t), "testdata"}, elem...)...)
}

// ReadTestdata reads a file under testdata.
func ReadTestdata(t testing.TB, elem ...string) []byte {
	t.Helper()
	b, err := os.ReadFile(Testdata(t, elem...))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// FixtureRepo generates a fresh fixture repo from the committed sources in
// testdata/fixtures into a temp dir and returns its path, so tests never
// depend on (or skip because of) a stale or missing testdata/fixture-repo.
// It needs the git binary and fails the test if generation fails.
func FixtureRepo(t testing.TB) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	out := filepath.Join(t.TempDir(), "testdata", "fixture-repo")
	if err := fixturegen.Generate(context.Background(), Testdata(t, "fixtures"), out); err != nil {
		t.Fatalf("generate fixture repo: %v", err)
	}
	return out
}

// FixturePRs returns testdata/fixtures/prs.json (the offline GitHub fixture
// read by gh.LoadFixture); it fails the test if the file is missing.
func FixturePRs(t testing.TB) string {
	t.Helper()
	p := Testdata(t, "fixtures", "prs.json")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("testdata/fixtures/prs.json: %v", err)
	}
	return p
}

// RecordingsDir returns testdata/recordings/<kind> (e.g. "jev", "review").
func RecordingsDir(t testing.TB, kind string) string {
	t.Helper()
	return Testdata(t, "recordings", kind)
}
