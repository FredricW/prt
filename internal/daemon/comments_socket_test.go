package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// The six M4 methods go Socket → Serve → InProcess and back with their
// params and results intact; refusals come back as RemoteError.
func TestSocketCommentMethods(t *testing.T) {
	ctx := context.Background()
	fake := &gh.Fake{Login: "fredric"}
	backing, st, _ := commentClient(t, fake)
	putComments(t, st, aiDraftComment())
	br := branchItem("feat/x", 1)
	br.Title = "Feature X"
	put(t, st, br)
	s, _, _ := startServer(t, backing)

	draft, err := s.SaveComment(ctx, proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "a.go", Line: 13, StartLine: 11, Body: "helper?"}})
	if err != nil || !strings.HasPrefix(draft.ID, "d-") || draft.UnitID != "u1" || draft.StartLine != 11 || draft.Author != "fredric" {
		t.Fatalf("save = %+v, %v", draft, err)
	}
	_, err = s.SaveComment(ctx, proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "a.go", Line: 99, Body: "x"}})
	var re *RemoteError
	if !errors.As(err, &re) || re.Method != proto.MethodComment || !strings.Contains(re.Msg, "line not in the diff") {
		t.Fatalf("refused save = %#v", err)
	}

	accepted, err := s.CommentAction(ctx, proto.CommentActionParams{ItemID: cItem, Action: proto.CommentAccept, CommentID: "ai-k1"})
	if err != nil || accepted.FindingKey != "k1" || accepted.Source != model.CommentUser {
		t.Fatalf("accept = %+v, %v", accepted, err)
	}

	res, err := s.SubmitReview(ctx, proto.SubmitParams{ItemID: cItem, Event: proto.EventComment, Body: "LGTM with nits"})
	if err != nil || res.ReviewID != 1 || len(res.Posted) != 2 || res.Posted[0].GHID == 0 {
		t.Fatalf("submit = %+v, %v", res, err)
	}

	if err := s.MarkViewed(ctx, proto.ViewedParams{ItemID: cItem, HeadSHA: cAnalysed}); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := st.ViewMark(ctx, cItem); err != nil || !ok || v.HeadSHA != cAnalysed {
		t.Fatalf("view mark = %+v, %v, %v", v, ok, err)
	}

	if err := s.Override(ctx, proto.OverrideParams{ItemID: cItem, Override: model.Override{UnitID: "u2", Question: "breaking", Value: "no"}}); err != nil {
		t.Fatal(err)
	}
	if ovs, err := st.Overrides(ctx, []string{"u2"}); err != nil || len(ovs) != 1 {
		t.Fatalf("overrides = %+v, %v", ovs, err)
	}

	pr, err := s.CreatePR(ctx, proto.CreatePRParams{ItemID: br.ID})
	if err != nil || pr.Item.ID != model.PRItemID(1) || pr.Item.Title != "Feature X" || pr.Attached != 0 {
		t.Fatalf("create PR = %+v, %v", pr, err)
	}
	if len(fake.Created) != 1 || fake.Created[0].Head != "feat/x" || fake.Created[0].Base != "main" {
		t.Fatalf("created = %+v", fake.Created)
	}
}
