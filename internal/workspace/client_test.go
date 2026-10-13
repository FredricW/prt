package workspace

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

var errBoom = errors.New("boom")

func ghInfo(key, ghRepo string) proto.RepoInfo {
	return proto.RepoInfo{Key: key, Label: ghRepo, GHRepo: ghRepo, Status: proto.RepoGitHub}
}

func localInfo(key string) proto.RepoInfo {
	return proto.RepoInfo{Key: key, Label: key, Status: proto.RepoNoRemote}
}

// TestOutMappingCoversEveryItemID fills every item id field of each result
// type (reflection, so fields added later are covered), maps it and checks
// that every id is prefixed, every Item.Repo set and the input untouched.
func TestOutMappingCoversEveryItemID(t *testing.T) {
	const key, local, wsID = "k", "pr:1", "k::pr:1"
	cases := []struct {
		name string
		run  func() (in, out any)
	}{
		{"Detail", func() (any, any) { v := filled[proto.Detail](local); return v, outDetail(key, v) }},
		{"Item", func() (any, any) { v := filled[model.Item](local); return v, outItem(key, v) }},
		{"[]Item", func() (any, any) { v := filled[[]model.Item](local); return v, outItems(key, v) }},
		{"SubmitResult", func() (any, any) { v := filled[proto.SubmitResult](local); return v, outSubmit(key, v) }},
		{"CreatePRResult", func() (any, any) { v := filled[proto.CreatePRResult](local); return v, outCreatePR(key, v) }},
		{"Comment", func() (any, any) { v := filled[model.Comment](local); return v, outComment(key, v) }},
		{"[]ReviewCall", func() (any, any) { v := filled[[]model.ReviewCall](local); return v, outCalls(key, v) }},
		{"Event", func() (any, any) { v := filled[proto.Event](local); return v, outEvent(key, v) }},
		{"SearchHit", func() (any, any) { v := filled[proto.SearchHit](local); return v, outHit(key, v) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out := tc.run()
			checkIDs(t, "out", out, wsID, key)
			checkIDs(t, "in", in, local, "") // the member's value is not modified
		})
	}
}

func TestEventCarriesRepo(t *testing.T) {
	e := outEvent("k", proto.Event{Type: proto.EventLog, Note: "hi"})
	if e.Repo != "k" || e.ItemID != "" || e.Item != nil || e.RunLog != nil {
		t.Errorf("event = %+v", e)
	}
}

// res adapts a (value, error) call to (any, error).
func res[T any](v T, err error) (any, error) { return v, err }

// TestRoutedMethodsMapIDs calls every item-scoped method with params whose
// item ids (all of them, by reflection) carry the repo prefix, and checks
// that the member got the local ids and the result carries workspace ids.
func TestRoutedMethodsMapIDs(t *testing.T) {
	const in, local = "k::pr:1", "pr:1"
	other := newFake(localInfo("a"))
	f := newFake(localInfo("k"))
	c := newTestClient(t, other, f)
	ctx := context.Background()
	cases := []struct {
		method string // the fake method that must receive the local ids
		call   func() (any, error)
		outIDs bool // the result has item ids
	}{
		{"DetailOf", func() (any, error) { return res(c.Detail(ctx, in)) }, true},
		{"DetailOf", func() (any, error) { return res(c.DetailOf(ctx, filled[proto.DetailParams](in))) }, true},
		{"Labels", func() (any, error) { return res(c.Labels(ctx, filled[proto.LabelsParams](in))) }, false},
		{"Refresh", func() (any, error) { return nil, c.Refresh(ctx, filled[proto.ItemParams](in)) }, false},
		{"Remove", func() (any, error) { return nil, c.Remove(ctx, in) }, false},
		{"RemovePreview", func() (any, error) { return nil, c.RemovePreview(ctx, in) }, false},
		{"Checkout", func() (any, error) { return res(c.Checkout(ctx, filled[proto.CheckoutParams](in))) }, false},
		{"EditGroups", func() (any, error) { return nil, c.EditGroups(ctx, filled[proto.GroupEditParams](in)) }, false},
		{"ReviewAction", func() (any, error) { return nil, c.ReviewAction(ctx, filled[proto.ReviewActionParams](in)) }, false},
		{"CallLog", func() (any, error) { return res(c.CallLog(ctx, filled[proto.CallLogParams](in))) }, true},
		{"SetCap", func() (any, error) { return nil, c.SetCap(ctx, filled[proto.CapParams](in)) }, false},
		{"SaveComment", func() (any, error) { return res(c.SaveComment(ctx, filled[proto.CommentParams](in))) }, true},
		{"CommentAction", func() (any, error) { return res(c.CommentAction(ctx, filled[proto.CommentActionParams](in))) }, true},
		{"SubmitReview", func() (any, error) { return res(c.SubmitReview(ctx, filled[proto.SubmitParams](in))) }, true},
		{"CreatePR", func() (any, error) { return res(c.CreatePR(ctx, filled[proto.CreatePRParams](in))) }, true},
		{"MarkViewed", func() (any, error) { return nil, c.MarkViewed(ctx, filled[proto.ViewedParams](in)) }, false},
		{"Override", func() (any, error) { return nil, c.Override(ctx, filled[proto.OverrideParams](in)) }, false},
		{"Stop", func() (any, error) { return res(c.Stop(ctx, filled[proto.StopParams](in))) }, false},
		{"Resume", func() (any, error) { return res(c.Resume(ctx, filled[proto.ResumeParams](in))) }, false},
		{"RunSync", func() (any, error) { return nil, c.RunSync(ctx, in) }, false},
	}
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			out, err := tc.call()
			if err != nil {
				t.Fatal(err)
			}
			switch arg := f.last(t, tc.method).(type) {
			case string:
				if arg != local {
					t.Errorf("member got %q, want %q", arg, local)
				}
			default:
				checkIDs(t, tc.method+" params", arg, local, "")
			}
			if tc.outIDs {
				checkIDs(t, tc.method+" result", out, in, "k")
			}
		})
	}
	if calls := other.argsOf("DetailOf"); len(calls) != 0 {
		t.Errorf("another repo was called: %v", calls)
	}
	stopped, err := c.Stop(ctx, proto.StopParams{ItemID: in})
	if err != nil || !slices.Equal(stopped.Stopped, []string{in}) {
		t.Errorf("Stop = %v, %v", stopped, err)
	}
	resumed, err := c.Resume(ctx, proto.ResumeParams{ItemID: in})
	if err != nil || !slices.Equal(resumed.Resumed, []string{in}) {
		t.Errorf("Resume = %v, %v", resumed, err)
	}
}

func TestRoutingErrors(t *testing.T) {
	c := newTestClient(t, newFake(localInfo("a")))
	ctx := context.Background()
	if _, err := c.Detail(ctx, "zzz::pr:1"); err == nil || err.Error() != `workspace: unknown repo "zzz"` {
		t.Errorf("unknown repo: %v", err)
	}
	if err := c.Refresh(ctx, proto.ItemParams{ItemID: "pr:1"}); err == nil || err.Error() != `workspace: item id "pr:1" has no repo prefix` {
		t.Errorf("no prefix: %v", err)
	}
	if _, err := c.Stop(ctx, proto.StopParams{}); err == nil || !strings.Contains(err.Error(), "has no repo prefix") {
		t.Errorf("stop without id: %v", err)
	}
}

func TestEditAndCommentKeepMismatchedIDs(t *testing.T) {
	f := newFake(localInfo("k"))
	c := newTestClient(t, f)
	ctx := context.Background()
	// An unprefixed inner id is already local; another repo's id is passed
	// through so the member rejects the mismatch.
	if err := c.EditGroups(ctx, proto.GroupEditParams{ItemID: "k::pr:1", Edit: model.GroupEdit{ItemID: "pr:1"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t, "EditGroups").(proto.GroupEditParams).Edit.ItemID; got != "pr:1" {
		t.Errorf("edit item id = %q", got)
	}
	if _, err := c.SaveComment(ctx, proto.CommentParams{ItemID: "k::pr:1", Comment: model.Comment{ItemID: "x::pr:1"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t, "SaveComment").(proto.CommentParams).Comment.ItemID; got != "x::pr:1" {
		t.Errorf("comment item id = %q", got)
	}
}

func TestItemsRankAcrossRepos(t *testing.T) {
	a := newFake(localInfo("a"))
	a.items = []model.Item{
		{ID: "pr:1", Section: model.SectionNeedsYou, Urgency: 8, UrgencyQuartile: 4},
		{ID: "pr:2", Section: model.SectionNeedsYou, Urgency: 2, UrgencyQuartile: 1},
		{ID: "branch:x", Section: model.SectionBranches, Urgency: 3, UrgencyQuartile: 4},
	}
	b := newFake(localInfo("b"))
	b.items = []model.Item{
		{ID: "pr:1", Section: model.SectionNeedsYou, Urgency: 4, UrgencyQuartile: 4},
		{ID: "pr:9", Section: model.SectionNeedsYou, Urgency: 1, UrgencyQuartile: 1},
	}
	c := newTestClient(t, b, a)
	items, err := c.Items(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range items {
		got = append(got, it.ID+"="+string(rune('0'+it.UrgencyQuartile)))
		if repo, _ := model.SplitRepoItemID(it.ID); it.Repo != repo {
			t.Errorf("%s: repo %q", it.ID, it.Repo)
		}
	}
	want := []string{"a::pr:1=4", "b::pr:1=2", "a::pr:2=1", "b::pr:9=1", "a::branch:x=2"}
	if !slices.Equal(got, want) {
		t.Errorf("items = %v, want %v", got, want)
	}
	if a.items[0].ID != "pr:1" || a.items[0].Repo != "" || b.items[0].UrgencyQuartile != 4 {
		t.Errorf("member items modified: %+v %+v", a.items[0], b.items[0])
	}
}

func TestItemsPartialFailure(t *testing.T) {
	a := newFake(localInfo("a"))
	a.itemsErr = errBoom
	b := newFake(localInfo("b"))
	b.items = []model.Item{{ID: "pr:1", Urgency: 1}}
	c := newTestClient(t, a, b)
	ctx := context.Background()
	items, err := c.Items(ctx)
	if err != nil || len(items) != 1 || items[0].ID != "b::pr:1" {
		t.Fatalf("Items = %v, %v", items, err)
	}
	ws, _ := c.Workspace(ctx)
	if ws.Repos[0].Note != "items: boom" || ws.Repos[1].Note != "" {
		t.Errorf("repos = %+v", ws.Repos)
	}

	b.itemsErr = errBoom
	if _, err := c.Items(ctx); err == nil || !strings.Contains(err.Error(), "a: boom") || !strings.Contains(err.Error(), "b: boom") {
		t.Errorf("all failed: %v", err)
	}

	a.itemsErr, b.itemsErr = nil, nil
	if _, err := c.Items(ctx); err != nil {
		t.Fatal(err)
	}
	ws, _ = c.Workspace(ctx)
	if ws.Repos[0].Note != "" {
		t.Errorf("note not cleared: %+v", ws.Repos[0])
	}
}

func TestDiscoverConcurrencyLimit(t *testing.T) {
	const limit, n = 2, 6
	var inFlight, peak atomic.Int32
	started := make(chan struct{}, n)
	release := make(chan struct{})
	ms := make([]Member, 0, n)
	for i := range n {
		f := newFake(localInfo(string(rune('a' + i))))
		f.discover = func(context.Context) error {
			cur := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				p := peak.Load()
				if cur <= p || peak.CompareAndSwap(p, cur) {
					break
				}
			}
			started <- struct{}{}
			<-release
			if f.info.Key == "c" {
				return errBoom
			}
			return nil
		}
		ms = append(ms, f.member())
	}
	c := newClientWith(t, ClientOptions{DiscoverConcurrency: limit}, ms...)
	done := make(chan error, 1)
	go func() { done <- c.Discover(context.Background()) }()
	for range limit {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("discovery did not start")
		}
	}
	select {
	case <-started:
		t.Fatalf("more than %d discoveries at once", limit)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	err := <-done
	if err == nil || err.Error() != "c: boom" {
		t.Errorf("Discover = %v, want c: boom", err)
	}
	if p := peak.Load(); p != limit {
		t.Errorf("peak = %d, want %d", p, limit)
	}
}

func TestDiscoverDefaultConcurrency(t *testing.T) {
	c := newTestClient(t)
	if c.discN != defaultDiscoverConcurrency {
		t.Errorf("discN = %d", c.discN)
	}
	if err := c.Discover(context.Background()); err != nil {
		t.Errorf("no members: %v", err)
	}
}

func branchHit(ref string) proto.SearchHit {
	return proto.SearchHit{Source: proto.SearchLocal, Kind: model.KindBranch, Ref: ref, Query: ref, ItemID: model.BranchItemID(ref)}
}

func prHit(n int) proto.SearchHit {
	return proto.SearchHit{Source: proto.SearchOpen, Kind: model.KindPR, Number: n, Ref: "feat/pr" + strconv.Itoa(n), Query: "#" + strconv.Itoa(n), ItemID: model.PRItemID(n)}
}

func hitQueries(r proto.SearchResult) []string {
	var out []string
	for _, h := range r.Hits {
		out = append(out, h.Query+" "+h.ItemID+" "+h.Repo)
	}
	return out
}

func TestSearch(t *testing.T) {
	ctx := context.Background()
	newPair := func() (*fakeMember, *fakeMember, *Client) {
		a := newFake(ghInfo("a", "acme/a"))
		a.hits = map[proto.SearchSource][]proto.SearchHit{
			proto.SearchLocal: {branchHit("feat/x")},
			proto.SearchOpen:  {prHit(12)},
		}
		a.hits[proto.SearchLocal][0].Listed = true
		b := newFake(ghInfo("b", "acme/b"))
		b.hits = map[proto.SearchSource][]proto.SearchHit{
			proto.SearchLocal: {branchHit("feat/x")},
			proto.SearchOpen:  {prHit(12)},
		}
		b.notes = map[proto.SearchSource]string{proto.SearchOpen: "GitHub search failed: rate limited"}
		return a, b, newTestClient(t, b, a)
	}

	t.Run("fan out in workspace order", func(t *testing.T) {
		_, _, c := newPair()
		r, err := c.Search(ctx, proto.SearchParams{Query: "feat", Source: proto.SearchLocal, Limit: 5})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"a:feat/x a::branch:feat/x a", "b:feat/x b::branch:feat/x b"}
		if got := hitQueries(r); !slices.Equal(got, want) {
			t.Errorf("hits = %v, want %v", got, want)
		}
		if !r.Hits[0].Listed || r.Hits[1].Listed || r.Query != "feat" || r.Source != proto.SearchLocal {
			t.Errorf("result = %+v", r)
		}
	})
	t.Run("same PR number in two repos", func(t *testing.T) {
		a, b, c := newPair()
		r, err := c.Search(ctx, proto.SearchParams{Query: "#12", Source: proto.SearchOpen})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"a#12 a::pr:12 a", "b#12 b::pr:12 b"}
		if got := hitQueries(r); !slices.Equal(got, want) {
			t.Errorf("hits = %v, want %v", got, want)
		}
		if r.Note != "acme/b: GitHub search failed: rate limited" {
			t.Errorf("note = %q", r.Note)
		}
		for _, f := range []*fakeMember{a, b} {
			if p := f.last(t, "Search").(proto.SearchParams); p.Query != "#12" || p.Limit != 0 || p.Repo != "" {
				t.Errorf("%s got %+v", f.info.Key, p)
			}
		}
	})
	t.Run("repo filter", func(t *testing.T) {
		a, b, c := newPair()
		r, err := c.Search(ctx, proto.SearchParams{Query: "feat", Source: proto.SearchLocal, Repo: "b"})
		if err != nil || len(r.Hits) != 1 || r.Hits[0].Repo != "b" {
			t.Fatalf("Search = %+v, %v", r, err)
		}
		if len(a.argsOf("Search")) != 0 {
			t.Error("filtered-out repo searched")
		}
		// A qualifier naming the filtered repo is stripped.
		if _, err := c.Search(ctx, proto.SearchParams{Query: "acme/b#12", Source: proto.SearchOpen, Repo: "b"}); err != nil {
			t.Fatal(err)
		}
		if p := b.last(t, "Search").(proto.SearchParams); p.Query != "#12" {
			t.Errorf("query = %q", p.Query)
		}
		// A qualifier naming another repo wins over the filter: that repo is
		// searched with the local part.
		r, err = c.Search(ctx, proto.SearchParams{Query: "a:feat", Source: proto.SearchLocal, Repo: "b"})
		if err != nil || len(r.Hits) != 1 || r.Hits[0].Repo != "a" || r.Hits[0].Query != "a:feat/x" {
			t.Fatalf("qualified search under another filter = %+v, %v", r, err)
		}
		if p := a.last(t, "Search").(proto.SearchParams); p.Query != "feat" {
			t.Errorf("a searched for %q, want the local part", p.Query)
		}
		if _, err := c.Search(ctx, proto.SearchParams{Query: "x", Source: proto.SearchLocal, Repo: "nope"}); err == nil {
			t.Error("unknown repo filter: no error")
		}
	})
	t.Run("qualified query", func(t *testing.T) {
		a, b, c := newPair()
		r, err := c.Search(ctx, proto.SearchParams{Query: "b:feat", Source: proto.SearchLocal})
		if err != nil || len(r.Hits) != 1 || r.Hits[0].Query != "b:feat/x" {
			t.Fatalf("Search = %+v, %v", r, err)
		}
		if len(a.argsOf("Search")) != 0 || b.last(t, "Search").(proto.SearchParams).Query != "feat" {
			t.Error("qualified query not routed with its local part")
		}
		// An unresolvable qualifier searches everything with the whole query.
		if _, err := c.Search(ctx, proto.SearchParams{Query: "zzz:feat", Source: proto.SearchLocal}); err != nil {
			t.Fatal(err)
		}
		if a.last(t, "Search").(proto.SearchParams).Query != "zzz:feat" {
			t.Error("unresolvable qualifier was stripped")
		}
	})
	t.Run("GitHub-less repos skipped", func(t *testing.T) {
		a := newFake(ghInfo("a", "acme/a"))
		a.hits = map[proto.SearchSource][]proto.SearchHit{proto.SearchClosed: {prHit(3)}}
		b := newFake(localInfo("b"))
		b.notes = map[proto.SearchSource]string{proto.SearchClosed: "GitHub unavailable"}
		c := newTestClient(t, a, b)
		r, err := c.Search(ctx, proto.SearchParams{Query: "!fix", Source: proto.SearchClosed})
		if err != nil || len(r.Hits) != 1 || r.Note != "" {
			t.Fatalf("Search = %+v, %v", r, err)
		}
		if len(b.argsOf("Search")) != 0 {
			t.Error("GitHub-less repo searched")
		}
		if _, err := c.Search(ctx, proto.SearchParams{Query: "x", Source: proto.SearchLocal}); err != nil || len(b.argsOf("Search")) != 1 {
			t.Errorf("local search skipped b: %v", err)
		}
	})
	t.Run("no GitHub anywhere", func(t *testing.T) {
		c := newTestClient(t, newFake(localInfo("a")), newFake(localInfo("b")))
		r, err := c.Search(ctx, proto.SearchParams{Query: "x", Source: proto.SearchOpen})
		if err != nil || r.Note != noteNoGitHub || len(r.Hits) != 0 {
			t.Errorf("Search = %+v, %v", r, err)
		}
		r, _ = c.Search(ctx, proto.SearchParams{Query: "a#3", Source: proto.SearchOpen})
		if r.Note != "a: GitHub unavailable" {
			t.Errorf("note = %q", r.Note)
		}
	})
	t.Run("live status wins", func(t *testing.T) {
		a := newFake(ghInfo("a", "acme/a"))
		a.live = proto.RepoInfo{Status: proto.RepoGitHubDisabled}
		c := newTestClient(t, a)
		if r, _ := c.Search(ctx, proto.SearchParams{Query: "x", Source: proto.SearchOpen}); len(a.argsOf("Search")) != 0 || r.Note == "" {
			t.Errorf("disabled repo searched: %+v", r)
		}
	})
	t.Run("member failures", func(t *testing.T) {
		a, b, c := newPair()
		a.searchErr = errBoom
		r, err := c.Search(ctx, proto.SearchParams{Query: "feat", Source: proto.SearchLocal})
		if err != nil || len(r.Hits) != 1 || r.Note != "acme/a: boom" {
			t.Errorf("one failed: %+v, %v", r, err)
		}
		b.searchErr = errBoom
		if _, err := c.Search(ctx, proto.SearchParams{Query: "feat", Source: proto.SearchLocal}); err == nil {
			t.Error("all failed: no error")
		}
	})
}

func TestAddAndPreview(t *testing.T) {
	ctx := context.Background()
	setup := func() (*fakeMember, *fakeMember, *fakeMember, *Client) {
		a := newFake(ghInfo("a", "acme/a"))
		b := newFake(ghInfo("svc/b", "acme/billing-svc"))
		n := newFake(localInfo("notes"))
		return a, b, n, newTestClient(t, a, b, n)
	}
	t.Run("qualified", func(t *testing.T) {
		a, b, _, c := setup()
		it, err := c.Add(ctx, "billing-svc#12")
		if err != nil || it.ID != "svc/b::pr:12" || it.Repo != "svc/b" {
			t.Fatalf("Add = %+v, %v", it, err)
		}
		if b.last(t, "Add") != "#12" || len(a.argsOf("Add")) != 0 || len(a.argsOf("Search")) != 0 {
			t.Error("qualified add not routed directly")
		}
		it, err = c.Preview(ctx, "notes:feat/x")
		if err != nil || it.ID != "notes::branch:feat/x" || it.Source != model.SourcePreview {
			t.Fatalf("Preview = %+v, %v", it, err)
		}
	})
	t.Run("unqualified PR in one repo", func(t *testing.T) {
		a, b, n, c := setup()
		b.hits = map[proto.SearchSource][]proto.SearchHit{proto.SearchOpen: {prHit(12)}}
		a.hits = map[proto.SearchSource][]proto.SearchHit{proto.SearchOpen: {prHit(13)}}
		it, err := c.Add(ctx, "12")
		if err != nil || it.ID != "svc/b::pr:12" {
			t.Fatalf("Add = %+v, %v", it, err)
		}
		if p := a.last(t, "Search").(proto.SearchParams); p.Source != proto.SearchOpen || p.Query != "#12" {
			t.Errorf("candidate search = %+v", p)
		}
		if len(n.argsOf("Search")) != 0 {
			t.Error("GitHub-less repo asked for a PR")
		}
		if b.last(t, "Add") != "12" {
			t.Errorf("added %v", b.last(t, "Add"))
		}
	})
	t.Run("unqualified ref in one repo", func(t *testing.T) {
		a, _, n, c := setup()
		a.hits = map[proto.SearchSource][]proto.SearchHit{proto.SearchLocal: {branchHit("feat/x-2")}}
		n.hits = map[proto.SearchSource][]proto.SearchHit{proto.SearchLocal: {branchHit("feat/x-2"), branchHit("feat/x")}}
		it, err := c.Preview(ctx, "feat/x")
		if err != nil || it.ID != "notes::branch:feat/x" {
			t.Fatalf("Preview = %+v, %v", it, err)
		}
		if p := a.last(t, "Search").(proto.SearchParams); p.Source != proto.SearchLocal || p.Limit != refCandidateLimit {
			t.Errorf("candidate search = %+v", p)
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		a, b, _, c := setup()
		a.hits = map[proto.SearchSource][]proto.SearchHit{proto.SearchOpen: {prHit(12)}, proto.SearchLocal: {branchHit("main2")}}
		b.hits = map[proto.SearchSource][]proto.SearchHit{proto.SearchOpen: {prHit(12)}, proto.SearchLocal: {branchHit("main2")}}
		_, err := c.Add(ctx, "#12")
		if err == nil || err.Error() != `workspace: ambiguous "#12": qualify it, e.g. a#12 or svc/b#12` {
			t.Errorf("Add = %v", err)
		}
		_, err = c.Preview(ctx, "main2")
		if err == nil || err.Error() != `workspace: ambiguous "main2": qualify it, e.g. a:main2 or svc/b:main2` {
			t.Errorf("Preview = %v", err)
		}
	})
	t.Run("none", func(t *testing.T) {
		_, _, _, c := setup()
		if _, err := c.Add(ctx, "#12"); err == nil || err.Error() != `workspace: no repo has "#12"` {
			t.Errorf("Add = %v", err)
		}
		// An unresolvable qualifier is an unqualified query.
		if _, err := c.Add(ctx, "zzz:feat"); err == nil || err.Error() != `workspace: no repo has "zzz:feat"` {
			t.Errorf("Add = %v", err)
		}
	})
	t.Run("single repo needs no search", func(t *testing.T) {
		f := newFake(localInfo("only"))
		c := newTestClient(t, f)
		it, err := c.Add(ctx, " fix login ")
		if err != nil || it.Repo != "only" || f.last(t, "Add") != "fix login" || len(f.argsOf("Search")) != 0 {
			t.Errorf("Add = %+v, %v", it, err)
		}
	})
	t.Run("one preview per workspace", func(t *testing.T) {
		a, b, n, c := setup()
		if _, err := c.Preview(ctx, "a#12"); err != nil {
			t.Fatal(err)
		}
		if len(a.argsOf("RemovePreviews")) != 0 || len(b.argsOf("RemovePreviews")) != 1 || len(n.argsOf("RemovePreviews")) != 1 {
			t.Errorf("after a preview in a: RemovePreviews a=%d b=%d notes=%d, want 0 1 1",
				len(a.argsOf("RemovePreviews")), len(b.argsOf("RemovePreviews")), len(n.argsOf("RemovePreviews")))
		}
		if _, err := c.Preview(ctx, "svc/b#7"); err != nil {
			t.Fatal(err)
		}
		if len(a.argsOf("RemovePreviews")) != 1 || len(b.argsOf("RemovePreviews")) != 1 {
			t.Errorf("a preview in b did not drop a's previews")
		}
		// A failed preview drops nothing.
		before := len(a.argsOf("RemovePreviews"))
		if _, err := c.Preview(ctx, "#99"); err == nil {
			t.Fatal("preview of a PR no repo has: no error")
		}
		if len(a.argsOf("RemovePreviews")) != before {
			t.Error("a failed preview dropped other previews")
		}
	})
	t.Run("a branch named like a qualifier", func(t *testing.T) {
		a, _, _, c := setup()
		a.hits = map[proto.SearchSource][]proto.SearchHit{proto.SearchLocal: {branchHit("feat#2")}}
		it, err := c.Add(ctx, "feat#2")
		if err != nil || it.ID != "a::branch:feat#2" {
			t.Errorf("Add = %+v, %v", it, err)
		}
	})
}

func TestStopResumeAll(t *testing.T) {
	a := newFake(localInfo("a"))
	a.stopped = []string{"pr:2", "pr:1"}
	a.resumed = []string{"pr:2"}
	b := newFake(localInfo("b"))
	b.stopped = []string{"branch:x"}
	c := newTestClient(t, b, a)
	ctx := context.Background()
	r, err := c.Stop(ctx, proto.StopParams{All: true, ItemID: "ignored"})
	if err != nil || !slices.Equal(r.Stopped, []string{"a::pr:1", "a::pr:2", "b::branch:x"}) {
		t.Errorf("Stop = %v, %v", r, err)
	}
	for _, f := range []*fakeMember{a, b} {
		if p := f.last(t, "Stop").(proto.StopParams); !p.All || p.ItemID != "" {
			t.Errorf("%s got %+v", f.info.Key, p)
		}
	}
	rr, err := c.Resume(ctx, proto.ResumeParams{All: true})
	if err != nil || !slices.Equal(rr.Resumed, []string{"a::pr:2"}) {
		t.Errorf("Resume = %v, %v", rr, err)
	}
	b.stopErr = errBoom
	r, err = c.Stop(ctx, proto.StopParams{All: true})
	if err == nil || err.Error() != "b: boom" || len(r.Stopped) != 3 {
		t.Errorf("Stop with a failure = %v, %v", r, err)
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	a := newFake(ghInfo("a", "acme/a"))
	a.applied = "concurrency 2"
	a.warnings = []string{"global prt.toml is shadowed"}
	b := newFake(localInfo("b"))
	b.applied = "nothing changed"
	b.warnings = []string{"unknown key x"}
	p := newFake(localInfo("p"))
	c := newClientWith(t, ClientOptions{}, a.member(), b.member(), p.plainMember())

	info, err := c.Settings(ctx, proto.SettingsParams{Repo: "b", Ensure: "routing"})
	if err != nil || info.Repo != "b" || info.RepoLabel != "b" || info.Workspace != "/ws" || info.Source != "b/prt.toml" {
		t.Errorf("Settings(b) = %+v, %v", info, err)
	}
	if got := b.last(t, "Settings").(proto.SettingsParams); got.Repo != "" || got.Ensure != "routing" {
		t.Errorf("member params = %+v", got)
	}
	if info, _ := c.Settings(ctx, proto.SettingsParams{}); info.Repo != "a" || info.RepoLabel != "acme/a" {
		t.Errorf("Settings() = %+v", info)
	}
	if _, err := c.Settings(ctx, proto.SettingsParams{Repo: "nope"}); err == nil {
		t.Error("unknown repo: no error")
	}

	// Repo scope: only that repo.
	info, err = c.SaveSettings(ctx, proto.SaveSettingsParams{Repo: "b", Values: proto.SettingsValues{Role: "be"}})
	if err != nil || info.Repo != "b" || info.Applied != "nothing changed" {
		t.Errorf("repo save = %+v, %v", info, err)
	}
	if got := b.last(t, "SaveSettings").(proto.SaveSettingsParams); got.Repo != "" || got.Values.Role != "be" {
		t.Errorf("member params = %+v", got)
	}
	if len(a.argsOf("ReloadSettings")) != 0 || len(a.argsOf("SaveSettings")) != 0 {
		t.Error("repo save touched another repo")
	}

	// Global scope: saved through a, reloaded everywhere else.
	info, err = c.SaveSettings(ctx, proto.SaveSettingsParams{Scope: proto.ScopeGlobal, Repo: "acme/a"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "acme/a: concurrency 2; b: nothing changed; p: " + noteNoReload; info.Applied != want {
		t.Errorf("applied = %q, want %q", info.Applied, want)
	}
	if want := []string{"global prt.toml is shadowed", "b: unknown key x"}; !slices.Equal(info.Warnings, want) {
		t.Errorf("warnings = %q, want %q", info.Warnings, want)
	}
	if info.Repo != "a" || len(b.argsOf("ReloadSettings")) != 1 || len(a.argsOf("ReloadSettings")) != 0 {
		t.Errorf("global save: %+v; reloads a=%d b=%d", info, len(a.argsOf("ReloadSettings")), len(b.argsOf("ReloadSettings")))
	}

	b.reloadErr = errBoom
	info, err = c.SaveSettings(ctx, proto.SaveSettingsParams{Scope: proto.ScopeGlobal})
	if err == nil || err.Error() != "b: boom" || !strings.Contains(info.Applied, "b: reload failed") {
		t.Errorf("reload failure: %+v, %v", info, err)
	}

	a.saveErr = errBoom
	before := len(b.argsOf("ReloadSettings"))
	if _, err := c.SaveSettings(ctx, proto.SaveSettingsParams{Scope: proto.ScopeGlobal}); !errors.Is(err, errBoom) {
		t.Errorf("save failure: %v", err)
	}
	if len(b.argsOf("ReloadSettings")) != before {
		t.Error("reloaded after a failed save")
	}
}

func TestRoutingByRepo(t *testing.T) {
	a, b := newFake(localInfo("a")), newFake(localInfo("b"))
	c := newTestClient(t, a, b)
	ctx := context.Background()
	ri, err := c.Routing(ctx, proto.RoutingParams{Repo: "b", Ensure: true})
	if err != nil || ri.Path != "b/prt.routing.toml" {
		t.Errorf("Routing(b) = %+v, %v", ri, err)
	}
	if p := b.last(t, "Routing").(proto.RoutingParams); p.Repo != "" || !p.Ensure {
		t.Errorf("member params = %+v", p)
	}
	if ri, _ := c.Routing(ctx, proto.RoutingParams{}); ri.Path != "a/prt.routing.toml" {
		t.Errorf("Routing() = %+v", ri)
	}
	if _, err := c.Routing(ctx, proto.RoutingParams{Repo: "zzz"}); err == nil {
		t.Error("unknown repo: no error")
	}
}

// recv reads one event or fails after a timeout.
func recv(t *testing.T, ch <-chan proto.Event) (proto.Event, bool) {
	t.Helper()
	select {
	case e, ok := <-ch:
		return e, ok
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an event")
		return proto.Event{}, false
	}
}

func TestEvents(t *testing.T) {
	a, b := newFake(localInfo("a")), newFake(localInfo("b"))
	c := newTestClient(t, a, b)
	it := model.Item{ID: "pr:1", Title: "x"}
	rl := model.RunLog{ItemID: "pr:1", Stage: model.StageJev}
	a.events <- proto.Event{Type: proto.EventStage, ItemID: "pr:1", Item: &it, RunLog: &rl}
	e, ok := recv(t, c.Events())
	if !ok || e.Repo != "a" || e.ItemID != "a::pr:1" || e.Item.ID != "a::pr:1" || e.Item.Repo != "a" || e.RunLog.ItemID != "a::pr:1" {
		t.Errorf("event = %+v", e)
	}
	if it.ID != "pr:1" || rl.ItemID != "pr:1" {
		t.Error("member's event modified")
	}
	b.events <- proto.Event{Type: proto.EventSettings}
	if e, _ := recv(t, c.Events()); e.Repo != "b" || e.Type != proto.EventSettings || e.ItemID != "" {
		t.Errorf("event = %+v", e)
	}

	// The merged stream ends once every member's stream has ended.
	a.closeEvents()
	b.closeEvents()
	if _, ok := recv(t, c.Events()); ok {
		t.Error("stream still open after every member's closed")
	}
}

func TestEventsCloseWithOpenMembers(t *testing.T) {
	a := newFake(localInfo("a"))
	a.keepEvents = true
	c := New("/ws", []Member{a.member()}, ClientOptions{})
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := recv(t, c.Events()); ok {
		t.Error("stream open after Close")
	}
}

func TestEventsDropWhenFull(t *testing.T) {
	a := newFake(localInfo("a"))
	a.events = make(chan proto.Event, eventBuffer+10)
	c := newTestClient(t, a)
	for range eventBuffer + 10 {
		a.events <- proto.Event{Type: proto.EventLog}
	}
	a.closeEvents()
	// Nobody reads the merged stream: the forwarder must still get through
	// every event (dropping the overflow) and end.
	forwarded := make(chan struct{})
	go func() {
		c.fwd.Wait()
		close(forwarded)
	}()
	select {
	case <-forwarded:
	case <-time.After(5 * time.Second):
		t.Fatal("forwarder blocked on a full merged stream")
	}
	n := 0
	for range c.Events() {
		n++
	}
	if n != eventBuffer {
		t.Errorf("received %d events, want %d", n, eventBuffer)
	}
}

func TestEventsNoMembers(t *testing.T) {
	c := New("/ws", nil, ClientOptions{})
	select {
	case <-c.Events():
		t.Fatal("stream of an empty workspace closed before Close")
	case <-time.After(20 * time.Millisecond):
	}
	_ = c.Close()
	if _, ok := recv(t, c.Events()); ok {
		t.Error("stream open after Close")
	}
}

func TestCloseIdempotent(t *testing.T) {
	a, b := newFake(localInfo("a")), newFake(localInfo("b"))
	b.closeErr = errBoom
	c := New("/ws", []Member{a.member(), b.member()}, ClientOptions{})
	err1 := c.Close()
	err2 := c.Close()
	if err1 == nil || err1.Error() != "b: boom" || !errors.Is(err2, errBoom) {
		t.Errorf("Close = %v, then %v", err1, err2)
	}
	if a.closes.Load() != 1 || b.closes.Load() != 1 {
		t.Errorf("member closes = %d, %d", a.closes.Load(), b.closes.Load())
	}
}

func TestWorkspaceInfo(t *testing.T) {
	ledger := daemon.NewLedger()
	ledger.Add("a", 0.5)
	ledger.Add("b", 0.25)
	a := newFake(ghInfo("a", "acme/a"))
	a.live = proto.RepoInfo{Role: "backend", SpentUSD: 0.5, Root: "/live/a"}
	b := newFake(localInfo("b"))
	b.info.Root = "/ws/b"
	notes := []string{"skipped worktree c (same repository as b)"}
	c := newClientWith(t, ClientOptions{Pool: daemon.NewPool(3), Ledger: ledger, Notes: notes}, b.plainMember(), a.member())
	notes[0] = "changed by the caller"
	ws, err := c.Workspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !ws.Multi || ws.Root != "/ws" || ws.Concurrency != 3 || ws.SpentUSD != 0.75 || !slices.Equal(ws.Notes, []string{"skipped worktree c (same repository as b)"}) {
		t.Errorf("workspace = %+v", ws)
	}
	want := []proto.RepoInfo{
		{Key: "a", Label: "acme/a", Root: "/live/a", GHRepo: "acme/a", Status: proto.RepoGitHub, Role: "backend", SpentUSD: 0.5},
		{Key: "b", Label: "b", Root: "/ws/b", Status: proto.RepoNoRemote, SpentUSD: 0.25},
	}
	if !slices.Equal(ws.Repos, want) {
		t.Errorf("repos = %+v\nwant    %+v", ws.Repos, want)
	}
	if keys := memberKeys(c.Members()); !slices.Equal(keys, []string{"a", "b"}) {
		t.Errorf("members = %v", keys)
	}

	empty := newTestClient(t, newFake(localInfo("x")))
	if ws, _ := empty.Workspace(context.Background()); ws.Concurrency != 0 || ws.SpentUSD != 0 || len(ws.Repos) != 1 {
		t.Errorf("no pool or ledger: %+v", ws)
	}
}

// A repo that could not be opened is listed among the repos (in key order,
// RepoError with its reason) but has no client: routing to it fails.
func TestWorkspaceListsUnopenedRepos(t *testing.T) {
	a := newFake(localInfo("a"))
	c := newFake(localInfo("c"))
	bad := proto.RepoInfo{Key: "b", Label: "b", Root: "/ws/b", Status: proto.RepoError, Note: "config: prt.toml: bad"}
	cl := newClientWith(t, ClientOptions{Unopened: []proto.RepoInfo{bad}}, c.member(), a.member())
	ws, err := cl.Workspace(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range ws.Repos {
		keys = append(keys, r.Key)
	}
	if !slices.Equal(keys, []string{"a", "b", "c"}) || ws.Repos[1] != bad {
		t.Errorf("repos = %+v", ws.Repos)
	}
	if _, err := cl.Detail(context.Background(), "b::pr:1"); err == nil {
		t.Error("an unopened repo was routed to")
	}
	if keys := memberKeys(cl.Members()); !slices.Equal(keys, []string{"a", "c"}) {
		t.Errorf("members = %v", keys)
	}
}

func TestRunSyncAndAnalysePending(t *testing.T) {
	a := newFake(localInfo("a"))
	a.pendingErr = errBoom
	b := newFake(localInfo("b"))
	p := newFake(localInfo("p"))
	c := newClientWith(t, ClientOptions{}, a.member(), b.member(), p.plainMember())
	ctx := context.Background()
	if err := c.RunSync(ctx, "b::branch:feat/x"); err != nil || b.last(t, "RunSync") != "branch:feat/x" {
		t.Errorf("RunSync = %v", err)
	}
	if err := c.RunSync(ctx, "p::pr:1"); err == nil || !strings.Contains(err.Error(), "cannot run") {
		t.Errorf("RunSync on a plain member = %v", err)
	}
	err := c.AnalysePending(ctx)
	if err == nil || err.Error() != "a: boom" {
		t.Errorf("AnalysePending = %v", err)
	}
	if len(b.argsOf("AnalysePending")) != 1 || len(p.argsOf("AnalysePending")) != 0 {
		t.Error("AnalysePending not fanned out to the members that have it")
	}
}
