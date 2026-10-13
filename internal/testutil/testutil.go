// Package testutil wires fixtures and fakes for tests across packages. It
// must only be imported from _test.go files.
package testutil

import (
	"os"
	"path/filepath"
	"testing"
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

// FixtureRepo returns testdata/fixture-repo, skipping the test if it has not
// been generated (`make fixtures`).
func FixtureRepo(t testing.TB) string {
	t.Helper()
	p := Testdata(t, "fixture-repo")
	if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
		t.Skip("fixture repo not generated; run `make fixtures`")
	}
	return p
}

// FixturePRs returns testdata/fixtures/prs.json (the offline GitHub fixture
// read by gh.LoadFixture), skipping the test if it does not exist yet.
func FixturePRs(t testing.TB) string {
	t.Helper()
	p := Testdata(t, "fixtures", "prs.json")
	if _, err := os.Stat(p); err != nil {
		t.Skip("testdata/fixtures/prs.json not present")
	}
	return p
}

// RecordingsDir returns testdata/recordings/<kind> (e.g. "jev", "review").
func RecordingsDir(t testing.TB, kind string) string {
	t.Helper()
	return Testdata(t, "recordings", kind)
}
