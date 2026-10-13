package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/route"
	"github.com/fredricw/prt/internal/store"
)

// reviewCalls counts the TaskReview requests a fake reviewer received.
func reviewCalls(r review.Reviewer) int {
	f, ok := r.(*review.Fake)
	if !ok {
		return 0
	}
	n := 0
	for _, req := range f.Calls {
		if req.Task == review.TaskReview {
			n++
		}
	}
	return n
}

func itemRows(t *testing.T, e *labelEnv) []model.Review {
	t.Helper()
	rows, err := e.st.ItemReviews(context.Background(), e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// A rules reload (KeepReviewed) re-routes only unreviewed groups: done
// rows keep their tier and are served from the cache, so the new rules
// cause no paid call for them. A plain run routes every group by the rules.
func TestKeepReviewedPinsDoneRows(t *testing.T) {
	e, p, _ := m3Env(t, review.FakeRespond(0.9))
	e.run(t, p)
	before := itemRows(t, e)
	fast := 0
	for _, r := range before {
		if r.Tier == model.TierFast && r.Status == model.ReviewDone {
			fast++
		}
	}
	if fast == 0 {
		t.Fatalf("no done fast rows to keep: %+v", before)
	}
	deep := p.d.Reviewers.Deep
	if err := p.SetRouting(config.Routing{CostCapUSD: 100, EscalateBelow: 0.5, DeepSlots: 1, Rules: []config.Rule{{When: "true", Tier: "deep"}}}); err != nil {
		t.Fatal(err)
	}
	if err := p.RunWith(context.Background(), e.item, RunOptions{KeepReviewed: true}); err != nil {
		t.Fatal(err)
	}
	if n := reviewCalls(deep); n != 0 {
		t.Errorf("reload re-reviewed %d done groups on deep", n)
	}
	for _, r := range itemRows(t, e) {
		if r.Tier == model.TierNone {
			continue
		}
		if r.Tier != model.TierFast || r.Status != model.ReviewDone || !strings.HasSuffix(r.RuleWords, keptWords) || r.RuleIndex != 0 || len(r.Findings) == 0 {
			t.Errorf("kept row = %+v", r)
		}
	}
	// A run that is not a reload routes by the new rules.
	if err := p.RunWith(context.Background(), e.item, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if n := reviewCalls(deep); n != fast {
		t.Errorf("plain run made %d deep calls, want %d", n, fast)
	}
}

// A cached review is served only when the tier's model and the review
// prompt version still match: changing the fast model re-reviews.
func TestCachedReviewMissesOnModelOrPromptChange(t *testing.T) {
	e, p, _ := m3Env(t, review.FakeRespond(0.9))
	e.run(t, p)
	first := reviewCalls(p.d.Reviewers.Fast)
	if first == 0 {
		t.Fatal("no fast reviews")
	}
	e.run(t, p)
	if n := reviewCalls(p.d.Reviewers.Fast); n != first {
		t.Fatalf("same model re-reviewed: %d calls, want %d", n, first)
	}
	newFast := &review.Fake{Name: "fake:fast-2", Respond: review.FakeRespond(0.9)}
	p.d.Reviewers.Fast = newFast
	e.run(t, p)
	if n := reviewCalls(newFast); n != first {
		t.Errorf("new fast model made %d review calls, want %d (cache must miss)", n, first)
	}
	// A cached row from an older prompt version is a miss too.
	ctx := context.Background()
	var groupID string
	for _, r := range itemRows(t, e) {
		if r.Tier == model.TierFast {
			groupID = r.GroupID
			break
		}
	}
	rv, ok, err := e.st.Review(ctx, groupID, model.TierFast)
	if err != nil || !ok {
		t.Fatalf("cached review: %v %v", ok, err)
	}
	if _, hit := p.cachedReview(ctx, groupID, model.TierFast, rv.Prompts); !hit {
		t.Fatal("current cached review missed")
	}
	rv.Prompt = "review/v0"
	if err := e.st.PutReview(ctx, rv); err != nil {
		t.Fatal(err)
	}
	if _, hit := p.cachedReview(ctx, groupID, model.TierFast, rv.Prompts); hit {
		t.Error("old prompt version served from the cache")
	}
	rv.Prompt = review.PromptReviewV1
	if err := e.st.PutReview(ctx, rv); err != nil {
		t.Fatal(err)
	}
	if _, hit := p.cachedReview(ctx, groupID, model.TierFast, append(rv.Prompts, model.PromptPerformance)); hit {
		t.Error("different specialist prompts served from the cache")
	}
}

// keepReviewed leaves user overrides and unreviewed rows to the new rules.
func TestKeepReviewedSkipsOverridesAndUnreviewed(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil)
	ctx := context.Background()
	fastTier := model.TierFast
	rows := []model.Review{
		{GroupID: "done", Tier: model.TierDeep, EscalatedOf: &fastTier, Status: model.ReviewDone, RuleWords: "if risk >= 4 → fast", RuleIndex: 3, Context: []string{"tests"}, ContextNone: []string{"callers"}},
		{GroupID: "failed", Tier: model.TierFast, Status: model.ReviewFailed},
		{GroupID: "forced", Tier: model.TierDeep, Status: model.ReviewDone},
	}
	if err := e.st.PutItemReviews(ctx, e.item.ID, rows); err != nil {
		t.Fatal(err)
	}
	decs := []route.Decision{
		{GroupID: "done", Tier: model.TierNone, Words: "no rule matched", HotSpot: 2},
		{GroupID: "failed", Tier: model.TierDeep, RuleIndex: 1},
		{GroupID: "forced", Tier: model.TierNone},
		{GroupID: "new", Tier: model.TierFast},
	}
	out := p.keepReviewed(ctx, e.item.ID, decs, map[string]model.Tier{"forced": model.TierDeep})
	if d := out[0]; d.Tier != model.TierFast || d.RuleIndex != 0 || d.HotSpot != 2 || len(d.Context) != 2 || !strings.HasSuffix(d.Words, keptWords) {
		t.Errorf("escalated done row kept as %+v, want its fast decision", d)
	}
	if again := p.keepReviewed(ctx, e.item.ID, out[:1], nil)[0]; strings.Count(again.Words, keptWords) != 1 {
		t.Errorf("kept words repeated: %q", again.Words)
	}
	for i := 1; i < len(decs); i++ {
		if out[i].Tier != decs[i].Tier || out[i].RuleIndex != decs[i].RuleIndex {
			t.Errorf("%s: %+v, want the new rules' decision", decs[i].GroupID, out[i])
		}
	}
}

// A retry reserves its estimate from the item's budget: with no room left
// the failed call is not retried and the row fails "retry over cost cap".
func TestRetryOverCapNotRetried(t *testing.T) {
	garbage := func(req review.Request) (review.Result, error) {
		if req.Task == review.TaskReview {
			return review.Result{Text: "not json", InputTokens: 10, OutputTokens: 10}, nil
		}
		return review.FakeRespond(0.9)(req)
	}
	e, p, _ := m3Env(t, garbage)
	p.fn.newBudget = func(_, spent float64) route.Budget { return route.NewBudget(spent, spent) } // full
	e.run(t, p)
	routed := 0
	for _, r := range itemRows(t, e) {
		if r.Tier == model.TierNone {
			continue
		}
		routed++
		if r.Status != model.ReviewFailed || r.Attempts != 1 || !strings.Contains(r.Err, "retry over cost cap") {
			t.Errorf("row = %+v, want failed after 1 attempt, retry over cap", r)
		}
	}
	if n := reviewCalls(p.d.Reviewers.Fast); routed == 0 || n != routed {
		t.Errorf("%d review calls for %d routed rows", n, routed)
	}
	// With room left the retry runs.
	e2, p2, _ := m3Env(t, garbage)
	e2.run(t, p2)
	for _, r := range itemRows(t, e2) {
		if r.Tier != model.TierNone && r.Attempts != 2 {
			t.Errorf("row with budget = %+v, want 2 attempts", r)
		}
	}
}

// Fast ready, deep missing: deep work is skipped with a stage-level note,
// and escalation is not attempted.
func TestDeepUnavailableNotes(t *testing.T) {
	e, p, _ := m3Env(t, review.FakeRespond(0.1), func(d *Deps) { d.Reviewers.Deep = nil })
	e.run(t, p)
	const want = "deep reviews skipped: no reviewer configured; escalation and calibration off"
	if rl := e.stage(t, model.StageReview); rl.Status != model.StageDone || !strings.Contains(rl.Note, want) {
		t.Errorf("review row = %+v", rl)
	}
	agg := e.agg(t)
	if agg.Review == nil || agg.Review.Note != want || agg.Review.Escalations != 0 {
		t.Errorf("review stats = %+v", agg.Review)
	}
	for _, r := range itemRows(t, e) {
		if r.EscalatedOf != nil || r.QueueNote != "" {
			t.Errorf("row escalated or noted without a deep reviewer: %+v", r)
		}
	}
}

// A cached review served after its unit moved follows the unit.
func TestRebaseFindings(t *testing.T) {
	u := model.ChangeUnit{ID: "u", Path: "a.go", Range: model.Range{New: model.Span{Start: 120, Count: 11}}}
	other := model.ChangeUnit{ID: "o", Path: "a.go", Range: model.Range{New: model.Span{Start: 10, Count: 5}}}
	cached := []model.Finding{{UnitID: "u", Path: "a.go", Line: 105}, {Path: "b.go", Line: 7}}
	got := rebaseFindings(cached, map[string]int{"u": 100, "o": 10}, []model.ChangeUnit{other, u})
	if got[0].Line != 125 || got[0].UnitID != "u" || got[1].Line != 7 || got[1].UnitID != "" {
		t.Errorf("rebased = %+v", got)
	}
	if cached[0].Line != 105 {
		t.Error("rebase modified the cached slice")
	}
	if got := rebaseFindings(cached, nil, []model.ChangeUnit{u}); got[0].Line != 105 {
		t.Errorf("no recorded starts: %+v, want unchanged", got)
	}
}

// A shifted unit (same id, new range) gets its cached findings moved.
func TestCachedReviewFollowsMovedUnit(t *testing.T) {
	e, p, _ := m3Env(t, review.FakeRespond(0.9))
	e.run(t, p)
	ctx := context.Background()
	rows := itemRows(t, e)
	var row model.Review
	for _, r := range rows {
		if r.Status == model.ReviewDone && len(r.Findings) > 0 && r.Findings[0].UnitID != "" {
			row = r
			break
		}
	}
	if row.GroupID == "" {
		t.Fatalf("no reviewed row with an anchored finding: %+v", rows)
	}
	c, ok, err := e.st.Review(ctx, row.GroupID, row.Tier)
	if err != nil || !ok || len(c.UnitStarts) == 0 {
		t.Fatalf("cached review %+v ok=%v err=%v, want UnitStarts", c, ok, err)
	}
	// Pretend the review ran when the unit sat 20 lines higher.
	f := c.Findings[0]
	c.UnitStarts[f.UnitID] -= 20
	c.Findings[0].Line -= 20
	if err := e.st.PutReview(ctx, c); err != nil {
		t.Fatal(err)
	}
	e.run(t, p)
	for _, r := range itemRows(t, e) {
		if r.GroupID == row.GroupID && (len(r.Findings) == 0 || r.Findings[0].Line != f.Line || r.Findings[0].UnitID != f.UnitID) {
			t.Errorf("served %+v, want line %d on %s", r.Findings, f.Line, f.UnitID)
		}
	}
}

// A re-run whose labels all come from the cache shows the Jev calls behind
// them, marked cached, instead of "0 calls".
func TestJevStatsCarriedWhenCached(t *testing.T) {
	e, p, _ := m3Env(t, review.FakeRespond(0.9))
	e.run(t, p)
	first := e.agg(t).Review
	if first == nil || first.JevCalls == 0 || first.JevCached {
		t.Fatalf("first run stats = %+v", first)
	}
	e.run(t, p)
	if rl := e.stage(t, model.StageJev); rl.Calls != 0 {
		t.Fatalf("second jev row = %+v, want a cache hit", rl)
	}
	second := e.agg(t).Review
	if second.JevCalls != first.JevCalls || second.JevCost != first.JevCost || !second.JevCached {
		t.Errorf("second run stats jev %d $%v cached %v, want %d $%v cached", second.JevCalls, second.JevCost, second.JevCached, first.JevCalls, first.JevCost)
	}
}

// failingStore fails PutItemReviews from call `from` on.
type failingStore struct {
	store.Store
	from  int32
	calls atomic.Int32
}

func (s *failingStore) PutItemReviews(ctx context.Context, itemID string, rs []model.Review) error {
	if s.calls.Add(1) >= s.from {
		return errors.New("disk full")
	}
	return s.Store.PutItemReviews(ctx, itemID, rs)
}

// Nothing blocks: a routing failure skips the reviews, a review failure
// keeps what landed, and either way the item aggregates.
func TestRouteAndReviewFailuresStillAggregate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		from  int32
		check func(t *testing.T, e *labelEnv)
	}{
		{"routing failed", 1, func(t *testing.T, e *labelEnv) {
			if rl := e.stage(t, model.StageRoute); rl.Status != model.StageFailed {
				t.Errorf("route row = %+v", rl)
			}
			if rl := e.stage(t, model.StageReview); rl.Status != model.StageSkipped || rl.Note != "routing failed" {
				t.Errorf("review row = %+v", rl)
			}
			if agg := e.agg(t); agg.Review == nil || !strings.Contains(agg.Review.Note, "routing failed") {
				t.Errorf("review stats = %+v", agg.Review)
			}
		}},
		{"reviews failed", 2, func(t *testing.T, e *labelEnv) {
			if rl := e.stage(t, model.StageReview); rl.Status != model.StageFailed {
				t.Errorf("review row = %+v", rl)
			}
			if agg := e.agg(t); agg.Review == nil || agg.Review.Status[model.ReviewDone] == 0 {
				t.Errorf("review stats = %+v, want the reviews that landed", agg.Review)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, p, _ := m3Env(t, review.FakeRespond(0.9), func(d *Deps) { d.Store = &failingStore{Store: d.Store, from: tc.from} })
			e.run(t, p)
			if rl := e.stage(t, model.StageAggregate); rl.Status != model.StageDone && rl.Status != model.StageCached {
				t.Errorf("aggregate row = %+v", rl)
			}
			tc.check(t, e)
		})
	}
}

// A run cancelled mid-review stores its rows queued, not running.
func TestCancelledReviewPersistsQueued(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	answer := func(req review.Request) (review.Result, error) {
		if req.Task == review.TaskReview {
			cancel()
			return review.Result{}, context.Canceled
		}
		return review.FakeRespond(0.9)(req)
	}
	e, p, _ := m3Env(t, answer)
	if err := p.RunWith(ctx, e.item, RunOptions{}); err == nil {
		t.Fatal("cancelled run: want error")
	}
	rows := itemRows(t, e)
	queued := 0
	for _, r := range rows {
		if r.Status == model.ReviewRunning {
			t.Errorf("row stored running: %+v", r)
		}
		if r.Tier != model.TierNone && r.Status == model.ReviewQueued {
			queued++
		}
	}
	if queued == 0 {
		t.Errorf("no queued rows after cancel: %+v", rows)
	}
}

// Deep calls never exceed deep_slots at once, and do reach it.
func TestDeepSlotsBoundConcurrency(t *testing.T) {
	const slots = 2
	var mu sync.Mutex
	inFlight, peak, total := 0, 0, 0
	release := make(chan struct{})
	answer := review.FakeRespond(0.9)
	slow := func(req review.Request) (review.Result, error) {
		if req.Task == review.TaskReview {
			mu.Lock()
			inFlight++
			total++
			peak = max(peak, inFlight)
			mu.Unlock()
			<-release
			mu.Lock()
			inFlight--
			mu.Unlock()
		}
		return answer(req)
	}
	e, p, _ := m3Env(t, slow, func(d *Deps) {
		d.Routing.Rules = []config.Rule{{When: "true", Tier: "deep"}}
		d.Routing.DeepSlots = slots
	})
	go func() {
		// Hold every call until the slots are full, then give a further
		// (wrongly admitted) call time to start before releasing.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			full := inFlight >= slots
			mu.Unlock()
			if full {
				break
			}
			time.Sleep(time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		close(release)
	}()
	e.run(t, p)
	if total <= slots {
		t.Fatalf("%d deep calls; the fixture needs more than deep_slots (%d) deep groups", total, slots)
	}
	if peak != slots {
		t.Errorf("peak concurrent deep calls = %d, want %d (deep_slots)", peak, slots)
	}
}
