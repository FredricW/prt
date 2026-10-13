package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/testutil"
)

// isolate keeps tests away from the user's config, state (workspace logs
// and sockets, pane widths), keys and GitHub login.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
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
	return gitRepoAt(t, t.TempDir(), origin)
}

// gitRepoAt initialises dir (created when missing) as gitRepo does.
func gitRepoAt(t *testing.T, dir, origin string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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

// A directory outside any repository is a workspace root (M5.3); with no
// repository under it prt fails and says so. A missing directory is still a
// plain error.
func TestNotARepo(t *testing.T) {
	isolate(t)
	if code, _, errOut := runPrt("--offline", "--snapshot", "--repo", t.TempDir()); code != 1 || !strings.Contains(errOut, "no git repositories under") {
		t.Fatalf("code %d err %q", code, errOut)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if code, _, errOut := runPrt("--offline", "--snapshot", "--repo", missing); code != 1 || errOut == "" || strings.Contains(errOut, "no git repositories") {
		t.Fatalf("missing dir: code %d err %q", code, errOut)
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
	// M5: the header names the repo and role from setup, sections are
	// dividers, and the Architecture tab renders.
	for _, want := range []string{"NEEDS YOU", "#101", "=== screen:", "prt · acme/portal · ", "picker + architecture"} {
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

// --jev-replay / --embed-replay wire recorded clients even offline, note the
// source, and read config only from the repo (a broken XDG config is unseen).
func TestReplayFlags(t *testing.T) {
	isolate(t)
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if err := os.MkdirAll(filepath.Join(xdg, "prt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(xdg, "prt", "prt.toml"), []byte("[rank\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := gitRepo(t, "")
	env, err := setup(context.Background(), options{repo: repo, offline: true, jevReplay: "rec/jev", embedReplay: "rec/embed", reviewReplay: "rec/review"})
	if err != nil {
		t.Fatalf("replay must ignore the XDG config: %v", err)
	}
	defer env.close()
	for tier, want := range map[string]string{"fast": env.deps.Config.Models.Fast, "deep": env.deps.Config.Models.Deep} {
		rev := env.deps.Reviewers.Fast
		if tier == "deep" {
			rev = env.deps.Reviewers.Deep
		}
		if r, ok := rev.(*review.Replay); !ok || r.Dir != "rec/review" || r.Name != want || !r.MissUnavailable {
			t.Errorf("%s reviewer = %#v, want a MissUnavailable replay of rec/review as %q", tier, rev, want)
		}
	}
	if r, ok := env.deps.Jev.(*jev.Replay); !ok || r.Dir != "rec/jev" || !r.MissUnavailable {
		t.Errorf("Jev = %#v, want a MissUnavailable replay of rec/jev", env.deps.Jev)
	}
	if r, ok := env.deps.Embedder.(*embed.Replay); !ok || r.Dir != "rec/embed" {
		t.Errorf("Embedder = %#v, want a replay of rec/embed", env.deps.Embedder)
	}
	if want := "Jev: replay of rec/jev · embeddings: replay of rec/embed · reviews: replay of rec/review (offline)"; env.srcNote != want {
		t.Errorf("note = %q, want %q", env.srcNote, want)
	}
	if _, err := setup(context.Background(), options{repo: repo, offline: true}); err == nil {
		t.Error("without replay the (broken) XDG config must still be read")
	}
}

// The fixture snapshot with recorded answers renders the by-change screen,
// and the replayed reviews land: on #101's Pipeline page most routed groups
// are done (under the default cap) and findings are counted.
func TestSnapshotReplay(t *testing.T) {
	isolate(t)
	repo := testutil.FixtureRepo(t)
	rec := filepath.Join("..", "..", "testdata", "recordings")
	code, out, errOut := runPrt("--offline", "--snapshot", "--now", "2026-10-13T12:00:00Z",
		"--width", "140", "--height", "60",
		"--jev-replay", filepath.Join(rec, "jev"), "--embed-replay", filepath.Join(rec, "embed"),
		"--review-replay", filepath.Join(rec, "review"),
		"--repo", repo, "--fixtures", testutil.FixturePRs(t), "--max-items", "1")
	if code != 0 {
		t.Fatalf("code %d err %q", code, errOut)
	}
	if !strings.Contains(out, "code-by-change") {
		t.Errorf("snapshot lacks the by-change screen:\n%s", out)
	}
	_, pipe, ok := strings.Cut(out, "=== screen: picker + pipeline · pr:101 ===")
	if !ok {
		t.Fatalf("snapshot lacks #101's pipeline screen:\n%s", out)
	}
	m := regexp.MustCompile(`(\d+) groups: done (\d+)`).FindStringSubmatch(pipe)
	if m == nil {
		t.Fatalf("pipeline screen lacks the totals status line:\n%s", pipe)
	}
	groups, _ := strconv.Atoi(m[1])
	done, _ := strconv.Atoi(m[2])
	if groups == 0 || done*4 < groups*3 {
		t.Errorf("#101: %d of %d groups reviewed in the replay snapshot, want at least three quarters", done, groups)
	}
	f := regexp.MustCompile(`findings (\d+)`).FindStringSubmatch(pipe)
	if f == nil || f[1] == "0" {
		t.Errorf("#101: no findings in the replay snapshot (%v)", f)
	}
}

// The M4 demo data: --drafts seeds the fixture drafts, and the replay
// snapshot shows them, an ai-draft, an open thread's ❱ marker and the
// "Since you looked" block with the recorded comment_addressed answers.
func TestSnapshotComments(t *testing.T) {
	isolate(t)
	repo := testutil.FixtureRepo(t)
	rec := filepath.Join("..", "..", "testdata", "recordings")
	args := []string{"--offline", "--snapshot", "--now", "2026-10-13T12:00:00Z",
		"--jev-replay", filepath.Join(rec, "jev"), "--embed-replay", filepath.Join(rec, "embed"),
		"--review-replay", filepath.Join(rec, "review"),
		"--repo", repo, "--fixtures", testutil.FixturePRs(t), "--max-items", "1",
		"--drafts", filepath.Join("..", "..", "testdata", "fixtures", "drafts.json")}
	code, out, errOut := runPrt(args...)
	if code != 0 {
		t.Fatalf("code %d err %q", code, errOut)
	}
	screen := func(name string) string {
		_, s, ok := strings.Cut(out, "=== screen: picker + "+name+" · pr:101 ===")
		if !ok {
			t.Fatalf("snapshot lacks #101's %s screen:\n%s", name, out)
		}
		s, _, _ = strings.Cut(s, "=== screen:")
		return s
	}
	since := screen("overview-since")
	for _, want := range []string{"Since you looked", "since e661461 (your last review)", "✓ addressed", "✗ not addressed"} {
		if !strings.Contains(since, want) {
			t.Errorf("overview-since lacks %q:\n%s", want, since)
		}
	}
	if s := screen("code-draft"); !strings.Contains(s, "✎ draft · you") || !strings.Contains(s, "2 drafts") {
		t.Errorf("code-draft screen lacks the seeded draft:\n%s", s)
	}
	if s := screen("code-ai-draft"); !strings.Contains(s, "╭─ ai-draft · ") || strings.Contains(s, "! [") {
		t.Errorf("code-ai-draft screen lacks the ai-draft or still draws its finding inline:\n%s", s)
	}
	if s := screen("code-thread"); !strings.Contains(s, "❱") {
		t.Errorf("code-thread screen lacks the thread marker:\n%s", s)
	}
	// Seeding again keeps what is stored: still two drafts, not four.
	if code, out, errOut = runPrt(args...); code != 0 || !strings.Contains(screen("code-draft"), " · 2 drafts · 0 pending") {
		t.Errorf("second seeding: code %d err %q, want 2 drafts kept", code, errOut)
	}
}

func TestSeedDraftsRefusesNonDrafts(t *testing.T) {
	isolate(t)
	repo := gitRepo(t, "")
	bad := filepath.Join(t.TempDir(), "d.json")
	if err := os.WriteFile(bad, []byte(`[{"id":"x","itemId":"pr:1","path":"a.go","line":1,"side":"new","body":"b","source":"github","state":"posted"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runPrt("--offline", "--snapshot", "--repo", repo, "--drafts", bad); code != 1 || !strings.Contains(errOut, "not a user draft") {
		t.Errorf("code %d err %q, want a refusal", code, errOut)
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
