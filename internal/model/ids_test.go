package model

import "testing"

func TestUnitIDIgnoresTrailingWhitespace(t *testing.T) {
	a := UnitID("a.go", "+foo()  \n-bar()\n")
	b := UnitID("a.go", "\n+foo()\n-bar()")
	if a != b {
		t.Fatalf("ids differ: %s vs %s", a, b)
	}
	if UnitID("b.go", "+foo()\n-bar()") == a {
		t.Fatal("path must be part of the id")
	}
	if UnitID("a.go", "-foo()\n-bar()") == a {
		t.Fatal("diff prefixes must be part of the id")
	}
}

func TestGroupIDOrderIndependent(t *testing.T) {
	if GroupID([]string{"b", "a"}) != GroupID([]string{"a", "b"}) {
		t.Fatal("group id must not depend on order")
	}
}

func TestContentHashUnambiguous(t *testing.T) {
	if ContentHash("ab", "c") == ContentHash("a", "bc") {
		t.Fatal("content hash must length-prefix parts")
	}
}

func TestItemIDs(t *testing.T) {
	if PRItemID(42) != "pr:42" || BranchItemID("feat/x") != "branch:feat/x" {
		t.Fatal("unexpected item id format")
	}
}
