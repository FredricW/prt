// Package gitx wraps the local git CLI behind the Git interface. Every git
// fact prt uses (refs, merge base, diffs, file contents, ahead counts,
// checkout) comes through here, so tests can substitute Fake.
//
// Only local operations live here; nothing in this package talks to the
// network (no fetch/push).
package gitx

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrNotFound is returned when a ref, path or revision does not exist.
var ErrNotFound = errors.New("gitx: not found")

// ErrNotWorkTree is wrapped by Open when git reports that the directory is
// not inside a git repository. cmd/prt then treats the directory as a
// workspace root (M5.3) when it exists. Any other failure (git missing,
// git refusing a repository it finds, a cancelled context) is returned
// without it, so it is reported as is.
var ErrNotWorkTree = errors.New("gitx: not a git work tree")

// Ref is a local or remote-tracking branch.
type Ref struct {
	Name       string    `json:"name"` // short name, e.g. "feat/x" or "origin/feat/x"
	FullName   string    `json:"full"` // e.g. "refs/heads/feat/x"
	SHA        string    `json:"sha"`  // commit the ref points at
	Upstream   string    `json:"upstream,omitempty"`
	Subject    string    `json:"subject"` // subject line of the tip commit
	Author     string    `json:"author"`
	CommitDate time.Time `json:"commitDate"`
	Remote     bool      `json:"remote,omitempty"`
}

// Commit is one entry of a log.
type Commit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
}

// CompareRef returns the ref to measure branches against for a base branch
// name: the remote-tracking "origin/<branch>" when it exists (a stale local
// copy would count upstream commits as "ahead" and put them in the diff),
// else branch itself.
func CompareRef(ctx context.Context, g Git, branch string) string {
	if branch == "" || strings.HasPrefix(branch, "origin/") {
		return branch
	}
	if _, err := g.ResolveRef(ctx, "refs/remotes/origin/"+branch); err == nil {
		return "origin/" + branch
	}
	return branch
}

// Git is the set of local git operations prt needs. Implementations must be
// safe for concurrent use.
type Git interface {
	// Root returns the absolute path of the work tree.
	Root() string
	// GitDir returns the absolute path of the .git directory (where .git/prt lives).
	GitDir(ctx context.Context) (string, error)
	// DefaultBranch returns the default branch name (origin/HEAD, else main, else master).
	DefaultBranch(ctx context.Context) (string, error)
	// RemoteURL returns the URL of the named remote (usually "origin").
	RemoteURL(ctx context.Context, remote string) (string, error)
	// Refs lists local branches (and remote-tracking ones when remotes is true).
	Refs(ctx context.Context, remotes bool) ([]Ref, error)
	// ResolveRef returns the commit SHA of a revision.
	ResolveRef(ctx context.Context, rev string) (string, error)
	// MergeBase returns the merge base of a and b.
	MergeBase(ctx context.Context, a, b string) (string, error)
	// Diff returns the unified diff base..head (rename detection on, no colour).
	Diff(ctx context.Context, base, head string) (string, error)
	// CountAhead returns the number of commits in head not in base (rev-list --count base..head).
	CountAhead(ctx context.Context, base, head string) (int, error)
	// Log returns commits in base..head, newest first, at most limit (0 = all).
	Log(ctx context.Context, base, head string, limit int) ([]Commit, error)
	// Show returns a file's contents at a revision; ErrNotFound if absent.
	Show(ctx context.Context, rev, path string) ([]byte, error)
	// ListFiles lists all paths in the tree at rev.
	ListFiles(ctx context.Context, rev string) ([]string, error)
	// IsDirty reports whether the work tree has uncommitted changes.
	IsDirty(ctx context.Context) (bool, error)
	// Checkout switches the work tree to ref.
	Checkout(ctx context.Context, ref string) error
}
