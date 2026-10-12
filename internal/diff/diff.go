// Package diff parses git's unified diff output into files, hunks and lines,
// and computes intra-line character diffs for paired lines. It has no I/O.
package diff

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"
)

// Op is a diff line operation.
type Op byte

// Line operations.
const (
	Context Op = ' '
	Add     Op = '+'
	Del     Op = '-'
)

// Line is one diff line. OldNo/NewNo are 1-based; 0 when absent on that side.
type Line struct {
	Op    Op     `json:"op"`
	Text  string `json:"text"` // without the leading op character
	OldNo int    `json:"oldNo,omitempty"`
	NewNo int    `json:"newNo,omitempty"`
	NoEOL bool   `json:"noEol,omitempty"` // followed by "\ No newline at end of file"
}

// Hunk is one @@ block.
type Hunk struct {
	OldStart int    `json:"oldStart"`
	OldLines int    `json:"oldLines"`
	NewStart int    `json:"newStart"`
	NewLines int    `json:"newLines"`
	Section  string `json:"section,omitempty"` // text after the second @@ (enclosing function hint)
	Lines    []Line `json:"lines"`
}

// Header returns the hunk's "@@ -a,b +c,d @@" line.
func (h Hunk) Header() string {
	s := fmt.Sprintf("@@ -%d,%d +%d,%d @@", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
	if h.Section != "" {
		s += " " + h.Section
	}
	return s
}

// Body returns the hunk lines in unified form (op + text), newline-separated.
func (h Hunk) Body() string {
	var b strings.Builder
	for i, l := range h.Lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteByte(byte(l.Op))
		b.WriteString(l.Text)
	}
	return b.String()
}

// Counts returns added and deleted line counts.
func (h Hunk) Counts() (adds, dels int) {
	for _, l := range h.Lines {
		switch l.Op {
		case Add:
			adds++
		case Del:
			dels++
		}
	}
	return
}

// File is one file's diff.
type File struct {
	OldPath    string `json:"oldPath"` // "" when added
	NewPath    string `json:"newPath"` // "" when deleted
	Added      bool   `json:"added,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
	Renamed    bool   `json:"renamed,omitempty"`
	Similarity int    `json:"similarity,omitempty"`
	Binary     bool   `json:"binary,omitempty"`
	OldMode    string `json:"oldMode,omitempty"`
	NewMode    string `json:"newMode,omitempty"`
	Hunks      []Hunk `json:"hunks"`
}

// Path returns the new path, or the old one for deletions.
func (f File) Path() string {
	if f.NewPath != "" {
		return f.NewPath
	}
	return f.OldPath
}

// Counts sums added and deleted lines over all hunks.
func (f File) Counts() (adds, dels int) {
	for _, h := range f.Hunks {
		a, d := h.Counts()
		adds += a
		dels += d
	}
	return
}

// Parse parses `git diff` output (with a/ b/ prefixes).
func Parse(text string) ([]File, error) {
	var files []File
	var cur *File
	var hunk *Hunk
	oldNo, newNo := 0, 0

	flushHunk := func() {
		if cur != nil && hunk != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	flushFile := func() {
		flushHunk()
		if cur != nil {
			files = append(files, *cur)
		}
		cur = nil
	}

	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		ln := sc.Text()
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			flushFile()
			a, b := splitGitHeader(strings.TrimPrefix(ln, "diff --git "))
			cur = &File{OldPath: a, NewPath: b}
		case cur == nil:
			// Preamble (e.g. commit message from format-patch); ignore.
		case hunk != nil && len(ln) > 0 && (ln[0] == ' ' || ln[0] == '+' || ln[0] == '-'):
			l := Line{Op: Op(ln[0]), Text: ln[1:]}
			switch l.Op {
			case Context:
				l.OldNo, l.NewNo = oldNo, newNo
				oldNo++
				newNo++
			case Add:
				l.NewNo = newNo
				newNo++
			case Del:
				l.OldNo = oldNo
				oldNo++
			}
			hunk.Lines = append(hunk.Lines, l)
		case hunk != nil && ln == "":
			// Some tools strip the space of empty context lines.
			hunk.Lines = append(hunk.Lines, Line{Op: Context, OldNo: oldNo, NewNo: newNo})
			oldNo++
			newNo++
		case strings.HasPrefix(ln, `\ `):
			if hunk != nil && len(hunk.Lines) > 0 {
				hunk.Lines[len(hunk.Lines)-1].NoEOL = true
			}
		case strings.HasPrefix(ln, "@@ "):
			flushHunk()
			h, err := parseHunkHeader(ln)
			if err != nil {
				return nil, fmt.Errorf("diff: line %d: %w", lineNo, err)
			}
			hunk = &h
			oldNo, newNo = h.OldStart, h.NewStart
		case strings.HasPrefix(ln, "--- "):
			if p := stripPrefix(strings.TrimPrefix(ln, "--- ")); p == "/dev/null" {
				cur.OldPath, cur.Added = "", true
			} else {
				cur.OldPath = p
			}
		case strings.HasPrefix(ln, "+++ "):
			if p := stripPrefix(strings.TrimPrefix(ln, "+++ ")); p == "/dev/null" {
				cur.NewPath, cur.Deleted = "", true
			} else {
				cur.NewPath = p
			}
		case strings.HasPrefix(ln, "new file mode "):
			cur.Added, cur.OldPath, cur.NewMode = true, "", strings.TrimPrefix(ln, "new file mode ")
		case strings.HasPrefix(ln, "deleted file mode "):
			cur.Deleted, cur.NewPath, cur.OldMode = true, "", strings.TrimPrefix(ln, "deleted file mode ")
		case strings.HasPrefix(ln, "old mode "):
			cur.OldMode = strings.TrimPrefix(ln, "old mode ")
		case strings.HasPrefix(ln, "new mode "):
			cur.NewMode = strings.TrimPrefix(ln, "new mode ")
		case strings.HasPrefix(ln, "rename from "):
			cur.Renamed, cur.OldPath = true, strings.TrimPrefix(ln, "rename from ")
		case strings.HasPrefix(ln, "rename to "):
			cur.Renamed, cur.NewPath = true, strings.TrimPrefix(ln, "rename to ")
		case strings.HasPrefix(ln, "similarity index "):
			cur.Similarity, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(ln, "similarity index "), "%"))
		case strings.HasPrefix(ln, "Binary files ") || ln == "GIT binary patch":
			cur.Binary = true
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flushFile()
	return files, nil
}

// splitGitHeader splits "a/x b/y" (paths without spaces handled exactly; with
// spaces, the --- / +++ lines that follow correct them).
func splitGitHeader(s string) (string, string) {
	if i := strings.Index(s, " b/"); i >= 0 && strings.HasPrefix(s, "a/") {
		return s[2:i], s[i+3:]
	}
	parts := strings.SplitN(s, " ", 2)
	if len(parts) == 2 {
		return stripPrefix(parts[0]), stripPrefix(parts[1])
	}
	return s, s
}

func stripPrefix(p string) string {
	p = strings.TrimSuffix(p, "\t")
	if i := strings.IndexByte(p, '\t'); i >= 0 {
		p = p[:i]
	}
	if strings.HasPrefix(p, "a/") || strings.HasPrefix(p, "b/") {
		return p[2:]
	}
	return p
}

func parseHunkHeader(ln string) (Hunk, error) {
	// @@ -l[,s] +l[,s] @@[ section]
	rest := strings.TrimPrefix(ln, "@@ ")
	end := strings.Index(rest, " @@")
	if end < 0 {
		return Hunk{}, fmt.Errorf("bad hunk header %q", ln)
	}
	ranges := strings.Fields(rest[:end])
	if len(ranges) != 2 || !strings.HasPrefix(ranges[0], "-") || !strings.HasPrefix(ranges[1], "+") {
		return Hunk{}, fmt.Errorf("bad hunk header %q", ln)
	}
	var h Hunk
	var err error
	if h.OldStart, h.OldLines, err = parseRange(ranges[0][1:]); err != nil {
		return Hunk{}, err
	}
	if h.NewStart, h.NewLines, err = parseRange(ranges[1][1:]); err != nil {
		return Hunk{}, err
	}
	h.Section = strings.TrimSpace(rest[end+3:])
	return h, nil
}

func parseRange(s string) (start, count int, err error) {
	a, b, ok := strings.Cut(s, ",")
	if start, err = strconv.Atoi(a); err != nil {
		return 0, 0, fmt.Errorf("bad range %q", s)
	}
	if !ok {
		return start, 1, nil
	}
	if count, err = strconv.Atoi(b); err != nil {
		return 0, 0, fmt.Errorf("bad range %q", s)
	}
	return start, count, nil
}
