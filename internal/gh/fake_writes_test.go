package gh

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// repoFixture is the shared offline fixture the demo and daemon tests use.
var repoFixture = filepath.Join("..", "..", "testdata", "fixtures", "prs.json")

func loadRepoFixture(t *testing.T) *Fake {
	t.Helper()
	f, err := LoadFixture(repoFixture)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFixtureThreadsNormalised(t *testing.T) {
	f := loadRepoFixture(t)
	if f.Repo != "acme/portal" {
		t.Errorf("Repo = %q, want acme/portal", f.Repo)
	}
	threads, err := f.Threads(context.Background(), 101)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 3 {
		t.Fatalf("#101 threads = %d, want 3", len(threads))
	}
	total := 0
	for _, th := range threads {
		for i, c := range th.Comments {
			total++
			wantState := model.CommentPosted
			if th.Resolved {
				wantState = model.CommentResolved
			}
			if c.ID != CommentID(c.GHID) || c.ItemID != "pr:101" || c.ThreadID != th.ID || c.Path != th.Path ||
				c.Side != th.Side || c.Line != th.Line || c.State != wantState || c.Source != model.CommentGitHub || c.Outdated != th.Outdated {
				t.Errorf("thread %s comment %d = %+v", th.ID, i, c)
			}
			if i > 0 && c.InReplyTo != th.Comments[0].GHID {
				t.Errorf("thread %s reply %d InReplyTo = %d", th.ID, i, c.InReplyTo)
			}
		}
	}
	if total != 5 {
		t.Errorf("comments = %d, want 5", total)
	}
	tok := threads[2]
	if tok.ID != "PRRT_fixture101_token" || !tok.Resolved || tok.Comments[0].State != model.CommentResolved {
		t.Errorf("token thread = %+v", tok)
	}
}

func TestFakeFromFixtureFillsComments(t *testing.T) {
	fx := Fixture{
		Repo: "acme/x",
		PRs:  []PR{{Number: 4}},
		Threads: map[string][]Thread{"4": {
			{ID: "T", Path: "a.go", OriginalLine: 9, Side: model.SideOld, Resolved: true, Outdated: true,
				Comments: []model.Comment{{GHID: 11, Body: "x"}, {ID: "keep", GHID: 12, Body: "y", Line: 3}}},
		}},
	}
	f, err := FakeFromFixture(fx)
	if err != nil {
		t.Fatal(err)
	}
	cs := f.ThreadMap[4][0].Comments
	want0 := model.Comment{ID: "gh-11", ItemID: "pr:4", ThreadID: "T", Path: "a.go", Line: 9, Side: model.SideOld,
		Body: "x", GHID: 11, Source: model.CommentGitHub, State: model.CommentResolved, Outdated: true}
	if cs[0] != want0 {
		t.Errorf("comment 0 = %+v\nwant %+v", cs[0], want0)
	}
	if cs[1].ID != "keep" || cs[1].Line != 3 {
		t.Errorf("comment 1 = %+v (explicit fields must be kept)", cs[1])
	}
	if &fx.Threads["4"][0].Comments[0] == &cs[0] || fx.Threads["4"][0].Comments[0].ID != "" {
		t.Error("normalisation must not alias or mutate the fixture")
	}
}

func TestFakeThreadsReturnsCopies(t *testing.T) {
	f := loadRepoFixture(t)
	ctx := context.Background()
	got, _ := f.Threads(ctx, 101)
	got[0].Resolved = true
	got[0].Comments[0].Body = "mutated"
	got[1].Comments = append(got[1].Comments, model.Comment{Body: "extra"})
	again, _ := f.Threads(ctx, 101)
	if again[0].Resolved || again[0].Comments[0].Body == "mutated" || len(again[1].Comments) != 1 {
		t.Errorf("mutating Threads' result changed the fake: %+v", again[:2])
	}
	if ts, err := f.Threads(ctx, 999); ts != nil || err != nil {
		t.Errorf("unknown PR = %v, %v", ts, err)
	}
}

func TestFakeSubmitReviewRecordsPayload(t *testing.T) {
	f := loadRepoFixture(t)
	in := submission()
	got, err := f.SubmitReview(context.Background(), 101, in)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := BuildReviewRequest(in)
	if len(f.Payloads) != 1 || len(f.Submitted) != 1 || len(f.PayloadPRs) != 1 || f.PayloadPRs[0] != 101 {
		t.Fatalf("recorded %d payloads, %d submissions, PRs %v", len(f.Payloads), len(f.Submitted), f.PayloadPRs)
	}
	gotJSON, _ := json.Marshal(f.Payloads[0])
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("payload = %s, want %s", gotJSON, wantJSON)
	}
	rangeJSON, _ := json.Marshal(f.Payloads[0].Comments[2])
	if string(rangeJSON) != `{"path":"web/src/range.ts","line":5,"side":"RIGHT","start_line":3,"start_side":"RIGHT","body":"Extract a helper."}` {
		t.Errorf("range comment = %s", rangeJSON)
	}
	if got.URL != "https://github.com/acme/portal/pull/101#pullrequestreview-1" {
		t.Errorf("URL = %q", got.URL)
	}
	for i, c := range got.Comments {
		if c.State != model.CommentPosted || c.ReviewID != 1 || c.ID != in.Comments[i].ID {
			t.Errorf("comment %d = %+v", i, c)
		}
	}
	bad := in
	bad.Event = "MERGE"
	if _, err := f.SubmitReview(context.Background(), 101, bad); !errors.Is(err, ErrBadReview) || len(f.Payloads) != 1 {
		t.Errorf("invalid review: err %v, payloads %d", err, len(f.Payloads))
	}
}

func TestFakeReply(t *testing.T) {
	f := loadRepoFixture(t)
	ctx := context.Background()
	c, err := f.Reply(ctx, "PRRT_fixture101_catch", "Still swallowed.")
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != CommentID(c.GHID) || c.ItemID != "pr:101" || c.ThreadID != "PRRT_fixture101_catch" || c.Line != 75 ||
		c.Side != model.SideNew || c.InReplyTo != 5003 || c.Author != "fredric" || c.State != model.CommentPosted {
		t.Errorf("reply = %+v", c)
	}
	if len(f.Replies) != 1 || f.Replies[0] != (FakeReply{ThreadID: "PRRT_fixture101_catch", Body: "Still swallowed."}) {
		t.Errorf("Replies = %+v", f.Replies)
	}
	threads, _ := f.Threads(ctx, 101)
	last := threads[1].Comments[len(threads[1].Comments)-1]
	if len(threads[1].Comments) != 2 || last != c {
		t.Errorf("thread comments = %+v", threads[1].Comments)
	}
	if _, err := f.Reply(ctx, "PRRT_fixture101_catch", " "); err == nil || len(f.Replies) != 1 {
		t.Errorf("empty reply: err %v, replies %d", err, len(f.Replies))
	}
}

func TestFakeResolveThread(t *testing.T) {
	f := loadRepoFixture(t)
	ctx := context.Background()
	if err := f.ResolveThread(ctx, "PRRT_fixture101_ttl"); err != nil {
		t.Fatal(err)
	}
	threads, _ := f.Threads(ctx, 101)
	if !threads[0].Resolved {
		t.Error("thread not resolved")
	}
	for _, c := range threads[0].Comments {
		if c.State != model.CommentResolved {
			t.Errorf("comment %s state = %s", c.ID, c.State)
		}
	}
	if len(f.Resolved) != 1 || f.Resolved[0] != "PRRT_fixture101_ttl" {
		t.Errorf("Resolved = %v", f.Resolved)
	}
	if threads[1].Resolved {
		t.Error("other thread resolved too")
	}
}

func TestFakeCreatePR(t *testing.T) {
	f := loadRepoFixture(t)
	f.HeadSHAs = map[string]string{"feat/new": "abc123"}
	hi := maxKey(f.PRs)
	p, err := f.CreatePR(context.Background(), NewPR{Title: "New", Head: "feat/new", Base: "main", Draft: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Number != hi+1 || p.HeadSHA != "abc123" || p.URL != "https://github.com/acme/portal/pull/"+strconv.Itoa(hi+1) ||
		p.State != "open" || !p.Draft || p.Author != "fredric" || p.BaseRef != "main" {
		t.Errorf("PR = %+v", p)
	}
	if got, _ := f.PR(context.Background(), p.Number); got.Title != "New" {
		t.Errorf("created PR not served: %+v", got)
	}
	if _, err := f.CreatePR(context.Background(), NewPR{Title: "x", Head: "h"}); !errors.Is(err, ErrBadPR) || len(f.Created) != 1 {
		t.Errorf("invalid PR: err %v, created %d", err, len(f.Created))
	}
}

func TestFakeWritesErr(t *testing.T) {
	boom := errors.New("boom")
	f := loadRepoFixture(t)
	f.Err = boom
	ctx := context.Background()
	calls := map[string]func() error{
		"Threads":       func() error { _, err := f.Threads(ctx, 101); return err },
		"SubmitReview":  func() error { _, err := f.SubmitReview(ctx, 101, submission()); return err },
		"Reply":         func() error { _, err := f.Reply(ctx, "PRRT_fixture101_ttl", "x"); return err },
		"ResolveThread": func() error { return f.ResolveThread(ctx, "PRRT_fixture101_ttl") },
		"CreatePR":      func() error { _, err := f.CreatePR(ctx, NewPR{Title: "t", Head: "h", Base: "main"}); return err },
	}
	for name, call := range calls {
		if err := call(); !errors.Is(err, boom) {
			t.Errorf("%s err = %v", name, err)
		}
	}
	if len(f.Submitted)+len(f.Payloads)+len(f.PayloadPRs)+len(f.Replies)+len(f.Resolved)+len(f.Created) != 0 {
		t.Error("a failing fake recorded a write")
	}
	f.Err = nil
	threads, _ := f.Threads(ctx, 101)
	if len(threads[0].Comments) != 2 || threads[0].Resolved {
		t.Error("a failing fake changed its threads")
	}
}
