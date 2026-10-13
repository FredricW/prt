package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestUnknownKeyWarns(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gitDir := t.TempDir()
	write(t, filepath.Join(gitDir, "prt"), MainFile, "[rank]\nw_bogus = 1\nw_risk = 1.5\n")
	c, err := Load(Dirs(gitDir))
	if err != nil {
		t.Fatalf("unknown key must not be fatal: %v", err)
	}
	if c.Rank.WRisk != 1.5 || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "rank.w_bogus") {
		t.Fatalf("got %+v warnings %q", c.Rank, c.Warnings)
	}
}

func TestSyntaxErrorIsFatal(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gitDir := t.TempDir()
	write(t, filepath.Join(gitDir, "prt"), MainFile, "[rank\nw_risk = \n")
	if _, err := Load(Dirs(gitDir)); err == nil {
		t.Fatal("want error for malformed TOML")
	}
}

// specSample is the spec's prt.toml example (Configuration), verbatim.
const specSample = `[user]
role = "frontend"            # free text, sent to Jev with relevance questions
owns = ["web/**", "packages/ui/**"]

[github]
repo = "acme/portal"         # default: origin remote
release_branches = ["release/*"]

[rank]
w_requested = 3.0
w_breaking = 2.0
w_risk = 0.8
w_test_gap = 0.6
w_relevance = 0.5
w_release = 2.0
w_ci_red = 2.5
w_age = 0.4
w_minutes = 0.02

[pipeline]
max_group_units = 8
embed_min_cosine = 0.72
keep_pair_p = 0.60
unsure_below = 0.60
concurrency = 4

[models]
embed = "onnx:models/code-embed-small.onnx"   # or "ollama:nomic-embed-code"
jev_endpoint = "https://api.<jev-domain>/v1/systemone"
fast = "anthropic:claude-haiku-5-5"
deep = "anthropic:claude-opus-5-5"
`

func TestSpecSampleLoads(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	gitDir := t.TempDir()
	write(t, filepath.Join(gitDir, "prt"), MainFile, specSample)
	c, err := Load(Dirs(gitDir))
	if err != nil {
		t.Fatalf("spec sample must load: %v", err)
	}
	if c.User.Role != "frontend" || len(c.User.Owns) != 2 || c.GitHub.Repo != "acme/portal" {
		t.Fatalf("decoded %+v", c)
	}
	if c.Models.JevBaseURL != "" || c.Pipeline.RefreshSeconds != 60 {
		t.Fatalf("jev_endpoint must not become the base URL; defaults kept: %+v", c.Models)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "jev_endpoint") {
		t.Fatalf("want one jev_endpoint warning, got %q", c.Warnings)
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
