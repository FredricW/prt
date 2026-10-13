package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
)

// submitDrafts seeds two sendable drafts (one a range), an outdated draft,
// an ai-draft and an already posted comment on PR #7.
func submitDrafts(t *testing.T, st store.Store) {
	t.Helper()
	single := userDraft("d-1", "a.go", 12, "Handle the error here.")
	rng := userDraft("d-2", "a.go", 13, "This block could be a helper.")
	rng.StartLine = 11
	outdated := userDraft("d-3", "a.go", 40, "Stale remark.")
	outdated.Outdated = true
	posted := userDraft("d-4", "a.go", 30, "Already sent.")
	posted.State, posted.GHID, posted.ReviewID = model.CommentPosted, 77, 7
	putComments(t, st, single, rng, outdated, aiDraftComment(), posted)
}

func TestSubmitReviewPayload(t *testing.T) {
	ctx := context.Background()
	fake := &gh.Fake{Login: "fredric"}
	c, st, jobs := commentClient(t, fake)
	submitDrafts(t, st)

	res, err := c.SubmitReview(ctx, proto.SubmitParams{ItemID: cItem, Event: proto.EventRequestChanges, Body: "Two things before merge."})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Payloads) != 1 || fake.PayloadPRs[0] != 7 {
		t.Fatalf("payloads = %+v to %v", fake.Payloads, fake.PayloadPRs)
	}
	got, err := json.MarshalIndent(fake.Payloads[0], "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "submit_payload.json")
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		t.Fatalf("payload:\n%s\nwant (%s):\n%s", got, golden, want)
	}

	if res.ReviewID != 1 || res.URL == "" || len(res.Posted) != 2 || len(res.Skipped) != 1 || res.Skipped[0].ID != "d-3" {
		t.Fatalf("result = %+v", res)
	}
	cs := storedComments(t, st, cItem)
	for i, id := range []string{"d-1", "d-2"} {
		p := cs[id]
		if p.State != model.CommentPosted || p.GHID != 1000+int64(i) || p.ReviewID != 1 || p.CommitSHA != cAnalysed || p.Source != model.CommentUser {
			t.Errorf("%s = %+v", id, p)
		}
		if res.Posted[i] != p {
			t.Errorf("result %s = %+v, stored %+v", id, res.Posted[i], p)
		}
	}
	if cs["d-3"].State != model.CommentDraft || cs["ai-k1"].Source != model.CommentAIDraft || cs["d-4"].GHID != 77 {
		t.Errorf("untouched comments changed: %+v", cs)
	}
	it, err := st.Item(ctx, cItem)
	if err != nil || it.ViewerReviewedSHA != cAnalysed || it.ViewerApproved {
		t.Fatalf("item = %+v, %v", it, err)
	}
	js := jobs.waitJobs(t, 1)
	if js[0].itemID != cItem || js[0].force || !js[0].rerun {
		t.Errorf("job = %+v", js[0])
	}
	evs := drain(c.Events())
	if !hasPatch(evs, cItem) || !hasItemEvent(evs, cItem) {
		t.Errorf("events = %+v", evs)
	}
}

func hasItemEvent(evs []proto.Event, itemID string) bool {
	for _, e := range evs {
		if e.Type == proto.EventItem && e.ItemID == itemID {
			return true
		}
	}
	return false
}

func TestSubmitReviewOnlySelected(t *testing.T) {
	fake := &gh.Fake{Login: "fredric"}
	c, st, _ := commentClient(t, fake)
	submitDrafts(t, st)
	res, err := c.SubmitReview(context.Background(), proto.SubmitParams{ItemID: cItem, Event: proto.EventComment, CommentIDs: []string{"d-2", "ai-k1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Posted) != 1 || res.Posted[0].ID != "d-2" || len(fake.Payloads[0].Comments) != 1 {
		t.Fatalf("result = %+v, payload = %+v", res, fake.Payloads)
	}
	if storedComments(t, st, cItem)["d-1"].State != model.CommentDraft {
		t.Error("unselected draft was sent")
	}
}

func TestSubmitReviewFailureKeepsDrafts(t *testing.T) {
	fake := &gh.Fake{Login: "fredric", Err: errors.New("422 Unprocessable Entity")}
	c, st, jobs := commentClient(t, fake)
	submitDrafts(t, st)
	before := storedComments(t, st, cItem)
	_, err := c.SubmitReview(context.Background(), proto.SubmitParams{ItemID: cItem, Event: proto.EventComment})
	wantErr(t, err, "422")
	after := storedComments(t, st, cItem)
	for id, b := range before {
		if after[id] != b {
			t.Errorf("%s changed: %+v → %+v", id, b, after[id])
		}
	}
	if it, _ := st.Item(context.Background(), cItem); it.ViewerReviewedSHA != "" {
		t.Error("failed review recorded on the item")
	}
	if js := jobs.snapshot(); len(js) != 0 {
		t.Errorf("failed review queued jobs: %+v", js)
	}
}

func TestSubmitReviewApproveWithoutComments(t *testing.T) {
	fake := &gh.Fake{Login: "fredric"}
	c, st, _ := commentClient(t, fake)
	res, err := c.SubmitReview(context.Background(), proto.SubmitParams{ItemID: cItem, Event: proto.EventApprove})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Payloads) != 1 || fake.Payloads[0].Event != "APPROVE" || len(fake.Payloads[0].Comments) != 0 || fake.Payloads[0].CommitID != cAnalysed || res.ReviewID != 1 {
		t.Fatalf("payloads = %+v, result = %+v", fake.Payloads, res)
	}
	it, err := st.Item(context.Background(), cItem)
	if err != nil || !it.ViewerApproved || it.ViewerReviewedSHA != cAnalysed {
		t.Fatalf("item = %+v, %v", it, err)
	}
}

func TestSubmitReviewRefusals(t *testing.T) {
	cases := []struct {
		name  string
		p     proto.SubmitParams
		noGH  bool
		setup func(*testing.T, store.Store)
		want  string
	}{
		{name: "comment with nothing", p: proto.SubmitParams{ItemID: cItem, Event: proto.EventComment, Body: " "}, want: "needs a summary or at least one draft"},
		{name: "request changes with only skipped", p: proto.SubmitParams{ItemID: cItem, Event: proto.EventRequestChanges}, want: "needs a summary",
			setup: func(t *testing.T, st store.Store) {
				d := userDraft("d-3", "a.go", 40, "x")
				d.Outdated = true
				putComments(t, st, d, aiDraftComment())
			}},
		{name: "unknown event", p: proto.SubmitParams{ItemID: cItem, Event: "MERGE"}, want: "unknown event"},
		{name: "unknown item", p: proto.SubmitParams{ItemID: "pr:404", Event: proto.EventApprove}, want: "not found"},
		{name: "branch", p: proto.SubmitParams{ItemID: model.BranchItemID("feat/x"), Event: proto.EventApprove}, want: "not a pull request",
			setup: func(t *testing.T, st store.Store) { put(t, st, branchItem("feat/x", 1)) }},
		{name: "no GitHub", p: proto.SubmitParams{ItemID: cItem, Event: proto.EventApprove}, noGH: true, want: "GitHub unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &gh.Fake{Login: "fredric"}
			var ghc gh.Client = fake
			if tc.noGH {
				ghc = nil
			}
			c, st, _ := commentClient(t, ghc)
			if tc.setup != nil {
				tc.setup(t, st)
			}
			_, err := c.SubmitReview(context.Background(), tc.p)
			wantErr(t, err, tc.want)
			if len(fake.Payloads) != 0 || len(fake.Submitted) != 0 {
				t.Fatalf("payload sent: %+v", fake.Payloads)
			}
		})
	}
}

// failingSubmit creates PRs but fails every review submission.
type failingSubmit struct{ *gh.Fake }

func (f failingSubmit) SubmitReview(context.Context, int, gh.ReviewSubmission) (gh.SubmittedReview, error) {
	return gh.SubmittedReview{}, errors.New("502 bad gateway")
}

const (
	brRef  = "feat/cache"
	brHead = "hc"
)

// createPRClient seeds branch item feat/cache (base origin/main) with two
// user drafts and an ai-draft (which must not move).
func createPRClient(t *testing.T, ghc gh.Client) (*InProcess, store.Store, *jobLog) {
	t.Helper()
	st := newTestStore(t)
	id := model.BranchItemID(brRef)
	put(t, st, model.Item{ID: id, Kind: model.KindBranch, Ref: brRef, BaseRef: "origin/main", HeadSHA: brHead, AnalysedSHA: brHead,
		Title: "Add profile caching", Source: model.SourceRanked, Section: model.SectionBranches})
	d1 := userDraft("d-1", "a.go", 12, "Handle the error here.")
	d2 := userDraft("d-2", "b.go", 4, "Name this better.")
	ai := aiDraftComment()
	for _, cm := range []*model.Comment{&d1, &d2, &ai} {
		cm.ItemID, cm.CommitSHA = id, brHead
	}
	putComments(t, st, d1, d2, ai)
	g := &gitx.Fake{Default: "main", RefList: []gitx.Ref{{Name: brRef, SHA: brHead}}}
	c := newClient(t, st, g, ghc, nil)
	return c, st, captureJobs(c)
}

func existingPRs() map[int]gh.PR {
	return map[int]gh.PR{5: {Number: 5, Title: "older", HeadRef: "other", HeadSHA: "ho", BaseRef: "main", Author: "bob"}}
}

func TestCreatePRAttachesDrafts(t *testing.T) {
	ctx := context.Background()
	fake := &gh.Fake{Login: "fredric", PRs: existingPRs()}
	c, st, jobs := createPRClient(t, fake)
	brID, prID := model.BranchItemID(brRef), model.PRItemID(6)

	res, err := c.CreatePR(ctx, proto.CreatePRParams{ItemID: brID, Body: "Caches profiles.", Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	want := gh.NewPR{Title: "Add profile caching", Body: "Caches profiles.", Head: brRef, Base: "main", Draft: true}
	if len(fake.Created) != 1 || fake.Created[0] != want {
		t.Fatalf("created = %+v, want %+v", fake.Created, want)
	}
	if len(fake.Payloads) != 1 || fake.PayloadPRs[0] != 6 || fake.Payloads[0].Event != "COMMENT" ||
		fake.Payloads[0].CommitID != brHead || len(fake.Payloads[0].Comments) != 2 {
		t.Fatalf("payloads = %+v to %v", fake.Payloads, fake.PayloadPRs)
	}
	if res.Attached != 2 || res.Item.ID != prID || res.Item.Kind != model.KindPR || res.Item.Number != 6 ||
		res.Item.HeadSHA != brHead || res.Item.Ref != brRef || res.Item.Author != "fredric" ||
		res.Item.Section != model.SectionYours || res.Item.Source != model.SourceRanked || !res.Item.Draft {
		t.Fatalf("result = %+v", res)
	}
	if _, err := st.Item(ctx, brID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("branch item still stored: %v", err)
	}
	if stored, err := st.Item(ctx, prID); err != nil || stored.ViewerReviewedSHA != brHead {
		t.Fatalf("PR item = %+v, %v", stored, err)
	}
	moved := storedComments(t, st, prID)
	for _, id := range []string{"d-1", "d-2"} {
		cm := moved[id]
		if cm.ItemID != prID || cm.State != model.CommentPosted || cm.GHID == 0 || cm.ReviewID != 1 || cm.CommitSHA != brHead {
			t.Errorf("%s = %+v", id, cm)
		}
	}
	left := storedComments(t, st, brID)
	if len(left) != 1 || left["ai-k1"].Source != model.CommentAIDraft {
		t.Errorf("left on the branch = %+v", left)
	}
	queued := map[string]bool{}
	for _, j := range jobs.waitJobs(t, 1) {
		queued[j.itemID] = true
	}
	if !queued[prID] {
		t.Errorf("PR item not queued: %+v", jobs.snapshot())
	}
	var removed bool
	for _, e := range drain(c.Events()) {
		removed = removed || (e.Type == proto.EventRemove && e.ItemID == brID)
	}
	if !removed {
		t.Error("no remove event for the branch item")
	}
}

func TestCreatePRWithoutDrafts(t *testing.T) {
	fake := &gh.Fake{Login: "fredric"}
	st := newTestStore(t)
	put(t, st, model.Item{ID: model.BranchItemID(brRef), Kind: model.KindBranch, Ref: brRef, HeadSHA: brHead, Title: "x"})
	c := newClient(t, st, &gitx.Fake{Default: "trunk", RefList: []gitx.Ref{{Name: brRef, SHA: brHead}}}, fake, nil)
	captureJobs(c)
	res, err := c.CreatePR(context.Background(), proto.CreatePRParams{ItemID: model.BranchItemID(brRef), Title: "  Title  "})
	if err != nil {
		t.Fatal(err)
	}
	if res.Attached != 0 || len(fake.Payloads) != 0 || fake.Created[0].Base != "trunk" || fake.Created[0].Title != "Title" {
		t.Fatalf("result = %+v, created = %+v, payloads = %+v", res, fake.Created, fake.Payloads)
	}
}

func TestCreatePRReviewFailureKeepsPR(t *testing.T) {
	ctx := context.Background()
	fake := &gh.Fake{Login: "fredric", PRs: existingPRs()}
	c, st, _ := createPRClient(t, failingSubmit{fake})
	res, err := c.CreatePR(ctx, proto.CreatePRParams{ItemID: model.BranchItemID(brRef)})
	wantErr(t, err, "PR #6 created, but its 2 draft(s) were not attached")
	if res.Item.ID != model.PRItemID(6) || res.Attached != 0 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := st.Item(ctx, model.PRItemID(6)); err != nil {
		t.Fatalf("PR item not stored: %v", err)
	}
	moved := storedComments(t, st, model.PRItemID(6))
	for _, id := range []string{"d-1", "d-2"} {
		if cm := moved[id]; cm.State != model.CommentDraft || cm.GHID != 0 || cm.CommitSHA != brHead {
			t.Errorf("%s = %+v", id, cm)
		}
	}
}

func TestCreatePRRefusals(t *testing.T) {
	cases := []struct {
		name  string
		p     proto.CreatePRParams
		noGH  bool
		setup func(*testing.T, store.Store)
		want  string
	}{
		{name: "PR item", p: proto.CreatePRParams{ItemID: cItem, Title: "x"}, want: "not a local branch",
			setup: func(t *testing.T, st store.Store) { put(t, st, model.Item{ID: cItem, Kind: model.KindPR, Number: 7}) }},
		{name: "no GitHub", p: proto.CreatePRParams{ItemID: model.BranchItemID(brRef)}, noGH: true, want: "GitHub unavailable"},
		{name: "PR exists for the ref", p: proto.CreatePRParams{ItemID: model.BranchItemID(brRef)}, want: "already has PR #9",
			setup: func(t *testing.T, st store.Store) {
				put(t, st, model.Item{ID: model.PRItemID(9), Kind: model.KindPR, Number: 9, Ref: brRef})
			}},
		{name: "no title", p: proto.CreatePRParams{ItemID: model.BranchItemID(brRef)}, want: "needs a title",
			setup: func(t *testing.T, st store.Store) {
				put(t, st, model.Item{ID: model.BranchItemID(brRef), Kind: model.KindBranch, Ref: brRef, BaseRef: "main"})
			}},
		{name: "base is head", p: proto.CreatePRParams{ItemID: model.BranchItemID(brRef), Base: brRef}, want: "head and base"},
		{name: "unknown item", p: proto.CreatePRParams{ItemID: "branch:nope"}, want: "not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &gh.Fake{Login: "fredric"}
			var ghc gh.Client = fake
			if tc.noGH {
				ghc = nil
			}
			c, st, _ := createPRClient(t, ghc)
			if tc.setup != nil {
				tc.setup(t, st)
			}
			_, err := c.CreatePR(context.Background(), tc.p)
			wantErr(t, err, tc.want)
			if len(fake.Created) != 0 || len(fake.Payloads) != 0 {
				t.Fatalf("GitHub written: %+v %+v", fake.Created, fake.Payloads)
			}
		})
	}
}
