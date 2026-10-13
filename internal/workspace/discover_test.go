package workspace

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeFile writes content to p, creating its directory.
func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

// mkRepo makes dir a work tree: dir/.git with a HEAD file.
func mkRepo(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	mkdir(t, filepath.Join(dir, ".git", "objects"))
	mkdir(t, filepath.Join(dir, ".git", "refs"))
}

// mkWorktree makes wt a linked worktree of the repo at main, as `git
// worktree add` does: main/.git/worktrees/<name> with HEAD and commondir,
// and a .git file in wt pointing at it (relative when rel is set).
func mkWorktree(t *testing.T, main, wt, name string, rel bool) {
	t.Helper()
	gd := filepath.Join(main, ".git", "worktrees", name)
	writeFile(t, filepath.Join(gd, "HEAD"), "ref: refs/heads/"+name+"\n")
	writeFile(t, filepath.Join(gd, "commondir"), "../..\n")
	target := gd
	if rel {
		r, err := filepath.Rel(wt, gd)
		if err != nil {
			t.Fatal(err)
		}
		target = r
	}
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+target+"\n")
}

// mkBare makes dir a bare repository.
func mkBare(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "HEAD"), "ref: refs/heads/main\n")
	mkdir(t, filepath.Join(dir, "objects"))
	mkdir(t, filepath.Join(dir, "refs"))
}

// symlink makes link point at target, skipping the test when the platform
// does not allow it.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	mkdir(t, filepath.Dir(link))
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func discover(t *testing.T, root string, o Options) Result {
	t.Helper()
	res, err := Discover(root, o)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func repoKeys(res Result) []string {
	keys := make([]string, 0, len(res.Repos))
	for _, r := range res.Repos {
		keys = append(keys, r.Key)
	}
	return keys
}

func wantKeys(t *testing.T, res Result, want ...string) {
	t.Helper()
	if got := repoKeys(res); !slices.Equal(got, want) {
		t.Errorf("keys = %q, want %q (notes %q)", got, want, res.Notes)
	}
}

func hasNote(notes []string, substr string) bool {
	return slices.ContainsFunc(notes, func(n string) bool { return strings.Contains(n, substr) })
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDiscoverDepth(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "a"))
	mkRepo(t, filepath.Join(root, "team", "portal"))
	mkRepo(t, filepath.Join(root, "x", "y", "deep"))

	res := discover(t, root, Options{})
	wantKeys(t, res, "a", "team/portal")
	if res.Root != realPath(t, root) {
		t.Errorf("root = %q, want %q", res.Root, realPath(t, root))
	}
	a := res.Repos[0]
	if want := filepath.Join(res.Root, "a"); a.Root != want || a.GitDir != filepath.Join(want, ".git") || a.CommonDir != a.GitDir {
		t.Errorf("repo a = %+v", a)
	}
	wantKeys(t, discover(t, root, Options{Depth: 3}), "a", "team/portal", "x/y/deep")
	wantKeys(t, discover(t, root, Options{Depth: 1}), "a")
}

func TestDiscoverSkipsDirectories(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "b"))
	mkRepo(t, filepath.Join(root, "node_modules", "x"))
	mkRepo(t, filepath.Join(root, "vendor", "y"))
	mkRepo(t, filepath.Join(root, ".cache", "z"))
	mkRepo(t, filepath.Join(root, ".venv", "v"))
	mkRepo(t, filepath.Join(root, "proj", "node_modules"))
	res := discover(t, root, Options{})
	wantKeys(t, res, "b")
	if len(res.Notes) != 0 {
		t.Errorf("notes = %q", res.Notes)
	}
}

func TestDiscoverNestedReposAreNotListed(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "a"))
	mkRepo(t, filepath.Join(root, "a", "sub"))
	// A submodule: a .git file into the superproject's modules.
	writeFile(t, filepath.Join(root, "a", ".git", "modules", "lib", "HEAD"), "0123\n")
	writeFile(t, filepath.Join(root, "a", "lib", ".git"), "gitdir: ../.git/modules/lib\n")
	wantKeys(t, discover(t, root, Options{}), "a")
}

func TestDiscoverExclude(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"archive/old", "archived", "keep", "team/tmp", "tmp/x"} {
		mkRepo(t, filepath.Join(root, filepath.FromSlash(p)))
	}
	tests := []struct {
		exclude []string
		want    []string
		note    string
	}{
		{nil, []string{"archive/old", "archived", "keep", "team/tmp", "tmp/x"}, ""},
		{[]string{"archive/**"}, []string{"archived", "keep", "team/tmp", "tmp/x"}, ""},
		{[]string{"./archive/**"}, []string{"archived", "keep", "team/tmp", "tmp/x"}, ""},
		{[]string{"archive"}, []string{"archived", "keep", "team/tmp", "tmp/x"}, ""},
		{[]string{"archive/*"}, []string{"archived", "keep", "team/tmp", "tmp/x"}, ""},
		{[]string{"arch*"}, []string{"keep", "team/tmp", "tmp/x"}, ""},
		{[]string{"**/tmp"}, []string{"archive/old", "archived", "keep"}, ""},
		{[]string{"tmp/**", "keep"}, []string{"archive/old", "archived", "team/tmp"}, ""},
		{[]string{"[bad"}, []string{"archive/old", "archived", "keep", "team/tmp", "tmp/x"}, `bad exclude glob "[bad"`},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.exclude, ","), func(t *testing.T) {
			res := discover(t, root, Options{Exclude: tt.exclude})
			wantKeys(t, res, tt.want...)
			if tt.note != "" && !hasNote(res.Notes, tt.note) {
				t.Errorf("notes = %q, want %q", res.Notes, tt.note)
			}
		})
	}
}

func TestDiscoverRootIsRepo(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "billing-svc")
	mkRepo(t, root)
	mkRepo(t, filepath.Join(root, "sub"))
	res := discover(t, root, Options{})
	wantKeys(t, res, "billing-svc")
	if r := res.Repos[0]; r.Root != realPath(t, root) || r.GitDir != filepath.Join(r.Root, ".git") {
		t.Errorf("repo = %+v", r)
	}
}

func TestDiscoverBrokenGitEntries(t *testing.T) {
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "ok"))
	writeFile(t, filepath.Join(root, "nogitdir", ".git"), "nonsense\n")
	writeFile(t, filepath.Join(root, "missing", ".git"), "gitdir: ../nowhere\n")
	mkdir(t, filepath.Join(root, "nohead", ".git"))
	mkRepo(t, filepath.Join(root, "nohead", "inner")) // inside a work tree: never searched
	res := discover(t, root, Options{})
	wantKeys(t, res, "ok")
	for _, n := range []string{"skipped nogitdir: .git file has no gitdir line", "skipped missing: git dir", "skipped nohead: git dir"} {
		if !hasNote(res.Notes, n) {
			t.Errorf("notes = %q, want one with %q", res.Notes, n)
		}
	}
}

func TestDiscoverBrokenRootIsSearched(t *testing.T) {
	root := t.TempDir()
	mkdir(t, filepath.Join(root, ".git")) // no HEAD
	mkRepo(t, filepath.Join(root, "a"))
	res := discover(t, root, Options{})
	wantKeys(t, res, "a")
	if !hasNote(res.Notes, "root is not a repository") {
		t.Errorf("notes = %q", res.Notes)
	}
}

func TestDiscoverIgnoresBareRepos(t *testing.T) {
	root := t.TempDir()
	mkBare(t, filepath.Join(root, "mirror.git"))
	mkBare(t, filepath.Join(root, "group", "plain"))
	mkRepo(t, filepath.Join(root, "group", "work"))
	res := discover(t, root, Options{})
	wantKeys(t, res, "group/work")
	if len(res.Notes) != 0 {
		t.Errorf("notes = %q", res.Notes)
	}
}

func TestDiscoverWorktrees(t *testing.T) {
	t.Run("main wins over a linked worktree", func(t *testing.T) {
		root := t.TempDir()
		main := filepath.Join(root, "main")
		mkRepo(t, main)
		mkWorktree(t, main, filepath.Join(root, "a-wt"), "a-wt", true)
		mkWorktree(t, main, filepath.Join(root, "z-wt"), "z-wt", false)
		res := discover(t, root, Options{})
		wantKeys(t, res, "main")
		for _, n := range []string{"skipped worktree a-wt (same repository as main)", "skipped worktree z-wt (same repository as main)"} {
			if !hasNote(res.Notes, n) {
				t.Errorf("notes = %q, want %q", res.Notes, n)
			}
		}
	})
	t.Run("main outside the root", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "upstream")
		mkRepo(t, outside)
		root := t.TempDir()
		mkWorktree(t, outside, filepath.Join(root, "w2"), "w2", false)
		mkWorktree(t, outside, filepath.Join(root, "w1"), "w1", true)
		res := discover(t, root, Options{})
		wantKeys(t, res, "w1")
		if !hasNote(res.Notes, "skipped worktree w2 (same repository as w1)") {
			t.Errorf("notes = %q", res.Notes)
		}
		r := res.Repos[0]
		if want := realPath(t, filepath.Join(outside, ".git")); r.CommonDir != want {
			t.Errorf("common dir = %q, want %q", r.CommonDir, want)
		}
		if want := realPath(t, filepath.Join(outside, ".git", "worktrees", "w1")); r.GitDir != want {
			t.Errorf("git dir = %q, want %q", r.GitDir, want)
		}
	})
}

func TestDiscoverSymlinks(t *testing.T) {
	t.Run("loop", func(t *testing.T) {
		root := t.TempDir()
		mkRepo(t, filepath.Join(root, "a"))
		symlink(t, "..", filepath.Join(root, "links", "loop"))
		res := discover(t, root, Options{Depth: 4})
		wantKeys(t, res, "a")
		if !hasNote(res.Notes, "symlink loop at links/loop") {
			t.Errorf("notes = %q", res.Notes)
		}
	})
	t.Run("second path to a seen repo", func(t *testing.T) {
		root := t.TempDir()
		mkRepo(t, filepath.Join(root, "a"))
		symlink(t, filepath.Join("..", "a"), filepath.Join(root, "links", "dup"))
		res := discover(t, root, Options{})
		wantKeys(t, res, "a")
		if len(res.Notes) != 0 {
			t.Errorf("notes = %q", res.Notes)
		}
	})
	t.Run("keys use the walked path", func(t *testing.T) {
		outside := t.TempDir()
		mkRepo(t, filepath.Join(outside, "r"))
		root := t.TempDir()
		symlink(t, outside, filepath.Join(root, "ext"))
		res := discover(t, root, Options{})
		wantKeys(t, res, "ext/r")
		if r := res.Repos[0]; r.Root != filepath.Join(res.Root, "ext", "r") || r.GitDir != realPath(t, filepath.Join(outside, "r", ".git")) {
			t.Errorf("repo = %+v", r)
		}
	})
	t.Run("symlinked root", func(t *testing.T) {
		target := t.TempDir()
		mkRepo(t, filepath.Join(target, "a"))
		link := filepath.Join(t.TempDir(), "ws")
		symlink(t, target, link)
		res := discover(t, link, Options{})
		wantKeys(t, res, "a")
		if res.Root != realPath(t, target) {
			t.Errorf("root = %q, want %q", res.Root, realPath(t, target))
		}
	})
}

func TestDiscoverKeysSortedAndCleaned(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"zeta", "team/portal", "alpha", "team/api", "we:ird"} {
		mkRepo(t, filepath.Join(root, filepath.FromSlash(p)))
	}
	wantKeys(t, discover(t, root, Options{}), "alpha", "team/api", "team/portal", "we_ird", "zeta")
}

func TestDiscoverUnreadableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	root := t.TempDir()
	mkRepo(t, filepath.Join(root, "a"))
	locked := filepath.Join(root, "locked")
	mkRepo(t, filepath.Join(locked, "inner"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	res := discover(t, root, Options{})
	wantKeys(t, res, "a")
	if !hasNote(res.Notes, "cannot read locked") {
		t.Errorf("notes = %q", res.Notes)
	}
}

func TestDiscoverRootErrors(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "f")
	writeFile(t, file, "x")
	for _, p := range []string{file, filepath.Join(root, "missing")} {
		if _, err := Discover(p, Options{}); err == nil {
			t.Errorf("Discover(%s) = nil error", p)
		}
	}
	// An empty directory is a workspace with no repos.
	res := discover(t, root, Options{})
	if len(res.Repos) != 0 || len(res.Notes) != 0 {
		t.Errorf("result = %+v", res)
	}
}
