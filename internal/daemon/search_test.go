package daemon

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

func hoursAgo(h int) time.Time { return testNow.Add(-time.Duration(h) * time.Hour) }

// searchGit has local branches, their remote-tracking copies, a remote-only
// branch, the default branch and HEAD symrefs.
func searchGit() *gitx.Fake {
	return &gitx.Fake{RefList: []gitx.Ref{
		{Name: "main", FullName: "refs/heads/main", SHA: "m1", CommitDate: hoursAgo(1)},
		{Name: "feat/login", FullName: "refs/heads/feat/login", SHA: "l1", Subject: "Add login form", Author: "ingrid", CommitDate: hoursAgo(5), Upstream: "origin/feat/login"},
		{Name: "feat/old", FullName: "refs/heads/feat/old", SHA: "o1", Subject: "Old work", Author: "bob", CommitDate: hoursAgo(500)},
		{Name: "spike", FullName: "refs/heads/spike", SHA: "s1", Subject: "Try JWT sessions", Author: "me", CommitDate: hoursAgo(2)},
		{Name: "origin/HEAD", FullName: "refs/remotes/origin/HEAD", SHA: "m1", Remote: true},
		{Name: "origin/main", FullName: "refs/remotes/origin/main", SHA: "m1", Remote: true, CommitDate: hoursAgo(1)},
		{Name: "origin/feat/login", FullName: "refs/remotes/origin/feat/login", SHA: "l0", Remote: true, CommitDate: hoursAgo(6)},
		{Name: "origin/spike", FullName: "refs/remotes/origin/spike", SHA: "s0", Remote: true, CommitDate: hoursAgo(3)},
		{Name: "origin/feat/remote", FullName: "refs/remotes/origin/feat/remote", SHA: "r1", Subject: "Remote feature", Author: "carol", Remote: true, CommitDate: hoursAgo(1)},
	}}
}

func searchGH() *gh.Fake {
	return &gh.Fake{Login: "me", PRs: map[int]gh.PR{
		482: {Number: 482, Title: "Migrate session to JWT", Author: "ingrid", HeadRef: "feat/jwt", State: "open", UpdatedAt: hoursAgo(48)},
		483: {Number: 483, Title: "Session cleanup", Author: "bob", HeadRef: "feat/cleanup", UpdatedAt: hoursAgo(3)},
		300: {Number: 300, Title: "Old session store", Author: "carol", HeadRef: "feat/store", State: "closed"},
		301: {Number: 301, Title: "Session cookies", Author: "dan", HeadRef: "feat/cookies", State: "merged"},
	}}
}

func refsOf(hits []proto.SearchHit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		if h.Kind == model.KindPR {
			out = append(out, h.Query)
			continue
		}
		out = append(out, h.Ref)
	}
	return out
}

func TestSearchLocal(t *testing.T) {
	cases := []struct {
		name  string
		query string
		limit int
		want  []string
	}{
		{"empty lists newest first, locals before remotes", "", 0, []string{"spike", "feat/login", "feat/old", "origin/feat/remote"}},
		{"name substring, case-insensitive", "FEAT", 0, []string{"feat/login", "feat/old", "origin/feat/remote"}},
		{"tip subject", "jwt", 0, []string{"spike"}},
		{"bang stripped", "!login", 0, []string{"feat/login"}},
		{"limit", "", 2, []string{"spike", "feat/login"}},
		{"no match", "zzz", 0, nil},
		{"default branch excluded", "main", 0, nil},
	}
	c := newClient(t, newTestStore(t), searchGit(), nil, nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.Search(context.Background(), proto.SearchParams{Query: tc.query, Source: proto.SearchLocal, Limit: tc.limit})
			if err != nil {
				t.Fatal(err)
			}
			if got := refsOf(res.Hits); !slices.Equal(got, tc.want) {
				t.Errorf("hits = %v, want %v", got, tc.want)
			}
			if res.Source != proto.SearchLocal || res.Query != tc.query || res.Note != "" {
				t.Errorf("result = %+v", res)
			}
		})
	}
}

func TestSearchLocalHitFields(t *testing.T) {
	c := newClient(t, newTestStore(t), searchGit(), nil, nil)
	res, err := c.Search(context.Background(), proto.SearchParams{Query: "login", Source: proto.SearchLocal})
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("search = %+v, %v", res, err)
	}
	want := proto.SearchHit{
		Source: proto.SearchLocal, Kind: model.KindBranch, Ref: "feat/login", Title: "Add login form", Author: "ingrid",
		UpdatedAt: hoursAgo(5), Query: "feat/login", ItemID: model.BranchItemID("feat/login"),
	}
	if res.Hits[0] != want {
		t.Errorf("hit = %+v, want %+v", res.Hits[0], want)
	}
}

func TestSearchGitHub(t *testing.T) {
	cases := []struct {
		name   string
		src    proto.SearchSource
		query  string
		limit  int
		want   []string
		states []string
		note   string
	}{
		{"open substring", proto.SearchOpen, "session", 0, []string{"#482", "#483"}, []string{"open", "open"}, ""},
		{"open limit", proto.SearchOpen, "session", 1, []string{"#482"}, nil, ""},
		{"closed and merged", proto.SearchClosed, "session", 0, []string{"#300", "#301"}, []string{"closed", "merged"}, ""},
		{"closed with bang", proto.SearchClosed, "!cookies", 0, []string{"#301"}, nil, ""},
		{"bare number", proto.SearchOpen, "482", 0, []string{"#482"}, []string{"open"}, ""},
		{"hash number of a closed PR", proto.SearchOpen, "#300", 0, []string{"#300"}, []string{"closed"}, ""},
		{"bang number", proto.SearchOpen, "!#301", 0, []string{"#301"}, []string{"merged"}, ""},
		{"unknown number", proto.SearchOpen, "#999", 0, nil, nil, "no PR #999"},
		{"empty query asks nothing", proto.SearchOpen, "  ", 0, nil, nil, ""},
		{"bang alone asks nothing", proto.SearchClosed, "!", 0, nil, nil, ""},
	}
	c := newClient(t, newTestStore(t), searchGit(), searchGH(), nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := c.Search(context.Background(), proto.SearchParams{Query: tc.query, Source: tc.src, Limit: tc.limit})
			if err != nil {
				t.Fatal(err)
			}
			if got := refsOf(res.Hits); !slices.Equal(got, tc.want) {
				t.Errorf("hits = %v, want %v", got, tc.want)
			}
			if res.Note != tc.note {
				t.Errorf("note = %q, want %q", res.Note, tc.note)
			}
			for i, s := range tc.states {
				if res.Hits[i].State != s {
					t.Errorf("hit %d state = %q, want %q", i, res.Hits[i].State, s)
				}
			}
		})
	}
}

func TestSearchPRHitFields(t *testing.T) {
	c := newClient(t, newTestStore(t), searchGit(), searchGH(), nil)
	res, err := c.Search(context.Background(), proto.SearchParams{Query: "jwt", Source: proto.SearchOpen})
	if err != nil || len(res.Hits) != 1 {
		t.Fatalf("search = %+v, %v", res, err)
	}
	want := proto.SearchHit{
		Source: proto.SearchOpen, Kind: model.KindPR, Number: 482, Ref: "feat/jwt", Title: "Migrate session to JWT",
		Author: "ingrid", State: "open", UpdatedAt: hoursAgo(48), Query: "#482", ItemID: model.PRItemID(482),
	}
	if res.Hits[0] != want {
		t.Errorf("hit = %+v, want %+v", res.Hits[0], want)
	}
}

func TestSearchDefaultLimit(t *testing.T) {
	g := &gitx.Fake{}
	for i := range 30 {
		g.RefList = append(g.RefList, gitx.Ref{Name: "b" + strings.Repeat("x", i), SHA: "s"})
	}
	c := newClient(t, newTestStore(t), g, nil, nil)
	res, err := c.Search(context.Background(), proto.SearchParams{Source: proto.SearchLocal})
	if err != nil || len(res.Hits) != defaultSearchLimit {
		t.Fatalf("got %d hits, %v; want %d", len(res.Hits), err, defaultSearchLimit)
	}
}

func TestSearchNotes(t *testing.T) {
	ctx := context.Background()
	t.Run("no GitHub", func(t *testing.T) {
		c := newClient(t, newTestStore(t), searchGit(), nil, nil)
		for _, src := range []proto.SearchSource{proto.SearchOpen, proto.SearchClosed} {
			res, err := c.Search(ctx, proto.SearchParams{Query: "x", Source: src})
			if err != nil || res.Note != "GitHub unavailable" || len(res.Hits) != 0 {
				t.Errorf("%s: %+v, %v", src, res, err)
			}
		}
	})
	t.Run("GitHub error", func(t *testing.T) {
		ghc := searchGH()
		ghc.Err = errors.New("rate limited")
		c := newClient(t, newTestStore(t), searchGit(), ghc, nil)
		for _, q := range []string{"session", "#482"} {
			res, err := c.Search(ctx, proto.SearchParams{Query: q, Source: proto.SearchOpen})
			if err != nil || res.Note != "GitHub search failed: rate limited" {
				t.Errorf("%q: %+v, %v", q, res, err)
			}
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ghc := searchGH()
		ghc.Err = context.Canceled
		c := newClient(t, newTestStore(t), searchGit(), ghc, nil)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := c.Search(cctx, proto.SearchParams{Query: "session", Source: proto.SearchOpen}); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})
	t.Run("unknown source", func(t *testing.T) {
		c := newClient(t, newTestStore(t), searchGit(), nil, nil)
		wantErr(t, func() error { _, err := c.Search(ctx, proto.SearchParams{Source: "web"}); return err }(), "unknown source")
	})
}

func TestSearchListed(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st,
		model.Item{ID: model.PRItemID(482), Kind: model.KindPR, Number: 482, Source: model.SourceRanked, Section: model.SectionNeedsYou},
		model.Item{ID: model.PRItemID(483), Kind: model.KindPR, Number: 483, Source: model.SourcePreview},
		model.Item{ID: model.BranchItemID("spike"), Kind: model.KindBranch, Ref: "spike", Source: model.SourceAdded, Section: model.SectionAdded},
	)
	c := newClient(t, st, searchGit(), searchGH(), nil)
	res, err := c.Search(ctx, proto.SearchParams{Query: "session", Source: proto.SearchOpen})
	if err != nil || len(res.Hits) != 2 {
		t.Fatalf("search = %+v, %v", res, err)
	}
	if !res.Hits[0].Listed || res.Hits[1].Listed {
		t.Errorf("listed = %v, %v; want ranked #482 listed, preview #483 not", res.Hits[0].Listed, res.Hits[1].Listed)
	}
	res, err = c.Search(ctx, proto.SearchParams{Query: "spike", Source: proto.SearchLocal})
	if err != nil || len(res.Hits) != 1 || !res.Hits[0].Listed {
		t.Errorf("local = %+v, %v; want spike listed", res, err)
	}
}

// previewClient has GitHub PRs and local refs, and records jobs instead of
// running the pipeline.
func previewClient(t *testing.T) (*InProcess, *jobLog) {
	t.Helper()
	g := searchGit()
	c := newClient(t, newTestStore(t), g, searchGH(), nil)
	return c, captureJobs(c)
}

func TestPreviewLifecycle(t *testing.T) {
	ctx := context.Background()
	c, jobs := previewClient(t)

	p, err := c.Preview(ctx, "#482")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != model.PRItemID(482) || p.Source != model.SourcePreview || p.Section != "" || p.Title != "Migrate session to JWT" {
		t.Fatalf("preview = %+v", p)
	}
	if js := jobs.waitJobs(t, 1); js[0].itemID != p.ID {
		t.Errorf("jobs = %+v", js)
	}
	if _, ok := itemMap(t, c)[p.ID]; ok {
		t.Error("Items lists the preview")
	}
	if d, err := c.Detail(ctx, p.ID); err != nil || d.Item.ID != p.ID {
		t.Errorf("Detail = %+v, %v", d.Item, err)
	}

	// Previewing the same item again keeps it.
	if again, err := c.Preview(ctx, "482"); err != nil || again.Source != model.SourcePreview {
		t.Fatalf("preview again = %+v, %v", again, err)
	}
	// Another preview replaces it.
	b, err := c.Preview(ctx, "feat/login")
	if err != nil || b.Source != model.SourcePreview || b.Kind != model.KindBranch {
		t.Fatalf("branch preview = %+v, %v", b, err)
	}
	if b.Title != "Add login form" || b.Upstream != "origin/feat/login" || !b.LastCommitAt.Equal(hoursAgo(5)) {
		t.Errorf("branch facts = %+v", b)
	}
	if _, err := c.d.Store.Item(ctx, p.ID); err == nil {
		t.Error("previous preview not removed")
	}

	// enter promotes it.
	drain(c.Events())
	added, err := c.Add(ctx, "feat/login")
	if err != nil || added.Source != model.SourceAdded || added.Section != model.SectionAdded {
		t.Fatalf("add = %+v, %v", added, err)
	}
	if !hasEvent(drain(c.Events()), proto.EventItem, b.ID) {
		t.Error("no item event for the promotion")
	}
	if _, ok := itemMap(t, c)[b.ID]; !ok {
		t.Error("promoted item not listed")
	}
	// A later preview does not remove an added item.
	if _, err := c.Preview(ctx, "#300"); err != nil {
		t.Fatal(err)
	}
	if _, ok := itemMap(t, c)[b.ID]; !ok {
		t.Error("a preview removed an added item")
	}
}

func TestPreviewOfListedItem(t *testing.T) {
	ctx := context.Background()
	c, _ := previewClient(t)
	put(t, c.d.Store, model.Item{ID: model.PRItemID(483), Kind: model.KindPR, Number: 483, Source: model.SourceRanked, Section: model.SectionInvolved})
	if _, err := c.Preview(ctx, "#482"); err != nil {
		t.Fatal(err)
	}
	it, err := c.Preview(ctx, "#483")
	if err != nil || it.Source != model.SourceRanked || it.Section != model.SectionInvolved {
		t.Fatalf("preview of a ranked item = %+v, %v", it, err)
	}
	if _, err := c.d.Store.Item(ctx, model.PRItemID(482)); err == nil {
		t.Error("the earlier preview was kept")
	}
}

func TestRemovePreviewIsSilent(t *testing.T) {
	ctx := context.Background()
	c, _ := previewClient(t)
	p, err := c.Preview(ctx, "#482")
	if err != nil {
		t.Fatal(err)
	}
	drain(c.Events())
	if err := c.Remove(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if hasEvent(drain(c.Events()), proto.EventRemove, p.ID) {
		t.Error("removing a preview emitted a remove event")
	}
	if _, err := c.d.Store.Item(ctx, p.ID); err == nil {
		t.Error("preview not removed")
	}
}

// RemovePreview (the TUI's discard when the prompt closes) never deletes a
// listed item: one previewed while already stored, or a preview promoted
// by enter before the discard landed.
func TestRemovePreviewKeepsListedItems(t *testing.T) {
	ctx := context.Background()
	c, _ := previewClient(t)
	ranked := model.Item{ID: model.PRItemID(483), Kind: model.KindPR, Number: 483, Source: model.SourceRanked, Section: model.SectionInvolved}
	put(t, c.d.Store, ranked)
	// tab on a listed hit, then esc.
	it, err := c.Preview(ctx, "#483")
	if err != nil {
		t.Fatal(err)
	}
	drain(c.Events())
	if err := c.RemovePreview(ctx, it.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := itemMap(t, c)[ranked.ID]; !ok {
		t.Error("RemovePreview deleted a ranked item")
	}
	if hasEvent(drain(c.Events()), proto.EventRemove, ranked.ID) {
		t.Error("RemovePreview emitted a remove event for a listed item")
	}
	// tab then enter before the preview reply: the late discard must keep
	// the promoted item.
	p, err := c.Preview(ctx, "#482")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Add(ctx, "#482"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemovePreview(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if got, ok := itemMap(t, c)[p.ID]; !ok || got.Source != model.SourceAdded {
		t.Errorf("RemovePreview after Add: item %+v listed %v", got, ok)
	}
	// A real preview is removed; an unknown id is not an error.
	q, err := c.Preview(ctx, "#300")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RemovePreview(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.d.Store.Item(ctx, q.ID); err == nil {
		t.Error("RemovePreview kept a preview")
	}
	if err := c.RemovePreview(ctx, "pr:999999"); err != nil {
		t.Errorf("RemovePreview of a missing id: %v", err)
	}
}

// A merged PR added from search records its state, so branch.state does
// not read it as an open PR.
func TestAddMergedPRKeepsState(t *testing.T) {
	c, _ := previewClient(t)
	it, err := c.Add(context.Background(), "#301")
	if err != nil || it.PRState != "merged" {
		t.Fatalf("add #301 = %+v, %v", it, err)
	}
}

func TestStartupRemovesPreviews(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st,
		model.Item{ID: model.PRItemID(482), Kind: model.KindPR, Number: 482, Source: model.SourcePreview, State: model.StateAnalysing},
		model.Item{ID: model.PRItemID(483), Kind: model.KindPR, Number: 483, Source: model.SourceAdded, Section: model.SectionAdded},
	)
	c := newClient(t, st, searchGit(), nil, nil)
	if _, err := st.Item(ctx, model.PRItemID(482)); err == nil {
		t.Error("leftover preview kept at start-up")
	}
	if _, ok := itemMap(t, c)[model.PRItemID(483)]; !ok {
		t.Error("added item removed at start-up")
	}
}

func TestDiscoveryKeepsPreviewsAndAdded(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st,
		model.Item{ID: model.PRItemID(300), Kind: model.KindPR, Number: 300, Source: model.SourceAdded, Section: model.SectionAdded},
	)
	g := searchGit()
	c := newClient(t, st, g, searchGH(), nil)
	captureJobs(c)
	p, err := c.Preview(ctx, "#301")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{model.PRItemID(300), p.ID} {
		if _, err := st.Item(ctx, id); err != nil {
			t.Errorf("%s pruned by discovery: %v", id, err)
		}
	}
	got, _ := st.Item(ctx, p.ID)
	if got.Source != model.SourcePreview || got.Section != "" {
		t.Errorf("preview changed by discovery: %+v", got)
	}
}

func TestDiscoveryRanksPreviewedBranch(t *testing.T) {
	ctx := context.Background()
	g := searchGit()
	g.Ahead = map[string]int{"origin/main..spike": 2}
	c := newClient(t, newTestStore(t), g, nil, nil)
	captureJobs(c)
	if _, err := c.Preview(ctx, "spike"); err != nil {
		t.Fatal(err)
	}
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	it, ok := itemMap(t, c)[model.BranchItemID("spike")]
	if !ok || it.Source != model.SourceRanked || it.Section != model.SectionBranches {
		t.Errorf("previewed branch after discovery = %+v (listed %v)", it, ok)
	}
}

func TestDiscoverBranchFacts(t *testing.T) {
	ctx := context.Background()
	g := searchGit()
	// Branches are compared with origin/main (it exists in searchGit).
	g.Ahead = map[string]int{"origin/main..feat/login": 3, "origin/feat/login..feat/login": 1, "origin/main..feat/old": 1}
	c := newClient(t, newTestStore(t), g, nil, nil)
	captureJobs(c)
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	m := itemMap(t, c)
	login := m[model.BranchItemID("feat/login")]
	if login.Upstream != "origin/feat/login" || !login.LastCommitAt.Equal(hoursAgo(5)) || login.Unpushed != 1 || login.Ahead != 3 {
		t.Errorf("feat/login = upstream %q, last commit %v, unpushed %d, ahead %d", login.Upstream, login.LastCommitAt, login.Unpushed, login.Ahead)
	}
	old := m[model.BranchItemID("feat/old")]
	if old.Upstream != "" || !old.LastCommitAt.Equal(hoursAgo(500)) {
		t.Errorf("feat/old = %+v", old)
	}

	// A new commit date alone is a change (item event).
	g.RefList[2].CommitDate = hoursAgo(400)
	drain(c.Events())
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if !hasEvent(drain(c.Events()), proto.EventItem, model.BranchItemID("feat/old")) {
		t.Error("LastCommitAt change emitted no item event")
	}
}

func TestUrgencyInputIdleDays(t *testing.T) {
	c := newClient(t, newTestStore(t), &gitx.Fake{}, nil, nil)
	cases := []struct {
		name string
		it   model.Item
		want float64
	}{
		{"branch", model.Item{Kind: model.KindBranch, LastCommitAt: hoursAgo(36)}, 1.5},
		{"branch, unknown date", model.Item{Kind: model.KindBranch}, 0},
		{"branch, future date", model.Item{Kind: model.KindBranch, LastCommitAt: testNow.Add(time.Hour)}, 0},
		{"PR", model.Item{Kind: model.KindPR, LastCommitAt: hoursAgo(48)}, 0},
	}
	for _, tc := range cases {
		if got := c.urgencyInput(tc.it).IdleDays; math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s: IdleDays = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSocketSearchAndPreview(t *testing.T) {
	ctx := context.Background()
	backing := socketBacking(t)
	captureJobs(backing)
	s, _, _ := startServer(t, backing)

	res, err := s.Search(ctx, proto.SearchParams{Query: "#4", Source: proto.SearchOpen})
	if err != nil || len(res.Hits) != 1 || res.Hits[0].Number != 4 || res.Hits[0].Query != "#4" || res.Source != proto.SearchOpen {
		t.Fatalf("search = %+v, %v", res, err)
	}
	res, err = s.Search(ctx, proto.SearchParams{Query: "feat", Source: proto.SearchLocal, Limit: 1})
	if err != nil || len(res.Hits) != 1 || res.Hits[0].Kind != model.KindBranch {
		t.Fatalf("local search = %+v, %v", res, err)
	}
	if _, err := s.Search(ctx, proto.SearchParams{Source: "nope"}); err == nil {
		t.Error("unknown source: no error over the socket")
	}

	it, err := s.Preview(ctx, "#4")
	if err != nil || it.ID != model.PRItemID(4) || it.Source != model.SourcePreview {
		t.Fatalf("preview = %+v, %v", it, err)
	}
	items, err := s.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range items {
		if x.ID == it.ID {
			t.Error("Items lists the preview over the socket")
		}
	}
}
