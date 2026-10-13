package fixturegen

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// fixturesDir is the committed portal fixture source tree.
	fixturesDir = "../../testdata/fixtures"
	// workspaceManifest is the committed M5.3 workspace manifest.
	workspaceManifest = "../../testdata/fixtures-workspace/manifest.json"
)

// workspaceRepos are every git repository the committed manifest creates:
// the two fixture repos and the decoys.
var workspaceRepos = []string{"portal", "billing-svc", "node_modules/left-pad", "vendor/old-lib", "archive/2019/legacy-app"}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func TestLoadManifest(t *testing.T) {
	m, err := LoadManifest(workspaceManifest)
	if err != nil {
		t.Fatal(err)
	}
	wantDir, err := filepath.Abs(filepath.Dir(workspaceManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Dir != wantDir {
		t.Errorf("Dir = %s, want %s", m.Dir, wantDir)
	}
	if len(m.Repos) != 2 || len(m.Plain) != 1 || len(m.Decoys) != 3 || len(m.Symlinks) != 1 {
		t.Fatalf("manifest = %+v, want 2 repos, 1 plain dir, 3 decoys, 1 symlink", m)
	}
	portalSrc, err := filepath.Abs(fixturesDir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Repos[0].Src != portalSrc {
		t.Errorf("portal src = %s, want %s", m.Repos[0].Src, portalSrc)
	}
	billingPRs := filepath.Join(wantDir, "billing-svc", "prs.json")
	for _, c := range []struct{ dir, want string }{
		{"portal", filepath.Join(portalSrc, "prs.json")},
		{"billing-svc", billingPRs},
		{"./billing-svc", billingPRs},
		{"notes", ""},
		{"node_modules/left-pad", ""},
		{"missing", ""},
	} {
		if got := m.PRsFor(c.dir); got != c.want {
			t.Errorf("PRsFor(%q) = %q, want %q", c.dir, got, c.want)
		}
	}
}

func TestLoadManifestRejects(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "prs.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := func(dir, src, origin string) string {
		return `{"dir": "` + dir + `", "src": "` + src + `", "origin": "` + origin + `"}`
	}
	ok := repo("svc", "src", "https://example.com/svc.git")
	withRepos := func(rs ...string) string { return `{"repos": [` + strings.Join(rs, ", ") + `]}` }
	withRepo := func(extra string) string { return `{"repos": [` + ok + `], ` + extra + `}` }
	for _, c := range []struct{ name, manifest, want string }{
		{"bad JSON", `{"repos": [`, "unexpected EOF"},
		{"unknown field", `{"repo": []}`, "unknown field"},
		{"duplicate dir", withRepos(ok, ok), "listed twice"},
		{"absolute dir", withRepos(repo("/svc", "src", "o")), "absolute"},
		{"escaping dir", withRepos(repo("../x", "src", "o")), "outside the workspace"},
		{"root dir", withRepos(repo(".", "src", "o")), "outside the workspace"},
		{"unclean dir", withRepos(repo("a//b", "src", "o")), "not clean"},
		{"empty origin", withRepos(repo("svc", "src", "")), "empty origin"},
		{"empty src", withRepos(repo("svc", "", "o")), "empty src"},
		{"missing src", withRepos(repo("svc", "nope", "o")), "src"},
		{"missing prs", withRepos(`{"dir": "svc", "src": "src", "origin": "o", "prs": "nope.json"}`), "prs"},
		{"escaping plain dir", withRepo(`"plain": [{"dir": "../notes"}]`), "outside the workspace"},
		{"escaping plain file", withRepo(`"plain": [{"dir": "notes", "files": {"../x": ""}}]`), "outside the workspace"},
		{"absolute decoy", withRepo(`"decoys": [{"dir": "/tmp/x", "why": "w"}]`), "absolute"},
		{"decoy inside a repo", withRepo(`"decoys": [{"dir": "svc/vendor/x", "why": "w"}]`), "overlap"},
		{"plain dir equals a repo", withRepo(`"plain": [{"dir": "svc"}]`), "listed twice"},
		{"escaping symlink", withRepo(`"symlinks": [{"path": "../loop", "target": "."}]`), "outside the workspace"},
		{"symlink target leaves", withRepo(`"symlinks": [{"path": "links/loop", "target": "../.."}]`), "leaves the workspace"},
		{"absolute symlink target", withRepo(`"symlinks": [{"path": "links/loop", "target": "/etc"}]`), "relative"},
	} {
		t.Run(c.name, func(t *testing.T) {
			file := filepath.Join(root, strings.ReplaceAll(c.name, " ", "-")+".json")
			if err := os.WriteFile(file, []byte(c.manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadManifest(file)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("LoadManifest = %v, want an error containing %q", err, c.want)
			}
		})
	}
	t.Run("missing file", func(t *testing.T) {
		if _, err := LoadManifest(filepath.Join(root, "nope.json")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("LoadManifest = %v, want ErrNotExist", err)
		}
	})
	t.Run("valid", func(t *testing.T) {
		file := filepath.Join(root, "valid.json")
		body := withRepo(`"plain": [{"dir": "notes", "files": {"a/b.md": ""}}], "symlinks": [{"path": "links/loop", "target": ".."}]`)
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		m, err := LoadManifest(file)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(root, "src"); m.Repos[0].Src != want || m.PRsFor("svc") != "" {
			t.Errorf("repo = %+v, want src %s and no prs", m.Repos[0], want)
		}
	})
}

func TestGenerateWorkspaceRefusesOutput(t *testing.T) {
	root := t.TempDir()
	for _, out := range []string{filepath.Join(root, "elsewhere"), filepath.Join(root, "testdata")} {
		if err := GenerateWorkspace(t.Context(), workspaceManifest, out); err == nil {
			t.Errorf("GenerateWorkspace(%s) must refuse a path outside testdata", out)
		}
	}
	// An output that holds the manifest (and so the sources) is refused
	// before anything is deleted.
	ws := filepath.Join(root, "testdata", "ws")
	manifest := filepath.Join(ws, "manifest.json")
	if err := os.MkdirAll(filepath.Join(ws, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"repos": [{"dir": "svc", "src": "src", "origin": "https://example.com/svc.git"}]}`
	if err := os.WriteFile(manifest, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	err := GenerateWorkspace(t.Context(), manifest, ws)
	if err == nil || !strings.Contains(err.Error(), "contains fixture source") {
		t.Errorf("GenerateWorkspace into the source dir = %v, want a refusal", err)
	}
	if _, err := os.Stat(manifest); err != nil {
		t.Errorf("manifest must survive a refused run: %v", err)
	}
}

func TestGenerateWorkspace(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	ws := filepath.Join(root, "testdata", "fixture-workspace")
	if err := GenerateWorkspace(t.Context(), workspaceManifest, ws); err != nil {
		t.Fatal(err)
	}
	first := allRefs(t, ws)

	t.Run("portal is the fixture repo", func(t *testing.T) {
		single := filepath.Join(root, "testdata", "fixture-repo")
		if err := Generate(t.Context(), fixturesDir, single); err != nil {
			t.Fatal(err)
		}
		if got, want := first["portal"], refs(t, single); got != want {
			t.Errorf("portal refs differ from Generate's:\n%s\nvs\n%s", got, want)
		}
		if url := gitOut(t, filepath.Join(ws, "portal"), "remote", "get-url", "origin"); url != OriginURL {
			t.Errorf("portal origin = %q, want %q", url, OriginURL)
		}
	})

	t.Run("repos and decoys", func(t *testing.T) {
		for _, dir := range workspaceRepos {
			if fi, err := os.Stat(filepath.Join(ws, dir, ".git")); err != nil || !fi.IsDir() {
				t.Errorf("%s/.git: want a directory (err %v)", dir, err)
			}
		}
		for _, dir := range workspaceRepos[2:] {
			decoy := filepath.Join(ws, dir)
			if n := gitOut(t, decoy, "rev-list", "--count", "main"); n != "1" {
				t.Errorf("decoy %s has %s commits on main, want 1", dir, n)
			}
			if remotes := gitOut(t, decoy, "remote"); remotes != "" {
				t.Errorf("decoy %s has remotes %q, want none", dir, remotes)
			}
		}
	})

	t.Run("plain dir", func(t *testing.T) {
		if _, err := os.Lstat(filepath.Join(ws, "notes", ".git")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("notes/.git: want none (err %v)", err)
		}
		b, err := os.ReadFile(filepath.Join(ws, "notes", "README.md"))
		if err != nil || string(b) != "Team notes. Not a git repository.\n" {
			t.Errorf("notes/README.md = %q, %v", b, err)
		}
	})

	t.Run("symlink loop", func(t *testing.T) {
		link := filepath.Join(ws, "links", "loop")
		if target, err := os.Readlink(link); err != nil || target != ".." {
			t.Fatalf("links/loop -> %q (err %v), want ..", target, err)
		}
		got, err := filepath.EvalSymlinks(link)
		if err != nil {
			t.Fatal(err)
		}
		if want, _ := filepath.EvalSymlinks(ws); got != want {
			t.Errorf("links/loop resolves to %s, want the workspace root %s", got, want)
		}
	})

	t.Run("deterministic", func(t *testing.T) {
		if err := GenerateWorkspace(t.Context(), workspaceManifest, ws); err != nil {
			t.Fatal(err)
		}
		again := allRefs(t, ws)
		for _, dir := range workspaceRepos {
			if first[dir] == "" || first[dir] != again[dir] {
				t.Errorf("%s refs differ between runs:\n%s\nvs\n%s", dir, first[dir], again[dir])
			}
		}
	})
}

// allRefs returns refs(t, repo) for every repo of the workspace at ws.
func allRefs(t *testing.T, ws string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, dir := range workspaceRepos {
		out[dir] = refs(t, filepath.Join(ws, dir))
	}
	return out
}

// refs returns every ref of repo and its SHA, one per line.
func refs(t *testing.T, repo string) string {
	t.Helper()
	return gitOut(t, repo, "for-each-ref", "--format=%(objectname) %(refname)")
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := repo{dir: dir}.gitOut(t.Context(), args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
