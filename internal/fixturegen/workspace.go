package fixturegen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// decoyMessage is the message of a decoy repository's only commit.
const decoyMessage = "chore: decoy repository"

// Manifest describes a fixture workspace (M5.3): a directory holding
// several generated repositories plus the things workspace discovery must
// cope with. In the file, every path is slash-separated and relative: Src
// and PRs to the manifest's directory, the rest to the workspace root.
//
//	{
//	  "repos":    [{"dir": "portal", "src": "../fixtures", "origin": "https://…", "prs": "../fixtures/prs.json"}],
//	  "plain":    [{"dir": "notes", "files": {"README.md": "…"}}],
//	  "decoys":   [{"dir": "node_modules/left-pad", "why": "node_modules is never searched"}],
//	  "symlinks": [{"path": "links/loop", "target": ".."}]
//	}
//
// No entry may lie inside another one.
type Manifest struct {
	// Dir is the manifest's directory (absolute); set by LoadManifest.
	Dir      string            `json:"-"`
	Repos    []ManifestRepo    `json:"repos"`
	Plain    []ManifestPlain   `json:"plain,omitempty"`
	Decoys   []ManifestDecoy   `json:"decoys,omitempty"`
	Symlinks []ManifestSymlink `json:"symlinks,omitempty"`
}

// ManifestRepo is a repository generated with GenerateWith.
type ManifestRepo struct {
	// Dir is the repo's directory, relative to the workspace root.
	Dir string `json:"dir"`
	// Src is the fixture source directory (base/, prs/, history/,
	// prt.toml); absolute after LoadManifest.
	Src string `json:"src"`
	// Origin is the URL of the repo's origin remote.
	Origin string `json:"origin"`
	// PRs is the repo's offline GitHub fixture (gh.Fixture), "" for none;
	// absolute after LoadManifest.
	PRs string `json:"prs,omitempty"`
}

// ManifestPlain is a directory that is not a git repository.
type ManifestPlain struct {
	// Dir is the directory, relative to the workspace root.
	Dir string `json:"dir"`
	// Files maps a path relative to Dir to the file's contents.
	Files map[string]string `json:"files,omitempty"`
}

// ManifestDecoy is a one-commit git repository that workspace discovery
// must skip.
type ManifestDecoy struct {
	// Dir is the repo's directory, relative to the workspace root.
	Dir string `json:"dir"`
	// Why says which discovery rule skips it; it goes into the README.
	Why string `json:"why"`
}

// ManifestSymlink is a symbolic link in the workspace.
type ManifestSymlink struct {
	// Path is the link, relative to the workspace root.
	Path string `json:"path"`
	// Target is the link's content as given to os.Symlink, relative to
	// the link's directory; it must resolve inside the workspace.
	Target string `json:"target"`
}

// LoadManifest reads and validates a workspace manifest and resolves its
// repos' Src and PRs to absolute paths.
func LoadManifest(file string) (Manifest, error) {
	abs, err := filepath.Abs(file)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve manifest %s: %w", file, err)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return Manifest{}, fmt.Errorf("read manifest: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("manifest %s: %w", abs, err)
	}
	m.Dir = filepath.Dir(abs)
	if err := m.resolve(); err != nil {
		return Manifest{}, fmt.Errorf("manifest %s: %w", abs, err)
	}
	return m, nil
}

// PRsFor returns the absolute prs.json of the repo at dir (a path relative
// to the workspace root, such as a workspace repo key), or "" when dir is
// not a repo of the manifest or has no PR fixture.
func (m Manifest) PRsFor(dir string) string {
	dir = path.Clean(filepath.ToSlash(dir))
	for _, r := range m.Repos {
		if r.Dir == dir {
			return r.PRs
		}
	}
	return ""
}

// resolve validates every entry and makes repo sources absolute.
func (m *Manifest) resolve() error {
	var entries []string
	for i := range m.Repos {
		if err := m.resolveRepo(&m.Repos[i]); err != nil {
			return err
		}
		entries = append(entries, m.Repos[i].Dir)
	}
	for _, p := range m.Plain {
		if err := checkPlain(p); err != nil {
			return err
		}
		entries = append(entries, p.Dir)
	}
	for _, d := range m.Decoys {
		if err := checkRel("decoy dir", d.Dir); err != nil {
			return err
		}
		entries = append(entries, d.Dir)
	}
	for _, s := range m.Symlinks {
		if err := checkSymlink(s); err != nil {
			return err
		}
		entries = append(entries, s.Path)
	}
	return checkOverlap(entries)
}

// resolveRepo validates r and makes its Src and PRs absolute.
func (m Manifest) resolveRepo(r *ManifestRepo) error {
	if err := checkRel("repo dir", r.Dir); err != nil {
		return err
	}
	if r.Origin == "" {
		return fmt.Errorf("repo %s: empty origin", r.Dir)
	}
	if r.Src == "" {
		return fmt.Errorf("repo %s: empty src", r.Dir)
	}
	r.Src = m.abs(r.Src)
	if err := requireExists(r.Dir, "src", r.Src); err != nil {
		return err
	}
	if r.PRs == "" {
		return nil
	}
	r.PRs = m.abs(r.PRs)
	return requireExists(r.Dir, "prs", r.PRs)
}

// abs resolves a manifest-relative path (an absolute one is kept).
func (m Manifest) abs(p string) string {
	p = filepath.FromSlash(p)
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(m.Dir, p)
}

func requireExists(dir, field, p string) error {
	if _, err := os.Stat(p); err != nil {
		return fmt.Errorf("repo %s: %s: %w", dir, field, err)
	}
	return nil
}

// checkRel returns an error unless p is a clean, slash-separated path
// strictly inside the directory it is relative to.
func checkRel(what, p string) error {
	switch {
	case p == "":
		return fmt.Errorf("%s: empty path", what)
	case path.IsAbs(p) || filepath.IsAbs(p):
		return fmt.Errorf("%s %q: absolute, want a relative path", what, p)
	case path.Clean(p) != p:
		return fmt.Errorf("%s %q: not clean (want %q)", what, p, path.Clean(p))
	case p == "." || !filepath.IsLocal(filepath.FromSlash(p)):
		return fmt.Errorf("%s %q: outside the workspace", what, p)
	}
	return nil
}

func checkPlain(p ManifestPlain) error {
	if err := checkRel("plain dir", p.Dir); err != nil {
		return err
	}
	for name := range p.Files {
		if err := checkRel("plain file "+p.Dir, name); err != nil {
			return err
		}
	}
	return nil
}

// checkSymlink validates the link path and refuses a target that resolves
// outside the workspace (the root itself is allowed), so a generated loop
// never points discovery at the rest of the filesystem.
func checkSymlink(s ManifestSymlink) error {
	if err := checkRel("symlink", s.Path); err != nil {
		return err
	}
	if s.Target == "" || path.IsAbs(s.Target) || filepath.IsAbs(s.Target) {
		return fmt.Errorf("symlink %s: target %q must be a non-empty relative path", s.Path, s.Target)
	}
	if to := path.Join(path.Dir(s.Path), s.Target); !filepath.IsLocal(filepath.FromSlash(to)) {
		return fmt.Errorf("symlink %s: target %q leaves the workspace", s.Path, s.Target)
	}
	return nil
}

// checkOverlap refuses two entries at the same path or one inside another,
// so each entry owns its subtree.
func checkOverlap(entries []string) error {
	for i, a := range entries {
		for _, b := range entries[i+1:] {
			switch {
			case a == b:
				return fmt.Errorf("%q is listed twice", a)
			case strings.HasPrefix(b, a+"/"), strings.HasPrefix(a, b+"/"):
				return fmt.Errorf("%q and %q overlap", a, b)
			}
		}
	}
	return nil
}

// GenerateWorkspace recreates the fixture workspace described by the
// manifest at manifestPath in out: every repo with GenerateWith (prt.toml
// installed from its sources), then the plain directories, the decoy repos
// and the symlinks. out must be inside a directory named testdata and must
// not contain the manifest or a repo's sources; it is deleted first. Two
// runs give identical refs.
func GenerateWorkspace(ctx context.Context, manifestPath, out string) error {
	absOut, err := outputDir(out)
	if err != nil {
		return err
	}
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	if err := m.checkOutput(absOut); err != nil {
		return err
	}
	if err := resetDir(absOut); err != nil {
		return err
	}
	for _, r := range m.Repos {
		if err := GenerateWith(ctx, r.Src, join(absOut, r.Dir), Options{OriginURL: r.Origin}); err != nil {
			return fmt.Errorf("repo %s: %w", r.Dir, err)
		}
	}
	for _, p := range m.Plain {
		if err := writePlain(absOut, p); err != nil {
			return err
		}
	}
	for _, d := range m.Decoys {
		if err := makeDecoy(ctx, absOut, d); err != nil {
			return err
		}
	}
	for _, s := range m.Symlinks {
		if err := makeSymlink(absOut, s); err != nil {
			return err
		}
	}
	return nil
}

// checkOutput refuses an output directory that holds the manifest or a
// repo's fixture sources, which resetting it would delete.
func (m Manifest) checkOutput(absOut string) error {
	sources := []string{m.Dir}
	for _, r := range m.Repos {
		sources = append(sources, r.Src)
		if r.PRs != "" {
			sources = append(sources, r.PRs)
		}
	}
	for _, s := range sources {
		if rel, err := filepath.Rel(absOut, s); err == nil && filepath.IsLocal(rel) {
			return fmt.Errorf("refusing to recreate %s: it contains fixture source %s", absOut, s)
		}
	}
	return nil
}

// join returns root joined with a slash-separated relative path.
func join(root, rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

// writePlain creates a non-repo directory and its files (mode 0644).
func writePlain(root string, p ManifestPlain) error {
	dir := join(root, p.Dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("plain %s: %w", p.Dir, err)
	}
	for _, name := range slices.Sorted(maps.Keys(p.Files)) {
		file := join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return fmt.Errorf("plain %s: %w", p.Dir, err)
		}
		if err := os.WriteFile(file, []byte(p.Files[name]), 0o644); err != nil {
			return fmt.Errorf("plain %s: %w", p.Dir, err)
		}
	}
	return nil
}

// makeDecoy creates a git repository with one commit (a README saying why
// discovery skips it), with the fixed identity and dates of repo.gitOut.
func makeDecoy(ctx context.Context, root string, d ManifestDecoy) error {
	r := repo{dir: join(root, d.Dir)}
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return fmt.Errorf("decoy %s: %w", d.Dir, err)
	}
	readme := fmt.Sprintf("# %s\n\nDecoy repository for workspace discovery: %s.\n", path.Base(d.Dir), d.Why)
	if err := os.WriteFile(filepath.Join(r.dir, "README.md"), []byte(readme), 0o644); err != nil {
		return fmt.Errorf("decoy %s: %w", d.Dir, err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "README.md"},
		{"commit", "-q", "-m", decoyMessage},
	} {
		if err := r.git(ctx, args...); err != nil {
			return fmt.Errorf("decoy %s: %w", d.Dir, err)
		}
	}
	return nil
}

// makeSymlink creates a symbolic link with the target exactly as given.
func makeSymlink(root string, s ManifestSymlink) error {
	link := join(root, s.Path)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return fmt.Errorf("symlink %s: %w", s.Path, err)
	}
	if err := os.Symlink(filepath.FromSlash(s.Target), link); err != nil {
		return fmt.Errorf("symlink %s: %w", s.Path, err)
	}
	return nil
}
