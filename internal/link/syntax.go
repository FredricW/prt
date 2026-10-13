package link

import (
	"context"
	"path"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fredricw/prt/internal/parse"
)

// maxFileBytes caps the size of a file side handed to tree-sitter (the same
// cap parse uses); larger files contribute no links.
const maxFileBytes = 2 << 20

// declInfo is one declaration of a parsed file side.
type declInfo struct {
	Name  string // qualified ("UserStore.Get", "ApiError", "api.get")
	Start int    // first row, 0-based
	End   int    // last row, 0-based inclusive
	Top   bool   // module-level (candidate for symbol-level move detection)
	Text  string // normalised source text (top-level declarations only)
}

// callInfo is one call (or constructor) expression.
type callInfo struct {
	Row  int    // row of the callee name, 0-based
	Name string // callee name ("getUser" for client.getUser(…))
	Obj  string // receiver/qualifier identifier, "" for a bare call
	New  bool   // a `new Foo(…)` constructor call
}

// importName is one name an import statement brings in.
type importName struct {
	Name  string `json:"n"`           // exported name in the imported module
	Local string `json:"l,omitempty"` // local binding when aliased
	Row   int    `json:"-"`           // row of the specifier, 0-based
}

// local is the name the importing file uses.
func (n importName) local() string {
	if n.Local != "" {
		return n.Local
	}
	return n.Name
}

// importInfo is one import (or re-export-from) statement.
type importInfo struct {
	Start    int          `json:"-"`
	End      int          `json:"-"`
	Spec     string       `json:"s"`           // module specifier or path
	Names    []importName `json:"n,omitempty"` // explicitly named imports
	Alias    string       `json:"a,omitempty"` // namespace binding (import * as x; Go package; Python module)
	ReExport bool         `json:"r,omitempty"` // export … from
	Star     bool         `json:"x,omitempty"` // export * from / from x import *
}

// fileSyntax is what stage 2 extracts from one parsed file side.
type fileSyntax struct {
	Lang    string
	Decls   []declInfo
	Calls   []callInfo
	Imports []importInfo
	// Exports are module-level `export { a }` names without a source: the
	// file re-exports something it imported or declared.
	Exports []importName
}

// declares reports whether the side declares name (as a whole qualified
// name or as its last segment).
func (s *fileSyntax) declares(name string) bool {
	if s == nil {
		return false
	}
	for _, d := range s.Decls {
		if d.Name == name || lastSegment(d.Name) == name {
			return true
		}
	}
	return false
}

// parseSyntax parses src as lang and extracts declarations, calls and
// imports. Unsupported languages, oversized sources and syntax errors give
// nil (the file then contributes no links).
func parseSyntax(ctx context.Context, lang string, src []byte) *fileSyntax {
	g := parse.Grammar(lang)
	if g == nil || len(src) > maxFileBytes {
		return nil
	}
	p := sitter.NewParser()
	defer p.Close()
	p.SetLanguage(g)
	tree, err := p.ParseCtx(ctx, nil, src)
	if err != nil {
		return nil
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		return nil
	}
	x := extractor{src: src, out: &fileSyntax{Lang: lang}}
	switch lang {
	case "go":
		x.goTop(root)
	case "python":
		x.pyTop(root)
	default:
		x.jsTop(root)
	}
	x.calls(root)
	return x.out
}

// extractor walks one tree.
type extractor struct {
	src []byte
	out *fileSyntax
}

func (x *extractor) text(n *sitter.Node) string {
	if n == nil {
		return ""
	}
	return n.Content(x.src)
}

func row(n *sitter.Node) int { return int(n.StartPoint().Row) }

// endRow is the last row a node covers; a node ending at column 0 ends on
// the previous row.
func endRow(n *sitter.Node) int {
	start, end := int(n.StartPoint().Row), int(n.EndPoint().Row)
	if n.EndPoint().Column == 0 && end > start {
		end--
	}
	return end
}

// namedChildren returns n's named children.
func namedChildren(n *sitter.Node) []*sitter.Node {
	out := make([]*sitter.Node, 0, n.NamedChildCount())
	for i := 0; i < int(n.NamedChildCount()); i++ {
		if c := n.NamedChild(i); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// addDecl records a declaration; top-level ones carry normalised text.
func (x *extractor) addDecl(name string, span *sitter.Node, top bool) {
	if name == "" || span == nil {
		return
	}
	d := declInfo{Name: name, Start: row(span), End: endRow(span), Top: top}
	if top {
		d.Text = normaliseDecl(x.text(span))
	}
	x.out.Decls = append(x.out.Decls, d)
}

// unquote strips string delimiters from a literal.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		switch s[0] {
		case '"', '\'', '`':
			if s[len(s)-1] == s[0] {
				return s[1 : len(s)-1]
			}
		}
	}
	return s
}

// ---- TS / TSX / JS ----

func (x *extractor) jsTop(root *sitter.Node) {
	for _, c := range namedChildren(root) {
		x.jsStatement(c, c)
	}
}

// jsStatement handles one module-level statement; span is the node whose
// rows and text the declaration covers (the export wrapper when exported).
func (x *extractor) jsStatement(n, span *sitter.Node) {
	switch n.Type() {
	case "import_statement":
		x.jsImport(n)
	case "export_statement":
		x.jsExport(n)
	case "function_declaration", "generator_function_declaration", "interface_declaration",
		"type_alias_declaration", "enum_declaration":
		x.addDecl(x.text(n.ChildByFieldName("name")), span, true)
	case "class_declaration", "abstract_class_declaration":
		name := x.text(n.ChildByFieldName("name"))
		x.addDecl(name, span, true)
		x.jsClassBody(name, n.ChildByFieldName("body"))
	case "lexical_declaration", "variable_declaration":
		x.jsVariables(n, span)
	}
}

func (x *extractor) jsExport(n *sitter.Node) {
	if d := n.ChildByFieldName("declaration"); d != nil {
		x.jsStatement(d, n)
		return
	}
	src := n.ChildByFieldName("source")
	imp := importInfo{Start: row(n), End: endRow(n), ReExport: true}
	if src != nil {
		imp.Spec = unquote(x.text(src))
	}
	star := false
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "*":
			star = true
		case "namespace_export":
			star = true
		case "export_clause":
			imp.Names = append(imp.Names, x.jsSpecifiers(c, "export_specifier")...)
		}
	}
	if src == nil {
		x.out.Exports = append(x.out.Exports, imp.Names...)
		return
	}
	imp.Star = star && len(imp.Names) == 0
	x.out.Imports = append(x.out.Imports, imp)
}

// jsSpecifiers reads import/export specifiers (name + optional alias).
func (x *extractor) jsSpecifiers(clause *sitter.Node, typ string) []importName {
	var out []importName
	for _, s := range namedChildren(clause) {
		if s.Type() != typ {
			continue
		}
		name := s.ChildByFieldName("name")
		if name == nil {
			continue
		}
		in := importName{Name: unquote(x.text(name)), Row: row(name)}
		if a := s.ChildByFieldName("alias"); a != nil {
			in.Local = x.text(a)
		}
		out = append(out, in)
	}
	return out
}

func (x *extractor) jsImport(n *sitter.Node) {
	src := n.ChildByFieldName("source")
	if src == nil {
		return
	}
	imp := importInfo{Start: row(n), End: endRow(n), Spec: unquote(x.text(src))}
	for _, c := range namedChildren(n) {
		if c.Type() != "import_clause" {
			continue
		}
		for _, k := range namedChildren(c) {
			switch k.Type() {
			case "identifier": // default import
				imp.Names = append(imp.Names, importName{Name: x.text(k), Row: row(k)})
			case "namespace_import":
				for _, id := range namedChildren(k) {
					if id.Type() == "identifier" {
						imp.Alias = x.text(id)
					}
				}
			case "named_imports":
				imp.Names = append(imp.Names, x.jsSpecifiers(k, "import_specifier")...)
			}
		}
	}
	x.out.Imports = append(x.out.Imports, imp)
}

// jsFuncValues are expression types that make a binding a function.
var jsFuncValues = map[string]bool{
	"arrow_function": true, "function_expression": true, "function": true, "generator_function": true,
}

func (x *extractor) jsVariables(n, span *sitter.Node) {
	decls := 0
	for _, d := range namedChildren(n) {
		if d.Type() == "variable_declarator" {
			decls++
		}
	}
	for _, d := range namedChildren(n) {
		if d.Type() != "variable_declarator" {
			continue
		}
		name := d.ChildByFieldName("name")
		if name == nil || name.Type() != "identifier" {
			continue
		}
		s := span
		if decls > 1 {
			s = d
		}
		x.addDecl(x.text(name), s, true)
		v := d.ChildByFieldName("value")
		switch {
		case v == nil:
		case v.Type() == "object":
			x.jsObjectMembers(x.text(name), v)
		case v.Type() == "class":
			x.jsClassBody(x.text(name), v.ChildByFieldName("body"))
		}
	}
}

// jsObjectMembers records `const api = { m() {}, n: () => … }` as api.m, api.n.
func (x *extractor) jsObjectMembers(obj string, o *sitter.Node) {
	for _, m := range namedChildren(o) {
		switch m.Type() {
		case "method_definition":
			x.addDecl(obj+"."+x.text(m.ChildByFieldName("name")), m, false)
		case "pair":
			if v := m.ChildByFieldName("value"); v != nil && jsFuncValues[v.Type()] {
				x.addDecl(obj+"."+unquote(x.text(m.ChildByFieldName("key"))), m, false)
			}
		}
	}
}

func (x *extractor) jsClassBody(class string, body *sitter.Node) {
	if body == nil || class == "" {
		return
	}
	for _, m := range namedChildren(body) {
		switch m.Type() {
		case "method_definition", "abstract_method_signature", "method_signature":
			x.addDecl(class+"."+x.text(m.ChildByFieldName("name")), m, false)
		case "public_field_definition", "field_definition":
			name := m.ChildByFieldName("name")
			if name == nil {
				name = m.ChildByFieldName("property")
			}
			x.addDecl(class+"."+x.text(name), m, false)
		}
	}
}

// ---- Go ----

func (x *extractor) goTop(root *sitter.Node) {
	for _, c := range namedChildren(root) {
		switch c.Type() {
		case "import_declaration":
			x.goImports(c)
		case "function_declaration":
			x.addDecl(x.text(c.ChildByFieldName("name")), c, true)
		case "method_declaration":
			name := x.text(c.ChildByFieldName("name"))
			if recv := x.goReceiver(c); recv != "" && name != "" {
				name = recv + "." + name
			}
			x.addDecl(name, c, true)
		case "type_declaration", "const_declaration", "var_declaration":
			x.goSpecs(c)
		}
	}
}

// goSpecs records each name of a type/const/var declaration. A declaration
// with one spec is a top-level move candidate as a whole.
func (x *extractor) goSpecs(n *sitter.Node) {
	specs := namedChildren(n)
	if len(specs) == 1 && specs[0].Type() == "var_spec_list" {
		specs = namedChildren(specs[0])
	}
	single := len(specs) == 1
	for _, s := range specs {
		switch s.Type() {
		case "type_spec", "type_alias":
			x.addGoSpecName(x.text(s.ChildByFieldName("name")), n, s, single)
		case "const_spec", "var_spec":
			for _, id := range namedChildren(s) {
				if id.Type() != "identifier" {
					break
				}
				x.addGoSpecName(x.text(id), n, s, single)
			}
		}
	}
}

func (x *extractor) addGoSpecName(name string, decl, spec *sitter.Node, single bool) {
	if single {
		x.addDecl(name, decl, true)
		return
	}
	x.addDecl(name, spec, false)
}

// goReceiver returns a method's receiver type without pointer or type
// parameters ("*Store[T]" → "Store"), as parse names Go methods.
func (x *extractor) goReceiver(n *sitter.Node) string {
	r := n.ChildByFieldName("receiver")
	if r == nil {
		return ""
	}
	for _, p := range namedChildren(r) {
		if p.Type() != "parameter_declaration" {
			continue
		}
		t := strings.TrimLeft(strings.TrimSpace(x.text(p.ChildByFieldName("type"))), "*( ")
		if i := strings.IndexAny(t, "[)"); i >= 0 {
			t = t[:i]
		}
		return strings.TrimSpace(t)
	}
	return ""
}

var goMajorVersion = regexp.MustCompile(`^v[0-9]+$`)

// goPackageName guesses the package name of an import path: its last
// segment, skipping a major-version suffix ("chi/v5" → "chi").
func goPackageName(p string) string {
	segs := strings.Split(p, "/")
	name := segs[len(segs)-1]
	if goMajorVersion.MatchString(name) && len(segs) > 1 {
		name = segs[len(segs)-2]
	}
	return strings.TrimPrefix(name, "go-")
}

func (x *extractor) goImports(n *sitter.Node) {
	var specs []*sitter.Node
	for _, c := range namedChildren(n) {
		switch c.Type() {
		case "import_spec":
			specs = append(specs, c)
		case "import_spec_list":
			for _, s := range namedChildren(c) {
				if s.Type() == "import_spec" {
					specs = append(specs, s)
				}
			}
		}
	}
	for _, s := range specs {
		p := unquote(x.text(s.ChildByFieldName("path")))
		alias := goPackageName(p)
		if a := s.ChildByFieldName("name"); a != nil {
			alias = x.text(a)
		}
		x.out.Imports = append(x.out.Imports, importInfo{Start: row(s), End: endRow(s), Spec: p, Alias: alias})
	}
}

// ---- Python ----

func (x *extractor) pyTop(root *sitter.Node) {
	for _, c := range namedChildren(root) {
		x.pyStatement(c, c, "")
	}
}

func (x *extractor) pyStatement(n, span *sitter.Node, class string) {
	top := class == ""
	switch n.Type() {
	case "import_statement":
		if top {
			x.pyImport(n)
		}
	case "import_from_statement":
		if top {
			x.pyImportFrom(n)
		}
	case "decorated_definition":
		if d := n.ChildByFieldName("definition"); d != nil {
			x.pyStatement(d, n, class)
		}
	case "function_definition":
		x.addDecl(qualify(class, x.text(n.ChildByFieldName("name"))), span, top)
	case "class_definition":
		name := x.text(n.ChildByFieldName("name"))
		x.addDecl(qualify(class, name), span, top)
		if body := n.ChildByFieldName("body"); body != nil {
			for _, m := range namedChildren(body) {
				x.pyStatement(m, m, qualify(class, name))
			}
		}
	case "expression_statement":
		for _, a := range namedChildren(n) {
			if a.Type() != "assignment" {
				continue
			}
			if l := a.ChildByFieldName("left"); l != nil && l.Type() == "identifier" {
				x.addDecl(qualify(class, x.text(l)), span, top)
			}
		}
	}
}

func qualify(class, name string) string {
	if class == "" || name == "" {
		return name
	}
	return class + "." + name
}

// pyImport handles `import a.b` / `import a.b as c` (namespace bindings).
func (x *extractor) pyImport(n *sitter.Node) {
	for _, c := range namedChildren(n) {
		imp := importInfo{Start: row(n), End: endRow(n)}
		switch c.Type() {
		case "dotted_name":
			imp.Spec = x.text(c)
			segs := strings.Split(imp.Spec, ".")
			imp.Alias = segs[len(segs)-1]
		case "aliased_import":
			imp.Spec = x.text(c.ChildByFieldName("name"))
			imp.Alias = x.text(c.ChildByFieldName("alias"))
		default:
			continue
		}
		x.out.Imports = append(x.out.Imports, imp)
	}
}

// pyImportFrom handles `from a.b import c, d as e` and `from . import *`.
func (x *extractor) pyImportFrom(n *sitter.Node) {
	mod := n.ChildByFieldName("module_name")
	if mod == nil {
		return
	}
	imp := importInfo{Start: row(n), End: endRow(n), Spec: x.text(mod)}
	for _, c := range namedChildren(n) {
		if c.StartByte() == mod.StartByte() && c.EndByte() == mod.EndByte() {
			continue
		}
		switch c.Type() {
		case "dotted_name":
			imp.Names = append(imp.Names, importName{Name: x.text(c), Row: row(c)})
		case "aliased_import":
			imp.Names = append(imp.Names, importName{
				Name: x.text(c.ChildByFieldName("name")), Local: x.text(c.ChildByFieldName("alias")), Row: row(c),
			})
		case "wildcard_import":
			imp.Star = true
		}
	}
	x.out.Imports = append(x.out.Imports, imp)
}

// ---- calls (all languages) ----

// calls records every call and constructor expression in the tree.
func (x *extractor) calls(root *sitter.Node) {
	stack := []*sitter.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch n.Type() {
		case "call_expression", "call":
			x.addCall(n.ChildByFieldName("function"), false)
		case "new_expression":
			x.addCall(n.ChildByFieldName("constructor"), true)
		}
		for i := int(n.NamedChildCount()) - 1; i >= 0; i-- {
			if c := n.NamedChild(i); c != nil {
				stack = append(stack, c)
			}
		}
	}
}

// addCall records the callee of one call: a bare identifier, or the member
// name of obj.m / pkg.F (obj kept when it is a plain identifier).
func (x *extractor) addCall(fn *sitter.Node, isNew bool) {
	if fn == nil {
		return
	}
	var name, obj *sitter.Node
	switch fn.Type() {
	case "identifier":
		name = fn
	case "member_expression":
		name, obj = fn.ChildByFieldName("property"), fn.ChildByFieldName("object")
	case "selector_expression":
		name, obj = fn.ChildByFieldName("field"), fn.ChildByFieldName("operand")
	case "attribute":
		name, obj = fn.ChildByFieldName("attribute"), fn.ChildByFieldName("object")
	}
	if name == nil {
		return
	}
	c := callInfo{Row: row(name), Name: x.text(name), New: isNew}
	if obj != nil {
		switch obj.Type() {
		case "identifier", "this", "self", "package_identifier":
			c.Obj = x.text(obj)
		default:
			c.Obj = "?"
		}
	}
	if c.Name != "" {
		x.out.Calls = append(x.out.Calls, c)
	}
}

// ---- normalisation ----

// normaliseDecl collapses whitespace on every line, drops blank lines and
// a leading `export ` / `export default `, so the same declaration moved
// between files (and exported only in its new home) compares equal.
func normaliseDecl(s string) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if n := strings.Join(strings.Fields(l), " "); n != "" {
			lines = append(lines, n)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	first := lines[0]
	first = strings.TrimPrefix(first, "export default ")
	first = strings.TrimPrefix(first, "export ")
	lines[0] = first
	return strings.Join(lines, "\n")
}

// lastSegment returns the part of a qualified name after the last dot.
func lastSegment(s string) string {
	if i := strings.LastIndexByte(s, '.'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// stem returns a path's base name without its extension.
func stem(p string) string {
	b := path.Base(p)
	return strings.TrimSuffix(b, path.Ext(b))
}
