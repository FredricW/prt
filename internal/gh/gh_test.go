package gh

import (
	"context"
	"testing"
)

func TestParseRemote(t *testing.T) {
	cases := map[string]Repo{
		"git@github.com:fredricw/prt.git":     {"fredricw", "prt"},
		"https://github.com/fredricw/prt":     {"fredricw", "prt"},
		"https://github.com/fredricw/prt.git": {"fredricw", "prt"},
	}
	for in, want := range cases {
		got, ok := ParseRemote(in)
		if !ok || got != want {
			t.Errorf("ParseRemote(%q) = %v, %v", in, got, ok)
		}
	}
	if _, ok := ParseRemote("https://gitlab.com/a/b"); ok {
		t.Error("non-GitHub remote must not parse")
	}
}

func TestFakeSearch(t *testing.T) {
	f := &Fake{PRs: map[int]PR{
		1: {Number: 1, Title: "Add login", HeadRef: "feat/login"},
		2: {Number: 2, Title: "Bump deps", HeadRef: "deps"},
	}}
	got, err := f.SearchPRs(context.Background(), "repo:a/b is:open login", 0)
	if err != nil || len(got) != 1 || got[0].Number != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	all, _ := f.SearchPRs(context.Background(), "is:open", 0)
	if len(all) != 2 {
		t.Fatalf("want 2, got %d", len(all))
	}
}
