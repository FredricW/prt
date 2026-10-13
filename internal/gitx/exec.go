package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Exec implements Git by running the git binary in a work tree.
type Exec struct {
	root string
	// Bin is the git binary; defaults to "git".
	Bin string
}

var _ Git = (*Exec)(nil)

// notRepoExit and notRepoMessage are how git (in the C locale) says that a
// directory is not inside a repository: "fatal: not a git repository (or
// any of the parent directories): .git", exit status 128.
const (
	notRepoExit    = 128
	notRepoMessage = "not a git repository"
)

// Open returns an Exec for the work tree containing dir. The error wraps
// ErrNotWorkTree only when git says dir is not inside a repository; git
// missing, a repository git refuses (dubious ownership) or a cancelled
// context come back as they are.
func Open(ctx context.Context, dir string) (*Exec, error) {
	g := &Exec{root: dir}
	// The C locale keeps git's message English, so it can be recognised.
	out, stderr, err := g.runEnv(ctx, []string{"LC_ALL=C"}, "rev-parse", "--show-toplevel")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == notRepoExit && strings.Contains(stderr, notRepoMessage) {
			return nil, fmt.Errorf("gitx: %s: %w: %w", dir, ErrNotWorkTree, err)
		}
		return nil, fmt.Errorf("gitx: %s: %w", dir, err)
	}
	g.root = strings.TrimSpace(out)
	return g, nil
}

func (g *Exec) bin() string {
	if g.Bin != "" {
		return g.Bin
	}
	return "git"
}

func (g *Exec) run(ctx context.Context, args ...string) (string, error) {
	out, _, err := g.runEnv(ctx, nil, args...)
	return out, err
}

// runEnv runs git with env added to the process environment and returns
// its stdout and stderr; the error names the command and wraps the exec
// error (an *exec.ExitError when git ran and failed).
func (g *Exec) runEnv(ctx context.Context, env []string, args ...string) (string, string, error) {
	full := append([]string{"-C", g.root, "-c", "core.quotepath=off", "-c", "color.ui=false"}, args...)
	cmd := exec.CommandContext(ctx, g.bin(), full...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		return stdout.String(), msg, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.String(), stderr.String(), nil
}

// Root implements Git.
func (g *Exec) Root() string { return g.root }

// GitDir implements Git.
func (g *Exec) GitDir(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return filepath.Clean(strings.TrimSpace(out)), nil
}

// DefaultBranch implements Git.
func (g *Exec) DefaultBranch(ctx context.Context) (string, error) {
	if out, err := g.run(ctx, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(strings.TrimSpace(out), "origin/"), nil
	}
	for _, b := range []string{"main", "master"} {
		if _, err := g.run(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			return b, nil
		}
	}
	return "", fmt.Errorf("gitx: no default branch: %w", ErrNotFound)
}

// RemoteURL implements Git.
func (g *Exec) RemoteURL(ctx context.Context, remote string) (string, error) {
	out, err := g.run(ctx, "remote", "get-url", remote)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return strings.TrimSpace(out), nil
}

const refFormat = "%(refname)%00%(refname:short)%00%(objectname)%00%(upstream:short)%00%(subject)%00%(authorname)%00%(committerdate:unix)"

// Refs implements Git.
func (g *Exec) Refs(ctx context.Context, remotes bool) ([]Ref, error) {
	patterns := []string{"refs/heads"}
	if remotes {
		patterns = append(patterns, "refs/remotes")
	}
	args := append([]string{"for-each-ref", "--format=" + refFormat}, patterns...)
	out, err := g.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x00")
		if len(f) < 7 {
			continue
		}
		if strings.HasSuffix(f[0], "/HEAD") {
			continue
		}
		secs, _ := strconv.ParseInt(f[6], 10, 64)
		refs = append(refs, Ref{
			FullName: f[0], Name: f[1], SHA: f[2], Upstream: f[3], Subject: f[4], Author: f[5],
			CommitDate: time.Unix(secs, 0).UTC(), Remote: strings.HasPrefix(f[0], "refs/remotes/"),
		})
	}
	return refs, nil
}

// ResolveRef implements Git.
func (g *Exec) ResolveRef(ctx context.Context, rev string) (string, error) {
	out, err := g.run(ctx, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrNotFound, rev)
	}
	return strings.TrimSpace(out), nil
}

// MergeBase implements Git.
func (g *Exec) MergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := g.run(ctx, "merge-base", a, b)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Diff implements Git.
func (g *Exec) Diff(ctx context.Context, base, head string) (string, error) {
	return g.run(ctx, "diff", "--no-color", "--no-ext-diff", "-M", "--src-prefix=a/", "--dst-prefix=b/", base, head)
}

// CountAhead implements Git.
func (g *Exec) CountAhead(ctx context.Context, base, head string) (int, error) {
	out, err := g.run(ctx, "rev-list", "--count", base+".."+head)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// Log implements Git.
func (g *Exec) Log(ctx context.Context, base, head string, limit int) ([]Commit, error) {
	args := []string{"log", "--format=%H%x00%s%x00%an%x00%ct"}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
	}
	rng := head
	if base != "" {
		rng = base + ".." + head
	}
	out, err := g.run(ctx, append(args, rng)...)
	if err != nil {
		return nil, err
	}
	var cs []Commit
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\x00")
		if len(f) < 4 {
			continue
		}
		secs, _ := strconv.ParseInt(f[3], 10, 64)
		cs = append(cs, Commit{SHA: f[0], Subject: f[1], Author: f[2], Date: time.Unix(secs, 0).UTC()})
	}
	return cs, nil
}

// Show implements Git.
func (g *Exec) Show(ctx context.Context, rev, path string) ([]byte, error) {
	out, err := g.run(ctx, "show", rev+":"+path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s:%s", ErrNotFound, rev, path)
	}
	return []byte(out), nil
}

// ListFiles implements Git. Names are NUL-separated (-z): core.quotepath=off
// stops quoting non-ASCII bytes, but git still C-quotes names with a quote,
// backslash, tab or newline in line mode.
func (g *Exec) ListFiles(ctx context.Context, rev string) ([]string, error) {
	out, err := g.run(ctx, "ls-tree", "-r", "-z", "--name-only", rev)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range strings.Split(out, "\x00") {
		if l != "" {
			files = append(files, l)
		}
	}
	return files, nil
}

// IsDirty implements Git.
func (g *Exec) IsDirty(ctx context.Context) (bool, error) {
	out, err := g.run(ctx, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) != "", nil
}

// Checkout implements Git. The trailing "--" makes git read ref as a
// revision only, never as a pathspec that would overwrite work-tree files.
func (g *Exec) Checkout(ctx context.Context, ref string) error {
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("gitx: refusing to check out %q", ref)
	}
	_, err := g.run(ctx, "checkout", ref, "--")
	return err
}
