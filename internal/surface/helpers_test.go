package surface

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/model"
)

// Revisions of the test sources.
const (
	revBase = "base"
	revHead = "head"
)

// mapSource is a Source over in-memory files: rev → path → content.
type mapSource map[string]map[string]string

func (m mapSource) Show(_ context.Context, rev, p string) ([]byte, error) {
	if c, ok := m[rev][p]; ok {
		return []byte(c), nil
	}
	return nil, fmt.Errorf("show %s:%s: %w", rev, p, fs.ErrNotExist)
}

// parsePatch parses a git diff (a format-patch preamble is skipped).
func parsePatch(t testing.TB, text string) []diff.File {
	t.Helper()
	if i := strings.Index(text, "diff --git "); i > 0 {
		text = text[i:]
	}
	files, err := diff.Parse(text)
	if err != nil {
		t.Fatalf("parse patch: %v", err)
	}
	return files
}

// patchInput turns a diff into an Input: one unit per hunk, no Source.
func patchInput(t testing.TB, text string) Input {
	t.Helper()
	files := parsePatch(t, text)
	units, mfiles := unitsOf(files)
	return Input{Units: units, Files: mfiles, BaseSHA: revBase, HeadSHA: revHead}
}

// unitsOf builds change units (one per hunk) and files from parsed diffs.
func unitsOf(files []diff.File) ([]model.ChangeUnit, []model.File) {
	var (
		units  []model.ChangeUnit
		mfiles []model.File
	)
	for _, f := range files {
		p := f.Path()
		mf := model.File{ItemID: "pr:1", Path: p, Added: f.Added, Deleted: f.Deleted, Renamed: f.Renamed}
		if f.Renamed {
			mf.OldPath = f.OldPath
		}
		mfiles = append(mfiles, mf)
		for _, h := range f.Hunks {
			units = append(units, hunkUnit(p, h, len(units)))
		}
	}
	return units, mfiles
}

var reSection = regexp.MustCompile(`func\s+(?:\([^)]*\)\s*)?(\w+)|def\s+(\w+)|function\s+(\w+)`)

// hunkUnit builds the change unit of one hunk, like stage 1 does for a
// hunk it cannot split; the symbol comes from git's section heading.
func hunkUnit(p string, h diff.Hunk, order int) model.ChangeUnit {
	u := model.ChangeUnit{ItemID: "pr:1", FileID: p, Path: p, Order: order}
	u.Range.Old.Start, u.Range.New.Start = h.OldStart, h.NewStart
	var body []string
	for _, l := range h.Lines {
		body = append(body, string(rune(l.Op))+l.Text)
		if l.Op != diff.Add {
			if u.Range.Old.Count == 0 {
				u.Range.Old.Start = l.OldNo
			}
			u.Range.Old.Count++
		}
		if l.Op != diff.Del {
			if u.Range.New.Count == 0 {
				u.Range.New.Start = l.NewNo
			}
			u.Range.New.Count++
		}
		switch l.Op {
		case diff.Add:
			u.Adds++
		case diff.Del:
			u.Dels++
		}
	}
	u.Diff = strings.Join(body, "\n")
	u.ID = model.UnitID(p, u.Diff)
	switch {
	case u.Dels == 0:
		u.Kind = model.UnitAdded
	case u.Adds == 0:
		u.Kind = model.UnitDeleted
	default:
		u.Kind = model.UnitModified
	}
	if m := reSection.FindStringSubmatch(h.Section); m != nil {
		u.Symbol = m[1] + m[2] + m[3]
	}
	return u
}

// applyPatch returns the head content of a file from its base content and
// its diff.
func applyPatch(base string, f diff.File) string {
	if f.Deleted {
		return ""
	}
	var old []string
	if base != "" {
		old = strings.Split(strings.TrimSuffix(base, "\n"), "\n")
	}
	var out []string
	next := 0 // index of the next base line to copy
	for _, h := range f.Hunks {
		start := h.OldStart - 1
		if h.OldLines == 0 {
			start = h.OldStart
		}
		for next < start && next < len(old) {
			out = append(out, old[next])
			next++
		}
		for _, l := range h.Lines {
			switch l.Op {
			case diff.Context:
				out = append(out, l.Text)
				next++
			case diff.Del:
				next++
			case diff.Add:
				out = append(out, l.Text)
			}
		}
	}
	for next < len(old) {
		out = append(out, old[next])
		next++
	}
	return strings.Join(out, "\n") + "\n"
}

// treeSource returns a Source whose base revision is the tree under dir
// and whose head revision is that tree with files applied.
func treeSource(t testing.TB, dir string, files []diff.File) mapSource {
	t.Helper()
	src := mapSource{revBase: {}, revHead: {}}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		src[revBase][filepath.ToSlash(rel)] = string(data)
		src[revHead][filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("read tree %s: %v", dir, err)
	}
	for _, f := range files {
		if f.Deleted {
			delete(src[revHead], f.OldPath)
			continue
		}
		base := src[revBase][f.OldPath]
		if f.Renamed {
			delete(src[revHead], f.OldPath)
		}
		src[revHead][f.NewPath] = applyPatch(base, f)
	}
	return src
}

// readFile reads a test file.
func readFile(t testing.TB, p string) string {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(data)
}

// loadCases reads testdata/<dir>/cases.txt: diffs separated by lines
// "=== <name>".
func loadCases(t testing.TB, dir string) map[string]string {
	t.Helper()
	text := readFile(t, filepath.Join("testdata", dir, "cases.txt"))
	out := map[string]string{}
	name := ""
	var b strings.Builder
	flush := func() {
		if name != "" {
			out[name] = b.String()
		}
		b.Reset()
	}
	for ln := range strings.SplitSeq(text, "\n") {
		if n, ok := strings.CutPrefix(ln, "=== "); ok {
			flush()
			name = strings.TrimSpace(n)
			continue
		}
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	flush()
	return out
}

// fmtEntry is an entry in test words: "added route: GET /x".
func fmtEntry(e model.SurfaceEntry) string {
	return e.Change + " " + e.What + ": " + e.Name
}

// allEntries lists every entry of a surface in test words, sorted.
func allEntries(cs *model.ChangeSurface) []string {
	var out []string
	for _, es := range [][]model.SurfaceEntry{cs.API, cs.DB, cs.Flags} {
		for _, e := range es {
			out = append(out, fmtEntry(e))
		}
	}
	slices.Sort(out)
	return out
}

// find returns the entry of cs with the given test words.
func find(t testing.TB, cs *model.ChangeSurface, words string) model.SurfaceEntry {
	t.Helper()
	for _, es := range [][]model.SurfaceEntry{cs.API, cs.DB, cs.Flags} {
		for _, e := range es {
			if fmtEntry(e) == words {
				return e
			}
		}
	}
	t.Fatalf("no entry %q in\n%s", words, strings.Join(allEntries(cs), "\n"))
	return model.SurfaceEntry{}
}

// assertEntries checks that cs holds exactly want (in test words).
func assertEntries(t testing.TB, cs *model.ChangeSurface, want []string) {
	t.Helper()
	got := allEntries(cs)
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("entries:\n got: %q\nwant: %q", got, want)
	}
}
