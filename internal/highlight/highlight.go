// Package highlight gives syntax colour classes from tree-sitter (spec Code
// page, Diff rendering: "Syntax colour from tree-sitter captures; falls
// back to plain for unknown languages").
//
// It parses source with the grammars of internal/parse and walks the tree,
// mapping nodes to the theme's syntax classes (theme.Syn*): comments, string
// literals, numbers, keywords (anonymous word tokens such as func, return,
// def, class), type identifiers and function names in declarations and
// calls. The result is a list of byte spans per line; callers paint them.
//
// Lines and Line are pure. A Highlighter adds a bounded in-memory cache
// (whole files by language and content hash, single lines by language and
// text) for callers that render the same text repeatedly.
package highlight

import (
	"context"
	"slices"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/fredricw/prt/internal/parse"
	"github.com/fredricw/prt/internal/theme"
)

// maxBytes caps the source handed to tree-sitter; larger input is plain.
const maxBytes = 2 << 20

// Span is one coloured run within a line: bytes [Start, End) of that line
// and the theme syntax class (theme.SynKeyword, theme.SynString, ...).
type Span struct {
	Start, End int
	Class      string
}

// Lines returns the spans of every line of src (lines split at "\n"; a
// final newline does not start an extra line), each sorted by Start. An
// unknown language, empty or oversized input, or a parser failure gives nil
// (plain). Syntax errors are tolerated: tree-sitter recovers and the nodes
// it did recognise are still coloured.
func Lines(lang string, src []byte) [][]Span {
	spans := fileSpans(lang, src)
	if spans == nil {
		return nil
	}
	return byLine(src, spans)
}

// Line returns the spans of one line parsed on its own (diff lines without
// their file); nil for an unknown language or a parser failure. A line that
// is not a complete construct (an opening "if (x) {") is still coloured
// from tree-sitter's error recovery.
func Line(lang, text string) []Span {
	if strings.ContainsRune(text, '\n') {
		text = text[:strings.IndexByte(text, '\n')]
	}
	ls := Lines(lang, []byte(text))
	if len(ls) == 0 {
		return nil
	}
	return ls[0]
}

// rawSpan is a span over the whole source.
type rawSpan struct {
	start, end uint32
	class      string
}

// fileSpans parses src and returns its coloured spans in source order.
func fileSpans(lang string, src []byte) []rawSpan {
	g := parse.Grammar(lang)
	if g == nil || len(src) == 0 || len(src) > maxBytes {
		return nil
	}
	p := sitter.NewParser()
	defer p.Close()
	p.SetLanguage(g)
	tree, err := p.ParseCtx(context.Background(), nil, src)
	if err != nil || tree == nil {
		return nil
	}
	defer tree.Close()
	root := tree.RootNode()
	if root == nil {
		return nil
	}
	w := walker{src: src, names: map[uint32]string{}}
	w.walk(root)
	slices.SortStableFunc(w.out, func(a, b rawSpan) int { return int(a.start) - int(b.start) })
	return w.out
}

// walker collects spans. names marks identifier nodes (by start byte) that
// a parent classified: declared and called function names, class names.
type walker struct {
	src   []byte
	names map[uint32]string
	out   []rawSpan
}

// walk visits n and its subtree iteratively (deep trees must not blow the
// stack).
func (w *walker) walk(root *sitter.Node) {
	stack := []*sitter.Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.IsMissing() || n.StartByte() >= n.EndByte() {
			continue
		}
		if c := w.classOf(n); c != "" {
			w.out = append(w.out, rawSpan{n.StartByte(), n.EndByte(), c})
			continue
		}
		w.markNames(n)
		for i := int(n.ChildCount()) - 1; i >= 0; i-- {
			if c := n.Child(i); c != nil {
				stack = append(stack, c)
			}
		}
	}
}

// Node types by class, across the Go, TypeScript/TSX, JavaScript and Python
// grammars.
var (
	commentTypes = map[string]bool{"comment": true, "line_comment": true, "block_comment": true}
	stringTypes  = map[string]bool{
		"interpreted_string_literal": true, "raw_string_literal": true, "rune_literal": true, // go
		"string": true, "template_string": true, "regex": true, // js/ts/python
	}
	numberTypes = map[string]bool{
		"int_literal": true, "float_literal": true, "imaginary_literal": true, // go
		"number": true, "integer": true, "float": true, // js/ts, python
	}
	constantTypes = map[string]bool{
		"true": true, "false": true, "nil": true, "iota": true, "null": true, "undefined": true,
		"none": true, "True": true, "False": true, "None": true,
	}
	typeTypes = map[string]bool{"type_identifier": true, "predefined_type": true}
	// nameTypes are the identifier kinds markNames may classify.
	nameTypes = map[string]bool{
		"identifier": true, "field_identifier": true, "property_identifier": true,
	}
)

// classOf returns the class of a leaf-like node, or "" to descend.
func (w *walker) classOf(n *sitter.Node) string {
	t := n.Type()
	switch {
	case commentTypes[t]:
		return theme.SynComment
	case stringTypes[t]:
		return theme.SynString
	case numberTypes[t]:
		return theme.SynNumber
	case n.IsNamed() && constantTypes[t]:
		return theme.SynNumber
	case typeTypes[t]:
		return theme.SynType
	case nameTypes[t]:
		return w.names[n.StartByte()]
	case !n.IsNamed() && isWord(t):
		return theme.SynKeyword
	}
	return ""
}

// isWord reports whether an anonymous token is a keyword: letters (and
// underscores) only, like "func", "return", "def", "class", "async".
func isWord(t string) bool {
	if t == "" || t == "_" {
		return false
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '_' {
			return false
		}
	}
	return true
}

// markNames classifies the identifier children of declarations and calls:
// function and method names, called functions (the last name of a
// selector), and class names.
func (w *walker) markNames(n *sitter.Node) {
	switch n.Type() {
	case "function_declaration", "method_declaration", "function_definition", "method_definition",
		"generator_function_declaration", "function_signature", "method_signature", "function_expression":
		w.mark(n.ChildByFieldName("name"), theme.SynFunction)
	case "class_declaration", "class_definition", "class", "abstract_class_declaration":
		w.mark(n.ChildByFieldName("name"), theme.SynType)
	case "call_expression", "call", "new_expression":
		f := n.ChildByFieldName("function")
		if f == nil {
			f = n.ChildByFieldName("constructor")
		}
		w.mark(calledName(f), theme.SynFunction)
	}
}

// calledName returns the identifier naming a called function: the callee
// itself, or the last part of a selector, member or attribute expression.
func calledName(f *sitter.Node) *sitter.Node {
	if f == nil {
		return nil
	}
	switch f.Type() {
	case "selector_expression":
		return f.ChildByFieldName("field")
	case "member_expression":
		return f.ChildByFieldName("property")
	case "attribute":
		return f.ChildByFieldName("attribute")
	case "generic_type", "instantiation_expression":
		return calledName(f.NamedChild(0))
	}
	return f
}

func (w *walker) mark(n *sitter.Node, class string) {
	if n != nil && nameTypes[n.Type()] {
		w.names[n.StartByte()] = class
	}
}

// byLine cuts source spans at line breaks into per-line spans with offsets
// relative to each line's start.
func byLine(src []byte, spans []rawSpan) [][]Span {
	starts := []int{0}
	for i, c := range src {
		if c == '\n' && i+1 < len(src) {
			starts = append(starts, i+1)
		}
	}
	out := make([][]Span, len(starts))
	li := 0
	for _, s := range spans {
		start, end := int(s.start), int(s.end)
		for li+1 < len(starts) && starts[li+1] <= start {
			li++
		}
		for l := li; l < len(starts) && starts[l] < end; l++ {
			lineEnd := len(src)
			if l+1 < len(starts) {
				lineEnd = starts[l+1] - 1 // the "\n"
			}
			a, b := max(start, starts[l]), min(end, lineEnd)
			if a < b {
				out[l] = append(out[l], Span{Start: a - starts[l], End: b - starts[l], Class: s.class})
			}
		}
	}
	return out
}
