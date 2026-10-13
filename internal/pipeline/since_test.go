package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/comments"
	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
	"github.com/fredricw/prt/internal/store"
	"github.com/fredricw/prt/internal/testutil"
)

// Fixture facts used below (testdata/fixtures/prs.json, history/).
const (
	v1SHA     = "e661461bccca9ae9395ab9e74da9836914d81a0c" // #101's viewerReviewedSha
	cachePath = "web/src/hooks/useProfileCache.ts"
	ttlID     = "gh-5001" // viewer: TTL is an hour (fixed by the push)
	catchID   = "gh-5003" // viewer: errors swallowed (not fixed)
)

// sinceEnv runs fixture PR #101 over the generated fixture repo with the
// offline GitHub fixture (viewer "fredric", three review threads) and a
// fake Jev.
type sinceEnv struct {
	st   store.Store
	git  gitx.Git
	gh   *gh.Fake
	jev  *jev.Fake
	item model.Item
	head string
}

func newSinceEnv(t *testing.T) *sinceEnv {
	t.Helper()
	ctx := context.Background()
	g, err := gitx.Open(ctx, testutil.FixtureRepo(t))
	if err != nil {
		t.Fatal(err)
	}
	fake, err := gh.LoadFixture(testutil.FixturePRs(t))
	if err != nil {
		t.Fatal(err)
	}
	pr := fake.PRs[101]
	e := &sinceEnv{st: newStore(t), git: g, gh: fake, jev: &jev.Fake{Answer: addressedAnswer}, head: pr.HeadSHA,
		item: model.Item{ID: model.PRItemID(101), Kind: model.KindPR, Number: 101, Ref: pr.HeadRef, BaseRef: pr.BaseRef,
			HeadSHA: pr.HeadSHA, Title: pr.Title, Body: pr.Body, Author: pr.Author, CreatedAt: pr.CreatedAt}}
	return e
}

// pipeline stores the item and builds a Pipeline over the env.
func (e *sinceEnv) pipeline(t *testing.T, mutate ...func(*Deps)) *Pipeline {
	t.Helper()
	if err := e.st.UpsertItem(context.Background(), e.item); err != nil {
		t.Fatal(err)
	}
	d := Deps{Git: e.git, GH: e.gh, Jev: e.jev, Store: e.st, Config: config.Defaults(), Questions: questions.Default(),
		Now: func() time.Time { return labelNow }}
	for _, m := range mutate {
		m(&d)
	}
	return New(d)
}

// run runs the item and returns its stored aggregate.
func (e *sinceEnv) run(t *testing.T, p *Pipeline) model.ItemAgg {
	t.Helper()
	ctx := context.Background()
	if err := p.Run(ctx, e.item); err != nil {
		t.Fatal(err)
	}
	agg, ok, err := e.st.Agg(ctx, e.item.ID)
	if err != nil || !ok {
		t.Fatalf("agg ok=%v err=%v", ok, err)
	}
	return agg
}

// addressedAnswer is the fake Jev: comment_addressed is "yes" for the TTL
// comment and "no" for anything else; other questions get the defaults.
func addressedAnswer(name string, q jev.Question, state any) jev.Answer {
	if name != model.QCommentAddressed {
		return jev.DefaultAnswer(name, q, state)
	}
	p := 0.1
	if strings.Contains(stateJSON(state), "An hour is far longer") {
		p = 0.9
	}
	return jev.Answer{Type: jev.Boolean, Probability: &p}
}

func stateJSON(state any) string {
	b, err := json.Marshal(state)
	if err != nil {
		return ""
	}
	return string(b)
}

// addressedRequests returns the comment_addressed requests e's Jev saw.
func (e *sinceEnv) addressedRequests() []jev.Request {
	var out []jev.Request
	for _, r := range e.jev.Calls {
		if _, ok := r.Questions[model.QCommentAddressed]; ok {
			out = append(out, r)
		}
	}
	return out
}

// changedSymbols renders the changed units as "path symbol".
func changedSymbols(t *testing.T, st store.Store, itemID string, ids []string) []string {
	t.Helper()
	us, err := st.Units(context.Background(), itemID)
	if err != nil {
		t.Fatal(err)
	}
	byID := unitsByID(us)
	var out []string
	for _, id := range ids {
		u := byID[id]
		out = append(out, u.Path+" "+u.Symbol)
	}
	return out
}

// sinceRows renders the Since comment rows as "id:line known/addressed".
func sinceRows(s *model.SinceLooked) []string {
	var out []string
	for _, r := range s.Comments {
		out = append(out, fmt.Sprintf("%s:%d %v/%v", r.CommentID, r.Line, r.Known, r.Addressed))
	}
	return out
}

// The viewer reviewed v1: the block compares v1 with the head, lists the
// two units the push changed and answers both of the viewer's comments; a
// re-run reuses the stored answers.
func TestSinceFromViewerReview(t *testing.T) {
	e := newSinceEnv(t)
	e.item.ViewerReviewedSHA = v1SHA
	p := e.pipeline(t)
	agg := e.run(t, p)
	s := agg.Since
	if s == nil {
		t.Fatal("no Since block")
	}
	if s.BaseSHA != v1SHA || s.HeadSHA != e.head || s.Source != model.SinceReviewed || s.Note != "" {
		t.Errorf("since = %+v", *s)
	}
	syms := changedSymbols(t, e.st, e.item.ID, s.ChangedUnits)
	if len(syms) != 2 || !slices.Contains(syms, cachePath+" clearProfileCache") {
		t.Errorf("changed units = %v, want the TTL constant and clearProfileCache", syms)
	}
	for _, sym := range syms {
		if !strings.HasPrefix(sym, cachePath+" ") {
			t.Errorf("changed unit %q outside %s", sym, cachePath)
		}
	}
	if got, want := sinceRows(s), []string{ttlID + ":6 true/true", catchID + ":75 true/false"}; !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
	if r := s.Comments[0]; r.Path != cachePath || r.UnitID == "" || r.Resolved || !strings.HasPrefix(r.Excerpt, "An hour is far longer") || r.P != 0.9 {
		t.Errorf("TTL row = %+v", r)
	}
	reqs := e.addressedRequests()
	if len(reqs) != 2 {
		t.Fatalf("%d comment_addressed requests, want 2", len(reqs))
	}
	ttl := stateJSON(reqs[0].State) + stateJSON(reqs[1].State)
	// The code the comment was made on (v1: one hour) and the push's diff.
	for _, want := range []string{"6: export const PROFILE_TTL_MS = 60 * 60 * 1000;", "-export const PROFILE_TTL_MS = 60 * 60 * 1000;", "+export const PROFILE_TTL_MS = 5 * 60 * 1000;"} {
		if !strings.Contains(ttl, want) {
			t.Errorf("requests lack %q:\n%s", want, ttl)
		}
	}
	if agg.Review == nil || agg.Review.JevCalls < 2 {
		t.Errorf("review stats %+v, want the comment_addressed calls counted", agg.Review)
	}

	b, ok, err := e.st.CacheGet(context.Background(), nsSinceUnits, e.item.ID+"|"+v1SHA)
	var cached []string
	if err != nil || !ok || json.Unmarshal(b, &cached) != nil || len(cached) == 0 {
		t.Errorf("base units not cached: ok=%v err=%v %s", ok, err, b)
	}

	again := e.run(t, p)
	if n := len(e.addressedRequests()); n != 2 {
		t.Errorf("re-run asked comment_addressed again (%d requests)", n)
	}
	if !slices.Equal(sinceRows(again.Since), sinceRows(s)) || !slices.Equal(again.Since.ChangedUnits, s.ChangedUnits) {
		t.Errorf("re-run since = %+v, want %+v", again.Since, s)
	}
}

// A view mark wins over the review: looking at the head after v1 keeps v1
// as the base (PrevHeadSHA) with source viewed.
func TestSinceFromViewMark(t *testing.T) {
	e := newSinceEnv(t)
	p := e.pipeline(t)
	if err := e.st.PutViewMark(context.Background(), model.ViewMark{ItemID: e.item.ID, HeadSHA: e.head, PrevHeadSHA: v1SHA}); err != nil {
		t.Fatal(err)
	}
	s := e.run(t, p).Since
	if s == nil || s.BaseSHA != v1SHA || s.Source != model.SinceViewed || len(s.ChangedUnits) != 2 || len(s.Comments) != 2 {
		t.Fatalf("since = %+v", s)
	}
}

// Degraded blocks: Jev missing or down leaves the rows unknown with a note;
// a base head that is not local has no changed units and a note; no base
// means no block.
func TestSinceDegraded(t *testing.T) {
	down := &jev.Fake{Err: fmt.Errorf("%w: down", jev.ErrUnavailable)}
	cases := []struct {
		name     string
		reviewed string
		jc       jev.Client // nil: the env's fake
		noJev    bool
		nilSince bool
		note     string
		changed  int
		known    bool
	}{
		{name: "no Jev", reviewed: v1SHA, noJev: true, note: "labels unavailable", changed: 2},
		{name: "Jev unavailable", reviewed: v1SHA, jc: down, note: "labels unavailable", changed: 2},
		{name: "base not local", reviewed: "0123456789abcdef0123456789abcdef01234567", note: "base head 0123456 not available locally", known: true},
		{name: "no base", nilSince: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newSinceEnv(t)
			e.item.ViewerReviewedSHA = c.reviewed
			p := e.pipeline(t, func(d *Deps) {
				switch {
				case c.noJev:
					d.Jev = nil
				case c.jc != nil:
					d.Jev = c.jc
				}
			})
			s := e.run(t, p).Since
			if c.nilSince {
				if s != nil {
					t.Fatalf("since = %+v, want none", *s)
				}
				return
			}
			if s == nil {
				t.Fatal("no Since block")
			}
			if !strings.Contains(s.Note, c.note) || len(s.ChangedUnits) != c.changed || s.ChangedUnits == nil {
				t.Errorf("since note %q changed %v, want note %q and %d changed", s.Note, s.ChangedUnits, c.note, c.changed)
			}
			if len(s.Comments) != 2 {
				t.Fatalf("rows = %v", sinceRows(s))
			}
			for _, r := range s.Comments {
				if r.Known != c.known || (!c.known && (r.Addressed || r.P != 0)) {
					t.Errorf("row %+v, want known %v", r, c.known)
				}
			}
		})
	}
}

// emptyDiffGit answers Diff(base, head) with an empty diff, as if nothing
// changed between them (a push after the fix touching other files only).
type emptyDiffGit struct {
	gitx.Git
	base, head string
}

func (g emptyDiffGit) Diff(ctx context.Context, base, head string) (string, error) {
	if base == g.base && head == g.head {
		return "", nil
	}
	return g.Git.Diff(ctx, base, head)
}

// A comment fixed by a push before the Since base is judged against the
// diff from its own commit, not the (here empty) base→head diff
// (regression: the fix was invisible and the comment flipped to "not
// addressed").
func TestSinceAddressedDiffsFromCommentCommit(t *testing.T) {
	ctx := context.Background()
	e := newSinceEnv(t)
	e.item.ViewerReviewedSHA = v1SHA
	mb, err := e.git.MergeBase(ctx, gitx.CompareRef(ctx, e.git, e.item.BaseRef), e.head)
	if err != nil {
		t.Fatal(err)
	}
	// The TTL comment was made on the PR's base commit, before v1.
	for ti, th := range e.gh.ThreadMap[101] {
		for ci, c := range th.Comments {
			if c.ID == ttlID {
				e.gh.ThreadMap[101][ti].Comments[ci].CommitSHA = mb
			}
		}
	}
	const fix = "+export const PROFILE_TTL_MS = 5 * 60 * 1000;"
	e.jev.Answer = func(name string, q jev.Question, state any) jev.Answer {
		if name != model.QCommentAddressed {
			return jev.DefaultAnswer(name, q, state)
		}
		p := 0.1
		if strings.Contains(stateJSON(state), fix) {
			p = 0.9
		}
		return jev.Answer{Type: jev.Boolean, Probability: &p}
	}
	p := e.pipeline(t, func(d *Deps) { d.Git = emptyDiffGit{Git: e.git, base: v1SHA, head: e.head} })
	s := e.run(t, p).Since
	if s == nil || len(s.Comments) != 2 {
		t.Fatalf("since = %+v", s)
	}
	for _, r := range s.Comments {
		if r.CommentID == ttlID && (!r.Known || !r.Addressed) {
			t.Errorf("TTL row = %+v, want addressed (its commit's diff has the fix)", r)
		}
	}
}

// Every read-modify-write of stored comments (threads merge, re-anchoring,
// ai-draft sync) holds the host's comment lock for the item, so a run never
// writes a stale copy over a concurrent submit or edit (regression: the
// pipeline took no lock).
func TestCommentWritesHoldCommentLock(t *testing.T) {
	e := newSinceEnv(t)
	var mu sync.Mutex
	held := map[string]int{}
	p := e.pipeline(t, func(d *Deps) {
		d.CommentLock = func(id string) func() {
			mu.Lock()
			held[id]++
			mu.Unlock()
			return func() {}
		}
	})
	e.run(t, p)
	mu.Lock()
	defer mu.Unlock()
	if n := held[e.item.ID]; n < 3 || len(held) != 1 {
		t.Errorf("comment lock taken %v, want ≥ 3 times for %s (threads, re-anchor, ai-drafts)", held, e.item.ID)
	}
}

// When neither the base head nor the comment's commit is local the
// requests carry the comment's unit diff.
func TestSinceBaseNotLocalUsesUnitDiff(t *testing.T) {
	e := newSinceEnv(t)
	e.item.ViewerReviewedSHA = "0123456789abcdef0123456789abcdef01234567"
	for ti, th := range e.gh.ThreadMap[101] {
		for ci := range th.Comments {
			e.gh.ThreadMap[101][ti].Comments[ci].CommitSHA = "fedcba9876543210fedcba9876543210fedcba98"
		}
	}
	e.run(t, e.pipeline(t))
	reqs := e.addressedRequests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests", len(reqs))
	}
	for _, r := range reqs {
		if st := stateJSON(r.State); !strings.Contains(st, "PROFILE_TTL_MS") && !strings.Contains(st, "catch") {
			t.Errorf("request without the unit's diff: %s", st)
		}
	}
}

// Branches have no GitHub review: only a view mark gives a base.
func TestSinceBranchUsesViewMarkOnly(t *testing.T) {
	ctx := context.Background()
	e := newSinceEnv(t)
	e.item = model.Item{ID: model.BranchItemID(e.item.Ref), Kind: model.KindBranch, Ref: e.item.Ref, BaseRef: "main",
		HeadSHA: e.head, ViewerReviewedSHA: v1SHA}
	p := e.pipeline(t)
	if s := e.run(t, p).Since; s != nil {
		t.Fatalf("branch since from a review SHA: %+v", *s)
	}
	if err := e.st.PutViewMark(ctx, model.ViewMark{ItemID: e.item.ID, HeadSHA: v1SHA}); err != nil {
		t.Fatal(err)
	}
	s := e.run(t, p).Since
	if s == nil || s.BaseSHA != v1SHA || s.Source != model.SinceViewed || len(s.ChangedUnits) != 2 || len(s.Comments) != 0 {
		t.Fatalf("branch since = %+v", s)
	}
}

// A draft saved on the v1 head moves with clearProfileCache to the real
// head, as comments.Reanchor dictates.
func TestDraftReanchoredAfterPush(t *testing.T) {
	ctx := context.Background()
	e := newSinceEnv(t)
	headItem := e.item
	e.item.HeadSHA = v1SHA
	p := e.pipeline(t)
	e.run(t, p)
	v1Units, err := e.st.Units(ctx, e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	var clearV1 model.ChangeUnit
	for _, u := range v1Units {
		if u.Path == cachePath && u.Symbol == "clearProfileCache" {
			clearV1 = u
		}
	}
	if clearV1.ID == "" {
		t.Fatalf("no clearProfileCache unit at v1 in %d units", len(v1Units))
	}
	// The body line "cache.clear();" right after the signature.
	draft := comments.Anchor(model.Comment{ID: "d-1", ItemID: e.item.ID, Path: cachePath, Line: clearV1.Range.New.Start + 1,
		Side: model.SideNew, Body: "Also clear inflight?", Source: model.CommentUser, State: model.CommentDraft, CommitSHA: v1SHA}, v1Units)
	if draft.UnitID != clearV1.ID {
		t.Fatalf("draft anchored to %q, want %q", draft.UnitID, clearV1.ID)
	}
	if err := e.st.PutComment(ctx, draft); err != nil {
		t.Fatal(err)
	}

	e.item = headItem
	if err := e.st.UpsertItem(ctx, headItem); err != nil {
		t.Fatal(err)
	}
	e.run(t, p)
	headUnits, err := e.st.Units(ctx, e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := draft
	if ch := comments.ReanchorAt([]model.Comment{draft}, headUnits, headItem.HeadSHA); len(ch) == 1 {
		want = ch[0]
	}
	if want.CommitSHA != headItem.HeadSHA {
		t.Errorf("draft commit = %s, want the head its line now describes %s", want.CommitSHA, headItem.HeadSHA)
	}
	if it, err := e.st.Item(ctx, e.item.ID); err != nil || it.UnitsSHA != headItem.HeadSHA {
		t.Errorf("item units head = %q (%v), want %s", it.UnitsSHA, err, headItem.HeadSHA)
	}
	cs, err := e.st.Comments(ctx, e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got model.Comment
	for _, c := range cs {
		if c.ID == "d-1" {
			got = c
		}
	}
	if got != want {
		t.Errorf("stored draft\n got %+v\nwant %+v", got, want)
	}
	for _, u := range headUnits {
		if u.Path == cachePath && u.Symbol == "clearProfileCache" && (got.UnitID != u.ID || got.Outdated || got.Line != draft.Line) {
			t.Errorf("draft = %+v, want it on head unit %s line %d", got, u.ID, draft.Line)
		}
	}
}
