package link

import (
	"path"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/parse"
)

func baseName(p string) string { return path.Base(p) }
func extOf(p string) string    { return path.Ext(p) }
func dirOf(p string) string    { return path.Dir(p) }

// view is the repo symbol index of the base revision overlaid with the
// declarations and imports of the item's changed files (both sides), so new
// head-side symbols resolve too.
type view struct {
	files  map[string]*viewFile
	byName map[string][]string // last name segment → sorted declaring paths
	dirs   map[string][]string // directory → sorted source paths
}

// viewFile is one source file of the view.
type viewFile struct {
	decls   map[string]bool // qualified names and their last segments
	exports map[string]bool // local `export { a }` names
	imports []importInfo
}

func newView(idx *symIndex) *view {
	v := &view{files: map[string]*viewFile{}}
	for p, f := range idx.Files {
		vf := v.file(p)
		for _, d := range f.Decls {
			vf.addDecl(d)
		}
		for _, e := range f.Exports {
			vf.exports[e] = true
		}
		vf.imports = append(vf.imports, f.Imports...)
	}
	return v
}

func (v *view) file(p string) *viewFile {
	f := v.files[p]
	if f == nil {
		f = &viewFile{decls: map[string]bool{}, exports: map[string]bool{}}
		v.files[p] = f
	}
	return f
}

func (f *viewFile) addDecl(name string) {
	f.decls[name] = true
	f.decls[lastSegment(name)] = true
}

// overlay adds one changed file side's syntax to the view.
func (v *view) overlay(p string, syn *fileSyntax) {
	if syn == nil {
		return
	}
	f := v.file(p)
	for _, d := range syn.Decls {
		f.addDecl(d.Name)
	}
	for _, e := range syn.Exports {
		f.exports[e.Name] = true
	}
	f.imports = append(f.imports, syn.Imports...)
}

// finish computes the lookup tables once all overlays are in.
func (v *view) finish() {
	v.byName = map[string][]string{}
	v.dirs = map[string][]string{}
	paths := make([]string, 0, len(v.files))
	for p := range v.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		v.dirs[dirOf(p)] = append(v.dirs[dirOf(p)], p)
		seen := map[string]bool{}
		for d := range v.files[p].decls {
			n := lastSegment(d)
			if !seen[n] {
				seen[n] = true
				v.byName[n] = append(v.byName[n], p)
			}
		}
	}
}

// declares reports whether file p declares name (real declarations only).
func (v *view) declares(p, name string) bool {
	f := v.files[p]
	return f != nil && f.decls[name]
}

// exists reports whether p is a known source file.
func (v *view) exists(p string) bool { return v.files[p] != nil }

// jsExtensions are tried, in order, for an extension-less relative import.
var jsExtensions = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", "/index.ts", "/index.tsx", "/index.js", "/index.jsx"}

// resolveSpec maps an import specifier in file from to the repo files it
// names: relative TS/JS paths (trying extensions and index files), Go
// import paths by directory suffix (every file of the package), Python
// dotted modules relative to the importing file's directory or the repo
// root. External modules resolve to nothing.
func (v *view) resolveSpec(from, spec string) []string {
	switch parse.Lang(from) {
	case "go":
		return v.resolveGo(spec)
	case "python":
		return v.resolvePython(from, spec)
	case "":
		return nil
	default:
		return v.resolveJS(from, spec)
	}
}

func (v *view) resolveJS(from, spec string) []string {
	if !strings.HasPrefix(spec, ".") {
		return nil
	}
	p := path.Clean(path.Join(dirOf(from), spec))
	if parse.Lang(p) != "" && v.exists(p) {
		return []string{p}
	}
	for _, ext := range jsExtensions {
		if v.exists(p + ext) {
			return []string{p + ext}
		}
	}
	return nil
}

// resolveGo returns the files of the repo directory whose path is the
// longest suffix of the import path.
func (v *view) resolveGo(spec string) []string {
	best := ""
	for d := range v.dirs {
		if d == "." || len(d) <= len(best) {
			continue
		}
		if spec == d || strings.HasSuffix(spec, "/"+d) {
			best = d
		}
	}
	if best == "" {
		return nil
	}
	return v.goFiles(best)
}

// goFiles returns the .go files of a directory (one package).
func (v *view) goFiles(dir string) []string {
	var out []string
	for _, p := range v.dirs[dir] {
		if parse.Lang(p) == "go" {
			out = append(out, p)
		}
	}
	return out
}

func (v *view) resolvePython(from, spec string) []string {
	dots := len(spec) - len(strings.TrimLeft(spec, "."))
	rest := strings.ReplaceAll(spec[dots:], ".", "/")
	var bases []string
	if dots > 0 {
		d := dirOf(from)
		for i := 1; i < dots; i++ {
			d = dirOf(d)
		}
		bases = []string{d}
	} else {
		bases = []string{dirOf(from), "."}
	}
	for _, b := range bases {
		p := path.Clean(path.Join(b, rest))
		for _, c := range []string{p + ".py", p + "/__init__.py"} {
			if v.exists(c) {
				return []string{c}
			}
		}
	}
	return nil
}

// maxReExportDepth bounds how many `export … from` hops are followed.
const maxReExportDepth = 3

// symbolRef is a name in a file.
type symbolRef struct {
	path, name string
}

// reExportTargets returns where file p re-exports name from
// (`export { name } from "x"`, `export { a as name } from "x"`,
// `export * from "x"`), with the name as the source module calls it.
func (v *view) reExportTargets(p, name string) []symbolRef {
	f := v.files[p]
	if f == nil {
		return nil
	}
	var out []symbolRef
	for _, imp := range f.imports {
		if !imp.ReExport {
			continue
		}
		orig := ""
		switch {
		case imp.Star:
			orig = name
		default:
			for _, n := range imp.Names {
				if n.local() == name {
					orig = n.Name
				}
			}
		}
		if orig == "" {
			continue
		}
		for _, t := range v.resolveSpec(p, imp.Spec) {
			out = append(out, symbolRef{path: t, name: orig})
		}
	}
	return out
}

// resolveCallee returns the one file defining the callee of c made from
// file p (syntax syn), or "" when it cannot be resolved unambiguously:
//
//  1. a namespace qualifier (import * as x, Go package, Python module)
//     resolves through that import;
//  2. the calling file itself;
//  3. a named import of the callee (re-exports are followed later);
//  4. Go: the calling file's package directory;
//  5. namespace and star imports declaring the name;
//  6. a unique declaration in the index.
func (v *view) resolveCallee(p string, syn *fileSyntax, c callInfo) string {
	n := c.Name
	if c.Obj != "" {
		for _, imp := range syn.Imports {
			if imp.Alias == c.Obj {
				return unique(v.declaring(v.resolveSpec(p, imp.Spec), n))
			}
		}
	}
	if v.declares(p, n) || syn.declares(n) {
		return p
	}
	if c.Obj == "" {
		for _, imp := range syn.Imports {
			for _, in := range imp.Names {
				if in.local() == n {
					return unique(v.resolveSpec(p, imp.Spec))
				}
			}
		}
	}
	if parse.Lang(p) == "go" {
		if f := unique(v.declaring(v.goFiles(dirOf(p)), n)); f != "" {
			return f
		}
	}
	var wide []string
	for _, imp := range syn.Imports {
		if imp.Alias != "" || imp.Star {
			wide = append(wide, v.declaring(v.resolveSpec(p, imp.Spec), n)...)
		}
	}
	if len(wide) > 0 {
		return unique(dedupe(wide))
	}
	return unique(v.byName[n])
}

// declaring filters files to those declaring name.
func (v *view) declaring(files []string, name string) []string {
	var out []string
	for _, f := range files {
		if v.declares(f, name) {
			out = append(out, f)
		}
	}
	return out
}

// unique returns the single element of s, or "".
func unique(s []string) string {
	if len(s) == 1 {
		return s[0]
	}
	return ""
}

// dedupe returns the sorted distinct elements of s.
func dedupe(s []string) []string {
	sort.Strings(s)
	out := s[:0]
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}
