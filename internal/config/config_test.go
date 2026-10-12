package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultsWhenMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c, err := Load(Dirs(t.TempDir()))
	if err != nil || c.Source != "builtin" || c.Rank.WRequested != 3.0 || c.Pipeline.MaxGroupUnits != 8 {
		t.Fatalf("%+v %v", c, err)
	}
	r, err := LoadRouting(Dirs(""))
	if err != nil || len(r.Rules) != 5 || r.CostCapUSD != 0.40 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestLookupOrderAndOverlay(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	gitDir := t.TempDir()
	write(t, filepath.Join(xdg, "prt"), MainFile, "[user]\nrole = \"xdg\"\n")
	c, err := Load(Dirs(gitDir))
	if err != nil || c.User.Role != "xdg" || c.Rank.WBreaking != 2.0 {
		t.Fatalf("xdg: %+v %v", c, err)
	}
	write(t, filepath.Join(gitDir, "prt"), MainFile, "[user]\nrole = \"repo\"\n[rank]\nw_breaking = 9.0\n")
	c, err = Load(Dirs(gitDir))
	if err != nil || c.User.Role != "repo" || c.Rank.WBreaking != 9.0 || c.Rank.WRisk != 0.8 {
		t.Fatalf("repo: %+v %v", c, err)
	}
}

func TestUnknownKeyRejected(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gitDir := t.TempDir()
	write(t, filepath.Join(gitDir, "prt"), MainFile, "[rank]\nw_bogus = 1\n")
	if _, err := Load(Dirs(gitDir)); err == nil {
		t.Fatal("want error for unknown key")
	}
}

func TestRoutingRulesReplace(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gitDir := t.TempDir()
	write(t, filepath.Join(gitDir, "prt"), RoutingFile, "cost_cap_usd = 1.0\n[[rule]]\nwhen = \"true\"\ntier = \"deep\"\n")
	r, err := LoadRouting(Dirs(gitDir))
	if err != nil || len(r.Rules) != 1 || r.Rules[0].Tier != "deep" || r.DeepSlots != 3 || r.CostCapUSD != 1.0 {
		t.Fatalf("%+v %v", r, err)
	}
}
