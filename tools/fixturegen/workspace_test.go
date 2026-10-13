package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	// workspaceManifest is the committed M5.3 workspace manifest.
	workspaceManifest = "../../testdata/fixtures-workspace/manifest.json"
	// billingSrc is the committed billing-svc fixture source tree.
	billingSrc = "../../testdata/fixtures-workspace/billing-svc"
	// billingOrigin is billing-svc's origin in the manifest.
	billingOrigin = "https://github.com/acme/billing-svc.git"
)

// billingBranches are billing-svc's branches: #7 (alice, review requested),
// #8 (the viewer's own) and a local-only spike.
var billingBranches = []branchShape{
	{"07-late-fees", 1, 7, billingFiles(true,
		"README.md", "internal/api/handlers.go", "internal/invoice/invoice.go", "internal/invoice/invoice_test.go")},
	{"08-currency-validation", 1, 8, billingFiles(true,
		"internal/api/handlers.go", "internal/api/handlers_test.go", "internal/invoice/currency.go")},
	{"09-spike-usage-export", 1, 0, billingFiles(false,
		"internal/api/handlers.go", "internal/export/usage.go", "internal/invoice/invoice.go")},
}

func TestNewConfig(t *testing.T) {
	for _, c := range []struct {
		name                 string
		src, out, workspace  string
		srcSet               bool
		wantOut, wantErrPart string
	}{
		{name: "repo default", src: "testdata/fixtures", wantOut: defaultRepoOut},
		{name: "workspace default", src: "testdata/fixtures", workspace: "m.json", wantOut: defaultWorkspaceOut},
		{name: "explicit out wins", src: "testdata/fixtures", out: "testdata/x", workspace: "m.json", wantOut: "testdata/x"},
		{name: "explicit src in repo mode", src: "other", srcSet: true, wantOut: defaultRepoOut},
		{name: "src with workspace", src: "other", srcSet: true, workspace: "m.json", wantErrPart: "cannot be combined"},
	} {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := newConfig(c.src, c.out, c.workspace, c.srcSet)
			if c.wantErrPart != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrPart) {
					t.Fatalf("err = %v, want one containing %q", err, c.wantErrPart)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.out != c.wantOut || cfg.src != c.src || cfg.workspace != c.workspace {
				t.Errorf("config = %+v, want out %q src %q workspace %q", cfg, c.wantOut, c.src, c.workspace)
			}
		})
	}
}

func TestWorkspaceBillingSvc(t *testing.T) {
	requireGit(t)
	out := filepath.Join(t.TempDir(), "testdata", "fixture-workspace")
	cfg, err := newConfig("testdata/fixtures", out, workspaceManifest, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.generate(t.Context()); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(out, "billing-svc")

	t.Run("config and remote", func(t *testing.T) {
		want, err := os.ReadFile(filepath.Join(billingSrc, "prt.toml"))
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
		if url := gitOut(t, repo, "remote", "get-url", "origin"); url != billingOrigin {
			t.Errorf("origin = %q, want %q", url, billingOrigin)
		}
		if head := gitOut(t, repo, "rev-parse", "--abbrev-ref", "HEAD"); head != "main" {
			t.Errorf("HEAD = %q, want main", head)
		}
		if msg := gitOut(t, repo, "log", "-1", "--format=%s", "main"); msg != "chore: import billing service" {
			t.Errorf("base commit message = %q, want the one in base-message.txt", msg)
		}
	})

	for _, b := range billingBranches {
		t.Run(b.name, func(t *testing.T) {
			assertBranch(t, repo, b)
			assertAuthorDate(t, repo, b.name)
		})
	}

	t.Run("prs.json", func(t *testing.T) {
		assertBillingPRs(t, repo)
	})
}

// billingFiles returns a branch assertion: exactly the want files changed
// (sorted), 20–80 changed lines, and a Go test among them iff withTest.
func billingFiles(withTest bool, want ...string) func(*testing.T, string, []numstat) {
	return func(t *testing.T, _ string, files []numstat) {
		t.Helper()
		got := paths(files)
		if !slices.Equal(got, want) {
			t.Errorf("changed files = %v, want %v", got, want)
		}
		if add, del := totals(files); add+del < 20 || add+del > 80 {
			t.Errorf("changed lines = %d, want 20-80", add+del)
		}
		hasTest := anyFile(files, func(p string) bool { return strings.HasSuffix(p, "_test.go") })
		if hasTest != withTest {
			t.Errorf("changes a _test.go file = %v, want %v", hasTest, withTest)
		}
	}
}

// assertAuthorDate checks a branch head's author date lies in the window
// the workspace snapshot (clock 2026-10-13T12:00Z) expects.
func assertAuthorDate(t *testing.T, repo, branch string) {
	t.Helper()
	at := authorDate(t, repo, branch)
	from := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	if at.Before(from) || at.After(to) {
		t.Errorf("%s authored %s, want between %s and %s", branch, at, from, to)
	}
}

// assertBillingPRs checks billing-svc's prs.json against the generated repo
// and the sections it must produce: #7 in Needs you, #8 in Yours.
func assertBillingPRs(t *testing.T, repo string) {
	t.Helper()
	fake := assertPRsMatchRepo(t, repo, filepath.Join(billingSrc, "prs.json"), billingBranches)
	if fake.Repo != "acme/billing-svc" {
		t.Errorf("repo = %q, want acme/billing-svc", fake.Repo)
	}
	if pr := fake.PRs[7]; pr.Author != "alice" || !pr.ReviewRequested {
		t.Errorf("#7 author %q reviewRequested %v, want alice and true (Needs you)", pr.Author, pr.ReviewRequested)
	}
	if pr := fake.PRs[8]; pr.Author != fake.Login || pr.ReviewRequested {
		t.Errorf("#8 author %q reviewRequested %v, want the viewer and false (Yours)", pr.Author, pr.ReviewRequested)
	}
	for n, pr := range fake.PRs {
		if want := fmt.Sprintf("https://github.com/acme/billing-svc/pull/%d", n); pr.URL != want {
			t.Errorf("#%d url = %q, want %q", n, pr.URL, want)
		}
		if pr.State != "open" || pr.CI == "" {
			t.Errorf("#%d state %q ci %q, want open with a CI state", n, pr.State, pr.CI)
		}
		if authored := authorDate(t, repo, pr.HeadRef); pr.CreatedAt.Before(authored) || pr.UpdatedAt.Before(pr.CreatedAt) {
			t.Errorf("#%d created %s updated %s, want createdAt >= head author date %s <= updatedAt", n, pr.CreatedAt, pr.UpdatedAt, authored)
		}
		if got := paths(diffNumstat(t, repo, pr.HeadRef)); !slices.Equal(pr.Files, got) {
			t.Errorf("#%d files = %v, git says %v", n, pr.Files, got)
		}
	}
}

func authorDate(t *testing.T, repo, ref string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, gitOut(t, repo, "log", "-1", "--format=%aI", ref))
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// paths returns the numstat entries' paths in order.
func paths(files []numstat) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.path)
	}
	return out
}
