package parse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// maxParseBytes caps the size of a file side handed to tree-sitter; larger
// files fall back to hunk-level units.
const maxParseBytes = 2 << 20

// maxSignature is the longest signature kept, in runes.
const maxSignature = 200

var (
	errTooLarge  = errors.New("parse: file too large to parse")
	errSyntax    = errors.New("parse: syntax errors in source")
	errNoGrammar = errors.New("parse: no grammar for language")
)

// Grammar returns the tree-sitter language for a Lang name ("go", "ts",
// "tsx", "js", "python"), or nil. Exported for stage 2 (internal/link),
// which runs its own queries (calls, imports, declarations) over file sides.
func Grammar(lang string) *sitter.Language { return grammar(lang) }

// grammar returns the tree-sitter language for a Lang name, or nil.
func grammar(lang string) *sitter.Language {
	switch lang {
	case "go":
		return golang.GetLanguage()
	case "ts":
		return typescript.GetLanguage()
	case "tsx":
		return tsx.GetLanguage()
	case "js":
		return javascript.GetLanguage()
	case "python":
		return python.GetLanguage()
	}
	return nil
}

// decl is one function-like declaration of a parsed file side.
type decl struct {
	name   string // qualified symbol, e.g. "UserStore.Get"
	sig    string // header text, whitespace collapsed
	start  int    // first row (0-based), including attached leading comments
	end    int    // last row (0-based, inclusive)
	sigRow int    // row where the header starts
}

// side is one parsed side (base or head) of a file.
type side struct {
	lang  string
	lines []string  // source lines without terminators
	decls []decl    // pre-order: an enclosing declaration precedes nested ones
	sym   []int32   // row → index into decls, -1 at module level
	toks  [][]token // row → tokens on that row
}

// lineSym is the symbol attributed to one diff line.
type lineSym struct {
	name string
	d    *decl
}

// at returns the innermost declaration enclosing a 0-based row.
func (s *side) at(row int) lineSym {
	if s == nil || row < 0 || row >= len(s.sym) || s.sym[row] < 0 {
		return lineSym{}
	}
	d := &s.decls[s.sym[row]]
	return lineSym{name: d.name, d: d}
}

// tokensAt returns the tree-sitter tokens of a 0-based row, or ok=false
// when the row is outside the file.
func (s *side) tokensAt(row int) ([]token, bool) {
	if s == nil || row < 0 || row >= len(s.toks) {
		return nil, false
	}
	return s.toks[row], true
}

// lineMatches reports whether the 0-based row holds text (ignoring a
// trailing carriage return on either side).
func (s *side) lineMatches(row int, text string) bool {
	if row < 0 || row >= len(s.lines) {
		return false
	}
	return strings.TrimSuffix(s.lines[row], "\r") == strings.TrimSuffix(text, "\r")
}

// parseSide parses src with the grammar for lang and collects declarations
// and per-row tokens. Any syntax error makes the side unusable.
func parseSide(ctx context.Context, lang string, src []byte) (*side, error) {
	g := grammar(lang)
	if g == nil {
		return nil, errNoGrammar
	}
	if len(src) > maxParseBytes {
		return nil, errTooLarge
	}
	p := sitter.NewParser()
	defer p.Close()
	p.SetLanguage(g)
	tree, err := p.ParseCtx(ctx, nil, src)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", lang, err)
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil || root.HasError() {
		return nil, errSyntax
	}
	s := &side{lang: lang, lines: splitLines(src)}
	w := declWalker{src: src, lang: lang}
	w.children(root, nil, false)
	s.decls = w.decls
	s.sym = rowSymbols(len(s.lines), s.decls)
	s.toks = rowTokens(root, src, len(s.lines))
	return s, nil
}

// splitLines splits source into lines; a final newline does not start an
// extra empty line.
func splitLines(src []byte) []string {
	if len(src) == 0 {
		return nil
	}
	lines := strings.Split(string(src), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// rowSymbols maps each row to its innermost declaration. decls is in
// pre-order, so nested declarations overwrite their parents' rows.
func rowSymbols(rows int, decls []decl) []int32 {
	sym := make([]int32, rows)
	for i := range sym {
		sym[i] = -1
	}
	for i, d := range decls {
		for r := d.start; r <= d.end && r < rows; r++ {
			if r >= 0 {
				sym[r] = int32(i)
			}
		}
	}
	return sym
}

// endRow is the last row a node covers; a node ending at column 0 ends on
// the previous row.
func endRow(n *sitter.Node) int {
	start, end := int(n.StartPoint().Row), int(n.EndPoint().Row)
	if n.EndPoint().Column == 0 && end > start {
		end--
	}
	return end
}

// declWalker collects function-like declarations in pre-order.
type declWalker struct {
	src   []byte
	lang  string
	decls []decl
}

// match describes how one node takes part in symbol naming.
type match struct {
	fn    bool         // a function-like declaration (recorded)
	scope bool         // a naming scope only (class, object-valued const)
	name  string       // name segment added to the qualifier
	span  *sitter.Node // node whose rows the declaration covers
	head  *sitter.Node // node where the signature starts
	body  *sitter.Node // body node; the signature ends where it starts
	// resetFunc is true for scopes (classes) whose members are named
	// declarations again even inside a function.
	resetFunc bool
}

func (w *declWalker) children(n *sitter.Node, quals []string, inFunc bool) {
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil || !c.IsNamed() {
			continue
		}
		w.visit(c, quals, inFunc)
	}
}

func (w *declWalker) visit(n *sitter.Node, quals []string, inFunc bool) {
	m := w.classify(n, inFunc)
	switch {
	case m.fn && m.name != "":
		q := with(quals, m.name)
		w.record(strings.Join(q, "."), m)
		w.children(n, q, true)
	case m.fn:
		// Anonymous function declaration: its lines stay with the enclosing
		// symbol, and nothing nested in it is a declaration of its own.
		w.children(n, quals, true)
	case m.scope:
		q := quals
		if m.name != "" {
			q = with(quals, m.name)
		}
		w.children(n, q, inFunc && !m.resetFunc)
	default:
		w.children(n, quals, inFunc)
	}
}

// with returns a fresh slice quals + name (never aliasing quals).
func with(quals []string, name string) []string {
	out := make([]string, 0, len(quals)+1)
	out = append(out, quals...)
	return append(out, name)
}

func (w *declWalker) record(name string, m match) {
	start := int(m.span.StartPoint().Row)
	end := endRow(m.span)
	start = leadingComments(m.span, start)
	w.decls = append(w.decls, decl{
		name:   name,
		sig:    w.signature(m.head, m.body),
		start:  start,
		end:    end,
		sigRow: int(m.head.StartPoint().Row),
	})
}

// leadingComments extends start upward over comments directly above the
// declaration (doc comments), stopping at a blank line or at a comment that
// trails code on its own row.
func leadingComments(n *sitter.Node, start int) int {
	for p := n.PrevNamedSibling(); p != nil && p.Type() == "comment"; p = p.PrevNamedSibling() {
		if int(p.EndPoint().Row)+1 < start {
			break
		}
		if prev := p.PrevNamedSibling(); prev != nil && prev.EndPoint().Row == p.StartPoint().Row {
			break
		}
		start = int(p.StartPoint().Row)
	}
	return start
}

// signature is the header text from head up to the body, whitespace
// collapsed and capped at maxSignature runes.
func (w *declWalker) signature(head, body *sitter.Node) string {
	from, to := head.StartByte(), head.EndByte()
	if body != nil && body.StartByte() >= from && body.StartByte() <= to {
		to = body.StartByte()
	}
	s := strings.Join(strings.Fields(string(w.src[from:to])), " ")
	if w.lang == "python" {
		s = strings.TrimSpace(strings.TrimSuffix(s, ":"))
	}
	if utf8.RuneCountInString(s) > maxSignature {
		s = strings.TrimSpace(string([]rune(s)[:maxSignature]))
	}
	return s
}

func (w *declWalker) content(n *sitter.Node) string {
	if n == nil {
		return ""
	}
	return n.Content(w.src)
}

func (w *declWalker) classify(n *sitter.Node, inFunc bool) match {
	switch w.lang {
	case "go":
		return w.classifyGo(n)
	case "python":
		return w.classifyPython(n)
	default:
		return w.classifyJS(n, inFunc)
	}
}

func (w *declWalker) classifyGo(n *sitter.Node) match {
	switch n.Type() {
	case "function_declaration":
		return match{fn: true, name: w.content(n.ChildByFieldName("name")),
			span: n, head: n, body: n.ChildByFieldName("body")}
	case "method_declaration":
		name := w.content(n.ChildByFieldName("name"))
		if recv := w.goReceiver(n); recv != "" && name != "" {
			name = recv + "." + name
		}
		return match{fn: true, name: name, span: n, head: n, body: n.ChildByFieldName("body")}
	}
	return match{}
}

// goReceiver returns a method's receiver type without pointer or type
// parameters ("*Store[T]" → "Store").
func (w *declWalker) goReceiver(n *sitter.Node) string {
	r := n.ChildByFieldName("receiver")
	if r == nil {
		return ""
	}
	for i := 0; i < int(r.NamedChildCount()); i++ {
		p := r.NamedChild(i)
		if p == nil || p.Type() != "parameter_declaration" {
			continue
		}
		t := strings.TrimSpace(w.content(p.ChildByFieldName("type")))
		t = strings.TrimLeft(t, "*( ")
		if i := strings.IndexAny(t, "[)"); i >= 0 {
			t = t[:i]
		}
		return strings.TrimSpace(t)
	}
	return ""
}

func (w *declWalker) classifyPython(n *sitter.Node) match {
	switch n.Type() {
	case "function_definition":
		span := n
		if p := n.Parent(); p != nil && p.Type() == "decorated_definition" {
			span = p
		}
		return match{fn: true, name: w.content(n.ChildByFieldName("name")),
			span: span, head: n, body: n.ChildByFieldName("body")}
	case "class_definition":
		return match{scope: true, resetFunc: true, name: w.content(n.ChildByFieldName("name"))}
	}
	return match{}
}

// jsFuncValues are expression node types that make a binding a function.
var jsFuncValues = map[string]bool{
	"arrow_function":      true,
	"function_expression": true,
	"function":            true,
	"generator_function":  true,
}

func (w *declWalker) classifyJS(n *sitter.Node, inFunc bool) match {
	switch n.Type() {
	case "function_declaration", "generator_function_declaration":
		span := exportWrap(n)
		return match{fn: true, name: w.content(n.ChildByFieldName("name")),
			span: span, head: span, body: n.ChildByFieldName("body")}
	case "class_declaration", "abstract_class_declaration", "class":
		return match{scope: true, resetFunc: true, name: w.className(n)}
	case "method_definition":
		if inFunc {
			return match{}
		}
		return match{fn: true, name: w.content(n.ChildByFieldName("name")),
			span: n, head: n, body: n.ChildByFieldName("body")}
	case "public_field_definition", "field_definition":
		v := n.ChildByFieldName("value")
		if inFunc || v == nil || !jsFuncValues[v.Type()] {
			return match{}
		}
		name := n.ChildByFieldName("name")
		if name == nil {
			name = n.ChildByFieldName("property")
		}
		return match{fn: true, name: w.content(name), span: n, head: n, body: v.ChildByFieldName("body")}
	case "variable_declarator":
		return w.classifyDeclarator(n, inFunc)
	}
	return match{}
}

// classifyDeclarator handles `const f = () => …` (a declaration) and
// `const api = { m() {} }` (a naming scope for the object's methods), both
// only outside function bodies.
func (w *declWalker) classifyDeclarator(n *sitter.Node, inFunc bool) match {
	name, v := n.ChildByFieldName("name"), n.ChildByFieldName("value")
	if inFunc || name == nil || name.Type() != "identifier" || v == nil {
		return match{}
	}
	switch {
	case jsFuncValues[v.Type()]:
		span := n
		if p := n.Parent(); p != nil && p.NamedChildCount() == 1 &&
			(p.Type() == "lexical_declaration" || p.Type() == "variable_declaration") {
			span = exportWrap(p)
		}
		return match{fn: true, name: w.content(name), span: span, head: span, body: v.ChildByFieldName("body")}
	case v.Type() == "object":
		return match{scope: true, name: w.content(name)}
	}
	return match{}
}

// className returns a class's name, or for an anonymous class expression
// bound to a variable, the variable's name.
func (w *declWalker) className(n *sitter.Node) string {
	if name := n.ChildByFieldName("name"); name != nil {
		return w.content(name)
	}
	if p := n.Parent(); p != nil && p.Type() == "variable_declarator" {
		if name := p.ChildByFieldName("name"); name != nil && name.Type() == "identifier" {
			return w.content(name)
		}
	}
	return ""
}

// exportWrap returns the enclosing export statement, if n is exported.
func exportWrap(n *sitter.Node) *sitter.Node {
	if p := n.Parent(); p != nil && p.Type() == "export_statement" {
		return p
	}
	return n
}

// stringTypes are node types treated as one string-literal token.
var stringTypes = map[string]bool{
	"string":                     true,
	"template_string":            true,
	"regex":                      true,
	"interpreted_string_literal": true,
	"raw_string_literal":         true,
	"rune_literal":               true,
}

// rowTokens walks the tree once and buckets its leaf tokens by row.
// String literals and comments are atomic; a token spanning rows is split
// into one piece per row.
func rowTokens(root *sitter.Node, src []byte, rows int) [][]token {
	toks := make([][]token, rows)
	stack := []*sitter.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		t := n.Type()
		if n.ChildCount() == 0 || stringTypes[t] || t == "comment" {
			emitToken(toks, n, src)
			continue
		}
		for i := int(n.ChildCount()) - 1; i >= 0; i-- {
			if c := n.Child(i); c != nil {
				stack = append(stack, c)
			}
		}
	}
	return toks
}

func emitToken(toks [][]token, n *sitter.Node, src []byte) {
	if n.StartByte() >= n.EndByte() || n.IsMissing() {
		return
	}
	kind := nodeKind(n)
	row := int(n.StartPoint().Row)
	for i, piece := range strings.Split(n.Content(src), "\n") {
		r := row + i
		if r >= len(toks) {
			break
		}
		if tk, ok := newToken(kind, piece); ok {
			toks[r] = append(toks[r], tk)
		}
	}
}

func nodeKind(n *sitter.Node) tokKind {
	t := n.Type()
	switch {
	case stringTypes[t]:
		return tokString
	case t == "comment":
		return tokComment
	case n.IsNamed() && strings.HasSuffix(t, "identifier"):
		return tokIdent
	}
	return tokOther
}
