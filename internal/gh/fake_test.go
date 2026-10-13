package gh

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestFakeQualifiers(t *testing.T) {
	f := &Fake{Login: "Me", PRs: map[int]PR{
		12: {Number: 12, Title: "Old login", State: "merged"},
		3:  {Number: 3, Title: "Add login", State: "open", ReviewRequested: true},
		7:  {Number: 7, Title: "Dropped idea", State: "closed", ViewerCommented: true},
		1:  {Number: 1, Title: "No state yet", Author: "me"},
		20: {Number: 20, Title: "Asked bob", Reviewers: []string{"bob"}},
	}}
	tests := []struct {
		query string
		limit int
		want  []int
	}{
		{"involves:@me", 0, []int{1, 3, 7}},
		{"involves:bob", 0, []int{20}},
		{"is:open involves:@me", 0, []int{1, 3}},
		{"repo:a/b is:pr is:open", 0, []int{1, 3, 20}},
		{"is:closed", 0, []int{7, 12}},
		{"is:merged", 0, []int{12}},
		{"IS:MERGED login", 0, []int{12}},
		{"is:open login", 0, []int{3}},
		{"login", 0, []int{3, 12}},
		{"involves:@me", 2, []int{1, 3}},
		{"#7", 0, []int{7}},
	}
	for _, tt := range tests {
		got, err := f.SearchPRs(context.Background(), tt.query, tt.limit)
		if err != nil {
			t.Fatal(err)
		}
		var nums []int
		for _, p := range got {
			nums = append(nums, p.Number)
		}
		if !slices.Equal(nums, tt.want) {
			t.Errorf("SearchPRs(%q, %d) = %v, want %v", tt.query, tt.limit, nums, tt.want)
		}
	}
}

func TestFakeListOpenPRs(t *testing.T) {
	f := &Fake{PRs: map[int]PR{
		2: {Number: 2, State: "open", Files: []string{"web/a.ts"}},
		1: {Number: 1},
		3: {Number: 3, State: "merged"},
	}}
	got, err := f.ListOpenPRs(context.Background(), 0)
	if err != nil || len(got) != 2 || got[0].Number != 1 || got[1].Number != 2 || got[1].Files[0] != "web/a.ts" {
		t.Fatalf("ListOpenPRs = %+v, %v", got, err)
	}
	if got, _ := f.ListOpenPRs(context.Background(), 1); len(got) != 1 {
		t.Fatalf("limit ignored: %+v", got)
	}
}

func TestMentions(t *testing.T) {
	tests := []struct {
		body string
		want bool
	}{
		{"@fredric can you look?", true},
		{"cc @Fredric.", true},
		{"thanks (@fredric)", true},
		{"@fredric-bot ran", false},
		{"@fredrickson", false},
		{"mail fredric@example.com", false},
		{"nobody", false},
	}
	for _, tt := range tests {
		if got := mentions(tt.body, "fredric"); got != tt.want {
			t.Errorf("mentions(%q) = %v, want %v", tt.body, got, tt.want)
		}
	}
}

func TestFakeErr(t *testing.T) {
	boom := errors.New("boom")
	f := &Fake{Err: boom, PRs: map[int]PR{1: {Number: 1}}}
	if _, err := f.SearchPRs(context.Background(), "", 0); !errors.Is(err, boom) {
		t.Errorf("SearchPRs err = %v", err)
	}
	if _, err := f.PR(context.Background(), 1); !errors.Is(err, boom) {
		t.Errorf("PR err = %v", err)
	}
}

func TestFakeConcurrent(t *testing.T) {
	f := &Fake{Login: "me", PRs: map[int]PR{1: {Number: 1, Title: "x"}}}
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.SearchPRs(ctx, "is:open", 0)
			_, _ = f.CreatePR(ctx, NewPR{Title: "y", Head: "h", Base: "main"})
			_, _ = f.Viewer(ctx)
		}()
	}
	wg.Wait()
	if len(f.PRs) != 9 {
		t.Errorf("PRs = %d, want 9", len(f.PRs))
	}
}

func TestLoadFixture(t *testing.T) {
	tests := []struct {
		file    string
		wantErr string
	}{
		{"fixture_valid.json", ""},
		{"fixture_dup.json", "listed twice"},
		{"fixture_badkey.json", `diffs key "one"`},
		{"does_not_exist.json", "read fixture"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			f, err := LoadFixture(filepath.Join("testdata", tt.file))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if v, _ := f.Viewer(ctx); v != "fredric" {
				t.Errorf("viewer = %q", v)
			}
			prs, _ := f.SearchPRs(ctx, "is:open", 0)
			if len(prs) != 2 || prs[0].Number != 1 {
				t.Errorf("prs = %+v", prs)
			}
			if d, err := f.PRDiff(ctx, 1); err != nil || !strings.HasPrefix(d, "diff --git") {
				t.Errorf("diff = %q, %v", d, err)
			}
			if th, _ := f.Threads(ctx, 1); len(th) != 1 || th[0].ID != "T1" {
				t.Errorf("threads = %+v", th)
			}
			if o, _ := f.CODEOWNERS(ctx, ""); o != "* @fredric\n" {
				t.Errorf("codeowners = %q", o)
			}
		})
	}
}

func TestFakeFromFixtureNoNumber(t *testing.T) {
	if _, err := FakeFromFixture(Fixture{PRs: []PR{{Title: "x"}}}); err == nil {
		t.Error("want error for PR without number")
	}
}
