package comments

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

var now = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// unit builds a unit on path with old/new spans.
func unit(id, path string, order, oldStart, oldCount, newStart, newCount int) model.ChangeUnit {
	return model.ChangeUnit{
		ID: id, Path: path, Order: order,
		Range: model.Range{Old: model.Span{Start: oldStart, Count: oldCount}, New: model.Span{Start: newStart, Count: newCount}},
	}
}

func draft(id, path string, line int, side model.Side) model.Comment {
	return model.Comment{ID: id, ItemID: "pr:1", Path: path, Line: line, Side: side, Body: "b", Source: model.CommentUser, State: model.CommentDraft}
}

func TestAnchor(t *testing.T) {
	units := []model.ChangeUnit{
		unit("u1", "a.go", 0, 10, 5, 10, 8),
		unit("u2", "a.go", 1, 20, 3, 25, 0), // pure deletion
		unit("u3", "b.go", 2, 0, 0, 1, 30),
	}
	tests := []struct {
		name       string
		c          model.Comment
		wantUnit   string
		wantOffset int
	}{
		{"new side inside", draft("c", "a.go", 12, model.SideNew), "u1", 2},
		{"empty side is new", draft("c", "a.go", 17, ""), "u1", 7},
		{"new side past span", draft("c", "a.go", 18, model.SideNew), "", 0},
		{"old side deletion", draft("c", "a.go", 21, model.SideOld), "u2", 1},
		{"deletion has no new side", draft("c", "a.go", 25, model.SideNew), "", 0},
		{"other path", draft("c", "b.go", 30, model.SideNew), "u3", 29},
		{"unknown path", draft("c", "c.go", 1, model.SideNew), "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.c.Outdated = true
			got := Anchor(tc.c, units)
			if got.UnitID != tc.wantUnit || got.UnitOffset != tc.wantOffset {
				t.Errorf("anchor = %q+%d, want %q+%d", got.UnitID, got.UnitOffset, tc.wantUnit, tc.wantOffset)
			}
			if got.Outdated == (tc.wantUnit != "") {
				t.Errorf("outdated = %v with unit %q", got.Outdated, got.UnitID)
			}
			if got.Line != tc.c.Line || got.Path != tc.c.Path {
				t.Error("Anchor must not move the comment")
			}
		})
	}
}

func TestReanchor(t *testing.T) {
	before := []model.ChangeUnit{unit("u1", "a.go", 0, 10, 5, 10, 8), unit("u2", "a.go", 1, 40, 2, 43, 4)}
	// A push added 5 lines above u1; u2 was rewritten (new id u2b).
	after := []model.ChangeUnit{unit("u1", "a.go", 0, 10, 5, 15, 8), unit("u2b", "a.go", 1, 40, 2, 42, 6)}

	moved := Anchor(draft("d1", "a.go", 12, model.SideNew), before)
	moved.StartLine = 11
	byPath := Anchor(draft("d2", "a.go", 44, model.SideNew), before) // u2 gone; line 44 now in u2b
	lost := Anchor(draft("d3", "a.go", 46, model.SideNew), before)
	lost.Line = 60 // nothing covers 60 after the push
	lost.UnitID = "u-gone"
	posted := Anchor(draft("p1", "a.go", 12, model.SideNew), before)
	posted.State = model.CommentPosted
	gh := model.Comment{ID: "gh-1", Path: "a.go", Line: 45, Side: model.SideNew, Source: model.CommentGitHub, State: model.CommentPosted}
	ai := Anchor(model.Comment{ID: "ai-k", Path: "a.go", Line: 10, Side: model.SideNew, Source: model.CommentAIDraft, State: model.CommentDraft}, before)
	same := Anchor(draft("d4", "a.go", 30, model.SideOld), nil) // no unit before or after, already outdated
	same.Outdated = true

	changed := Reanchor([]model.Comment{moved, byPath, lost, posted, gh, ai, same}, after)
	got := map[string]model.Comment{}
	for _, c := range changed {
		got[c.ID] = c
	}
	if c := got["d1"]; c.Line != 17 || c.StartLine != 16 || c.UnitID != "u1" || c.Outdated {
		t.Errorf("d1 = line %d start %d unit %q outdated %v, want 17/16/u1", c.Line, c.StartLine, c.UnitID, c.Outdated)
	}
	if c := got["d2"]; c.Line != 44 || c.UnitID != "u2b" || c.UnitOffset != 2 || c.Outdated {
		t.Errorf("d2 = %+v, want re-anchored by path/line to u2b", c)
	}
	if c := got["d3"]; !c.Outdated || c.Line != 60 || c.UnitID != "" {
		t.Errorf("d3 = %+v, want outdated at its old line", c)
	}
	if c, ok := got["p1"]; !ok || c.Line != 12 || c.UnitID != "" {
		t.Errorf("posted p1 must keep its line and lose the stale anchor: %+v (changed %v)", c, ok)
	}
	if c, ok := got["gh-1"]; !ok || c.Line != 45 || c.UnitID != "u2b" {
		t.Errorf("github comment must keep its line and gain an anchor: %+v", c)
	}
	if c := got["ai-k"]; c.Line != 15 || c.UnitID != "u1" {
		t.Errorf("ai-draft = %+v, want moved with u1 to 15", c)
	}
	if _, ok := got["d4"]; ok {
		t.Error("an unchanged comment must not be returned")
	}
	if len(Reanchor([]model.Comment{got["d1"]}, after)) != 0 {
		t.Error("Reanchor must be idempotent")
	}
}

// A range from a deleted (old) line to an added (new) line keeps its start
// on the old side when only the new span moves (regression: StartLine was
// shifted by the new side's delta).
func TestReanchorCrossSideRange(t *testing.T) {
	before := []model.ChangeUnit{unit("u1", "a.go", 0, 50, 2, 50, 2)}
	after := []model.ChangeUnit{unit("u1", "a.go", 0, 50, 2, 55, 2)} // only New.Start moved
	d := draft("d1", "a.go", 51, model.SideNew)
	d.StartLine, d.StartSide = 50, model.SideOld
	d = Anchor(d, before)
	if d.UnitID != "u1" || d.StartOffset != 0 || d.UnitOffset != 1 {
		t.Fatalf("anchor = %+v", d)
	}
	changed := Reanchor([]model.Comment{d}, after)
	if len(changed) != 1 {
		t.Fatalf("changed = %+v", changed)
	}
	if c := changed[0]; c.Line != 56 || c.StartLine != 50 || c.StartSide != model.SideOld {
		t.Errorf("moved = line %d start %d (%s), want 56 / old 50", c.Line, c.StartLine, c.StartSide)
	}
	// And the old side moving alone moves only the start.
	after2 := []model.ChangeUnit{unit("u1", "a.go", 0, 60, 2, 50, 2)}
	d2 := draft("d2", "a.go", 51, model.SideNew)
	d2.StartLine, d2.StartSide = 51, model.SideOld
	d2 = Anchor(d2, before)
	if c := Reanchor([]model.Comment{d2}, after2); len(c) != 1 || c[0].Line != 51 || c[0].StartLine != 61 {
		t.Errorf("old side moved = %+v, want line 51 start 61", c)
	}
}

// An outdated GitHub thread's line is an OriginalLine on an older commit:
// it must not be anchored to whatever current unit covers that number.
func TestReanchorOutdatedGitHubUnanchored(t *testing.T) {
	units := []model.ChangeUnit{unit("u1", "a.go", 0, 100, 40, 100, 40)}
	gh := model.Comment{ID: "gh-9", Path: "a.go", Line: 120, Side: model.SideNew, Source: model.CommentGitHub,
		State: model.CommentPosted, Outdated: true, UnitID: "u-old", UnitOffset: 3}
	changed := Reanchor([]model.Comment{gh}, units)
	if len(changed) != 1 || changed[0].UnitID != "" || changed[0].UnitOffset != 0 || !changed[0].Outdated || changed[0].Line != 120 {
		t.Errorf("outdated github = %+v, want unanchored, still outdated at 120", changed)
	}
	gh.UnitID, gh.UnitOffset = "", 0
	if c := Reanchor([]model.Comment{gh}, units); len(c) != 0 {
		t.Errorf("unanchored outdated comment must be unchanged: %+v", c)
	}
}

func TestMergeGitHub(t *testing.T) {
	ghc := func(id int64, line int) model.Comment {
		return model.Comment{ID: GitHubID(id), GHID: id, Path: "a.go", Line: line, Source: model.CommentGitHub, State: model.CommentPosted}
	}
	kept := ghc(1, 5)
	kept.UnitID, kept.UnitOffset = "u1", 2
	stored := []model.Comment{
		kept,
		ghc(2, 7), // gone from GitHub
		{ID: "d-posted", GHID: 3, Source: model.CommentUser, State: model.CommentPosted},
		{ID: "d-posted-unseen", GHID: 9, Source: model.CommentUser, State: model.CommentPosted},
		{ID: "d-draft", Source: model.CommentUser, State: model.CommentDraft},
		{ID: "ai-x", Source: model.CommentAIDraft, State: model.CommentDraft},
	}
	resolved := ghc(1, 5)
	resolved.State = model.CommentResolved
	loaded := []model.Comment{ghc(1, 5), ghc(3, 9)}

	put, del := MergeGitHub(stored, loaded)
	if !slices.Equal(del, []string{"d-posted", "gh-2"}) {
		t.Errorf("del = %v", del)
	}
	if len(put) != 1 || put[0].ID != "gh-3" {
		t.Errorf("put = %+v, want only the new gh-3 (gh-1 unchanged)", put)
	}
	put, _ = MergeGitHub(stored, []model.Comment{resolved})
	if len(put) != 1 || put[0].State != model.CommentResolved || put[0].UnitID != "u1" || put[0].UnitOffset != 2 {
		t.Errorf("a changed comment must be put with the stored anchor kept: %+v", put)
	}
}

func TestFindingKeyAndAIDrafts(t *testing.T) {
	units := []model.ChangeUnit{unit("u1", "a.go", 0, 10, 5, 10, 8)}
	f1 := model.Finding{UnitID: "u1", Path: "a.go", Line: 12, Severity: model.SevHigh, Claim: "Nil map write.", Fix: "Init the map."}
	f2 := model.Finding{UnitID: "u1", Path: "a.go", Line: 11, Severity: model.SevLow, Claim: "Name."}
	unanchored := model.Finding{Path: "a.go", Severity: model.SevLow, Claim: "Whole file."}
	reviews := []model.Review{
		{GroupID: "g1", Model: "anthropic:deep", Findings: []model.Finding{f1, f2, unanchored, f1}},
		{GroupID: "g2", Calibration: &model.Calibration{Findings: []model.Finding{f2}}},
	}
	shifted := f1
	shifted.Line = 40
	if FindingKey("pr:1", "g1", f1) != FindingKey("pr:1", "g1", shifted) {
		t.Error("the key must not depend on the line")
	}
	old := f1
	old.Side = model.SideOld
	if FindingKey("pr:1", "g1", f1) == FindingKey("pr:1", "g1", old) || FindingKey("pr:1", "g1", f1) == FindingKey("pr:1", "g2", f1) {
		t.Error("the key must depend on side and group")
	}

	all := AIDrafts("pr:1", reviews, units, nil, now)
	if len(all) != 2 || all[0].Line != 11 || all[1].Line != 12 {
		t.Fatalf("drafts = %+v, want f2 then f1 (deduplicated, unanchored and calibration skipped)", all)
	}
	d := all[1]
	if d.ID != AIDraftID(d.FindingKey) || d.Source != model.CommentAIDraft || d.State != model.CommentDraft || d.Severity != model.SevHigh ||
		d.Author != "anthropic:deep" || d.UnitID != "u1" || d.UnitOffset != 2 || d.Side != model.SideNew {
		t.Errorf("draft = %+v", d)
	}
	if d.Body != "**high** Nil map write.\n\nSuggested fix: Init the map." {
		t.Errorf("body = %q", d.Body)
	}
	marks := []model.FindingMark{{ItemID: "pr:1", FindingKey: all[0].FindingKey, State: model.FindingDismissed}}
	if left := AIDrafts("pr:1", reviews, units, marks, now); len(left) != 1 || left[0].ID != d.ID {
		t.Errorf("marked findings must be skipped: %+v", left)
	}
}

func TestSyncAIDrafts(t *testing.T) {
	a := model.Comment{ID: "ai-a", Path: "a.go", Line: 1, Body: "x", Source: model.CommentAIDraft, State: model.CommentDraft, CreatedAt: now}
	b := model.Comment{ID: "ai-b", Path: "a.go", Line: 2, Body: "y", Source: model.CommentAIDraft, State: model.CommentDraft, CreatedAt: now}
	user := model.Comment{ID: "d-1", Source: model.CommentUser, State: model.CommentDraft}
	movedA := a
	movedA.Line, movedA.CreatedAt = 5, now.Add(time.Hour)
	c := model.Comment{ID: "ai-c", Path: "b.go", Line: 3, Source: model.CommentAIDraft, State: model.CommentDraft}

	put, del := SyncAIDrafts([]model.Comment{a, b, user}, []model.Comment{movedA, c})
	if !slices.Equal(del, []string{"ai-b"}) {
		t.Errorf("del = %v", del)
	}
	if len(put) != 2 || put[0].ID != "ai-a" || put[0].Line != 5 || !put[0].CreatedAt.Equal(now) || put[1].ID != "ai-c" {
		t.Errorf("put = %+v", put)
	}
	unchanged := a
	unchanged.CreatedAt = now.Add(time.Hour)
	if put, del := SyncAIDrafts([]model.Comment{a}, []model.Comment{unchanged}); len(put) != 0 || len(del) != 0 {
		t.Errorf("an unchanged draft must not be rewritten: put %v del %v", put, del)
	}
}

func TestAcceptAndForSubmit(t *testing.T) {
	ai := model.Comment{ID: "ai-k", ItemID: "pr:1", UnitID: "u1", UnitOffset: 2, Path: "a.go", Line: 12, Side: model.SideNew,
		Body: "b", Source: model.CommentAIDraft, State: model.CommentDraft, FindingKey: "k", Severity: model.SevHigh}
	acc := Accept(ai, "d-9", "fredric", now)
	if acc.ID != "d-9" || acc.Source != model.CommentUser || acc.State != model.CommentDraft || acc.Author != "fredric" ||
		acc.FindingKey != "k" || acc.Severity != "" || acc.UnitID != "u1" || !acc.CreatedAt.Equal(now) {
		t.Errorf("accepted = %+v", acc)
	}

	outdated := draft("d-out", "a.go", 3, model.SideNew)
	outdated.Outdated = true
	blank := draft("d-blank", "a.go", 4, model.SideNew)
	blank.Body = "  "
	posted := draft("d-posted", "a.go", 5, model.SideNew)
	posted.State = model.CommentPosted
	cs := []model.Comment{draft("d-b", "b.go", 1, model.SideNew), draft("d-a2", "a.go", 9, model.SideOld), draft("d-a1", "a.go", 2, model.SideNew),
		outdated, blank, posted, ai, acc}

	tests := []struct {
		name        string
		ids         []string
		send, skips []string
	}{
		{"all drafts", nil, []string{"d-a1", "d-a2", "d-9", "d-b"}, []string{"d-out", "d-blank"}},
		{"selected", []string{"d-b", "ai-k", "d-out"}, []string{"d-b"}, []string{"d-out"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			send, skipped := ForSubmit(cs, tc.ids)
			if ids(send) != strings.Join(tc.send, ",") || ids(skipped) != strings.Join(tc.skips, ",") {
				t.Errorf("send %s skipped %s, want %v %v", ids(send), ids(skipped), tc.send, tc.skips)
			}
		})
	}
}

func ids(cs []model.Comment) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return strings.Join(out, ",")
}

func TestViewMarksAndSinceBase(t *testing.T) {
	m := NextViewMark(model.ViewMark{}, false, "pr:1", "h1", now)
	if m.HeadSHA != "h1" || m.PrevHeadSHA != "" || m.ItemID != "pr:1" {
		t.Errorf("first mark = %+v", m)
	}
	if again := NextViewMark(m, true, "pr:1", "h1", now.Add(time.Hour)); again != m {
		t.Errorf("same head must not change the mark: %+v", again)
	}
	m2 := NextViewMark(m, true, "pr:1", "h2", now.Add(time.Hour))
	if m2.HeadSHA != "h2" || m2.PrevHeadSHA != "h1" {
		t.Errorf("new head mark = %+v", m2)
	}
	if NextViewMark(m2, true, "pr:1", "", now) != m2 {
		t.Error("an empty head must not change the mark")
	}

	reviewed := model.Item{ViewerReviewedSHA: "r0"}
	tests := []struct {
		name     string
		it       model.Item
		head     string
		mark     model.ViewMark
		ok       bool
		base, by string
	}{
		{"no mark, no review", model.Item{}, "h2", model.ViewMark{}, false, "", ""},
		{"no mark, reviewed earlier head", reviewed, "h2", model.ViewMark{}, false, "r0", model.SinceReviewed},
		{"no mark, reviewed this head", model.Item{ViewerReviewedSHA: "h2"}, "h2", model.ViewMark{}, false, "", ""},
		{"looked at an older head", reviewed, "h2", m, true, "h1", model.SinceViewed},
		{"reading the new head keeps the base", reviewed, "h2", m2, true, "h1", model.SinceViewed},
		{"first look is this head: fall back to review", reviewed, "h1", m, true, "r0", model.SinceReviewed},
		{"push after the last look", model.Item{}, "h3", m2, true, "h2", model.SinceViewed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, by := SinceBase(tc.it, tc.head, tc.mark, tc.ok)
			if base != tc.base || by != tc.by {
				t.Errorf("SinceBase = %q/%q, want %q/%q", base, by, tc.base, tc.by)
			}
		})
	}
}

func TestChangedUnitsAndPrevious(t *testing.T) {
	old := []model.ChangeUnit{{ID: "a"}, {ID: "b"}}
	cur := []model.ChangeUnit{{ID: "c"}, {ID: "a"}, {ID: "d"}}
	if got := ChangedUnits(old, cur); !slices.Equal(got, []string{"c", "d"}) {
		t.Errorf("changed = %v", got)
	}
	if got := ChangedUnits(cur, cur); got == nil || len(got) != 0 {
		t.Errorf("no change must be an empty, non-nil list: %#v", got)
	}

	gh := func(id int64, author, commit string, line int) model.Comment {
		return model.Comment{ID: GitHubID(id), GHID: id, Author: author, CommitSHA: commit, Path: "a.go", Line: line, Source: model.CommentGitHub, State: model.CommentPosted}
	}
	reply := gh(3, "fredric", "v1", 2)
	reply.InReplyTo = 1
	localCopy := model.Comment{ID: "d-x", GHID: 1, Author: "fredric", CommitSHA: "v1", Path: "a.go", Line: 1, Source: model.CommentUser, State: model.CommentPosted}
	localOnly := model.Comment{ID: "d-y", GHID: 7, Author: "Fredric", CommitSHA: "v1", Path: "a.go", Line: 0, Source: model.CommentUser, State: model.CommentPosted}
	draftMine := draft("d-z", "a.go", 4, model.SideNew)
	draftMine.Author = "fredric"
	cs := []model.Comment{gh(1, "fredric", "v1", 9), gh(2, "alice", "v1", 1), reply, gh(4, "fredric", "head", 1), localCopy, localOnly, draftMine}
	if got := ids(Previous(cs, "fredric", "head")); got != "d-y,gh-1" {
		t.Errorf("previous = %s, want d-y,gh-1", got)
	}
	if Previous(cs, "", "head") != nil {
		t.Error("no viewer, no previous comments")
	}
}

func TestExcerptAndIDs(t *testing.T) {
	tests := []struct{ in, want string }{
		{"\n\n  First line.  \nsecond", "First line."},
		{"", ""},
		{strings.Repeat("é", 100), strings.Repeat("é", 79) + "…"},
	}
	for _, tc := range tests {
		if got := Excerpt(tc.in); got != tc.want {
			t.Errorf("Excerpt(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if GitHubID(5001) != "gh-5001" || AIDraftID("k") != "ai-k" {
		t.Error("id helpers")
	}
	a, b := DraftID("pr:1", now, 1), DraftID("pr:1", now, 2)
	if a == b || !strings.HasPrefix(a, "d-") || a != DraftID("pr:1", now, 1) {
		t.Errorf("draft ids %q %q must be distinct, prefixed and deterministic", a, b)
	}
}
