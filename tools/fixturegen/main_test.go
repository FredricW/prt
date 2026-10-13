package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/gh"
)

// fixtureSrc is the committed fixture source tree, relative to this package.
const fixtureSrc = "../../testdata/fixtures"

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func TestRunPlainDiffIsDeterministic(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	mustWrite(t, filepath.Join(src, "base", "a.txt"), "one\n")
	mustWrite(t, filepath.Join(src, "prs", "01-add-two.patch"),
		"diff --git a/a.txt b/a.txt\n--- a/a.txt\n+++ b/a.txt\n@@ -1 +1,2 @@\n one\n+two\n")
	out := filepath.Join(root, "testdata", "repo")

	if err := run(t.Context(), src, out); err != nil {
		t.Fatal(err)
	}
	first := gitOut(t, out, "rev-parse", "01-add-two")
	if err := run(t.Context(), src, out); err != nil {
		t.Fatal(err)
	}
	if again := gitOut(t, out, "rev-parse", "01-add-two"); again != first {
		t.Fatalf("SHAs must be deterministic: %s vs %s", first, again)
	}
	if _, err := os.Stat(filepath.Join(out, ".git", "prt", "prt.toml")); !os.IsNotExist(err) {
		t.Fatalf("prt.toml must only be installed when the source has one (stat err %v)", err)
	}
}

func TestRunRefusesOutputOutsideTestdata(t *testing.T) {
	root := t.TempDir()
	for _, out := range []string{filepath.Join(root, "elsewhere"), filepath.Join(root, "testdata")} {
		if err := run(t.Context(), filepath.Join(root, "src"), out); err == nil {
			t.Errorf("run(%s) must refuse", out)
		}
	}
}

// branchShape is what each fixture branch must look like relative to main.
type branchShape struct {
	name   string
	ahead  int
	pr     int // 0 for local-only branches
	assert func(t *testing.T, repo string, files []numstat)
}

var branches = []branchShape{
	{"01-user-profile-refactor", 1, 101, assertMixedRefactor},
	{"02-rename-fetchjson", 1, 102, assertMechanicalRename},
	{"03-remove-legacy-billing", 1, 103, assertDeletionsOnly},
	{"04-bump-deps", 1, 104, assertDependencyBump},
	{"05-spike-dark-mode", 2, 0, func(*testing.T, string, []numstat) {}},
}

func TestFixtureRepo(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	repo := filepath.Join(root, "testdata", "fixture-repo")
	if err := run(t.Context(), fixtureSrc, repo); err != nil {
		t.Fatal(err)
	}

	t.Run("deterministic", func(t *testing.T) {
		other := filepath.Join(root, "testdata", "fixture-repo-again")
		if err := run(t.Context(), fixtureSrc, other); err != nil {
			t.Fatal(err)
		}
		if a, b := refs(t, repo), refs(t, other); a != b {
			t.Fatalf("refs differ between runs:\n%s\nvs\n%s", a, b)
		}
	})

	t.Run("config and remote", func(t *testing.T) {
		want, err := os.ReadFile(filepath.Join(fixtureSrc, "prt.toml"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(repo, ".git", "prt", "prt.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("prt.toml = %q, want %q", got, want)
		}
		if url := gitOut(t, repo, "remote", "get-url", "origin"); url != originURL {
			t.Errorf("origin = %q, want %q", url, originURL)
		}
		if head := gitOut(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); head != "main" {
			t.Errorf("HEAD = %q, want main", head)
		}
	})

	for _, b := range branches {
		t.Run(b.name, func(t *testing.T) {
			ahead := gitOut(t, repo, "rev-list", "--count", "main.."+b.name)
			if ahead != strconv.Itoa(b.ahead) {
				t.Errorf("%s is %s commits ahead of main, want %d", b.name, ahead, b.ahead)
			}
			if behind := gitOut(t, repo, "rev-list", "--count", b.name+"..main"); behind != "0" {
				t.Errorf("%s is %s commits behind main, want 0", b.name, behind)
			}
			b.assert(t, repo, diffNumstat(t, repo, b.name))
		})
	}

	t.Run("prs.json", func(t *testing.T) {
		assertPRFixture(t, repo)
	})

	t.Run("earlier head of #101", func(t *testing.T) {
		assertEarlierHead(t, repo)
	})
}

// assertEarlierHead checks the history ref behind #101's "Since you
// looked" fixture: a sibling of the branch head off main, differing only in
// useProfileCache.ts, named by prs.json viewerReviewedSha, and the comments
// of the viewer's threads were made on it.
func assertEarlierHead(t *testing.T, repo string) {
	t.Helper()
	ref := "refs/fixture/01-user-profile-refactor/v1"
	sha := gitOut(t, repo, "rev-parse", ref)
	if parents := gitOut(t, repo, "rev-list", "--parents", "-n", "1", ref); parents != sha+" "+gitOut(t, repo, "rev-parse", "main") {
		t.Errorf("%s parents = %q, want main only", ref, parents)
	}
	if files := gitOut(t, repo, "diff", "--name-only", ref, "01-user-profile-refactor"); files != "web/src/hooks/useProfileCache.ts" {
		t.Errorf("v1..head changes %q, want only web/src/hooks/useProfileCache.ts", files)
	}
	if heads := gitOut(t, repo, "for-each-ref", "--contains", sha, "refs/heads"); heads != "" {
		t.Errorf("v1 must not be on a local branch, found %q", heads)
	}
	fake, err := gh.LoadFixture(filepath.Join(fixtureSrc, "prs.json"))
	if err != nil {
		t.Fatal(err)
	}
	pr := fake.PRs[101]
	if pr.ViewerReviewedSHA != sha {
		t.Errorf("#101 viewerReviewedSha = %s, generated v1 %s", pr.ViewerReviewedSHA, sha)
	}
	mine := 0
	for _, th := range fake.ThreadMap[101] {
		for _, c := range th.Comments {
			if c.ThreadID != th.ID || c.Path != th.Path || c.Line != th.Line {
				t.Errorf("thread %s comment %d: thread/path/line %s %s:%d", th.ID, c.GHID, c.ThreadID, c.Path, c.Line)
			}
			if c.Author == fake.Login && c.InReplyTo == 0 {
				mine++
				if c.CommitSHA != sha {
					t.Errorf("viewer comment %d made on %s, want v1 %s", c.GHID, c.CommitSHA, sha)
				}
			}
		}
	}
	if mine != 2 {
		t.Errorf("viewer has %d top-level comments on #101, want 2", mine)
	}
}

func assertPRFixture(t *testing.T, repo string) {
	t.Helper()
	fake, err := gh.LoadFixture(filepath.Join(fixtureSrc, "prs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fake.Login != "fredric" {
		t.Errorf("viewer = %q, want fredric", fake.Login)
	}
	mainSHA := gitOut(t, repo, "rev-parse", "main")
	seen := map[int]bool{}
	for _, b := range branches {
		if b.pr == 0 {
			for n, pr := range fake.PRs {
				if pr.HeadRef == b.name {
					t.Errorf("local-only branch %s must not have a PR, found #%d", b.name, n)
				}
			}
			continue
		}
		pr, ok := fake.PRs[b.pr]
		if !ok {
			t.Errorf("PR #%d missing", b.pr)
			continue
		}
		seen[b.pr] = true
		if pr.HeadRef != b.name || pr.BaseRef != "main" {
			t.Errorf("#%d refs = %s<-%s, want main<-%s", b.pr, pr.BaseRef, pr.HeadRef, b.name)
		}
		if head := gitOut(t, repo, "rev-parse", b.name); pr.HeadSHA != head {
			t.Errorf("#%d headSha = %s, generated %s", b.pr, pr.HeadSHA, head)
		}
		if pr.BaseSHA != mainSHA {
			t.Errorf("#%d baseSha = %s, main is %s", b.pr, pr.BaseSHA, mainSHA)
		}
		add, del := totals(diffNumstat(t, repo, b.name))
		files := len(diffNumstat(t, repo, b.name))
		if pr.Additions != add || pr.Deletions != del || pr.ChangedFiles != files {
			t.Errorf("#%d stats = +%d -%d %d files, git says +%d -%d %d files",
				b.pr, pr.Additions, pr.Deletions, pr.ChangedFiles, add, del, files)
		}
		if !strings.Contains(pr.Body, "## Changes") {
			t.Errorf("#%d body has no '## Changes' section", b.pr)
		}
	}
	if len(fake.PRs) != len(seen) {
		t.Errorf("prs.json has %d PRs, expected exactly %d", len(fake.PRs), len(seen))
	}
}

func assertMixedRefactor(t *testing.T, _ string, files []numstat) {
	t.Helper()
	add, del := totals(files)
	if n := add + del; n < 300 || n > 500 {
		t.Errorf("changed lines = %d, want 300-500", n)
	}
	for _, want := range []string{".ts", ".tsx", ".go"} {
		if !anyFile(files, func(p string) bool { return strings.HasSuffix(p, want) }) {
			t.Errorf("no %s file changed", want)
		}
	}
	if !anyFile(files, func(p string) bool { return strings.Contains(p, ".test.") || strings.HasSuffix(p, "_test.go") }) {
		t.Error("no test file changed")
	}
}

func assertMechanicalRename(t *testing.T, repo string, files []numstat) {
	t.Helper()
	if len(files) != 20 {
		t.Errorf("changed files = %d, want 20", len(files))
	}
	for path, h := range hunkLines(t, repo, "02-rename-fetchjson") {
		if len(h.removed) == 0 || len(h.removed) != len(h.added) {
			t.Errorf("%s: %d removed vs %d added lines", path, len(h.removed), len(h.added))
			continue
		}
		for i, old := range h.removed {
			if want := strings.ReplaceAll(old, "fetchJSON", "requestJSON"); want != h.added[i] || want == old {
				t.Errorf("%s: %q -> %q is not a fetchJSON->requestJSON rename", path, old, h.added[i])
			}
		}
	}
}

func assertDeletionsOnly(t *testing.T, _ string, files []numstat) {
	t.Helper()
	if add, del := totals(files); add != 0 || del == 0 {
		t.Errorf("+%d -%d, want only deletions", add, del)
	}
}

func assertDependencyBump(t *testing.T, _ string, files []numstat) {
	t.Helper()
	want := []string{"server/go.mod", "server/go.sum", "web/package-lock.json", "web/package.json"}
	var got []string
	for _, f := range files {
		got = append(got, f.path)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("changed files = %v, want %v", got, want)
	}
}

// numstat is one line of `git diff --numstat`.
type numstat struct {
	added, deleted int
	path           string
}

func diffNumstat(t *testing.T, repo, branch string) []numstat {
	t.Helper()
	var out []numstat
	for _, line := range strings.Split(gitOut(t, repo, "diff", "--numstat", "main..."+branch), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			t.Fatalf("bad numstat line %q", line)
		}
		a, errA := strconv.Atoi(parts[0])
		d, errD := strconv.Atoi(parts[1])
		if errA != nil || errD != nil {
			t.Fatalf("non-numeric numstat line %q", line)
		}
		out = append(out, numstat{added: a, deleted: d, path: parts[2]})
	}
	return out
}

func totals(files []numstat) (added, deleted int) {
	for _, f := range files {
		added += f.added
		deleted += f.deleted
	}
	return added, deleted
}

func anyFile(files []numstat, pred func(string) bool) bool {
	for _, f := range files {
		if pred(f.path) {
			return true
		}
	}
	return false
}

// changedLines holds a file's removed and added lines in diff order.
type changedLines struct{ removed, added []string }

// hunkLines parses `git diff -U0 main...branch` into per-file changed lines.
func hunkLines(t *testing.T, repo, branch string) map[string]*changedLines {
	t.Helper()
	files := map[string]*changedLines{}
	var cur *changedLines
	inHunk := false
	for _, line := range strings.Split(gitOut(t, repo, "diff", "-U0", "main..."+branch), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur = &changedLines{}
			files[line[strings.LastIndex(line, " b/")+3:]] = cur
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "-"):
			cur.removed = append(cur.removed, line[1:])
		case inHunk && strings.HasPrefix(line, "+"):
			cur.added = append(cur.added, line[1:])
		}
	}
	return files
}

// refs returns every ref and its SHA, one per line.
func refs(t *testing.T, repo string) string {
	t.Helper()
	return gitOut(t, repo, "for-each-ref", "--format=%(objectname) %(refname)")
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
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
