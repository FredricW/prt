package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

// maxGitFileSize bounds the .git file read: a real one is one short line.
const maxGitFileSize = 64 << 10

// Repo is one repository found by Discover.
type Repo struct {
	// Key is the repo's workspace key: model.CleanRepoKey of its path
	// relative to the root (slash-separated, the walked path, not the
	// symlink-resolved one), or the root's base name when the root itself
	// is a repository.
	Key string
	// Root is the absolute work tree (the directory holding .git).
	Root string
	// GitDir is the absolute git dir: the .git directory, or the target of
	// a .git file (linked worktree, submodule), symlinks resolved.
	GitDir string
	// CommonDir is the absolute common git dir: GitDir, except for linked
	// worktrees, where it is the main work tree's git dir. Two work trees of
	// one repository share it, so it is the deduplication key.
	CommonDir string
}

// Options configures Discover.
type Options struct {
	// Depth is how many directory levels below the root are searched (the
	// root is level 0); ≤ 0 means config.DefaultWorkspaceDepth.
	Depth int
	// Exclude are doublestar globs of directories never searched, matched
	// against the slash-separated path relative to the root. A glob ending
	// in "/**" also excludes the directory it names ("archive/**" excludes
	// archive itself).
	Exclude []string
}

// Result is what Discover found.
type Result struct {
	// Root is the absolute root with symlinks resolved.
	Root string
	// Repos are the repositories found, sorted by Key.
	Repos []Repo
	// Notes explain what was skipped (broken .git entries, duplicate
	// worktrees, symlink loops, unreadable directories, bad globs).
	Notes []string
}

// Discover lists the git repositories under root with a pure filesystem
// walk: no git process runs and no file is read except .git entries.
//
// A directory is a repository when it holds a .git directory or a .git file
// ("gitdir: <path>", relative to the directory holding it) whose git dir
// has a HEAD file; an entry without one is skipped with a note. When root
// itself is a repository it is the only one. Otherwise repositories are
// looked for at levels 1..Depth below root, never inside a repository's
// work tree (nested repositories and submodules belong to their
// superproject), never inside .git, node_modules, vendor, other
// dot-directories or excluded directories. The walk is breadth first (each
// level in lexical order), so a directory reached by several paths
// (directory symlinks) is reached first at its shallowest level, searched
// from there and keyed by that path; later paths to it are skipped (a loop
// back to an ancestor with a note). Linked worktrees of one repository are
// listed once: the main work tree wins, else the first by key. Bare
// repositories are ignored. Keys that clash after cleaning get a "~2", "~3"
// suffix (with a note).
//
// The error is non-nil only when root is not a readable directory.
func Discover(root string, o Options) (Result, error) {
	dir, err := realDir(root)
	if err != nil {
		return Result{}, err
	}
	w := newWalker(dir, o)
	if done := w.walkRoot(); !done {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return Result{}, fmt.Errorf("workspace: read root %s: %w", root, err)
		}
		w.visited[dir] = true
		w.visitEntries(queued{dir: dir, real: dir}, entries)
		w.walk()
	}
	return w.result(), nil
}

// realDir returns root as an absolute path with symlinks resolved, or an
// error when it is not a directory.
func realDir(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("workspace: root %s: %w", root, err)
	}
	dir, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("workspace: root %s: %w", root, err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return "", fmt.Errorf("workspace: root %s: %w", root, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("workspace: root %s is not a directory", root)
	}
	return dir, nil
}

// found is a repository seen by the walk, before deduplication.
type found struct {
	repo Repo
	main bool   // .git is a directory (not a linked worktree)
	rel  string // the walked path relative to the root, before cleaning
}

// queued is a directory whose entries the walk visits next: dir (the walked
// path), rel (relative to the root), real (symlinks resolved) and its level
// (the root is 0).
type queued struct {
	dir, rel, real string
	level          int
}

// walker holds one Discover walk's state.
type walker struct {
	root    string
	depth   int
	exclude []string
	// visited are the resolved paths of the directories reached (searched,
	// queued or found to be a repository). The walk is breadth first, so a
	// directory is reached first at its shallowest level.
	visited map[string]bool
	queue   []queued
	found   []found
	notes   []string
}

func newWalker(root string, o Options) *walker {
	w := &walker{root: root, depth: o.Depth, visited: map[string]bool{}}
	if w.depth <= 0 {
		w.depth = config.DefaultWorkspaceDepth
	}
	for _, g := range o.Exclude {
		g = strings.TrimPrefix(strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(g)), "./"), "/")
		if g == "" {
			continue
		}
		if !doublestar.ValidatePattern(g) {
			w.note("bad exclude glob %q ignored", g)
			continue
		}
		w.exclude = append(w.exclude, g)
	}
	return w
}

func (w *walker) note(format string, args ...any) {
	w.notes = append(w.notes, fmt.Sprintf(format, args...))
}

// walkRoot handles a root that is itself a repository; it reports whether
// the walk is done. A root with a broken .git entry is noted and then
// searched like any other directory (the user asked for this root).
func (w *walker) walkRoot() bool {
	ge, present, err := readGitEntry(w.root)
	switch {
	case err != nil:
		w.note("root is not a repository (%v); searching below it", err)
		return false
	case !present:
		return false
	}
	key := model.CleanRepoKey(filepath.Base(w.root))
	if key == "" {
		key = "root"
	}
	w.found = append(w.found, found{repo: ge.repo(key, w.root), main: ge.main, rel: key})
	return true
}

// walk searches the queued directories, level by level (breadth first).
func (w *walker) walk() {
	for len(w.queue) > 0 {
		d := w.queue[0]
		w.queue = w.queue[1:]
		entries, err := os.ReadDir(d.dir)
		if err != nil {
			w.note("cannot read %s: %v", d.rel, err)
			continue
		}
		w.visitEntries(d, entries)
	}
}

// visitEntries visits the entries of d, in lexical order, one level below
// it.
func (w *walker) visitEntries(d queued, entries []fs.DirEntry) {
	for _, e := range entries {
		w.visit(filepath.Join(d.dir, e.Name()), path.Join(d.rel, e.Name()), d.real, e, d.level+1)
	}
}

// visit looks at one directory entry at level (≥ 1): a repository is
// recorded, any other directory is queued to be searched while level <
// depth.
func (w *walker) visit(p, rel, parentReal string, e fs.DirEntry, level int) {
	if skipName(e.Name()) || w.excluded(rel) {
		return
	}
	isDir, isLink := dirKind(p, e)
	if !isDir {
		return
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		w.note("cannot resolve %s: %v", rel, err)
		return
	}
	if w.visited[resolved] {
		if isLink && within(parentReal, resolved) {
			w.note("symlink loop at %s (back to %s)", rel, w.display(resolved))
		}
		return
	}
	w.visited[resolved] = true
	ge, present, err := readGitEntry(p)
	switch {
	case errors.Is(err, fs.ErrPermission):
		w.note("cannot read %s: %v", rel, err)
		return
	case err != nil:
		w.note("skipped %s: %v", rel, err)
		return
	case present:
		w.found = append(w.found, found{repo: ge.repo(model.CleanRepoKey(rel), p), main: ge.main, rel: rel})
		return
	case isBare(p) || level >= w.depth:
		return
	}
	w.queue = append(w.queue, queued{dir: p, rel: rel, real: resolved, level: level})
}

// display is a resolved path relative to the root when it is inside it.
func (w *walker) display(resolved string) string {
	if resolved == w.root {
		return "the root"
	}
	if r, err := filepath.Rel(w.root, resolved); err == nil && !strings.HasPrefix(r, "..") {
		return filepath.ToSlash(r)
	}
	return resolved
}

// excluded reports whether rel matches an Exclude glob. A glob "x/**" also
// matches x itself, so the walk never enters it.
func (w *walker) excluded(rel string) bool {
	for _, g := range w.exclude {
		if ok, _ := doublestar.Match(g, rel); ok {
			return true
		}
		if base, ok := strings.CutSuffix(g, "/**"); ok {
			if m, _ := doublestar.Match(base, rel); m {
				return true
			}
		}
	}
	return false
}

// result deduplicates the repositories found by common git dir and sorts
// them by key.
func (w *walker) result() Result {
	sort.SliceStable(w.found, func(i, j int) bool { return w.found[i].repo.Key < w.found[j].repo.Key })
	var kept []found
	byCommon := map[string]int{}
	for _, f := range w.found {
		j, dup := byCommon[f.repo.CommonDir]
		if !dup {
			byCommon[f.repo.CommonDir] = len(kept)
			kept = append(kept, f)
			continue
		}
		if f.main && !kept[j].main {
			w.note("skipped worktree %s (same repository as %s)", kept[j].repo.Key, f.repo.Key)
			kept[j] = f
			continue
		}
		w.note("skipped worktree %s (same repository as %s)", f.repo.Key, kept[j].repo.Key)
	}
	repos := w.uniqueKeys(kept)
	sort.Slice(repos, func(i, j int) bool { return repos[i].Key < repos[j].Key })
	return Result{Root: w.root, Repos: repos, Notes: w.notes}
}

// uniqueKeys makes the keys of kept (sorted by key) unique: directories
// whose cleaned paths clash ("we:ird" and "we_ird", "my repo" and
// "my_repo") would share item ids and routing. The one whose path is its
// key keeps it, else the first walked; the others get "<key>~2", "<key>~3"
// (the first free) and a note.
func (w *walker) uniqueKeys(kept []found) []Repo {
	sort.SliceStable(kept, func(i, j int) bool {
		a, b := kept[i], kept[j]
		if a.repo.Key != b.repo.Key {
			return a.repo.Key < b.repo.Key
		}
		return a.rel == a.repo.Key && b.rel != b.repo.Key
	})
	taken := make(map[string]bool, len(kept))
	for _, f := range kept {
		taken[f.repo.Key] = true
	}
	repos := make([]Repo, 0, len(kept))
	for i, f := range kept {
		r := f.repo
		if i > 0 && kept[i-1].repo.Key == r.Key {
			key := r.Key
			for n := 2; ; n++ {
				if k := fmt.Sprintf("%s~%d", key, n); !taken[k] {
					r.Key = k
					break
				}
			}
			taken[r.Key] = true
			w.note("%s is keyed %s (%s is taken)", f.rel, r.Key, key)
		}
		repos = append(repos, r)
	}
	return repos
}

// skipName reports directories never searched: dot-directories (.git,
// .cache, .venv, …), node_modules and vendor.
func skipName(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor"
}

// dirKind reports whether the entry is a directory, following a symlink,
// and whether it is a symlink.
func dirKind(p string, e fs.DirEntry) (isDir, isLink bool) {
	if e.Type()&fs.ModeSymlink == 0 {
		return e.IsDir(), false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir(), true
}

// within reports whether p is dir or below it (both resolved paths).
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// gitEntry is a valid .git entry.
type gitEntry struct {
	gitDir, commonDir string
	main              bool
}

func (g gitEntry) repo(key, root string) Repo {
	return Repo{Key: key, Root: root, GitDir: g.gitDir, CommonDir: g.commonDir}
}

// readGitEntry reads dir/.git. present is false when there is none; err is
// set when there is one that is not a usable git dir (no gitdir line, no
// HEAD, unreadable).
func readGitEntry(dir string) (g gitEntry, present bool, err error) {
	dotgit := filepath.Join(dir, ".git")
	fi, err := os.Stat(dotgit)
	if errors.Is(err, fs.ErrNotExist) {
		return gitEntry{}, false, nil
	}
	if err != nil {
		return gitEntry{}, true, fmt.Errorf(".git: %w", err)
	}
	if fi.IsDir() {
		g = gitEntry{gitDir: dotgit, commonDir: dotgit, main: true}
	} else {
		target, err := readGitFile(dotgit, fi)
		if err != nil {
			return gitEntry{}, true, err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(dir, target)
		}
		g.gitDir = filepath.Clean(target)
		g.commonDir = commonDir(g.gitDir)
	}
	if !isFile(filepath.Join(g.gitDir, "HEAD")) {
		return gitEntry{}, true, fmt.Errorf("git dir %s has no HEAD", g.gitDir)
	}
	g.gitDir, g.commonDir = resolve(g.gitDir), resolve(g.commonDir)
	return g, true, nil
}

// readGitFile returns the path of a .git file's "gitdir:" line.
func readGitFile(p string, fi fs.FileInfo) (string, error) {
	if fi.Size() > maxGitFileSize {
		return "", errors.New(".git file too large to be a gitdir link")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", fmt.Errorf(".git: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:"); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New(".git file has no gitdir line")
}

// commonDir is the common git dir of gitDir: its "commondir" file
// (relative to gitDir) when present, else gitDir.
func commonDir(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return gitDir
	}
	c := strings.TrimSpace(string(b))
	if c == "" {
		return gitDir
	}
	if !filepath.IsAbs(c) {
		c = filepath.Join(gitDir, c)
	}
	return filepath.Clean(c)
}

// isBare reports a bare repository (HEAD, objects and refs, no .git).
func isBare(dir string) bool {
	return isFile(filepath.Join(dir, "HEAD")) && isDir(filepath.Join(dir, "objects")) && isDir(filepath.Join(dir, "refs"))
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// resolve returns p with symlinks resolved, or cleaned when that fails.
func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
