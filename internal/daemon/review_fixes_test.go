package daemon

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// While a run is in flight stage 1 has already stored the new head's units
// and moved the drafts (UnitsSHA h2, drafts on h2) but AnalysedSHA is still
// h1: the review must be posted on h2, the commit the lines describe, and a
// draft still on another commit is skipped, never sent with lines from one
// commit and commit_id of another (regression).
func TestSubmitReviewPostsOnUnitsHead(t *testing.T) {
	ctx := context.Background()
	fake := &gh.Fake{Login: "fredric"}
	c, st, _ := commentClient(t, fake)
	it, err := st.Item(ctx, cItem)
	if err != nil {
		t.Fatal(err)
	}
	it.UnitsSHA = "h2"
	put(t, st, it)
	moved := userDraft("d-1", "a.go", 12, "On the new head.")
	moved.CommitSHA = "h2"
	stale := userDraft("d-2", "a.go", 13, "Still on h1.")
	putComments(t, st, moved, stale)

	res, err := c.SubmitReview(ctx, proto.SubmitParams{ItemID: cItem, Event: proto.EventComment})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Payloads) != 1 || fake.Payloads[0].CommitID != "h2" || len(fake.Payloads[0].Comments) != 1 {
		t.Fatalf("payload = %+v, want one comment on h2", fake.Payloads)
	}
	if len(res.Posted) != 1 || res.Posted[0].ID != "d-1" || len(res.Skipped) != 1 || res.Skipped[0].ID != "d-2" {
		t.Fatalf("result = %+v", res)
	}
	if got, _ := st.Item(ctx, cItem); got.ViewerReviewedSHA != "h2" || !got.ViewerReviewedAt.Equal(testNow) {
		t.Errorf("item reviewed = %s at %v", got.ViewerReviewedSHA, got.ViewerReviewedAt)
	}
}

// New drafts take the units' head, not the (older) analysed head.
func TestSaveCommentUsesUnitsHead(t *testing.T) {
	ctx := context.Background()
	c, st, _ := commentClient(t, &gh.Fake{Login: "fredric"})
	it, _ := st.Item(ctx, cItem)
	it.UnitsSHA = "h2"
	put(t, st, it)
	d, err := c.SaveComment(ctx, proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "a.go", Line: 11, Body: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if d.CommitSHA != "h2" {
		t.Errorf("draft commit = %s, want h2", d.CommitSHA)
	}
}

// An ai-draft whose line no unit covers becomes an outdated user draft when
// accepted, so a submit skips it instead of GitHub rejecting the review.
func TestAcceptOffDiffAIDraftIsOutdated(t *testing.T) {
	ctx := context.Background()
	c, st, _ := commentClient(t, &gh.Fake{Login: "fredric"})
	ai := aiDraftComment()
	ai.UnitID, ai.UnitOffset, ai.Line = "", 0, 40
	putComments(t, st, ai)
	d, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: cItem, Action: proto.CommentAccept, CommentID: ai.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Outdated || d.UnitID != "" || d.Line != 40 {
		t.Errorf("accepted = %+v, want outdated at line 40", d)
	}
}

// Draft ids must not repeat across processes that share a store and a
// fixed clock (--now): the second would overwrite the first.
func TestDraftIDsUniqueAcrossClients(t *testing.T) {
	ctx := context.Background()
	c1, st, _ := commentClient(t, &gh.Fake{Login: "fredric"})
	c2 := newClient(t, st, &gitx.Fake{}, &gh.Fake{Login: "fredric"}, nil)
	captureJobs(c2)
	in := model.Comment{Path: "a.go", Line: 11, Body: "first"}
	a, err := c1.SaveComment(ctx, proto.CommentParams{ItemID: cItem, Comment: in})
	if err != nil {
		t.Fatal(err)
	}
	in.Body = "second"
	b, err := c2.SaveComment(ctx, proto.CommentParams{ItemID: cItem, Comment: in})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == b.ID {
		t.Fatalf("both clients made draft id %s", a.ID)
	}
	if cs := storedComments(t, st, cItem); cs[a.ID].Body != "first" || cs[b.ID].Body != "second" {
		t.Errorf("stored = %+v", cs)
	}
}

// A branch with commits not on its upstream cannot become a PR: GitHub
// would open it on the pushed head (or refuse a branch it never saw).
func TestCreatePRRefusesUnpushedBranch(t *testing.T) {
	ctx := context.Background()
	fake := &gh.Fake{Login: "fredric", PRs: existingPRs()}
	c, st, _ := createPRClient(t, fake)
	br, err := st.Item(ctx, model.BranchItemID(brRef))
	if err != nil {
		t.Fatal(err)
	}
	br.Unpushed = 2
	put(t, st, br)
	_, err = c.CreatePR(ctx, proto.CreatePRParams{ItemID: br.ID})
	wantErr(t, err, "push feat/cache first: 2 commits")
	if len(fake.Created) != 0 {
		t.Fatalf("PR created: %+v", fake.Created)
	}

	// Pushed since discovery: the upstream count is re-read from git.
	g := &gitx.Fake{Default: "main", RefList: []gitx.Ref{{Name: brRef, SHA: brHead, Upstream: "origin/" + brRef}},
		Ahead: map[string]int{"origin/" + brRef + ".." + brRef: 0}}
	c2 := newClient(t, st, g, fake, nil)
	captureJobs(c2)
	if _, err := c2.CreatePR(ctx, proto.CreatePRParams{ItemID: br.ID}); err != nil {
		t.Fatalf("pushed branch: %v", err)
	}
}

// When the branch moved after its analysis, its drafts' lines describe the
// analysed commit, not the PR's head: they are moved to the PR but not
// posted with the wrong commit_id (regression: commit was the local head).
func TestCreatePRKeepsDraftsWhenHeadMoved(t *testing.T) {
	ctx := context.Background()
	fake := &gh.Fake{Login: "fredric", PRs: existingPRs(), HeadSHAs: map[string]string{brRef: "hnew"}}
	c, st, _ := createPRClient(t, fake)
	br, _ := st.Item(ctx, model.BranchItemID(brRef))
	br.HeadSHA = "hnew" // AnalysedSHA stays brHead
	put(t, st, br)
	res, err := c.CreatePR(ctx, proto.CreatePRParams{ItemID: br.ID})
	wantErr(t, err, "describe hc, not the PR's head hnew")
	if res.Item.Number != 6 || res.Attached != 0 || len(fake.Payloads) != 0 {
		t.Fatalf("result = %+v, payloads = %+v", res, fake.Payloads)
	}
	moved := storedComments(t, st, model.PRItemID(6))
	for _, id := range []string{"d-1", "d-2"} {
		if cm := moved[id]; cm.State != model.CommentDraft || cm.CommitSHA != brHead {
			t.Errorf("%s = %+v, want a draft on %s", id, cm, brHead)
		}
	}
}
