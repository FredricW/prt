package surface

import (
	"bytes"
	"context"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Bounds on the file contents read through Source.
const (
	maxSrcFiles = 64        // whole files read per side
	maxSrcBytes = 512 << 10 // larger files are left unread (diff-only detection)
)

// side is one side of the diff.
type side int

const (
	sideOld side = iota // base: removed and context lines
	sideNew             // head: added and context lines
)

// op returns the diff op of the side's changed lines.
func (s side) op() byte {
	if s == sideOld {
		return '-'
	}
	return '+'
}

// sides lists both sides, old first.
var sides = [2]side{sideOld, sideNew}

// view is a unit's diff split into its two sides, each scanned.
type view struct {
	u         *model.ChangeUnit
	f         *fileInfo
	sides     [2][]line // context plus removed (old) or added (new) lines
	changed   [2][]line // the removed (old) and added (new) lines alone
	truncated bool      // the unit had more than maxUnitLines lines
	docs      [2]*doc   // sideDoc's choice per side, once made
}

// newView splits and scans u's diff, numbering lines from its range starts.
func newView(u *model.ChangeUnit, f *fileInfo) *view {
	v := &view{u: u, f: f}
	if u.Diff == "" {
		return v
	}
	lx := [2]lexer{{lang: f.lang}, {lang: f.lang}}
	no := [2]int{max(u.Range.Old.Start, 1), max(u.Range.New.Start, 1)}
	n := 0
	for raw := range strings.SplitSeq(strings.TrimSuffix(u.Diff, "\n"), "\n") {
		if n++; n > maxUnitLines {
			v.truncated = true
			break
		}
		op, text := splitOp(raw)
		for _, s := range sides {
			if op == ' ' || op == s.op() {
				l := scanLine(&lx[s], no[s], op, text)
				v.sides[s] = append(v.sides[s], l)
				if op == s.op() {
					v.changed[s] = append(v.changed[s], l)
				}
				no[s]++
			}
		}
	}
	return v
}

// changedLines returns the unit's changed lines on side s (shared: callers
// must not modify the slice).
func (v *view) changedLines(s side) []line { return v.changed[s] }

// fileInfo is one changed file of the item and its units.
type fileInfo struct {
	path    string
	oldPath string // base path (differs from path when renamed)
	lang    string
	test    bool
	db      dbFile
	file    model.File // zero when the file is not in Input.Files
	views   []*view
	// changed holds the changed line numbers of every unit of the file,
	// per side.
	changed [2]map[int]bool
	full    [2]*doc // whole-file docs read through Source (nil when unavailable)
	read    [2]bool
}

// isChanged reports whether line no on side s is a changed line of the file.
func (f *fileInfo) isChanged(s side, no int) bool { return f.changed[s][no] }

// doc is a run of consecutive scanned lines of one side of a file: the
// whole file read through Source, or a unit's lines.
type doc struct {
	path  string
	lang  string
	lines []line
	first int    // line number of lines[0]
	full  bool   // the whole file
	sym   string // the unit's symbol, for a unit doc
	// Analyses, computed on first use.
	gi    *goInfo
	ji    *jsInfo
	pi    *pyInfo
	downs []int8 // per line: 0 no migration section marker yet, 1 up, 2 down
	seq   int8   // 0 unknown, 1 knex, 2 sequelize (JS migrations)
	// jsonUse memoises whether a type name appears next to a JSON writer.
	jsonUse map[string]bool
}

// idx returns the index of line number no, or -1.
func (d *doc) idx(no int) int {
	if d == nil {
		return -1
	}
	i := no - d.first
	if i < 0 || i >= len(d.lines) {
		return -1
	}
	return i
}

// newFileDoc scans a whole file.
func newFileDoc(p, lang string, data []byte) *doc {
	d := &doc{path: p, lang: lang, first: 1, full: true}
	lx := lexer{lang: lang}
	text := strings.TrimSuffix(string(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))), "\n")
	no := 1
	for t := range strings.SplitSeq(text, "\n") {
		d.lines = append(d.lines, scanLine(&lx, no, ' ', t))
		no++
	}
	return d
}

// unitDoc is the doc of a unit's lines on side s (nil when the side is
// empty).
func unitDoc(v *view, s side) *doc {
	ls := v.sides[s]
	if len(ls) == 0 {
		return nil
	}
	return &doc{path: v.f.path, lang: v.f.lang, lines: ls, first: ls[0].no, sym: v.u.Symbol}
}

// reader reads whole files through Source, bounded and cached per file.
type reader struct {
	ctx   context.Context
	src   Source
	revs  [2]string
	reads [2]int
	notes *notes
}

// fullDoc returns f's whole-file doc on side s, reading it on first use;
// nil when there is no Source, the file does not exist on that side, or it
// cannot be read.
func (r *reader) fullDoc(f *fileInfo, s side) *doc {
	if f.read[s] {
		return f.full[s]
	}
	f.read[s] = true
	if s == sideOld && f.file.Added || s == sideNew && f.file.Deleted {
		return nil
	}
	if r.src == nil || r.revs[s] == "" {
		r.notes.add(noteNoSource)
		return nil
	}
	if r.reads[s] >= maxSrcFiles {
		r.notes.add(noteReadCap)
		return nil
	}
	r.reads[s]++
	p := f.path
	if s == sideOld && f.oldPath != "" {
		p = f.oldPath
	}
	data, err := r.src.Show(r.ctx, r.revs[s], p)
	if err != nil || len(data) > maxSrcBytes {
		r.notes.add(noteUnreadable)
		return nil
	}
	f.full[s] = newFileDoc(f.path, f.lang, data)
	return f.full[s]
}

// exists reports whether a file other than the item's changed files exists
// at side s (e.g. the settings.py next to a urls.py); false without a
// Source.
func (r *reader) exists(s side, p string) bool {
	if r.src == nil || r.revs[s] == "" || r.reads[s] >= maxSrcFiles {
		return false
	}
	r.reads[s]++
	_, err := r.src.Show(r.ctx, r.revs[s], p)
	return err == nil
}

// sideDoc is the best doc for v's side s: the whole file when it was read
// and agrees with the unit's lines, else the unit's own lines.
func (b *builder) sideDoc(v *view, s side) *doc {
	if v.docs[s] != nil {
		return v.docs[s]
	}
	d := unitDoc(v, s)
	if full := b.rd.fullDoc(v.f, s); full != nil && agrees(full, v.sides[s]) {
		d = full
	}
	v.docs[s] = d
	return d
}

// agrees reports whether every line of ls has the same text in d.
func agrees(d *doc, ls []line) bool {
	for _, l := range ls {
		i := d.idx(l.no)
		if i < 0 || d.lines[i].text != l.text {
			return false
		}
	}
	return true
}

// fileDocs are the docs of f on side s for prefix bindings: the whole file
// when readable, else each unit's lines.
func (b *builder) fileDocs(f *fileInfo, s side) []*doc {
	if full := b.rd.fullDoc(f, s); full != nil {
		return []*doc{full}
	}
	var out []*doc
	for _, v := range f.views {
		if d := unitDoc(v, s); d != nil {
			out = append(out, d)
		}
	}
	return out
}

// notes collects the degradation notes of one Build (ChangeSurface.Note).
type notes struct{ set map[string]bool }

// Degradation notes, in the order they are reported.
const (
	noteNoSource   = "file contents unavailable: router prefixes and enclosing types from the diff only"
	noteUnreadable = "some files not readable: detection from the diff only there"
	noteReadCap    = "file read cap reached: detection from the diff only for the rest"
	noteTruncated  = "large diff: detection capped per unit"
	noteStopped    = "detection stopped early (cancelled)"
)

var noteOrder = []string{noteStopped, noteTruncated, noteNoSource, noteUnreadable, noteReadCap}

func (n *notes) add(s string) {
	if n.set == nil {
		n.set = map[string]bool{}
	}
	n.set[s] = true
}

// String joins the notes in a fixed order.
func (n *notes) String() string {
	var out []string
	for _, s := range noteOrder {
		if n.set[s] {
			out = append(out, s)
		}
	}
	return strings.Join(out, "; ")
}
