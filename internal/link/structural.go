package link

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/parse"
)

// fileSides is one changed file's paths on each side ("" when absent).
type fileSides struct {
	base, head string
}

// sides maps every unit path to its file's base and head paths, from
// in.Files when known (renames, added and deleted files), else the unit
// path on both sides.
func sides(files []model.File, units []model.ChangeUnit) map[string]fileSides {
	out := map[string]fileSides{}
	for _, f := range files {
		s := fileSides{base: f.Path, head: f.Path}
		if f.OldPath != "" {
			s.base = f.OldPath
		}
		if f.Added {
			s.base = ""
		}
		if f.Deleted {
			s.head = ""
		}
		out[f.Path] = s
	}
	for _, u := range units {
		if _, ok := out[u.Path]; !ok {
			out[u.Path] = fileSides{base: u.Path, head: u.Path}
		}
	}
	return out
}

// run is the source-backed half of Structural for one item.
type run struct {
	units  []model.ChangeUnit
	lines  []unitLines
	sides  map[string]fileSides
	byPath map[string][]int       // unit indices by head and base path
	head   map[string]*fileSyntax // by head path; nil when unusable
	base   map[string]*fileSyntax // by base path; nil when unusable
	view   *view
}

// newRun loads both sides of every changed file and the symbol index.
func newRun(ctx context.Context, in Input, units []model.ChangeUnit) (*run, error) {
	r := &run{
		units:  units,
		lines:  make([]unitLines, len(units)),
		sides:  sides(in.Files, units),
		byPath: map[string][]int{},
		head:   map[string]*fileSyntax{},
		base:   map[string]*fileSyntax{},
	}
	for i, u := range units {
		r.lines[i] = changedLines(u)
		s := r.sides[u.Path]
		r.byPath[u.Path] = append(r.byPath[u.Path], i)
		if s.base != "" && s.base != u.Path {
			r.byPath[s.base] = append(r.byPath[s.base], i)
		}
	}
	paths := make([]string, 0, len(r.sides))
	for p := range r.sides {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("link: %w", err)
		}
		s := r.sides[p]
		if s.head != "" {
			r.head[s.head] = loadSyntax(ctx, in.Src, in.HeadSHA, s.head)
		}
		if s.base != "" {
			r.base[s.base] = loadSyntax(ctx, in.Src, in.BaseSHA, s.base)
		}
	}
	idx, err := loadIndex(ctx, in)
	if err != nil {
		return nil, err
	}
	r.view = newView(idx)
	for _, p := range paths {
		s := r.sides[p]
		r.view.overlay(s.base, r.base[s.base])
		r.view.overlay(s.head, r.head[s.head])
	}
	r.view.finish()
	return r, nil
}

// loadSyntax fetches and parses one file side; any problem gives nil.
func loadSyntax(ctx context.Context, src Source, rev, p string) *fileSyntax {
	lang := parse.Lang(p)
	if lang == "" || rev == "" {
		return nil
	}
	b, err := src.Show(ctx, rev, p)
	if err != nil {
		return nil
	}
	return parseSyntax(ctx, lang, b)
}

// unitSide returns the path, syntax and own rows of a unit: base rows of
// Range.Old for pure deletions, head rows of Range.New otherwise.
func (r *run) unitSide(i int) (string, *fileSyntax, map[int]bool) {
	u := r.units[i]
	s := r.sides[u.Path]
	if baseSide(u) {
		return s.base, r.base[s.base], spanRows(u.Range.Old)
	}
	return s.head, r.head[s.head], spanRows(u.Range.New)
}

// symMatches reports whether a unit symbol defines name: equal, equal last
// segment ("UserStore.Get" for Get), or a member of name ("ApiError.constructor"
// for ApiError, so constructing or importing a class reaches its members).
func symMatches(sym, name string) bool {
	if sym == "" || name == "" {
		return false
	}
	return sym == name || lastSegment(sym) == name || strings.HasPrefix(sym, name+".")
}

// defining returns the changed units of file p that define name: by symbol,
// or by containing a changed local re-export (`export { name }`). When none
// do, `export … from` re-exports are followed (up to maxReExportDepth hops).
func (r *run) defining(p, name string, depth int) []string {
	var out []string
	for _, i := range r.byPath[p] {
		u := r.units[i]
		if symMatches(u.Symbol, name) || r.reExportsChanged(i, p, name) {
			out = append(out, u.ID)
		}
	}
	if len(out) > 0 || depth >= maxReExportDepth {
		return out
	}
	for _, t := range r.view.reExportTargets(p, name) {
		out = append(out, r.defining(t.path, t.name, depth+1)...)
	}
	return out
}

// reExportsChanged reports whether unit i changes a line of file p holding
// a local `export { name }`.
func (r *run) reExportsChanged(i int, p, name string) bool {
	check := func(syn *fileSyntax, rows map[int]bool) bool {
		if syn == nil {
			return false
		}
		for _, e := range syn.Exports {
			if e.local() == name && rows[e.Row] {
				return true
			}
		}
		return false
	}
	return check(r.head[p], r.lines[i].added) || check(r.base[p], r.lines[i].deleted)
}

// calls links each unit to the changed units defining what its own lines call.
func (r *run) calls(ctx context.Context, ls *linkSet) error {
	for i, u := range r.units {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("link: %w", err)
		}
		p, syn, rows := r.unitSide(i)
		if syn == nil {
			continue
		}
		for _, c := range syn.Calls {
			if !rows[c.Row] {
				continue
			}
			f := r.view.resolveCallee(p, syn, c)
			if f == "" {
				continue
			}
			for _, to := range r.defining(f, c.Name, 0) {
				ls.structural(u.ID, to, model.LinkCalls)
			}
		}
	}
	return nil
}

// imports links each unit whose changed import lines name a symbol to the
// changed units defining it.
func (r *run) imports(ctx context.Context, ls *linkSet) error {
	for i, u := range r.units {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("link: %w", err)
		}
		s := r.sides[u.Path]
		for _, ref := range r.importedRefs(s.head, r.head[s.head], r.lines[i].added) {
			for _, to := range r.defining(ref.path, ref.name, 0) {
				ls.structural(u.ID, to, model.LinkImports)
			}
		}
		for _, ref := range r.importedRefs(s.base, r.base[s.base], r.lines[i].deleted) {
			for _, to := range r.defining(ref.path, ref.name, 0) {
				ls.structural(u.ID, to, model.LinkImports)
			}
		}
	}
	return nil
}

// importedRefs returns the (file, name) pairs named by the import
// statements of file p touched by the changed rows. Named imports count
// when the specifier's own row changed; a namespace import (Go package,
// Python module, `import * as x`) counts when any statement row changed and
// names what the file calls through its binding (x.Name(…)).
func (r *run) importedRefs(p string, syn *fileSyntax, changed map[int]bool) []symbolRef {
	if syn == nil || len(changed) == 0 {
		return nil
	}
	var out []symbolRef
	for _, imp := range syn.Imports {
		if len(imp.Names) > 0 {
			for _, n := range imp.Names {
				if !changed[n.Row] {
					continue
				}
				for _, f := range r.view.resolveSpec(p, imp.Spec) {
					out = append(out, symbolRef{path: f, name: n.Name})
				}
			}
			continue
		}
		if imp.Alias == "" || !anyRow(changed, imp.Start, imp.End) {
			continue
		}
		files := r.view.resolveSpec(p, imp.Spec)
		for _, name := range qualifiedCalls(syn, imp.Alias) {
			for _, f := range r.view.declaring(files, name) {
				out = append(out, symbolRef{path: f, name: name})
			}
		}
	}
	return out
}

// qualifiedCalls returns the distinct names called as alias.Name(…).
func qualifiedCalls(syn *fileSyntax, alias string) []string {
	var out []string
	for _, c := range syn.Calls {
		if c.Obj == alias {
			out = append(out, c.Name)
		}
	}
	return dedupe(out)
}

// anyRow reports whether any row in [start, end] is set.
func anyRow(rows map[int]bool, start, end int) bool {
	for r := start; r <= end; r++ {
		if rows[r] {
			return true
		}
	}
	return false
}

// movedDecl is a top-level declaration deleted from or added to a file.
type movedDecl struct {
	path string // base path (deleted) or head path (added)
	decl declInfo
}

// symbolMoves links declarations deleted from one file to declarations with
// the same name and normalised text added in another: every unit covering a
// deleted line of the origin links to every unit covering an added line of
// the destination.
func (r *run) symbolMoves(ls *linkSet) {
	deleted := map[string][]movedDecl{}
	var added []movedDecl
	for _, p := range sortedKeys(r.sides) {
		s := r.sides[p]
		for _, d := range r.vanished(s.base, s.head, r.base, r.head, s.head == "") {
			deleted[d.Name] = append(deleted[d.Name], movedDecl{path: s.base, decl: d})
		}
		for _, d := range r.vanished(s.head, s.base, r.head, r.base, s.base == "") {
			added = append(added, movedDecl{path: s.head, decl: d})
		}
	}
	for _, a := range added {
		for _, d := range deleted[a.decl.Name] {
			if d.path == a.path || d.decl.Text == "" || d.decl.Text != a.decl.Text {
				continue
			}
			from := r.covering(d.path, d.decl, func(ul unitLines) map[int]bool { return ul.deleted })
			to := r.covering(a.path, a.decl, func(ul unitLines) map[int]bool { return ul.added })
			for _, f := range from {
				for _, t := range to {
					ls.structural(f, t, model.LinkMoved)
				}
			}
		}
	}
}

// vanished returns the top-level declarations of file side p (in syn) whose
// name the other side q lacks. otherAbsent says q does not exist (file
// added or deleted); an unreadable q gives nothing.
func (r *run) vanished(p, q string, syn, other map[string]*fileSyntax, otherAbsent bool) []declInfo {
	if p == "" || syn[p] == nil {
		return nil
	}
	names := map[string]bool{}
	if !otherAbsent {
		o := other[q]
		if o == nil {
			return nil
		}
		for _, d := range o.Decls {
			names[d.Name] = true
		}
	}
	var out []declInfo
	for _, d := range syn[p].Decls {
		if d.Top && !names[d.Name] {
			out = append(out, d)
		}
	}
	return out
}

// covering returns the ids of units of file p with a changed row (as
// picked by rows) inside the declaration.
func (r *run) covering(p string, d declInfo, rows func(unitLines) map[int]bool) []string {
	var out []string
	for _, i := range r.byPath[p] {
		if anyRow(rows(r.lines[i]), d.Start, d.End) {
			out = append(out, r.units[i].ID)
		}
	}
	return out
}

func sortedKeys(m map[string]fileSides) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// movedLinks links each unit parse paired as moved to its origin.
func movedLinks(units []model.ChangeUnit, ls *linkSet) {
	for _, u := range units {
		if u.MovedFrom != "" {
			ls.structural(u.MovedFrom, u.ID, model.LinkMoved)
		}
	}
}

// hunkIDs numbers units by diff hunk: consecutive units (in Order) of one
// path whose ranges touch on both sides came from the same hunk.
func hunkIDs(units []model.ChangeUnit) []int {
	ids := make([]int, len(units))
	h := 0
	for i := range units {
		if i > 0 && !sameHunk(units[i-1], units[i]) {
			h++
		}
		ids[i] = h
	}
	return ids
}

func sameHunk(a, b model.ChangeUnit) bool {
	return a.Path == b.Path && spanEnd(a.Range.Old) == spanBefore(b.Range.Old) &&
		spanEnd(a.Range.New) == spanBefore(b.Range.New)
}

// spanEnd is the last line of a span (the line before it when empty).
func spanEnd(s model.Span) int {
	if s.Count == 0 {
		return s.Start
	}
	return s.Start + s.Count - 1
}

// spanBefore is the line just before a span.
func spanBefore(s model.Span) int {
	if s.Count == 0 {
		return s.Start
	}
	return s.Start - 1
}

// sameSymbolLinks links units sharing a non-empty Symbol: a star from the
// lowest-Order unit to every other member outside the root's hunk.
func sameSymbolLinks(units []model.ChangeUnit, ls *linkSet) {
	hunks := hunkIDs(units)
	root := map[string]int{}
	for i, u := range units {
		if u.Symbol == "" {
			continue
		}
		r, ok := root[u.Symbol]
		if !ok {
			root[u.Symbol] = i
			continue
		}
		if hunks[r] != hunks[i] {
			ls.structural(units[r].ID, u.ID, model.LinkSameSymbol)
		}
	}
}

// testStem returns a test file's stem without its test markers
// ("client.test.ts" → "client", "users_test.go" → "users",
// "test_report.py" → "report").
func testStem(p string) string {
	s := stem(p)
	switch extOf(p) {
	case ".go":
		return strings.TrimSuffix(s, "_test")
	case ".py":
		if t := strings.TrimPrefix(s, "test_"); t != s {
			return t
		}
		return strings.TrimSuffix(s, "_test")
	}
	if t := strings.TrimSuffix(s, ".test"); t != s {
		return t
	}
	return strings.TrimSuffix(s, ".spec")
}

// testScope returns the directory code under test may live in for test
// file p, and whether subdirectories count: the parent of a __tests__,
// test or tests directory (any depth below it), else p's own directory.
func testScope(p string) (string, bool) {
	segs := strings.Split(dirOf(p), "/")
	for i, s := range segs {
		if testDirs[s] {
			if i == 0 {
				return ".", true
			}
			return strings.Join(segs[:i], "/"), true
		}
	}
	return dirOf(p), false
}

// inScope reports whether code file c is in a test's scope.
func inScope(c, dir string, deep bool) bool {
	cd := dirOf(c)
	if !deep {
		return cd == dir
	}
	return dir == "." || cd == dir || strings.HasPrefix(cd, dir+"/")
}

// symbolKey is the name a test would mention for a unit symbol: its last
// segment, or the class name for constructors.
func symbolKey(sym string) string {
	segs := strings.Split(sym, ".")
	k := segs[len(segs)-1]
	if (k == "constructor" || k == "__init__") && len(segs) > 1 {
		k = segs[len(segs)-2]
	}
	return k
}

// minTestSymbol is the shortest symbol name a test mention can match.
const minTestSymbol = 4

// testLinks links test units to code units by symbol name: a test unit
// links to every code unit whose symbol it mentions. The filename rule is a
// fallback for a test unit that mentions no changed symbol: it then links
// to the units of its code file. Linking every test unit to every unit of
// the same-named file made one component of whole PRs (DECISIONS "M2 test
// links: symbol first, filename as fallback").
func testLinks(units []model.ChangeUnit, ls *linkSet) {
	var tests, code []model.ChangeUnit
	for _, u := range units {
		if isTestPath(u.Path) {
			tests = append(tests, u)
		} else {
			code = append(code, u)
		}
	}
	for _, t := range tests {
		ts := testStem(t.Path)
		dir, deep := testScope(t.Path)
		idents := identifiers(t.Diff)
		var byName, byFile []string
		for _, c := range code {
			if stem(c.Path) == ts && inScope(c.Path, dir, deep) {
				byFile = append(byFile, c.ID)
			}
			key := symbolKey(c.Symbol)
			if c.Symbol != "" && utf8.RuneCountInString(key) >= minTestSymbol && idents[key] {
				byName = append(byName, c.ID)
			}
		}
		to := byName
		if len(to) == 0 {
			to = byFile
		}
		for _, id := range to {
			ls.structural(t.ID, id, model.LinkTests)
		}
	}
}

// identifiers returns the identifier-like words of a unit diff (the +/-/
// space prefix of each line dropped).
func identifiers(unitDiff string) map[string]bool {
	out := map[string]bool{}
	for _, l := range strings.Split(unitDiff, "\n") {
		if l == "" {
			continue
		}
		for _, w := range strings.FieldsFunc(l[1:], func(r rune) bool { return !isIdentRune(r) }) {
			out[w] = true
		}
	}
	return out
}

func isIdentRune(r rune) bool {
	return r == '_' || r == '$' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127
}
