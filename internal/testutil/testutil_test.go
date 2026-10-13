package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/fredricw/prt/internal/fixturegen"
)

func TestFixtureWorkspace(t *testing.T) {
	ws := FixtureWorkspace(t)
	if filepath.Base(ws) != "fixture-workspace" || !filepath.IsAbs(ws) {
		t.Errorf("FixtureWorkspace = %s, want an absolute .../testdata/fixture-workspace", ws)
	}
	for _, dir := range []string{"portal", "billing-svc"} {
		if _, err := os.Stat(filepath.Join(ws, dir, ".git", "prt", "prt.toml")); err != nil {
			t.Errorf("%s: want a repo with prt.toml installed: %v", dir, err)
		}
	}
}

func TestFixtureWorkspaceManifest(t *testing.T) {
	p := FixtureWorkspaceManifest(t)
	if !filepath.IsAbs(p) {
		t.Errorf("FixtureWorkspaceManifest = %s, want an absolute path", p)
	}
	m, err := fixturegen.LoadManifest(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.PRsFor("portal"), FixturePRs(t); got != want {
		t.Errorf("PRsFor(portal) = %s, want %s", got, want)
	}
}
