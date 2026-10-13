package route

import (
	"strings"
	"testing"
)

func triOf(v Value) Tri {
	switch {
	case v.Kind == KindUnknown:
		return Unknown
	case v.B:
		return True
	}
	return False
}

func TestKleeneTables(t *testing.T) {
	vals := []Value{b(false), b(true), unk}
	and := [3][3]Tri{ // [a][b], index False, True, Unknown
		{False, False, False},
		{False, True, Unknown},
		{False, Unknown, Unknown},
	}
	or := [3][3]Tri{
		{False, True, Unknown},
		{True, True, True},
		{Unknown, True, Unknown},
	}
	not := [3]Tri{True, False, Unknown}
	andE, _ := Parse("a && b")
	orE, _ := Parse("a || b")
	notE, _ := Parse("!a")
	for _, av := range vals {
		for _, bv := range vals {
			env := mapEnv{"a": av, "b": bv}
			if got, err := andE.Eval(env); err != nil || got != and[triOf(av)][triOf(bv)] {
				t.Errorf("%v && %v = %v, %v; want %v", triOf(av), triOf(bv), got, err, and[triOf(av)][triOf(bv)])
			}
			if got, err := orE.Eval(env); err != nil || got != or[triOf(av)][triOf(bv)] {
				t.Errorf("%v || %v = %v, %v; want %v", triOf(av), triOf(bv), got, err, or[triOf(av)][triOf(bv)])
			}
		}
		if got, err := notE.Eval(mapEnv{"a": av}); err != nil || got != not[triOf(av)] {
			t.Errorf("!%v = %v, %v; want %v", triOf(av), got, err, not[triOf(av)])
		}
	}
}

func TestEvalUnknownAndShortCircuit(t *testing.T) {
	tests := []struct {
		src  string
		env  mapEnv
		want Tri
	}{
		{"risk >= 4", mapEnv{"risk": unk}, Unknown},
		{"risk >= 4", mapEnv{}, Unknown}, // missing name
		{"kind == test", mapEnv{"kind": unk}, Unknown},
		{"kind != test", mapEnv{"kind": unk}, Unknown},
		{"kind in [a, b]", mapEnv{"kind": unk}, Unknown},
		{"risk >= relevance", mapEnv{"risk": n(4), "relevance": unk}, Unknown},
		{"mechanical || kind in [formatting]", mapEnv{"mechanical": b(true), "kind": unk}, True},
		{"mechanical || kind in [formatting]", mapEnv{"mechanical": b(false), "kind": unk}, Unknown},
		// Short-circuit skips a type error on the right.
		{"x || (risk && kind)", mapEnv{"x": b(true), "risk": n(1), "kind": s("a")}, True},
		{"x && (risk && kind)", mapEnv{"x": b(false), "risk": n(1), "kind": s("a")}, False},
		{"risk == 4", mapEnv{"risk": unk}, Unknown},
		{"!u", mapEnv{"u": unk}, Unknown},
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

func TestEvalTypeErrors(t *testing.T) {
	env := mapEnv{"risk": n(4), "kind": s("test"), "breaking": b(true), "u": unk}
	tests := []struct {
		src     string
		msgPart string
	}{
		{"risk", "number, not a condition"},
		{"kind", "string, not a condition"},
		{"4", "number, not a condition"},
		{`"x"`, "string, not a condition"},
		{"risk && kind", "not a condition"},
		{"!risk", "not a condition"},
		{"kind >= 4", "needs numbers"},
		{"breaking > 0", "needs numbers"},
		{"kind == 4", "cannot compare string"},
		{"risk == high", "cannot compare number"},
		{"u || risk", "not a condition"},
		{"breaking && kind", "not a condition"},
	}
	for _, tt := range tests {
		e, err := Parse(tt.src)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.src, err)
		}
		_, err = e.Eval(env)
		if err == nil || !strings.Contains(err.Error(), tt.msgPart) {
			t.Errorf("Eval(%q) err = %v, want containing %q", tt.src, err, tt.msgPart)
		}
	}
}
