package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/testutil"
)

// isolate keeps tests away from the user's config, keys and GitHub login.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("JEV_API_KEY", "")
	t.Setenv("PRT_ANTHROPIC_API_KEY", "")
	t.Setenv("OLLAMA_HOST", "http://127.0.0.1:1")
	prev := ghToken
	ghToken = func(context.Context) (string, error) { return "", gh.ErrNoToken }
	t.Cleanup(func() { ghToken = prev })
}

// gitRepo creates an empty-history git repo with an optional GitHub origin.
func gitRepo(t *testing.T, origin string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", origin}} {
		if args[0] == "remote" && origin == "" {
			continue
		}
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func runPrt(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func writePrtToml(t *testing.T, repo, body string) {
	t.Helper()
	dir := filepath.Join(repo, ".git", "prt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prt.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestVersion(t *testing.T) {
	code, out, _ := runPrt("--version")
	if code != 0 || !strings.HasPrefix(out, "prt ") {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestBadFlags(t *testing.T) {
	if code, _, _ := runPrt("--bogus"); code != 2 {
		t.Errorf("unknown flag: code %d", code)
	}
	if code, _, errOut := runPrt("--now", "yesterday"); code != 2 || !strings.Contains(errOut, "--now") {
		t.Errorf("--now parse error: code %d err %q", code, errOut)
	}
}

func TestNotARepo(t *testing.T) {
	isolate(t)
	if code, _, errOut := runPrt("--offline", "--snapshot", "--repo", t.TempDir()); code != 1 || errOut == "" {
		t.Fatalf("code %d err %q", code, errOut)
	}
}

func TestSnapshotAgainstFixtures(t *testing.T) {
	isolate(t)
	repo := testutil.FixtureRepo(t)
	code, out, errOut := runPrt("--offline", "--snapshot", "--now", "2026-10-13T12:00:00Z",
		"--repo", repo, "--fixtures", testutil.FixturePRs(t), "--max-items", "1")
	if code != 0 {
		t.Fatalf("code %d err %q", code, errOut)
	}
	for _, want := range []string{"Needs you", "#101", "=== screen:"} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot lacks %q:\n%s", want, out)
		}
	}
}

func TestConfigUnknownKeyWarnsMalformedFails(t *testing.T) {
	isolate(t)
	repo := gitRepo(t, "")
	writePrtToml(t, repo, "[models]\njev_endpoint = \"https://x\"\n[rank]\nw_bogus = 1\n")
	if code, _, errOut := runPrt("--offline", "--snapshot", "--repo", repo); code != 0 {
		t.Fatalf("unknown keys must only warn: code %d err %q", code, errOut)
	}
	log, err := os.ReadFile(filepath.Join(repo, ".git", "prt", "prt.log"))
	if err != nil || !strings.Contains(string(log), "jev_endpoint") || !strings.Contains(string(log), "w_bogus") {
		t.Errorf("warnings not logged: %v\n%s", err, log)
	}
	writePrtToml(t, repo, "[rank\n")
	if code, _, errOut := runPrt("--offline", "--snapshot", "--repo", repo); code != 1 || !strings.Contains(errOut, "config") {
		t.Fatalf("malformed TOML: code %d err %q", code, errOut)
	}
}

// Without keys or a gh login the online setup degrades instead of failing.
func TestSetupDegradesWithoutKeys(t *testing.T) {
	isolate(t)
	repo := gitRepo(t, "https://github.com/acme/portal.git")
	env, err := setup(context.Background(), options{repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	defer env.close()
	if env.deps.GH != nil {
		t.Error("no gh token: GitHub must be disabled")
	}
	if !strings.Contains(env.ghNote, "no token") {
		t.Errorf("GitHub note = %q, want the reason (no token)", env.ghNote)
	}
	if env.deps.Jev == nil || env.deps.Reviewers.Fast == nil || env.deps.Reviewers.Deep == nil {
		t.Errorf("clients must exist and report missing keys at call time: %+v", env.deps)
	}
	off, err := setup(context.Background(), options{repo: repo, offline: true})
	if err != nil {
		t.Fatal(err)
	}
	defer off.close()
	if off.deps.GH != nil || off.deps.Jev != nil || off.deps.Embedder != nil || off.deps.Reviewers.Fast != nil {
		t.Errorf("--offline must disable every external client: %+v", off.deps)
	}
	if !strings.Contains(off.ghNote, "offline") {
		t.Errorf("offline GitHub note = %q", off.ghNote)
	}
}

// --now must reach the pipeline (urgency age term), not just the TUI.
func TestNowReachesPipeline(t *testing.T) {
	isolate(t)
	repo := gitRepo(t, "")
	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	env, err := setup(context.Background(), options{repo: repo, offline: true, nowFn: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
	}
	defer env.close()
	if env.deps.Now == nil || !env.deps.Now().Equal(fixed) {
		t.Fatal("pipeline clock is not the --now clock")
	}
}
