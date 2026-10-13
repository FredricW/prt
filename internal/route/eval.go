package route

import (
	"fmt"
	"strconv"
)

// node is one parsed sub-expression. val returns its value: logical
// operators and comparisons yield KindBool or KindUnknown.
type node interface {
	val(env Env) (Value, error)
	text() string // source fragment, for error messages
}

// expr is a parsed `when` expression.
type expr struct {
	src    string
	root   node
	idents []string
}

func (e *expr) Eval(env Env) (Tri, error) { return cond(e.root, env) }
func (e *expr) String() string            { return e.src }

func (e *expr) Idents() []string { return append([]string(nil), e.idents...) }

// cond evaluates n as a condition: it must be a bool or Unknown.
func cond(n node, env Env) (Tri, error) {
	v, err := n.val(env)
	if err != nil {
		return Unknown, err
	}
	switch v.Kind {
	case KindUnknown:
		return Unknown, nil
	case KindBool:
		if v.B {
			return True, nil
		}
		return False, nil
	}
	return Unknown, fmt.Errorf("`%s` is a %s, not a condition", n.text(), kindName(v.Kind))
}

func triValue(t Tri) Value {
	if t == Unknown {
		return Value{}
	}
	return Value{Kind: KindBool, B: t == True}
}

func kindName(k ValueKind) string {
	switch k {
	case KindBool:
		return "boolean"
	case KindNum:
		return "number"
	case KindStr:
		return "string"
	}
	return "unknown"
}

type identNode struct{ name string }

func (n *identNode) text() string { return n.name }

func (n *identNode) val(env Env) (Value, error) {
	v, ok := env.Lookup(n.name)
	if !ok {
		return Value{}, nil
	}
	return v, nil
}

type litNode struct {
	v   Value
	src string
}

func (n *litNode) text() string           { return n.src }
func (n *litNode) val(Env) (Value, error) { return n.v, nil }

type notNode struct {
	x   node
	src string
}

func (n *notNode) text() string { return n.src }

func (n *notNode) val(env Env) (Value, error) {
	t, err := cond(n.x, env)
	if err != nil {
		return Value{}, err
	}
	switch t {
	case True:
		return triValue(False), nil
	case False:
		return triValue(True), nil
	}
	return triValue(Unknown), nil
}

// logicNode is && or || with Kleene semantics and short-circuit: the right
// side is not evaluated when the left side decides the result.
type logicNode struct {
	op   string
	l, r node
	src  string
}

func (n *logicNode) text() string { return n.src }

func (n *logicNode) val(env Env) (Value, error) {
	decisive := True // || is decided by a True operand, && by a False one
	if n.op == "&&" {
		decisive = False
	}
	l, err := cond(n.l, env)
	if err != nil {
		return Value{}, err
	}
	if l == decisive {
		return triValue(decisive), nil
	}
	r, err := cond(n.r, env)
	if err != nil {
		return Value{}, err
	}
	switch {
	case r == decisive:
		return triValue(decisive), nil
	case l == Unknown || r == Unknown:
		return triValue(Unknown), nil
	}
	// Both operands are the non-decisive value.
	return triValue(l), nil
}

type cmpNode struct {
	op   string
	l, r node
	src  string
}

func (n *cmpNode) text() string { return n.src }

func (n *cmpNode) val(env Env) (Value, error) {
	lv, err := n.l.val(env)
	if err != nil {
		return Value{}, err
	}
	rv, err := n.r.val(env)
	if err != nil {
		return Value{}, err
	}
	if lv.Kind == KindUnknown || rv.Kind == KindUnknown {
		return triValue(Unknown), nil
	}
	switch n.op {
	case "==", "!=":
		if lv.Kind != rv.Kind {
			return Value{}, fmt.Errorf("`%s`: cannot compare %s `%s` with %s `%s`",
				n.src, kindName(lv.Kind), n.l.text(), kindName(rv.Kind), n.r.text())
		}
		eq := valueString(lv) == valueString(rv)
		return Value{Kind: KindBool, B: eq == (n.op == "==")}, nil
	}
	if lv.Kind != KindNum || rv.Kind != KindNum {
		return Value{}, fmt.Errorf("`%s`: %s needs numbers, got %s and %s",
			n.src, n.op, kindName(lv.Kind), kindName(rv.Kind))
	}
	var b bool
	switch n.op {
	case ">=":
		b = lv.N >= rv.N
	case "<=":
		b = lv.N <= rv.N
	case ">":
		b = lv.N > rv.N
	default:
		b = lv.N < rv.N
	}
	return Value{Kind: KindBool, B: b}, nil
}

// inNode is `x in [a, b]`, compared by string form.
type inNode struct {
	l     node
	items []Value
	src   string
}

func (n *inNode) text() string { return n.src }

func (n *inNode) val(env Env) (Value, error) {
	lv, err := n.l.val(env)
	if err != nil {
		return Value{}, err
	}
	if lv.Kind == KindUnknown {
		return triValue(Unknown), nil
	}
	s := valueString(lv)
	for _, it := range n.items {
		if valueString(it) == s {
			return triValue(True), nil
		}
	}
	return triValue(False), nil
}

// valueString is the string form `in` compares: strings as is, numbers in
// shortest form (4, 0.5), booleans true/false.
func valueString(v Value) string {
	switch v.Kind {
	case KindNum:
		return strconv.FormatFloat(v.N, 'g', -1, 64)
	case KindBool:
		return strconv.FormatBool(v.B)
	case KindStr:
		return v.S
	}
	return ""
}
