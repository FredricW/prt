package comments

import (
	"slices"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

// Two units with the same id (model.NormaliseHunk drops leading blank
// context) can start their spans at different points: the draft on "+y"
// must stay on "+y", not land on a blank context line (regression).
func TestReanchorIgnoresLeadingBlankContext(t *testing.T) {
	v1 := model.ChangeUnit{ID: "u", Path: "a.go", Diff: " \n \n-x\n+y",
		Range: model.Range{Old: model.Span{Start: 1, Count: 3}, New: model.Span{Start: 1, Count: 3}}}
	v2 := model.ChangeUnit{ID: "u", Path: "a.go", Diff: " \n \n \n-x\n+y",
		Range: model.Range{Old: model.Span{Start: 10, Count: 4}, New: model.Span{Start: 10, Count: 4}}}
	if model.NormaliseHunk(v1.Diff) != model.NormaliseHunk(v2.Diff) {
		t.Fatal("fixture: the two bodies must normalise alike (same unit id)")
	}
	d := Anchor(draft("d1", "a.go", 3, model.SideNew), []model.ChangeUnit{v1})   // "+y"
	old := Anchor(draft("d2", "a.go", 3, model.SideOld), []model.ChangeUnit{v1}) // "-x"
	ch := Reanchor([]model.Comment{d, old}, []model.ChangeUnit{v2})
	if len(ch) != 2 || ch[0].Line != 13 || ch[0].Outdated || ch[1].Line != 13 || ch[1].Outdated {
		t.Fatalf("re-anchored = %+v, want both on line 13 (the +y and -x lines)", ch)
	}
	// A comment on a leading blank line that the new unit no longer has
	// falls back to Anchor by line.
	blank := Anchor(draft("d3", "a.go", 10, model.SideNew), []model.ChangeUnit{v2}) // v2's first blank line
	v3 := v1
	v3.Range = model.Range{Old: model.Span{Start: 20, Count: 3}, New: model.Span{Start: 20, Count: 3}}
	if blank.UnitOffset != -3 {
		t.Fatalf("blank offset = %d, want -3", blank.UnitOffset)
	}
	if ch := Reanchor([]model.Comment{blank}, []model.ChangeUnit{v3}); len(ch) != 1 || !ch[0].Outdated {
		t.Errorf("blank line outside the new unit = %+v, want outdated", ch)
	}
}

// Drafts that move get the head their new lines describe.
func TestReanchorAtStampsCommit(t *testing.T) {
	before := []model.ChangeUnit{unit("u1", "a.go", 0, 10, 5, 10, 8)}
	after := []model.ChangeUnit{unit("u1", "a.go", 0, 10, 5, 15, 8)}
	d := Anchor(draft("d1", "a.go", 12, model.SideNew), before)
	d.CommitSHA = "h1"
	gh := model.Comment{ID: "gh-1", Path: "a.go", Line: 12, Side: model.SideNew, Source: model.CommentGitHub, State: model.CommentPosted, CommitSHA: "h0"}
	ch := ReanchorAt([]model.Comment{d, gh}, after, "h2")
	got := map[string]model.Comment{}
	for _, c := range ch {
		got[c.ID] = c
	}
	if c := got["d1"]; c.Line != 17 || c.CommitSHA != "h2" {
		t.Errorf("draft = %+v, want line 17 on h2", c)
	}
	if c, ok := got["gh-1"]; ok && c.CommitSHA != "h0" {
		t.Errorf("github comment commit changed: %+v", c)
	}
	// Unmoved but on another commit: still stamped.
	same := Anchor(draft("d2", "a.go", 12, model.SideNew), after)
	same.CommitSHA = "h1"
	if ch := ReanchorAt([]model.Comment{same}, after, "h2"); len(ch) != 1 || ch[0].CommitSHA != "h2" {
		t.Errorf("unmoved draft = %+v, want stamped h2", ch)
	}
}

// A finding on a line outside every unit is an outdated ai-draft, and
// Reanchor agrees, so it is neither rewritten every run nor ever sent.
func TestAIDraftOffDiffIsOutdated(t *testing.T) {
	units := []model.ChangeUnit{unit("u1", "a.go", 0, 10, 5, 10, 5)}
	f := model.Finding{UnitID: "u1", Path: "a.go", Line: 40, Severity: model.SevMedium, Claim: "Elsewhere."}
	ds := AIDrafts("pr:1", []model.Review{{GroupID: "g", Findings: []model.Finding{f}}}, units, nil, now)
	if len(ds) != 1 || !ds[0].Outdated || ds[0].UnitID != "" {
		t.Fatalf("drafts = %+v, want one outdated, unanchored", ds)
	}
	if ch := Reanchor(ds, units); len(ch) != 0 {
		t.Errorf("Reanchor changed the off-diff ai-draft: %+v", ch)
	}
	acc := Accept(ds[0], "d-1", "me", now)
	if send, skipped := ForSubmit([]model.Comment{acc}, nil); len(send) != 0 || len(skipped) != 1 {
		t.Errorf("accepted off-diff draft: send %v skipped %v", send, skipped)
	}
}

// When the id lookup after a submit failed, the local posted copy has no
// GHID: GitHub's copy must replace it, not sit next to it (regression).
func TestMergeGitHubReplacesPostedCopyWithoutGHID(t *testing.T) {
	local := model.Comment{ID: "d-1", Path: "a.go", Line: 12, Side: model.SideNew, Body: "Handle the error.", Author: "me",
		Source: model.CommentUser, State: model.CommentPosted, CommitSHA: "h1", ReviewID: 5}
	other := local
	other.ID, other.Body = "d-2", "Not posted on GitHub yet."
	// GitHub's copy, after a later push moved its current line.
	gh := model.Comment{ID: GitHubID(77), GHID: 77, Path: "a.go", Line: 20, OriginalLine: 12, Side: model.SideNew,
		Body: "Handle the error.\n", Author: "me", Source: model.CommentGitHub, State: model.CommentPosted, CommitSHA: "h1"}
	_, del := MergeGitHub([]model.Comment{local, other}, []model.Comment{gh})
	if !slices.Equal(del, []string{"d-1"}) {
		t.Errorf("del = %v, want the matched local copy only", del)
	}
	if got := ids(Previous([]model.Comment{local, other, gh}, "me", "h9")); got != "d-2,gh-77" {
		t.Errorf("previous = %s, want d-2,gh-77 (d-1 is GitHub's gh-77)", got)
	}
	wrongCommit := gh
	wrongCommit.CommitSHA = "h0"
	if _, del := MergeGitHub([]model.Comment{local}, []model.Comment{wrongCommit}); len(del) != 0 {
		t.Errorf("a comment on another commit must not match: del %v", del)
	}
}

// A review submitted on github.com after the user last opened the item in
// prt is "your last review": it wins over the older view mark.
func TestSinceBaseNewerReviewWins(t *testing.T) {
	t1 := now
	mark := NextViewMark(model.ViewMark{}, false, "pr:1", "h1", t1)
	reviewedLater := model.Item{ViewerReviewedSHA: "h2", ViewerReviewedAt: t1.Add(time.Hour)}
	if base, by := SinceBase(reviewedLater, "h3", mark, true); base != "h2" || by != model.SinceReviewed {
		t.Errorf("review after the look: %s/%s, want h2/reviewed", base, by)
	}
	reviewedEarlier := model.Item{ViewerReviewedSHA: "h0", ViewerReviewedAt: t1.Add(-time.Hour)}
	if base, by := SinceBase(reviewedEarlier, "h3", mark, true); base != "h1" || by != model.SinceViewed {
		t.Errorf("review before the look: %s/%s, want h1/viewed", base, by)
	}
	// Reading the analysed head: the base is the previous head, first looked
	// at PrevAt; a review after that wins.
	m2 := NextViewMark(mark, true, "pr:1", "h3", t1.Add(2*time.Hour))
	if m2.PrevAt != t1 {
		t.Fatalf("PrevAt = %v, want %v", m2.PrevAt, t1)
	}
	if base, _ := SinceBase(reviewedLater, "h3", m2, true); base != "h2" {
		t.Errorf("reading h3 after reviewing h2: base %s, want h2", base)
	}
	if base, _ := SinceBase(reviewedEarlier, "h3", m2, true); base != "h1" {
		t.Errorf("reading h3, review before h1: base %s, want h1", base)
	}
}
