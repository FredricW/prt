package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
)

// comment returns the stored comment id of the env's item (ok false when
// absent).
func (e *labelEnv) comment(t *testing.T, id string) (model.Comment, bool) {
	t.Helper()
	cs, err := e.st.Comments(context.Background(), e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.ID == id {
			return c, true
		}
	}
	return model.Comment{}, false
}

// commentsBySource returns the ids of the env item's stored comments of
// one source, sorted.
func (e *labelEnv) commentsBySource(t *testing.T, src model.CommentSource) []string {
	t.Helper()
	cs, err := e.st.Comments(context.Background(), e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		if c.Source == src {
			out = append(out, c.ID)
		}
	}
	slices.Sort(out)
	return out
}

// Threads load on every run and merge: a later resolve shows, a removed
// thread's comments go, GitHub's copy replaces a local posted one, and a
// GitHub error keeps what is stored with a note on the fetch row.
func TestThreadsLoadAndMerge(t *testing.T) {
	ctx := context.Background()
	e := newLabelEnv(t)
	thread := func(id string, ghID int64, resolved bool) gh.Thread {
		return gh.Thread{ID: id, Path: "web/src/api/client.ts", Line: 2, Side: model.SideNew, Resolved: resolved,
			Comments: []model.Comment{{GHID: ghID, Author: "alice", Body: "Why?", CommitSHA: "h1"}}}
	}
	fake := &gh.Fake{Login: "me", ThreadMap: map[int][]gh.Thread{1: {thread("T1", 101, false), thread("T2", 102, false)}}}
	p := e.pipeline(&jev.Fake{}, func(d *Deps) { d.GH = fake })
	local := model.Comment{ID: "d-local", ItemID: e.item.ID, Path: "web/src/api/client.ts", Line: 2, Body: "Why?",
		Source: model.CommentUser, State: model.CommentPosted, GHID: 102}
	if err := e.st.PutComment(ctx, local); err != nil {
		t.Fatal(err)
	}

	e.run(t, p)
	if got := e.commentsBySource(t, model.CommentGitHub); !slices.Equal(got, []string{"gh-101", "gh-102"}) {
		t.Fatalf("github comments = %v", got)
	}
	if _, ok := e.comment(t, "d-local"); ok {
		t.Error("local posted copy kept next to GitHub's")
	}
	c, _ := e.comment(t, "gh-101")
	if c.State != model.CommentPosted || c.ThreadID != "T1" || c.Path != "web/src/api/client.ts" || c.Line != 2 || c.ItemID != e.item.ID || c.UnitID == "" {
		t.Errorf("gh-101 = %+v, want posted in T1, anchored to a unit", c)
	}
	if rl := e.stage(t, model.StageFetch); rl.Calls != 1 {
		t.Errorf("fetch row calls = %d, want the Threads call", rl.Calls)
	}

	fake.ThreadMap[1] = []gh.Thread{thread("T1", 101, true)}
	e.run(t, p) // a fetch-cache hit still reloads threads
	if c, _ := e.comment(t, "gh-101"); c.State != model.CommentResolved {
		t.Errorf("gh-101 state %q after resolve", c.State)
	}
	if _, ok := e.comment(t, "gh-102"); ok {
		t.Error("gh-102 kept after its thread went")
	}

	fake.Err = errors.New("boom")
	e.run(t, p)
	if rl := e.stage(t, model.StageFetch); !strings.Contains(rl.Note, "threads unavailable: boom") || rl.Status == model.StageFailed {
		t.Errorf("fetch row = %+v, want a soft note", rl)
	}
	if _, ok := e.comment(t, "gh-101"); !ok {
		t.Error("stored comment dropped on a GitHub error")
	}
}

func TestThreadComments(t *testing.T) {
	ts := []gh.Thread{{ID: "T", Path: "a.go", OriginalLine: 9, StartLine: 7, Side: model.SideOld, Outdated: true, Resolved: true,
		Comments: []model.Comment{{GHID: 5, Body: "x", State: model.CommentPosted}, {Body: "no id"}, {ID: "gh-6", Path: "b.go", Line: 3, Body: "y"}}}}
	got := threadComments("pr:1", ts)
	if len(got) != 2 {
		t.Fatalf("comments = %+v", got)
	}
	a, b := got[0], got[1]
	if a.ID != "gh-5" || a.ItemID != "pr:1" || a.ThreadID != "T" || a.Path != "a.go" || a.Line != 9 || a.StartLine != 7 ||
		a.Side != model.SideOld || a.State != model.CommentResolved || !a.Outdated || a.Source != model.CommentGitHub {
		t.Errorf("normalised = %+v", a)
	}
	if b.ID != "gh-6" || b.Path != "b.go" || b.Line != 3 {
		t.Errorf("own position not kept: %+v", b)
	}
}

// findingReviews answers reviews with one medium finding on the first
// unit's first added line while on is set, else none; prose as usual.
func findingReviews(on *atomic.Bool) func(review.Request) (review.Result, error) {
	return func(req review.Request) (review.Result, error) {
		if req.Task != review.TaskReview {
			return review.FakeRespond(0.9)(req)
		}
		fs := []model.Finding{}
		if on.Load() && len(req.Units) > 0 {
			u := req.Units[0]
			line := u.Range.New.Start
			if ls := model.AddedLineNumbers(u); len(ls) > 0 {
				line = ls[0]
			}
			fs = append(fs, model.Finding{Path: u.Path, Line: line, Severity: model.SevMedium, Claim: "Check this."})
		}
		return review.Result{Text: review.FakeReviewJSON(fs, review.VerdictComment, 0.9)}, nil
	}
}

// AI drafts follow the findings: one per finding, gone when the finding
// goes, and a finding the user dismissed never comes back.
func TestAIDraftsFollowFindings(t *testing.T) {
	ctx := context.Background()
	var on atomic.Bool
	on.Store(true)
	e, p, _ := m3Env(t, findingReviews(&on))
	rerun := func() {
		t.Helper()
		e.events = nil
		if err := p.RunWith(ctx, e.item, RunOptions{FromStage: model.StageReview}); err != nil {
			t.Fatal(err)
		}
	}
	rerun()
	drafts := e.commentsBySource(t, model.CommentAIDraft)
	if len(drafts) == 0 {
		t.Fatal("no ai-drafts from findings")
	}
	first, _ := e.comment(t, drafts[0])
	if first.State != model.CommentDraft || first.FindingKey == "" || first.Severity != model.SevMedium || !strings.Contains(first.Body, "Check this.") {
		t.Errorf("ai-draft = %+v", first)
	}

	on.Store(false)
	rerun()
	if got := e.commentsBySource(t, model.CommentAIDraft); len(got) != 0 {
		t.Fatalf("ai-drafts %v kept after their findings went", got)
	}

	on.Store(true)
	if err := e.st.PutFindingMark(ctx, model.FindingMark{ItemID: e.item.ID, FindingKey: first.FindingKey, State: model.FindingDismissed}); err != nil {
		t.Fatal(err)
	}
	rerun()
	got := e.commentsBySource(t, model.CommentAIDraft)
	if slices.Contains(got, first.ID) || len(got) != len(drafts)-1 {
		t.Errorf("ai-drafts after dismissing %s = %v, want the other %d", first.ID, got, len(drafts)-1)
	}
}

func TestKeepKnownAnswers(t *testing.T) {
	q := model.QCommentAddressed
	stored := []model.Label{{UnitID: "c1", Question: q, Value: model.LabelYes, Ctx: "old"}, {UnitID: "c2", Question: q, Value: model.LabelUnknown}}
	fresh := []model.Label{
		{UnitID: "c1", Question: q, Value: model.LabelUnknown, Ctx: "new"}, // would erase c1's answer
		{UnitID: "c2", Question: q, Value: model.LabelUnknown, Ctx: "new"}, // nothing known to keep
		{UnitID: "c3", Question: q, Value: model.LabelNo, Ctx: "new"},
	}
	var got []string
	for _, l := range keepKnownAnswers(fresh, stored) {
		got = append(got, l.UnitID)
	}
	if !slices.Equal(got, []string{"c2", "c3"}) {
		t.Errorf("kept %v", got)
	}
}

func TestNumberedWindow(t *testing.T) {
	text := "a\nb\nc\nd\ne\n"
	cases := []struct {
		line, n int
		want    string
	}{
		{3, 1, "2: b\n3: c\n4: d\n"},
		{1, 2, "1: a\n2: b\n3: c\n"},
		{5, 10, "1: a\n2: b\n3: c\n4: d\n5: e\n"},
		{9, 1, ""},
	}
	for _, c := range cases {
		if got := numberedWindow(text, c.line, c.n); got != c.want {
			t.Errorf("numberedWindow(%d, %d) = %q, want %q", c.line, c.n, got, c.want)
		}
	}
}

// Old-side findings shift by the old span's start; reviews cached before
// sides fall back to the anchor start.
func TestRebaseOldSideFindings(t *testing.T) {
	u := model.ChangeUnit{ID: "u", Path: "a.go", Kind: model.UnitModified,
		Range: model.Range{Old: model.Span{Start: 60, Count: 6}, New: model.Span{Start: 120, Count: 2}},
		Diff:  " c\n-o1\n-o2\n-o3\n-o4\n+n\n"}
	// When reviewed the unit sat at old 50 / new 100; old line 53 was the
	// third line of the unit (a deletion).
	cached := []model.Finding{{UnitID: "u", Path: "a.go", Line: 53, Side: model.SideOld}}
	got := rebaseFindings(cached, map[string]int{"u": 100, "u" + oldStartSuffix: 50}, []model.ChangeUnit{u})
	if got[0].Line != 63 || got[0].Side != model.SideOld || got[0].UnitID != "u" {
		t.Errorf("rebased = %+v, want old line 63", got[0])
	}
	// Without a recorded old start the anchor (new) start's shift applies.
	legacy := rebaseFindings(cached, map[string]int{"u": 110}, []model.ChangeUnit{u})
	if legacy[0].Line != 63 {
		t.Errorf("legacy rebase = %+v, want line 53+10", legacy[0])
	}
	if s := unitStarts([]model.ChangeUnit{u}); s["u"] != 120 || s["u"+oldStartSuffix] != 60 {
		t.Errorf("unitStarts = %v", s)
	}
}
