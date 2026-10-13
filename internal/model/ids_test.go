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

func TestRepoItemIDRoundTrip(t *testing.T) {
	tests := []struct {
		repo, id, want string
	}{
		{"", "pr:12", "pr:12"},
		{"billing-svc", "pr:12", "billing-svc::pr:12"},
		{"team/portal", "branch:feat/x", "team/portal::branch:feat/x"},
		{"a", "branch:x::y", "a::branch:x::y"},
	}
	for _, tt := range tests {
		got := RepoItemID(tt.repo, tt.id)
		if got != tt.want {
			t.Errorf("RepoItemID(%q, %q) = %q, want %q", tt.repo, tt.id, got, tt.want)
		}
		r, l := SplitRepoItemID(got)
		if r != tt.repo || l != tt.id {
			t.Errorf("SplitRepoItemID(%q) = %q, %q; want %q, %q", got, r, l, tt.repo, tt.id)
		}
	}
	if r, l := SplitRepoItemID("::pr:1"); r != "" || l != "::pr:1" {
		t.Errorf("empty repo prefix split as %q, %q", r, l)
	}
}

func TestCleanRepoKey(t *testing.T) {
	tests := map[string]string{
		"":              "",
		"./billing-svc": "billing-svc",
		"team/portal/":  "team/portal",
		`team\portal`:   "team/portal",
		"odd:name":      "odd_name",
	}
	for in, want := range tests {
		if got := CleanRepoKey(in); got != want {
			t.Errorf("CleanRepoKey(%q) = %q, want %q", in, got, want)
		}
	}
}
