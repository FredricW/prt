package route

import (
	"fmt"
	"strconv"
	"strings"
)

// SyntaxError is a `when` parse error at a 1-based column of the source.
type SyntaxError struct {
	Col int    // 1-based byte column
	Msg string // readable message
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("col %d: %s", e.Col, e.Msg) }

type tokKind int8

const (
	tEOF tokKind = iota
	tWord
	tNum
	tStr
	tOp // ! && || == != >= <= > < [ ] , ( )
)

type token struct {
	kind tokKind
	text string // operator, word or the decoded string
	num  float64
	pos  int // byte offset in the source
	end  int // byte offset just past the token
}

// lex splits src into tokens, ending with a tEOF token.
func lex(src string) ([]token, error) {
	var out []token
	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case isWordStart(c):
			j := i + 1
			for j < len(src) && isWordChar(src[j]) {
				j++
			}
			w := src[i:j]
			if strings.HasSuffix(w, ".") {
				return nil, &SyntaxError{Col: j, Msg: fmt.Sprintf("identifier %q ends with '.'", w)}
			}
			out = append(out, token{kind: tWord, text: w, pos: i, end: j})
			i = j
		case c >= '0' && c <= '9':
			t, err := lexNum(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
			i = t.end
		case c == '"':
			t, err := lexStr(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
			i = t.end
		default:
			t, err := lexOp(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, t)
			i = t.end
		}
	}
	return append(out, token{kind: tEOF, pos: len(src), end: len(src)}), nil
}

func isWordStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// isWordChar allows dots (tag.auth), and '/' and '-' so option values such
// as env/flag or follow-up can be written bare inside `in [...]`.
func isWordChar(c byte) bool {
	return isWordStart(c) || (c >= '0' && c <= '9') || c == '.' || c == '/' || c == '-'
}

func lexNum(src string, i int) (token, error) {
	j := i
	for j < len(src) && (src[j] >= '0' && src[j] <= '9' || src[j] == '.') {
		j++
	}
	if j < len(src) && isWordStart(src[j]) {
		return token{}, &SyntaxError{Col: j + 1, Msg: fmt.Sprintf("unexpected %q after number", src[j])}
	}
	n, err := strconv.ParseFloat(src[i:j], 64)
	if err != nil {
		return token{}, &SyntaxError{Col: i + 1, Msg: fmt.Sprintf("bad number %q", src[i:j])}
	}
	return token{kind: tNum, text: src[i:j], num: n, pos: i, end: j}, nil
}

func lexStr(src string, i int) (token, error) {
	var b strings.Builder
	for j := i + 1; j < len(src); j++ {
		switch src[j] {
		case '\\':
			if j+1 < len(src) {
				j++
				b.WriteByte(src[j])
			}
		case '"':
			return token{kind: tStr, text: b.String(), pos: i, end: j + 1}, nil
		default:
			b.WriteByte(src[j])
		}
	}
	return token{}, &SyntaxError{Col: i + 1, Msg: "unterminated string"}
}

var twoCharOps = []string{"&&", "||", "==", "!=", ">=", "<="}

func lexOp(src string, i int) (token, error) {
	for _, op := range twoCharOps {
		if strings.HasPrefix(src[i:], op) {
			return token{kind: tOp, text: op, pos: i, end: i + 2}, nil
		}
	}
	c := src[i]
	if strings.IndexByte("!><[],()", c) >= 0 {
		return token{kind: tOp, text: string(c), pos: i, end: i + 1}, nil
	}
	msg := fmt.Sprintf("unexpected character %q", c)
	switch c {
	case '&':
		msg += ", did you mean '&&'?"
	case '|':
		msg += ", did you mean '||'?"
	case '=':
		msg += ", did you mean '=='?"
	}
	return token{}, &SyntaxError{Col: i + 1, Msg: msg}
}

// parser is a recursive-descent parser over the token list:
//
//	or    := and ('||' and)*
//	and   := cmp ('&&' cmp)*
//	cmp   := unary [ ('=='|'!=') rhsEq | ('>='|'<='|'>'|'<') unary | 'in' list ]
//	unary := '!' unary | primary
//	primary := word | number | string | true | false | '(' or ')'
type parser struct {
	src    string
	toks   []token
	i      int
	idents []string
	seen   map[string]bool
}

// Parse compiles a `when` expression (grammar and semantics in the package
// doc). Errors are *SyntaxError values with a 1-based column.
func Parse(src string) (Expr, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{src: src, toks: toks, seen: map[string]bool{}}
	if p.peek().kind == tEOF {
		return nil, &SyntaxError{Col: 1, Msg: "empty expression"}
	}
	root, err := p.or()
	if err != nil {
		return nil, err
	}
	if t := p.peek(); t.kind != tEOF {
		return nil, p.errAt(t, fmt.Sprintf("unexpected %s", describe(t)))
	}
	return &expr{src: src, root: root, idents: p.idents}, nil
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tEOF {
		p.i++
	}
	return t
}

func (p *parser) isOp(op string) bool {
	t := p.peek()
	return t.kind == tOp && t.text == op
}

func (p *parser) errAt(t token, msg string) error {
	return &SyntaxError{Col: t.pos + 1, Msg: msg}
}

func describe(t token) string {
	switch t.kind {
	case tEOF:
		return "end of expression"
	case tStr:
		return fmt.Sprintf("string %q", t.text)
	default:
		return fmt.Sprintf("%q", t.text)
	}
}

func (p *parser) text(from, to int) string { return strings.TrimSpace(p.src[from:to]) }

func (p *parser) or() (node, error) {
	start := p.peek().pos
	l, err := p.and()
	if err != nil {
		return nil, err
	}
	for p.isOp("||") {
		p.next()
		r, err := p.and()
		if err != nil {
			return nil, err
		}
		l = &logicNode{op: "||", l: l, r: r, src: p.text(start, p.toks[p.i-1].end)}
	}
	return l, nil
}

func (p *parser) and() (node, error) {
	start := p.peek().pos
	l, err := p.cmp()
	if err != nil {
		return nil, err
	}
	for p.isOp("&&") {
		p.next()
		r, err := p.cmp()
		if err != nil {
			return nil, err
		}
		l = &logicNode{op: "&&", l: l, r: r, src: p.text(start, p.toks[p.i-1].end)}
	}
	return l, nil
}

func (p *parser) cmp() (node, error) {
	start := p.peek().pos
	l, err := p.unary()
	if err != nil {
		return nil, err
	}
	t := p.peek()
	switch {
	case t.kind == tOp && (t.text == "==" || t.text == "!="):
		p.next()
		r, err := p.rhsEq()
		if err != nil {
			return nil, err
		}
		return &cmpNode{op: t.text, l: l, r: r, src: p.text(start, p.toks[p.i-1].end)}, nil
	case t.kind == tOp && (t.text == ">=" || t.text == "<=" || t.text == ">" || t.text == "<"):
		p.next()
		r, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &cmpNode{op: t.text, l: l, r: r, src: p.text(start, p.toks[p.i-1].end)}, nil
	case t.kind == tWord && t.text == "in":
		p.next()
		items, err := p.list()
		if err != nil {
			return nil, err
		}
		return &inNode{l: l, items: items, src: p.text(start, p.toks[p.i-1].end)}, nil
	}
	return l, nil
}

func (p *parser) unary() (node, error) {
	if t := p.peek(); t.kind == tOp && t.text == "!" {
		p.next()
		x, err := p.unary()
		if err != nil {
			return nil, err
		}
		return &notNode{x: x, src: p.text(t.pos, p.toks[p.i-1].end)}, nil
	}
	return p.primary()
}

func (p *parser) primary() (node, error) {
	t := p.next()
	switch t.kind {
	case tNum:
		return &litNode{v: Value{Kind: KindNum, N: t.num}, src: t.text}, nil
	case tStr:
		return &litNode{v: Value{Kind: KindStr, S: t.text}, src: p.src[t.pos:t.end]}, nil
	case tWord:
		switch t.text {
		case "true", "false":
			return &litNode{v: Value{Kind: KindBool, B: t.text == "true"}, src: t.text}, nil
		case "in":
			return nil, p.errAt(t, "unexpected 'in', expected a value before it")
		}
		p.addIdent(t.text)
		return &identNode{name: t.text}, nil
	case tOp:
		if t.text == "(" {
			x, err := p.or()
			if err != nil {
				return nil, err
			}
			if c := p.peek(); !p.isOp(")") {
				return nil, p.errAt(c, fmt.Sprintf("expected ')' to close '(' at col %d, got %s", t.pos+1, describe(c)))
			}
			p.next()
			return x, nil
		}
	}
	return nil, p.errAt(t, fmt.Sprintf("expected a value, got %s", describe(t)))
}

// rhsEq parses the right side of == / !=: a bare word is a string literal.
func (p *parser) rhsEq() (node, error) {
	if t := p.peek(); t.kind == tWord && t.text != "true" && t.text != "false" && t.text != "in" {
		p.next()
		return &litNode{v: Value{Kind: KindStr, S: t.text}, src: t.text}, nil
	}
	return p.unary()
}

// list parses `[a, b, ...]`; items are literals, bare words are strings.
func (p *parser) list() ([]Value, error) {
	if t := p.peek(); !p.isOp("[") {
		return nil, p.errAt(t, fmt.Sprintf("expected '[' after 'in', got %s", describe(t)))
	}
	open := p.next()
	if p.isOp("]") {
		return nil, p.errAt(open, "empty list after 'in'")
	}
	var out []Value
	for {
		t := p.next()
		switch t.kind {
		case tWord:
			out = append(out, wordValue(t.text))
		case tNum:
			out = append(out, Value{Kind: KindNum, N: t.num})
		case tStr:
			out = append(out, Value{Kind: KindStr, S: t.text})
		default:
			return nil, p.errAt(t, fmt.Sprintf("expected a list item, got %s", describe(t)))
		}
		sep := p.next()
		if sep.kind == tOp && sep.text == "]" {
			return out, nil
		}
		if sep.kind != tOp || sep.text != "," {
			return nil, p.errAt(sep, fmt.Sprintf("expected ',' or ']' in list, got %s", describe(sep)))
		}
	}
}

func wordValue(w string) Value {
	if w == "true" || w == "false" {
		return Value{Kind: KindBool, B: w == "true"}
	}
	return Value{Kind: KindStr, S: w}
}

func (p *parser) addIdent(name string) {
	if !p.seen[name] {
		p.seen[name] = true
		p.idents = append(p.idents, name)
	}
}
