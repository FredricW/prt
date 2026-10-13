package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
	"github.com/fredricw/prt/internal/testutil"
)

// jobLog replaces a client's queue so queued jobs are recorded, not run
// (the pipeline would otherwise rewrite the comments under test).
type jobLog struct {
	mu   sync.Mutex
	jobs []job
}

func captureJobs(c *InProcess) *jobLog {
	l := &jobLog{}
	c.q = newQueue(1, &c.wg, func(j job) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.jobs = append(l.jobs, j)
	})
	return l
}

func (l *jobLog) snapshot() []job {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]job(nil), l.jobs...)
}

// waitJobs waits until at least n jobs ran and returns them.
func (l *jobLog) waitJobs(t *testing.T, n int) []job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		js := l.snapshot()
		if len(js) >= n {
			return js
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d jobs, want %d", len(js), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const (
	cItem     = "pr:7"
	cAnalysed = "h1"
)

// commentUnits: a.go new lines 10–14 (old 10–12) and 30–31; b.go old 5–6
// (a deletion).
func commentUnits() []model.ChangeUnit {
	return []model.ChangeUnit{
		{ID: "u1", ItemID: cItem, Path: "a.go", Range: model.Range{Old: model.Span{Start: 10, Count: 3}, New: model.Span{Start: 10, Count: 5}}, Order: 0},
		{ID: "u2", ItemID: cItem, Path: "a.go", Range: model.Range{Old: model.Span{Start: 28, Count: 0}, New: model.Span{Start: 30, Count: 2}}, Order: 1},
		{ID: "u3", ItemID: cItem, Path: "b.go", Range: model.Range{Old: model.Span{Start: 5, Count: 2}}, Order: 2},
	}
}

// commentClient returns a client over a store seeded with PR #7 (analysed
// at h1, head moved to h2) and its units, with jobs recorded.
func commentClient(t *testing.T, ghc gh.Client) (*InProcess, store.Store, *jobLog) {
	t.Helper()
	st := newTestStore(t)
	put(t, st, model.Item{
		ID: cItem, Kind: model.KindPR, Number: 7, Ref: "feat/c", BaseRef: "main",
		HeadSHA: "h2", AnalysedSHA: cAnalysed, Source: model.SourceRanked, Section: model.SectionNeedsYou, OpenThreads: 2,
	})
	if err := st.PutUnits(context.Background(), cItem, commentUnits()); err != nil {
		t.Fatal(err)
	}
	c := newClient(t, st, &gitx.Fake{}, ghc, nil)
	return c, st, captureJobs(c)
}

func putComments(t *testing.T, st store.Store, cs ...model.Comment) {
	t.Helper()
	for _, cm := range cs {
		if err := st.PutComment(context.Background(), cm); err != nil {
			t.Fatal(err)
		}
	}
}

func storedComments(t *testing.T, st store.Store, itemID string) map[string]model.Comment {
	t.Helper()
	cs, err := st.Comments(context.Background(), itemID)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]model.Comment{}
	for _, cm := range cs {
		m[cm.ID] = cm
	}
	return m
}

func userDraft(id, path string, line int, body string) model.Comment {
	return model.Comment{ID: id, ItemID: cItem, Path: path, Line: line, Side: model.SideNew, Body: body,
		Author: "fredric", Source: model.CommentUser, State: model.CommentDraft, CommitSHA: cAnalysed, CreatedAt: testNow}
}

func aiDraftComment() model.Comment {
	return model.Comment{ID: "ai-k1", ItemID: cItem, UnitID: "u1", UnitOffset: 1, Path: "a.go", Line: 11, Side: model.SideNew,
		Body: "**high** nil deref", Author: "claude", Source: model.CommentAIDraft, State: model.CommentDraft,
		FindingKey: "k1", Severity: model.SevHigh, CreatedAt: testNow}
}

func wantErr(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), sub) {
		t.Fatalf("err = %v, want it to mention %q", err, sub)
	}
}

func TestSaveCommentCreates(t *testing.T) {
	cases := []struct {
		name               string
		in                 model.Comment
		gh                 bool
		wantUnit           string
		wantOff            int
		wantSide           model.Side
		wantStart          int
		wantAuthor         string
		wantStartSideEmpty bool
	}{
		{name: "new side default", in: model.Comment{Path: "a.go", Line: 12, Body: "why?"}, gh: true, wantUnit: "u1", wantOff: 2, wantSide: model.SideNew, wantAuthor: "fredric"},
		{name: "second unit", in: model.Comment{Path: "a.go", Line: 31, Side: model.SideNew, Body: "x"}, gh: true, wantUnit: "u2", wantOff: 1, wantSide: model.SideNew, wantAuthor: "fredric"},
		{name: "old side", in: model.Comment{Path: "b.go", Line: 6, Side: model.SideOld, Body: "gone?"}, gh: true, wantUnit: "u3", wantOff: 1, wantSide: model.SideOld, wantAuthor: "fredric"},
		{name: "range", in: model.Comment{Path: "a.go", Line: 13, StartLine: 11, StartSide: model.SideNew, Body: "helper"}, gh: true, wantUnit: "u1", wantOff: 3, wantSide: model.SideNew, wantStart: 11, wantAuthor: "fredric", wantStartSideEmpty: true},
		{name: "one-line range is a line", in: model.Comment{Path: "a.go", Line: 13, StartLine: 13, Body: "x"}, gh: true, wantUnit: "u1", wantOff: 3, wantSide: model.SideNew, wantAuthor: "fredric"},
		{name: "offline author", in: model.Comment{Path: "a.go", Line: 10, Body: "x"}, wantUnit: "u1", wantSide: model.SideNew, wantAuthor: fallbackAuthor},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var ghc gh.Client
			if tc.gh {
				ghc = &gh.Fake{Login: "fredric"}
			}
			c, st, _ := commentClient(t, ghc)
			tc.in.UnitID = "bogus" // the daemon anchors; the client's anchor is ignored
			got, err := c.SaveComment(ctx, proto.CommentParams{ItemID: cItem, Comment: tc.in})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(got.ID, "d-") || got.ItemID != cItem || got.Source != model.CommentUser || got.State != model.CommentDraft {
				t.Errorf("identity = %+v", got)
			}
			if got.UnitID != tc.wantUnit || got.UnitOffset != tc.wantOff || got.Side != tc.wantSide || got.StartLine != tc.wantStart {
				t.Errorf("anchor = unit %q off %d side %q start %d", got.UnitID, got.UnitOffset, got.Side, got.StartLine)
			}
			if got.Author != tc.wantAuthor || got.CommitSHA != cAnalysed || !got.CreatedAt.Equal(testNow) || !got.UpdatedAt.Equal(testNow) {
				t.Errorf("author %q commit %q created %v updated %v", got.Author, got.CommitSHA, got.CreatedAt, got.UpdatedAt)
			}
			if tc.wantStartSideEmpty && got.StartSide != "" {
				t.Errorf("start side %q, want \"\" (same as side)", got.StartSide)
			}
			if s, ok := storedComments(t, st, cItem)[got.ID]; !ok || s.Body != tc.in.Body {
				t.Errorf("stored = %+v, %v", s, ok)
			}
		})
	}
}

// A V selection from a deleted line down to an added one is a LEFT→RIGHT
// range: kept as sent even when the line numbers are equal or the old one
// is larger (regression: collapsed to a line, or refused).
func TestSaveCommentCrossSideRange(t *testing.T) {
	cases := []struct {
		name        string
		start, line int
	}{
		{"equal numbers", 11, 11},
		{"old number larger", 12, 11},
		{"old number smaller", 10, 13},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, st, _ := commentClient(t, &gh.Fake{Login: "fredric"})
			in := model.Comment{Path: "a.go", Line: tc.line, Side: model.SideNew, StartLine: tc.start, StartSide: model.SideOld, Body: "x"}
			got, err := c.SaveComment(context.Background(), proto.CommentParams{ItemID: cItem, Comment: in})
			if err != nil {
				t.Fatal(err)
			}
			if got.StartLine != tc.start || got.StartSide != model.SideOld || got.Line != tc.line || got.Side != model.SideNew {
				t.Errorf("saved = start %d (%s) line %d (%s)", got.StartLine, got.StartSide, got.Line, got.Side)
			}
			if got.UnitID != "u1" || got.StartOffset != tc.start-10 {
				t.Errorf("anchor = %q start offset %d", got.UnitID, got.StartOffset)
			}
			if s := storedComments(t, st, cItem)[got.ID]; s.StartLine != tc.start || s.StartSide != model.SideOld {
				t.Errorf("stored = %+v", s)
			}
		})
	}
	c, _, _ := commentClient(t, &gh.Fake{Login: "fredric"})
	_, err := c.SaveComment(context.Background(), proto.CommentParams{ItemID: cItem,
		Comment: model.Comment{Path: "a.go", Line: 11, Side: model.SideNew, StartLine: 20, StartSide: model.SideOld, Body: "x"}})
	wantErr(t, err, "start line not in the diff")
}

// Pending comments left by a process that died mid-submission are drafts
// again when a client starts (regression: stuck pending for good).
func TestStartupRecoversPendingComments(t *testing.T) {
	st := newTestStore(t)
	put(t, st, model.Item{ID: cItem, Kind: model.KindPR, Number: 7, HeadSHA: "h2", AnalysedSHA: cAnalysed})
	pending := userDraft("d-p", "a.go", 12, "in flight")
	pending.State = model.CommentPending
	posted := userDraft("d-posted", "a.go", 11, "sent")
	posted.State, posted.GHID = model.CommentPosted, 4
	putComments(t, st, pending, posted)
	newClient(t, st, &gitx.Fake{}, nil, nil)
	cs := storedComments(t, st, cItem)
	if cs["d-p"].State != model.CommentDraft || cs["d-p"].Body != "in flight" {
		t.Errorf("pending = %+v, want a draft again", cs["d-p"])
	}
	if cs["d-posted"].State != model.CommentPosted {
		t.Errorf("posted comment changed: %+v", cs["d-posted"])
	}
}

func TestSaveCommentDraftIDsUnique(t *testing.T) {
	c, _, _ := commentClient(t, nil)
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		got, err := c.SaveComment(context.Background(), proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "a.go", Line: 12, Body: "x"}})
		if err != nil {
			t.Fatal(err)
		}
		if seen[got.ID] {
			t.Fatalf("duplicate id %s", got.ID)
		}
		seen[got.ID] = true
	}
}

func TestSaveCommentRefusals(t *testing.T) {
	cases := []struct {
		name string
		p    proto.CommentParams
		want string
	}{
		{"unknown item", proto.CommentParams{ItemID: "pr:404", Comment: model.Comment{Path: "a.go", Line: 12, Body: "x"}}, "not found"},
		{"item mismatch", proto.CommentParams{ItemID: cItem, Comment: model.Comment{ItemID: "pr:8", Path: "a.go", Line: 12, Body: "x"}}, "sent for item"},
		{"blank body", proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "a.go", Line: 12, Body: "  \n"}}, "empty body"},
		{"no line", proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "a.go", Body: "x"}}, "needs a path and a line"},
		{"line outside units", proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "a.go", Line: 20, Body: "x"}}, "line not in the diff"},
		{"wrong side", proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "b.go", Line: 6, Side: model.SideNew, Body: "x"}}, "line not in the diff"},
		{"other file", proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "c.go", Line: 12, Body: "x"}}, "line not in the diff"},
		{"start after line", proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "a.go", Line: 12, StartLine: 13, Body: "x"}}, "must come before"},
		{"start outside units", proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "a.go", Line: 12, StartLine: 3, Body: "x"}}, "start line not in the diff"},
		{"bad side", proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "a.go", Line: 12, Side: "left", Body: "x"}}, "unknown side"},
		{"edit unknown", proto.CommentParams{ItemID: cItem, Comment: model.Comment{ID: "d-nope", Path: "a.go", Line: 12, Body: "x"}}, "not found"},
		{"edit ai-draft", proto.CommentParams{ItemID: cItem, Comment: model.Comment{ID: "ai-k1", Path: "a.go", Line: 12, Body: "x"}}, "only your drafts can be edited"},
		{"edit posted", proto.CommentParams{ItemID: cItem, Comment: model.Comment{ID: "d-posted", Path: "a.go", Line: 12, Body: "x"}}, "only your drafts can be edited"},
		{"edit github", proto.CommentParams{ItemID: cItem, Comment: model.Comment{ID: "gh-1", Path: "a.go", Line: 12, Body: "x"}}, "only your drafts can be edited"},
		{"delete ai-draft", proto.CommentParams{ItemID: cItem, Delete: true, Comment: model.Comment{ID: "ai-k1"}}, "only your drafts can be deleted"},
		{"delete posted", proto.CommentParams{ItemID: cItem, Delete: true, Comment: model.Comment{ID: "d-posted"}}, "only your drafts can be deleted"},
		{"delete github", proto.CommentParams{ItemID: cItem, Delete: true, Comment: model.Comment{ID: "gh-1"}}, "only your drafts can be deleted"},
		{"delete without id", proto.CommentParams{ItemID: cItem, Delete: true}, "no comment id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, st, _ := commentClient(t, &gh.Fake{Login: "fredric"})
			posted := userDraft("d-posted", "a.go", 12, "sent")
			posted.State, posted.GHID = model.CommentPosted, 9
			ghc := model.Comment{ID: "gh-1", ItemID: cItem, Path: "a.go", Line: 12, Body: "theirs", Source: model.CommentGitHub, State: model.CommentPosted, GHID: 1}
			putComments(t, st, aiDraftComment(), posted, ghc)
			before := storedComments(t, st, cItem)
			_, err := c.SaveComment(context.Background(), tc.p)
			wantErr(t, err, tc.want)
			after := storedComments(t, st, cItem)
			if len(after) != len(before) {
				t.Fatalf("store changed: %d → %d comments", len(before), len(after))
			}
			for id, b := range before {
				if after[id] != b {
					t.Errorf("%s changed: %+v → %+v", id, b, after[id])
				}
			}
		})
	}
}

func TestSaveCommentNotAnalysed(t *testing.T) {
	st := newTestStore(t)
	put(t, st, model.Item{ID: cItem, Kind: model.KindPR, Number: 7, HeadSHA: "h2"})
	c := newClient(t, st, &gitx.Fake{}, nil, nil)
	_, err := c.SaveComment(context.Background(), proto.CommentParams{ItemID: cItem, Comment: model.Comment{Path: "a.go", Line: 12, Body: "x"}})
	wantErr(t, err, "not analysed yet")
}

func TestSaveCommentEditAndDelete(t *testing.T) {
	ctx := context.Background()
	c, st, _ := commentClient(t, &gh.Fake{Login: "fredric"})
	created := testNow.Add(-time.Hour)
	d := userDraft("d-1", "a.go", 12, "first")
	d.UnitID, d.UnitOffset, d.CreatedAt, d.CommitSHA, d.Author = "u1", 2, created, "h0", "someone"
	putComments(t, st, d)
	drain(c.Events())

	// Move it to the second unit and change the body.
	got, err := c.SaveComment(ctx, proto.CommentParams{ItemID: cItem, Comment: model.Comment{ID: "d-1", Path: "ignored.go", Line: 31, Body: "second", Author: "x", Source: model.CommentGitHub}})
	if err != nil {
		t.Fatal(err)
	}
	want := d
	want.Body, want.Line, want.UnitID, want.UnitOffset, want.CommitSHA, want.UpdatedAt = "second", 31, "u2", 1, cAnalysed, testNow
	if got != want {
		t.Fatalf("edited = %+v\nwant     %+v", got, want)
	}
	if s := storedComments(t, st, cItem)["d-1"]; s != want {
		t.Fatalf("stored = %+v", s)
	}
	if !hasPatch(drain(c.Events()), cItem) {
		t.Error("no patch event after edit")
	}

	// A body-only edit keeps the position.
	got, err = c.SaveComment(ctx, proto.CommentParams{ItemID: cItem, Comment: model.Comment{ID: "d-1", Body: "third"}})
	if err != nil || got.Line != 31 || got.UnitID != "u2" || got.Body != "third" {
		t.Fatalf("body edit = %+v, %v", got, err)
	}

	got, err = c.SaveComment(ctx, proto.CommentParams{ItemID: cItem, Delete: true, Comment: model.Comment{ID: "d-1"}})
	if err != nil || got != (model.Comment{}) {
		t.Fatalf("delete = %+v, %v", got, err)
	}
	if _, ok := storedComments(t, st, cItem)["d-1"]; ok {
		t.Fatal("draft not deleted")
	}
	if !hasPatch(drain(c.Events()), cItem) {
		t.Error("no patch event after delete")
	}
}

func hasPatch(evs []proto.Event, itemID string) bool {
	for _, e := range evs {
		if e.Type == proto.EventPatch && e.ItemID == itemID {
			return true
		}
	}
	return false
}

func findingMarks(t *testing.T, st store.Store) []model.FindingMark {
	t.Helper()
	ms, err := st.FindingMarks(context.Background(), cItem)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestCommentActionAccept(t *testing.T) {
	ctx := context.Background()
	c, st, jobs := commentClient(t, &gh.Fake{Login: "fredric"})
	putComments(t, st, aiDraftComment())
	got, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: cItem, Action: proto.CommentAccept, CommentID: "ai-k1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.ID, "d-") || got.Source != model.CommentUser || got.State != model.CommentDraft ||
		got.Author != "fredric" || got.FindingKey != "k1" || got.Severity != "" || got.UnitID != "u1" ||
		got.Line != 11 || got.Body != "**high** nil deref" || got.CommitSHA != cAnalysed {
		t.Fatalf("accepted = %+v", got)
	}
	cs := storedComments(t, st, cItem)
	if _, ok := cs["ai-k1"]; ok || cs[got.ID] != got {
		t.Fatalf("stored = %+v", cs)
	}
	ms := findingMarks(t, st)
	if len(ms) != 1 || ms[0].State != model.FindingAccepted || ms[0].FindingKey != "k1" || ms[0].CommentID != got.ID {
		t.Fatalf("marks = %+v", ms)
	}
	if !hasPatch(drain(c.Events()), cItem) {
		t.Error("no patch event")
	}
	if js := jobs.snapshot(); len(js) != 0 {
		t.Errorf("accept queued jobs: %+v", js)
	}
}

func TestCommentActionDismiss(t *testing.T) {
	ctx := context.Background()
	t.Run("plain", func(t *testing.T) {
		c, st, jobs := commentClient(t, nil)
		putComments(t, st, aiDraftComment())
		got, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: cItem, Action: proto.CommentDismiss, CommentID: "ai-k1"})
		if err != nil || got != (model.Comment{}) {
			t.Fatalf("dismiss = %+v, %v", got, err)
		}
		if len(storedComments(t, st, cItem)) != 0 {
			t.Fatal("ai-draft not removed")
		}
		ms := findingMarks(t, st)
		if len(ms) != 1 || ms[0].State != model.FindingDismissed || ms[0].Override != nil {
			t.Fatalf("marks = %+v", ms)
		}
		if js := jobs.snapshot(); len(js) != 0 {
			t.Errorf("plain dismiss queued jobs: %+v", js)
		}
		// Detail carries the mark, so the Code page stops drawing the finding.
		d, err := c.Detail(ctx, cItem)
		if err != nil || len(d.FindingMarks) != 1 || d.FindingMarks[0].FindingKey != ms[0].FindingKey {
			t.Errorf("Detail.FindingMarks = %+v, %v", d.FindingMarks, err)
		}
	})
	t.Run("with override", func(t *testing.T) {
		c, st, jobs := commentClient(t, nil)
		putComments(t, st, aiDraftComment())
		o := &model.Override{UnitID: "u1", Question: "risk", Value: "low"}
		if _, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: cItem, Action: proto.CommentDismiss, CommentID: "ai-k1", Override: o}); err != nil {
			t.Fatal(err)
		}
		ms := findingMarks(t, st)
		if len(ms) != 1 || ms[0].Override == nil || ms[0].Override.Value != "low" || !ms[0].Override.At.Equal(testNow) {
			t.Fatalf("marks = %+v", ms)
		}
		ovs, err := st.Overrides(ctx, []string{"u1"})
		if err != nil || len(ovs) != 1 || ovs[0].Question != "risk" || ovs[0].Value != "low" {
			t.Fatalf("overrides = %+v, %v", ovs, err)
		}
		js := jobs.waitJobs(t, 1)
		if js[0].itemID != cItem || js[0].force || !js[0].rerun {
			t.Errorf("job = %+v", js[0])
		}
	})
	t.Run("override without question", func(t *testing.T) {
		c, st, _ := commentClient(t, nil)
		putComments(t, st, aiDraftComment())
		_, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: cItem, Action: proto.CommentDismiss, CommentID: "ai-k1", Override: &model.Override{UnitID: "u1"}})
		wantErr(t, err, "needs a unit and a question")
		if len(storedComments(t, st, cItem)) != 1 || len(findingMarks(t, st)) != 0 {
			t.Fatal("a refused dismiss changed the store")
		}
	})
}

func TestCommentActionRefusals(t *testing.T) {
	cases := []struct {
		name string
		p    proto.CommentActionParams
		want string
	}{
		{"accept user draft", proto.CommentActionParams{Action: proto.CommentAccept, CommentID: "d-1"}, "not an ai-draft"},
		{"accept unknown", proto.CommentActionParams{Action: proto.CommentAccept, CommentID: "ai-zz"}, "not found"},
		{"dismiss user draft", proto.CommentActionParams{Action: proto.CommentDismiss, CommentID: "d-1"}, "not an ai-draft"},
		{"unknown action", proto.CommentActionParams{Action: "approve"}, "unknown action"},
		{"reply without thread", proto.CommentActionParams{Action: proto.CommentReply, Body: "x"}, "no thread"},
		{"reply blank", proto.CommentActionParams{Action: proto.CommentReply, ThreadID: "T1", Body: " "}, "empty body"},
		{"resolve without thread", proto.CommentActionParams{Action: proto.CommentResolve}, "no thread"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &gh.Fake{Login: "fredric"}
			c, st, _ := commentClient(t, fake)
			putComments(t, st, aiDraftComment(), userDraft("d-1", "a.go", 12, "x"))
			tc.p.ItemID = cItem
			_, err := c.CommentAction(context.Background(), tc.p)
			wantErr(t, err, tc.want)
			if len(storedComments(t, st, cItem)) != 2 || len(findingMarks(t, st)) != 0 || len(fake.Replies) != 0 || len(fake.Resolved) != 0 {
				t.Fatal("a refused action wrote something")
			}
		})
	}
}

// fixtureClient seeds PR #101 from the offline GitHub fixture.
func fixtureClient(t *testing.T) (*InProcess, store.Store, *gh.Fake) {
	t.Helper()
	fake, err := gh.LoadFixture(testutil.FixturePRs(t))
	if err != nil {
		t.Fatal(err)
	}
	st := newTestStore(t)
	id := model.PRItemID(101)
	put(t, st, model.Item{ID: id, Kind: model.KindPR, Number: 101, HeadSHA: "b3ca0eb0e8ea06083fd828b2fe49a4c8c4ea98ea", OpenThreads: 2})
	for _, th := range fake.ThreadMap[101] {
		putComments(t, st, th.Comments...)
	}
	c := newClient(t, st, &gitx.Fake{}, fake, nil)
	captureJobs(c)
	return c, st, fake
}

func TestCommentActionReplyAndResolve(t *testing.T) {
	ctx := context.Background()
	id := model.PRItemID(101)
	const thread = "PRRT_fixture101_catch"

	t.Run("reply", func(t *testing.T) {
		c, st, fake := fixtureClient(t)
		got, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: id, Action: proto.CommentReply, ThreadID: thread, Body: "Fixed in the next push."})
		if err != nil {
			t.Fatal(err)
		}
		if len(fake.Replies) != 1 || fake.Replies[0] != (gh.FakeReply{ThreadID: thread, Body: "Fixed in the next push."}) {
			t.Fatalf("replies = %+v", fake.Replies)
		}
		if got.ID != "gh-900001" || got.ItemID != id || got.ThreadID != thread || got.InReplyTo != 5003 ||
			got.Source != model.CommentGitHub || got.State != model.CommentPosted || got.Author != "fredric" || got.CreatedAt.IsZero() {
			t.Fatalf("reply = %+v", got)
		}
		if s, ok := storedComments(t, st, id)[got.ID]; !ok || s != got {
			t.Fatalf("stored reply = %+v, %v", s, ok)
		}
		if !hasPatch(drain(c.Events()), id) {
			t.Error("no patch event")
		}
	})

	t.Run("resolve", func(t *testing.T) {
		c, st, fake := fixtureClient(t)
		got, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: id, Action: proto.CommentResolve, ThreadID: thread})
		if err != nil || got != (model.Comment{}) {
			t.Fatalf("resolve = %+v, %v", got, err)
		}
		if len(fake.Resolved) != 1 || fake.Resolved[0] != thread {
			t.Fatalf("resolved = %v", fake.Resolved)
		}
		cs := storedComments(t, st, id)
		if cs["gh-5003"].State != model.CommentResolved || cs["gh-5001"].State != model.CommentPosted {
			t.Fatalf("states: 5003 %s, 5001 %s", cs["gh-5003"].State, cs["gh-5001"].State)
		}
		it, err := st.Item(ctx, id)
		if err != nil || it.OpenThreads != 1 {
			t.Fatalf("open threads = %d, %v", it.OpenThreads, err)
		}
		// Resolving again is idempotent locally.
		if _, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: id, Action: proto.CommentResolve, ThreadID: thread}); err != nil {
			t.Fatal(err)
		}
		if it, _ := st.Item(ctx, id); it.OpenThreads != 1 {
			t.Fatalf("second resolve changed open threads to %d", it.OpenThreads)
		}
	})

	t.Run("GitHub error", func(t *testing.T) {
		c, st, fake := fixtureClient(t)
		fake.Err = errors.New("502 bad gateway")
		before := storedComments(t, st, id)
		_, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: id, Action: proto.CommentResolve, ThreadID: thread})
		wantErr(t, err, "502")
		if after := storedComments(t, st, id); after["gh-5003"] != before["gh-5003"] {
			t.Fatal("failed resolve changed the stored thread")
		}
	})
}

// A reply takes its position and side from the thread's stored comments
// (the mutation has no diff side): a reply on a LEFT thread is anchored on
// the old side (regression: stored with Side "" and anchored as new).
func TestReplyCopiesThreadSide(t *testing.T) {
	ctx := context.Background()
	c, st, _ := commentClient(t, &gh.Fake{Login: "fredric"})
	first := model.Comment{ID: "gh-1", ItemID: cItem, Path: "b.go", Line: 6, Side: model.SideOld, Body: "why drop this?",
		Source: model.CommentGitHub, State: model.CommentPosted, GHID: 1, ThreadID: "T1", UnitID: "u3", UnitOffset: 1}
	putComments(t, st, first)
	got, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: cItem, Action: proto.CommentReply, ThreadID: "T1", Body: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != "b.go" || got.Line != 6 || got.Side != model.SideOld || got.UnitID != "u3" || got.UnitOffset != 1 {
		t.Errorf("reply = %+v, want b.go:6 old side in u3", got)
	}
}

func TestCommentActionNeedsGitHub(t *testing.T) {
	ctx := context.Background()
	for _, a := range []proto.CommentAction{proto.CommentReply, proto.CommentResolve} {
		t.Run(string(a), func(t *testing.T) {
			c, st, _ := commentClient(t, nil)
			c.SetGitHubNote("no GitHub token (run `gh auth login`)")
			_, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: cItem, Action: a, ThreadID: "T1", Body: "x"})
			wantErr(t, err, "GitHub unavailable: no GitHub token")
			if len(storedComments(t, st, cItem)) != 0 {
				t.Fatal("stored something without GitHub")
			}
		})
	}
	t.Run("branch item", func(t *testing.T) {
		c, st, _ := commentClient(t, &gh.Fake{})
		put(t, st, branchItem("feat/x", 1))
		_, err := c.CommentAction(ctx, proto.CommentActionParams{ItemID: model.BranchItemID("feat/x"), Action: proto.CommentReply, ThreadID: "T1", Body: "x"})
		wantErr(t, err, "not a pull request")
	})
}

func TestMarkViewedSequence(t *testing.T) {
	ctx := context.Background()
	c, st, jobs := commentClient(t, nil)
	steps := []struct {
		head string
		want model.ViewMark
	}{
		{"h1", model.ViewMark{ItemID: cItem, HeadSHA: "h1", At: testNow}},
		{"h1", model.ViewMark{ItemID: cItem, HeadSHA: "h1", At: testNow}},
		{"", model.ViewMark{ItemID: cItem, HeadSHA: "h1", At: testNow}},
		{"h2", model.ViewMark{ItemID: cItem, HeadSHA: "h2", PrevHeadSHA: "h1", At: testNow.Add(time.Minute)}},
	}
	for i, s := range steps {
		now := testNow.Add(time.Duration(i/3) * time.Minute)
		c.now = func() time.Time { return now }
		if err := c.MarkViewed(ctx, proto.ViewedParams{ItemID: cItem, HeadSHA: s.head}); err != nil {
			t.Fatal(err)
		}
		got, ok, err := st.ViewMark(ctx, cItem)
		if err != nil || !ok || got.ItemID != s.want.ItemID || got.HeadSHA != s.want.HeadSHA ||
			got.PrevHeadSHA != s.want.PrevHeadSHA || !got.At.Equal(s.want.At) {
			t.Fatalf("step %d (%q): mark = %+v, %v, %v; want %+v", i, s.head, got, ok, err, s.want)
		}
	}
	if js := jobs.snapshot(); len(js) != 0 {
		t.Errorf("MarkViewed queued jobs: %+v", js)
	}
	if err := c.MarkViewed(ctx, proto.ViewedParams{ItemID: "pr:404", HeadSHA: "h1"}); err == nil {
		t.Error("unknown item accepted")
	}
}

func TestMarkViewedEmptyHeadStoresNothing(t *testing.T) {
	ctx := context.Background()
	c, st, _ := commentClient(t, nil)
	if err := c.MarkViewed(ctx, proto.ViewedParams{ItemID: cItem}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ViewMark(ctx, cItem); err != nil || ok {
		t.Fatalf("mark stored for an empty head: %v, %v", ok, err)
	}
}

func TestOverride(t *testing.T) {
	ctx := context.Background()
	c, st, jobs := commentClient(t, nil)
	for _, bad := range []model.Override{{Question: "risk"}, {UnitID: "u1"}, {UnitID: " ", Question: "risk"}} {
		wantErr(t, c.Override(ctx, proto.OverrideParams{ItemID: cItem, Override: bad}), "needs a unit and a question")
	}
	wantErr(t, c.Override(ctx, proto.OverrideParams{ItemID: "pr:404", Override: model.Override{UnitID: "u1", Question: "risk"}}), "not found")
	drain(c.Events())
	if err := c.Override(ctx, proto.OverrideParams{ItemID: cItem, Override: model.Override{UnitID: "u1", Question: "risk", Value: "high"}}); err != nil {
		t.Fatal(err)
	}
	ovs, err := st.Overrides(ctx, []string{"u1"})
	if err != nil || len(ovs) != 1 || ovs[0].Value != "high" || !ovs[0].At.Equal(testNow) {
		t.Fatalf("overrides = %+v, %v", ovs, err)
	}
	js := jobs.waitJobs(t, 1)
	if len(js) != 1 || js[0].itemID != cItem || js[0].force || !js[0].rerun {
		t.Fatalf("jobs = %+v", js)
	}
	if !hasPatch(drain(c.Events()), cItem) {
		t.Error("no patch event")
	}
}

func TestDetailSortsComments(t *testing.T) {
	c, st, _ := commentClient(t, nil)
	putComments(t, st,
		userDraft("d-c", "b.go", 1, "x"),
		userDraft("d-b", "a.go", 12, "x"),
		model.Comment{ID: "d-a", ItemID: cItem, Path: "a.go", Line: 12, StartLine: 11, Body: "x", Source: model.CommentUser, State: model.CommentDraft},
		userDraft("d-0", "a.go", 12, "x"),
		userDraft("d-z", "a.go", 3, "x"),
	)
	det, err := c.Detail(context.Background(), cItem)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, cm := range det.Comments {
		ids = append(ids, cm.ID)
	}
	if got := strings.Join(ids, ","); got != "d-z,d-0,d-b,d-a,d-c" {
		t.Fatalf("order = %s", got)
	}
}

// Discovery copies the commit of the viewer's latest review.
func TestDiscoveryCopiesViewerReviewedSHA(t *testing.T) {
	ctx := context.Background()
	fake, err := gh.LoadFixture(testutil.FixturePRs(t))
	if err != nil {
		t.Fatal(err)
	}
	st := newTestStore(t)
	c := newClient(t, st, &gitx.Fake{Default: "main"}, fake, nil)
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	it, err := st.Item(ctx, model.PRItemID(101))
	if err != nil {
		t.Fatal(err)
	}
	if it.ViewerReviewedSHA != "e661461bccca9ae9395ab9e74da9836914d81a0c" {
		t.Fatalf("ViewerReviewedSHA = %q", it.ViewerReviewedSHA)
	}
	if it, _ := st.Item(ctx, model.PRItemID(102)); it.ViewerReviewedSHA != "" {
		t.Fatalf("#102 ViewerReviewedSHA = %q", it.ViewerReviewedSHA)
	}
}
