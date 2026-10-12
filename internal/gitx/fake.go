package gitx

import (
	"context"
	"fmt"
	"sync"
)

// Fake is an in-memory Git for tests. Populate the exported maps before use;
// keys for Diffs, Ahead, Logs and MergeBases are "base..head".
type Fake struct {
	mu sync.Mutex

	RootDir    string
	Dir        string // git dir
	Default    string
	Remotes    map[string]string
	RefList    []Ref
	SHAs       map[string]string // rev -> sha
	MergeBases map[string]string
	Diffs      map[string]string
	Ahead      map[string]int
	Logs       map[string][]Commit
	Files      map[string]map[string][]byte // rev -> path -> content
	Dirty      bool
	CheckedOut []string
}

var _ Git = (*Fake)(nil)

func key(base, head string) string { return base + ".." + head }

// Root implements Git.
func (f *Fake) Root() string { return f.RootDir }

// GitDir implements Git.
func (f *Fake) GitDir(context.Context) (string, error) { return f.Dir, nil }

// DefaultBranch implements Git.
func (f *Fake) DefaultBranch(context.Context) (string, error) {
	if f.Default == "" {
		return "main", nil
	}
	return f.Default, nil
}

// RemoteURL implements Git.
func (f *Fake) RemoteURL(_ context.Context, remote string) (string, error) {
	if u, ok := f.Remotes[remote]; ok {
		return u, nil
	}
	return "", fmt.Errorf("%w: remote %s", ErrNotFound, remote)
}

// Refs implements Git.
func (f *Fake) Refs(_ context.Context, remotes bool) ([]Ref, error) {
	var out []Ref
	for _, r := range f.RefList {
		if r.Remote && !remotes {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// ResolveRef implements Git.
func (f *Fake) ResolveRef(_ context.Context, rev string) (string, error) {
	if s, ok := f.SHAs[rev]; ok {
		return s, nil
	}
	for _, r := range f.RefList {
		if r.Name == rev || r.FullName == rev || r.SHA == rev {
			return r.SHA, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrNotFound, rev)
}

// MergeBase implements Git.
func (f *Fake) MergeBase(_ context.Context, a, b string) (string, error) {
	if s, ok := f.MergeBases[key(a, b)]; ok {
		return s, nil
	}
	return "", fmt.Errorf("%w: merge-base %s %s", ErrNotFound, a, b)
}

// Diff implements Git.
func (f *Fake) Diff(_ context.Context, base, head string) (string, error) {
	if d, ok := f.Diffs[key(base, head)]; ok {
		return d, nil
	}
	return "", fmt.Errorf("%w: diff %s", ErrNotFound, key(base, head))
}

// CountAhead implements Git.
func (f *Fake) CountAhead(_ context.Context, base, head string) (int, error) {
	return f.Ahead[key(base, head)], nil
}

// Log implements Git.
func (f *Fake) Log(_ context.Context, base, head string, limit int) ([]Commit, error) {
	cs := f.Logs[key(base, head)]
	if limit > 0 && len(cs) > limit {
		cs = cs[:limit]
	}
	return cs, nil
}

// Show implements Git.
func (f *Fake) Show(_ context.Context, rev, path string) ([]byte, error) {
	if b, ok := f.Files[rev][path]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("%w: %s:%s", ErrNotFound, rev, path)
}

// ListFiles implements Git.
func (f *Fake) ListFiles(_ context.Context, rev string) ([]string, error) {
	var out []string
	for p := range f.Files[rev] {
		out = append(out, p)
	}
	return out, nil
}

// IsDirty implements Git.
func (f *Fake) IsDirty(context.Context) (bool, error) { return f.Dirty, nil }

// Checkout implements Git.
func (f *Fake) Checkout(_ context.Context, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CheckedOut = append(f.CheckedOut, ref)
	return nil
}
