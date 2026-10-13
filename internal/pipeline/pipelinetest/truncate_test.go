package pipelinetest_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/review"
)

// baseCap is the output cap the pipeline asks for first, per task and tier.
func baseCap(req review.Request) int {
	switch req.Task {
	case review.TaskReview:
		if req.Tier == model.TierDeep {
			return review.ReviewMaxTokensDeep
		}
		return review.ReviewMaxTokensFast
	case review.TaskTLDR:
		return review.TLDRMaxTokens
	}
	return review.TitleMaxTokens
}

// truncateFirst answers every request cut off at max_tokens when it asks
// for its task's base cap, and properly once the cap has grown: reviews
// with one low finding at confidence 0.9 (no escalation), prose with fixed
// text and no tokens (free, so the plan alone fills the cap).
func truncateFirst(req review.Request) (review.Result, error) {
	ok := func(req review.Request) (review.Result, error) {
		switch req.Task {
		case review.TaskReview:
			return reviewResult(req, 0.9, nil), nil
		case review.TaskTLDR:
			return review.Result{Text: review.FakeTLDR}, nil
		}
		return review.Result{Text: review.FakeTitle}, nil
	}
	return review.FakeTruncate(baseCap(req)+1, ok)(req)
}

// TestFixtureTruncationRetry: every first call is cut off at max_tokens;
// the pipeline retries each once with the cap doubled, the rows end done
// after two attempts, and the call log shows the routed call and the
// retry.
func TestFixtureTruncationRetry(t *testing.T) {
	fast := &review.Fake{Name: fastSpec, Respond: truncateFirst}
	deep := &review.Fake{Name: deepSpec, Respond: truncateFirst}
	f := openFixture(t, review.Tiers{Fast: fast, Deep: deep}, routing(nil))
	o := runPR(t, f, 102, pipeline.RunOptions{})
	routed := 0
	for _, r := range o.Reviews {
		if r.Tier == model.TierNone {
			continue
		}
		routed++
		if r.Status != model.ReviewDone || r.Attempts != 2 {
			t.Errorf("row %q: %s after %d attempts (%s), want done after 2", r.GroupName, r.Status, r.Attempts, r.Err)
		}
		calls, err := f.Pipeline.CallLog(context.Background(), o.Item.ID, r.GroupID)
		if err != nil {
			t.Fatal(err)
		}
		var purposes, errs []string
		for _, c := range calls {
			if c.Task == string(review.TaskReview) {
				purposes = append(purposes, c.Purpose)
				errs = append(errs, c.Err)
			}
		}
		if strings.Join(purposes, ",") != "routed,retry" || !strings.Contains(errs[0], "max_tokens") || errs[1] != "" {
			t.Errorf("row %q call log purposes %v, errors %q; want the truncated routed call, then a clean retry", r.GroupName, purposes, errs)
		}
	}
	if routed == 0 {
		t.Fatal("no routed rows")
	}
	for _, fk := range []*review.Fake{fast, deep} {
		for i, c := range fk.Calls {
			if c.MaxTokens > baseCap(c) && (i == 0 || fk.Calls[i-1].MaxTokens != baseCap(c)) && c.Task == review.TaskReview {
				t.Errorf("grown request %d not preceded by its truncated original", i)
			}
			if c.MaxTokens != baseCap(c) && c.MaxTokens != 2*baseCap(c) {
				t.Errorf("request with MaxTokens %d (base %d)", c.MaxTokens, baseCap(c))
			}
		}
	}
	if o.Agg.TLDR != review.FakeTLDR {
		t.Errorf("TL;DR after a truncated first call = %q", o.Agg.TLDR)
	}
}

// TestFixtureTruncationReplay records #102 through review.Recorder over the
// truncating fake into a temp dir (the truncated responses and the
// retries), then replays it from that dir with review.Replay: the replayed
// truncation reaches the retry's recording, every row ends done after two
// attempts, and nothing misses.
func TestFixtureTruncationReplay(t *testing.T) {
	dir := t.TempDir()
	rec := review.Tiers{
		Fast: &review.Recorder{Live: &review.Fake{Name: fastSpec, Respond: truncateFirst}, Dir: dir},
		Deep: &review.Recorder{Live: &review.Fake{Name: deepSpec, Respond: truncateFirst}, Dir: dir},
	}
	f := openFixture(t, rec, routing(nil))
	recorded := runPR(t, f, 102, pipeline.RunOptions{})
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	truncated := 0
	for _, p := range files {
		rc, ok, err := loadRecordingFile(p)
		if err != nil || !ok {
			t.Fatalf("recording %s: %v", p, err)
		}
		if rc.Truncated {
			truncated++
		}
	}
	if truncated == 0 || truncated*2 != len(files) {
		t.Fatalf("%d recordings, %d truncated: want each truncated call next to its retry", len(files), truncated)
	}

	fast := &reviewMisses{next: &review.Replay{Name: fastSpec, Dir: dir}}
	deep := &reviewMisses{next: &review.Replay{Name: deepSpec, Dir: dir}}
	g := openFixture(t, review.Tiers{Fast: fast, Deep: deep}, routing(nil))
	o := runPR(t, g, 102, pipeline.RunOptions{})
	if misses := append(fast.list(), deep.list()...); len(misses) > 0 {
		t.Fatalf("%d misses replaying the truncation recordings: %v", len(misses), misses)
	}
	if a, b := tierCounts(recorded.Reviews), tierCounts(o.Reviews); a != b {
		t.Errorf("replayed rows %s, recorded %s", b, a)
	}
	for _, r := range o.Reviews {
		if r.Tier != model.TierNone && (r.Status != model.ReviewDone || r.Attempts != 2) {
			t.Errorf("replayed row %q: %s after %d attempts", r.GroupName, r.Status, r.Attempts)
		}
	}
	if o.Agg.TLDR != review.FakeTLDR {
		t.Errorf("replayed TL;DR = %q", o.Agg.TLDR)
	}
}

// loadRecordingFile reads one review recording.
func loadRecordingFile(path string) (review.Recording, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return review.Recording{}, false, err
	}
	var rec review.Recording
	if err := json.Unmarshal(b, &rec); err != nil {
		return review.Recording{}, false, err
	}
	return rec, true, nil
}

// TestFixtureTruncationRetryOverCap: with a cap that holds the plan but not
// a truncation retry's estimate (the grown request's full max_tokens), no
// retry is sent and each row fails "retry over cost cap" after one
// attempt.
func TestFixtureTruncationRetryOverCap(t *testing.T) {
	// The plan's reservation, measured with an unlimited cap: the routed
	// calls' estimates plus what the group titles (truncated, then
	// retried) cost, which the plan counts as spent.
	probe := openFixture(t, review.Tiers{Fast: &review.Fake{Name: fastSpec, Respond: truncateFirst}, Deep: &review.Fake{Name: deepSpec, Respond: truncateFirst}}, routing(nil))
	po := runPR(t, probe, 102, pipeline.RunOptions{})
	planned := 0.0
	for _, r := range po.Reviews {
		planned += r.EstCost
	}
	for _, rl := range po.RunLogs {
		if rl.Stage == model.StageCluster && rl.Status == model.StageDone {
			planned += rl.Cost
		}
	}
	if planned == 0 {
		t.Fatal("nothing planned")
	}
	fast := &review.Fake{Name: fastSpec, Respond: truncateFirst}
	deep := &review.Fake{Name: deepSpec, Respond: truncateFirst}
	f := openFixture(t, review.Tiers{Fast: fast, Deep: deep}, routing(func(r *config.Routing) { r.CostCapUSD = planned + 0.01 }))
	o := runPR(t, f, 102, pipeline.RunOptions{})
	routed := 0
	for _, r := range o.Reviews {
		if r.Tier == model.TierNone {
			continue
		}
		routed++
		if r.Status != model.ReviewFailed || r.Attempts != 1 || !strings.Contains(r.Err, "retry over cost cap") {
			t.Errorf("row %q: %s after %d attempts, err %q; want failed, retry over cost cap", r.GroupName, r.Status, r.Attempts, r.Err)
		}
	}
	reviews := 0
	for _, c := range append(append([]review.Request(nil), fast.Calls...), deep.Calls...) {
		if c.Task == review.TaskReview {
			reviews++
			if c.MaxTokens != baseCap(c) {
				t.Errorf("a grown review request was sent (MaxTokens %d)", c.MaxTokens)
			}
		}
	}
	if routed == 0 || reviews != routed {
		t.Errorf("%d review calls for %d routed rows, want one each", reviews, routed)
	}
}
