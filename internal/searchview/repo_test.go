package searchview

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// repoHit is a workspace hit: the repo key on the hit and its item id.
func repoHit(h proto.SearchHit, repo string) proto.SearchHit {
	h.Repo = repo
	h.ItemID = model.RepoItemID(repo, h.ItemID)
	return h
}

func TestRowRepoTag(t *testing.T) {
	pr := repoHit(prHit(proto.SearchOpen, 12, "Retry failed webhooks", "alice", "open", 48*time.Hour), "billing-svc")
	br := repoHit(brHit("feat/x", "Add login form", "bob", 5*time.Hour), "web")
	tests := []struct {
		name string
		hit  proto.SearchHit
		w    int
		want string
	}{
		{"wide: repo and author", pr, 60, "  pr  #12 Retry failed webhooks · billing-svc · alice · 2d"},
		{"author dropped first", pr, 40, "  pr  #12 Retry fail… · billing-svc · 2d"},
		{"author dropped at 34", pr, 34, "  pr  #12 Retr… · billing-svc · 2d"},
		{"then the repo", pr, 30, "  pr  #12 Retry failed w… · 2d"},
		{"too narrow for any tail", pr, 16, "  pr  #12 Retry…"},
		{"branch", br, 60, "  br  feat/x  Add login form · web · bob · 5h"},
		{"no repo: as before", prHit(proto.SearchOpen, 12, "Retry failed webhooks", "alice", "open", 48*time.Hour), 40,
			"  pr  #12 Retry failed web… · alice · 2d"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newModel(&fakeSearch{})
			m.SetSize(tt.w, 8)
			got := m.row(tt.hit, false, now)
			if got != tt.want {
				t.Errorf("row at %d = %q, want %q", tt.w, got, tt.want)
			}
			if w := ansi.StringWidth(got); w > tt.w {
				t.Errorf("row is %d wide, want ≤ %d", w, tt.w)
			}
		})
	}
}

// Hits of two repos with the same number stay two results: the workspace
// prefixes their item ids. A repeat of one of them from another source is
// still dropped.
func TestMergeKeepsReposApart(t *testing.T) {
	m := newModel(&fakeSearch{})
	m.SetSearcher(nil) // results are delivered by hand
	run(m.Open())
	typeText(&m, "12")
	seq := m.seq
	a := repoHit(prHit(proto.SearchOpen, 12, "Retry webhooks", "alice", "open", 0), "a")
	b := repoHit(prHit(proto.SearchOpen, 12, "Fix login", "bob", "open", 0), "b")
	aClosed := repoHit(prHit(proto.SearchClosed, 12, "Retry webhooks", "alice", "closed", 0), "a")
	deliver(&m,
		ResultMsg{Seq: seq, Result: proto.SearchResult{Source: proto.SearchOpen, Hits: []proto.SearchHit{a, b}}},
		ResultMsg{Seq: seq, Result: proto.SearchResult{Source: proto.SearchClosed, Hits: []proto.SearchHit{aClosed}}},
	)
	want := []string{"a::pr:12", "b::pr:12"}
	if got := ids(m.Hits()); !slices.Equal(got, want) {
		t.Fatalf("hits = %v, want %v", got, want)
	}
	if m.Hits()[0].Source != proto.SearchOpen {
		t.Errorf("duplicate kept the closed copy: %+v", m.Hits()[0])
	}
	v := m.View()
	if !strings.Contains(v, "· a · alice") || !strings.Contains(v, "· b · bob") {
		t.Errorf("rows lack their repo tags:\n%s", v)
	}
}
