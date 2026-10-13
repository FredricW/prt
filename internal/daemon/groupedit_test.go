package daemon

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

func TestEditGroups(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	g := &gitx.Fake{Default: "main", SHAs: map[string]string{"main": "base", "feat": "head"},
		MergeBases: map[string]string{"main..head": "base"}, Diffs: map[string]string{"base..head": oneLineDiff("a.go")}}
	c := newClient(t, st, g, nil, nil)
	it := model.Item{ID: model.BranchItemID("feat"), Kind: model.KindBranch, Ref: "feat", HeadSHA: "head", Source: model.SourceRanked}
	put(t, st, it)

	bad := []proto.GroupEditParams{
		{ItemID: it.ID, Edit: model.GroupEdit{Op: model.EditMerge}},                                               // no units
		{ItemID: it.ID, Edit: model.GroupEdit{Op: "explode", Units: []string{"a"}}},                               // unknown op
		{ItemID: it.ID, Edit: model.GroupEdit{ItemID: "branch:other", Op: model.EditSplit, Units: []string{"a"}}}, // mismatched item
		{ItemID: "branch:missing", Edit: model.GroupEdit{Op: model.EditSplit, Units: []string{"a"}}},              // unknown item
	}
	for i, p := range bad {
		if err := c.EditGroups(ctx, p); err == nil {
			t.Errorf("bad edit %d: want error", i)
		}
	}
	if err := c.EditGroups(ctx, proto.GroupEditParams{ItemID: it.ID, Edit: model.GroupEdit{Op: model.EditSplit, Units: []string{"u1"}}}); err != nil {
		t.Fatal(err)
	}
	edits, err := st.GroupEdits(ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].ItemID != it.ID || !edits[0].At.Equal(testNow) || edits[0].Units[0] != "u1" {
		t.Fatalf("stored edits = %+v", edits)
	}
	if err := c.EditGroups(ctx, proto.GroupEditParams{ItemID: it.ID, Edit: model.GroupEdit{Op: model.EditReset}}); err != nil {
		t.Fatal(err)
	}
	if edits, err = st.GroupEdits(ctx, it.ID); err != nil || len(edits) != 0 {
		t.Fatalf("after reset = %+v, %v", edits, err)
	}
}
