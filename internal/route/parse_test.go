package route

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/config"
)

// mapEnv is a test Env.
type mapEnv map[string]Value

func (m mapEnv) Lookup(n string) (Value, bool) { v, ok := m[n]; return v, ok }

func b(v bool) Value    { return Value{Kind: KindBool, B: v} }
func n(v float64) Value { return Value{Kind: KindNum, N: v} }
func s(v string) Value  { return Value{Kind: KindStr, S: v} }

var unk = Value{Kind: KindUnknown}

func TestParseDefaultRules(t *testing.T) {
	for _, r := range config.DefaultRouting().Rules {
		e, err := Parse(r.When)
		if err != nil {
			t.Fatalf("Parse(%q): %v", r.When, err)
		}
		if e.String() != r.When {
			t.Errorf("String() = %q, want %q", e.String(), r.When)
		}
	}
}

func TestParseIdents(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		{"mechanical || kind in [formatting, moved, generated, dependency]", []string{"mechanical", "kind"}},
		{"risk >= 4 || tag.auth || tag.money || tag.async", []string{"risk", "tag.auth", "tag.money", "tag.async"}},
		{"kind == test", []string{"kind"}},
		{`kind == "test" && kind != perf`, []string{"kind"}},
		{"true", nil},
		{"risk >= relevance && needs_context.callers", []string{"risk", "relevance", "needs_context.callers"}},
		{"contract in [env/flag, \"api shape\"]", []string{"contract"}},
	}
	for _, tt := range tests {
		e, err := Parse(tt.src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.src, err)
		}
		if got := e.Idents(); !slices.Equal(got, tt.want) {
			t.Errorf("Idents(%q) = %v, want %v", tt.src, got, tt.want)
		}
	}
}

func TestParsePrecedence(t *testing.T) {
	tests := []struct {
		src  string
		env  mapEnv
		want Tri
	}{
		// && binds tighter than ||.
		{"a || b && c", mapEnv{"a": b(true), "b": b(false), "c": b(false)}, True},
		{"(a || b) && c", mapEnv{"a": b(true), "b": b(false), "c": b(false)}, False},
		// ! binds tighter than &&.
		{"!a && b", mapEnv{"a": b(false), "b": b(true)}, True},
		{"!(a && b)", mapEnv{"a": b(true), "b": b(false)}, True},
		// comparisons bind tighter than && / ||.
		{"risk >= 4 || x", mapEnv{"risk": n(4), "x": b(false)}, True},
		{"risk >= 4 && kind == test", mapEnv{"risk": n(5), "kind": s("test")}, True},
		{"kind in [a, b] && !x", mapEnv{"kind": s("b"), "x": b(false)}, True},
		{"!!a", mapEnv{"a": b(true)}, True},
		{"((a))", mapEnv{"a": b(true)}, True},
		{"lines > 10.5 && units < 3", mapEnv{"lines": n(11), "units": n(2)}, True},
		{"risk <= 2", mapEnv{"risk": n(3)}, False},
		{`kind == "run locally"`, mapEnv{"kind": s("run locally")}, True},
		{"x == true", mapEnv{"x": b(true)}, True},
		{"risk in [4, 5]", mapEnv{"risk": n(4)}, True},
		{"risk == 4", mapEnv{"risk": n(4)}, True},
		{"risk != 4", mapEnv{"risk": n(4)}, False},
	}
	for _, tt := range tests {
		e, err := Parse(tt.src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.src, err)
		}
		got, err := e.Eval(tt.env)
		if err != nil {
			t.Fatalf("Eval(%q): %v", tt.src, err)
		}
		if got != tt.want {
			t.Errorf("Eval(%q) = %v, want %v", tt.src, got, tt.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		src     string
		col     int
		msgPart string
	}{
		{"", 1, "empty"},
		{"   ", 1, "empty"},
		{"(a || b", 8, "expected ')'"},
		{"a || b)", 7, "unexpected \")\""},
		{"a ||", 5, "expected a value"},
		{"a &&", 5, "expected a value"},
		{"risk >=", 8, "expected a value"},
		{"kind ==", 8, "expected a value"},
		{"!", 2, "expected a value"},
		{"kind in []", 9, "empty list"},
		{"kind in [a,", 12, "expected a list item"},
		{"kind in [a b]", 12, "expected ',' or ']'"},
		{"kind in a", 9, "expected '['"},
		{"a & b", 3, "did you mean '&&'"},
		{"a | b", 3, "did you mean '||'"},
		{"a = b", 3, "did you mean '=='"},
		{"a $ b", 3, "unexpected character"},
		{`kind == "test`, 9, "unterminated string"},
		{"tag. && x", 4, "ends with '.'"},
		{"4x", 2, "after number"},
		{"1.2.3", 1, "bad number"},
		{"a b", 3, "unexpected \"b\""},
		{"in", 1, "unexpected 'in'"},
	}
	for _, tt := range tests {
		_, err := Parse(tt.src)
		var se *SyntaxError
		if !errors.As(err, &se) {
			t.Errorf("Parse(%q) err = %v, want *SyntaxError", tt.src, err)
			continue
		}
		if se.Col != tt.col || !strings.Contains(se.Msg, tt.msgPart) {
			t.Errorf("Parse(%q) = col %d %q, want col %d containing %q", tt.src, se.Col, se.Msg, tt.col, tt.msgPart)
		}
	}
}

func FuzzParse(f *testing.F) {
	for _, r := range config.DefaultRouting().Rules {
		f.Add(r.When)
	}
	for _, seed := range []string{"", "(", ")", "!", "a in [", `"`, "a == b == c", "((a)", "x in [1, \"y\", true]", "risk >= 4.5 && !tag.auth"} {
		f.Add(seed)
	}
	env := mapEnv{"risk": n(4), "kind": s("test"), "mechanical": b(false), "tag.auth": unk}
	f.Fuzz(func(t *testing.T, src string) {
		e, err := Parse(src)
		if err != nil {
			return
		}
		if e.String() != src {
			t.Fatalf("String() = %q, want %q", e.String(), src)
		}
		_, _ = e.Eval(env)
		_ = e.Idents()
	})
}
