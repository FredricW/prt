package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline/pipelinetest"
	"github.com/fredricw/prt/internal/review"
)

// A truncated call is recorded and counted as written and truncated, not as
// a failure (the pipeline retries it with a larger cap).
func TestReviewGuardTruncationIsNotAFailure(t *testing.T) {
	dir := t.TempDir()
	b := &reviewBudget{capUSD: 3, totalCap: 5}
	live := &review.Fake{Name: haiku, Respond: review.FakeTruncate(1<<20, review.FakeRespond(0.9))}
	g := &reviewGuard{next: &review.Recorder{Live: live, Dir: dir}, dir: dir, budget: b}
	req := tldrReq("cut off")
	if _, err := g.Review(context.Background(), req); !errors.Is(err, review.ErrTruncated) {
		t.Fatalf("err = %v, want a truncation", err)
	}
	st := g.stats()
	if st.written != 1 || len(st.truncated) != 1 || len(st.failures) != 0 || st.calls != 1 {
		t.Fatalf("stats %+v", st)
	}
	// The truncated recording is served again without a call.
	if _, err := g.Review(context.Background(), req); !errors.Is(err, review.ErrTruncated) || len(live.Calls) != 1 {
		t.Fatalf("replayed truncation: %v, %d live calls", err, len(live.Calls))
	}
	if st := g.stats(); st.reused != 1 {
		t.Fatalf("stats after replay %+v", st)
	}
}

func TestFailedRows(t *testing.T) {
	outs := map[int]pipelinetest.Outcome{101: {Reviews: []model.Review{
		{GroupName: "a", Tier: model.TierDeep, Status: model.ReviewDone},
		{GroupName: "b", Tier: model.TierFast, Status: model.ReviewFailed, Err: "boom"},
	}}}
	branches := map[string]pipelinetest.Outcome{pipelinetest.BranchRefs[0]: {Reviews: []model.Review{
		{GroupName: "c", Tier: model.TierFast, Status: model.ReviewFailed, Err: "cut off"},
	}}}
	got := failedRows(outs, branches)
	if len(got) != 2 || !strings.Contains(got[0], `#101 group "b" (fast): boom`) || !strings.Contains(got[1], "cut off") {
		t.Fatalf("failed rows %q", got)
	}
	if len(failedRows(nil, nil)) != 0 {
		t.Error("no outcomes: want no rows")
	}
}

func TestUnusedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"aaa.json", "bbb.json", "ccc.json", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := unusedFiles(dir, map[string]bool{"bbb": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || filepath.Base(got[0]) != "aaa.json" || filepath.Base(got[1]) != "ccc.json" {
		t.Fatalf("unused %v", got)
	}
}

func TestKeyLog(t *testing.T) {
	l := &keyLog{}
	l.add(kindJev, "k1", nil)
	l.add(kindJev, "k1", nil)
	l.add(kindReview, "k2", nil)
	l.add(kindReview, "k3", errors.New("miss"))
	if u := l.used(kindJev); len(u) != 1 || !u["k1"] {
		t.Errorf("jev used %v", u)
	}
	if u := l.used(kindReview); len(u) != 1 || !u["k2"] {
		t.Errorf("review used %v (a miss is not a use)", u)
	}
	if m := l.missList(); len(m) != 1 {
		t.Errorf("misses %v", m)
	}
	if missOnly(review.ErrTruncated, review.ErrNoRecording) != nil {
		t.Error("a served truncation counts as a miss")
	}
	if missOnly(review.ErrNoRecording, review.ErrNoRecording) == nil {
		t.Error("a miss is not reported")
	}
}

func TestUnusedFlags(t *testing.T) {
	for _, args := range [][]string{{"--prune"}, {"--unused", "--live"}, {"--unused", "--review"}} {
		if _, err := parseFlags(args, io.Discard); err == nil {
			t.Errorf("parseFlags(%v): want an error", args)
		}
	}
	c, err := parseFlags([]string{"--unused", "--prune"}, io.Discard)
	if err != nil || !c.unused || !c.prune {
		t.Fatalf("parseFlags = %+v, %v", c, err)
	}
}
