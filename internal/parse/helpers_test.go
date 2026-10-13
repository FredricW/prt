package parse

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/model"
)

const (
	baseRev = "base"
	headRev = "head"
)

// fakeSource serves file contents keyed by rev + ":" + path.
type fakeSource map[string][]byte

func (f fakeSource) Show(_ context.Context, rev, path string) ([]byte, error) {
	b, ok := f[rev+":"+path]
	if !ok {
		return nil, fmt.Errorf("fake: %s:%s: %w", rev, path, errNotFound)
	}
	return b, nil
}

var errNotFound = errors.New("not found")

// change is one file of a synthetic item: old/new contents ("" with the
// matching flag unset = absent side).
type change struct {
	path     string
	old, new string
	added    bool // no base side
	deleted  bool // no head side
}

// fixture reads internal/parse/testdata/<rel>.
func fixture(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// build diffs every change with `git diff --no-index` (local only) and
// registers both sides in a fake source.
func build(t *testing.T, changes ...change) ([]diff.File, fakeSource) {
	t.Helper()
	src := fakeSource{}
	var files []diff.File
	for _, c := range changes {
		f := gitDiff(t, c)
		if !c.added {
			src[baseRev+":"+c.path] = []byte(c.old)
		}
		if !c.deleted {
			src[headRev+":"+c.path] = []byte(c.new)
		}
		files = append(files, f)
	}
	return files, src
}

func gitDiff(t *testing.T, c change) diff.File {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	oldArg, newArg := "/dev/null", "/dev/null"
	if !c.added {
		oldArg = "old"
		writeFile(t, filepath.Join(dir, oldArg), c.old)
	}
	if !c.deleted {
		newArg = "new"
		writeFile(t, filepath.Join(dir, newArg), c.new)
	}
	cmd := exec.CommandContext(context.Background(), "git", "diff", "--no-index", "--no-color", "--no-ext-diff",
		"--src-prefix=a/", "--dst-prefix=b/", "--diff-algorithm=myers", "-U3", "--", oldArg, newArg)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	// Exit status 1 means "files differ".
	var exit *exec.ExitError
	if err != nil && (!errors.As(err, &exit) || exit.ExitCode() != 1) {
		t.Fatalf("git diff: %v", err)
	}
	files, err := diff.Parse(string(out))
	if err != nil || len(files) != 1 {
		t.Fatalf("diff.Parse: %v (%d files)\n%s", err, len(files), out)
	}
	f := files[0]
	f.OldPath, f.NewPath, f.Renamed = c.path, c.path, false
	if c.added {
		f.OldPath = ""
	}
	if c.deleted {
		f.NewPath = ""
	}
	return f
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// split runs Split over the files with the given source.
func split(t *testing.T, files []diff.File, src Source) []model.ChangeUnit {
	t.Helper()
	units, err := Split(context.Background(), Input{
		ItemID: "pr:1", BaseSHA: baseRev, HeadSHA: headRev, Files: files, Src: src,
	})
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	checkInvariants(t, files, units)
	return units
}

// checkInvariants verifies that each hunk's units concatenate back to the
// hunk body, that every unit has a changed line, and that counts, ranges,
// ids and order are consistent. It returns the units grouped by hunk.
func checkInvariants(t *testing.T, files []diff.File, units []model.ChangeUnit) [][]model.ChangeUnit {
	t.Helper()
	var groups [][]model.ChangeUnit
	i := 0
	for _, f := range files {
		if f.Binary {
			continue
		}
		for _, h := range f.Hunks {
			if !hasChanges(h.Lines) {
				continue
			}
			body, got := h.Body(), ""
			var group []model.ChangeUnit
			for got != body {
				if i >= len(units) {
					t.Fatalf("%s %s: units ran out; got %q want %q", f.Path(), h.Header(), got, body)
				}
				u := units[i]
				i++
				if got != "" {
					got += "\n"
				}
				got += u.Diff
				if u.Path != f.Path() || !strings.HasPrefix(body, got) {
					t.Fatalf("%s %s: unit %d (%s) does not continue the hunk:\n%s", f.Path(), h.Header(), u.Order, u.Path, u.Diff)
				}
				checkUnit(t, u, i-1)
				group = append(group, u)
			}
			groups = append(groups, group)
		}
	}
	if i != len(units) {
		t.Fatalf("%d units left over after all hunks", len(units)-i)
	}
	return groups
}

func checkUnit(t *testing.T, u model.ChangeUnit, order int) {
	t.Helper()
	var adds, dels, oldN, newN int
	for _, l := range strings.Split(u.Diff, "\n") {
		switch l[0] {
		case '+':
			adds++
			newN++
		case '-':
			dels++
			oldN++
		default:
			oldN++
			newN++
		}
	}
	switch {
	case adds+dels == 0:
		t.Errorf("unit %d has no changed line:\n%s", order, u.Diff)
	case adds != u.Adds || dels != u.Dels:
		t.Errorf("unit %d counts +%d -%d, diff has +%d -%d", order, u.Adds, u.Dels, adds, dels)
	case u.Range.Old.Count != oldN || u.Range.New.Count != newN:
		t.Errorf("unit %d range %+v, diff has %d old / %d new lines", order, u.Range, oldN, newN)
	case u.Order != order:
		t.Errorf("unit order %d, want %d", u.Order, order)
	case u.ID != model.UnitID(u.Path, u.Diff) || u.FileID != u.Path:
		t.Errorf("unit %d id/file id not content-based", order)
	case u.ShapeHash == "":
		t.Errorf("unit %d has no shape hash", order)
	}
}

// bySymbol returns the units of path with the given symbol.
func bySymbol(units []model.ChangeUnit, path, sym string) []model.ChangeUnit {
	var out []model.ChangeUnit
	for _, u := range units {
		if u.Path == path && u.Symbol == sym {
			out = append(out, u)
		}
	}
	return out
}

func symbols(units []model.ChangeUnit) []string {
	out := make([]string, len(units))
	for i, u := range units {
		out[i] = fmt.Sprintf("%q", u.Symbol)
	}
	return out
}
