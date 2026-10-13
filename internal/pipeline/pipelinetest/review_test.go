package pipelinetest_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/pipeline/pipelinetest"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/testutil"
)

// Fake model specs of the review fixture tests.
const (
	fastSpec = "fake:fast"
	deepSpec = "fake:deep"
)

// recordedJev returns a strict replay of the committed Jev recordings that
// remembers misses; the test is skipped while there are none.
func recordedJev(t *testing.T) *missingKeys {
	t.Helper()
	dir := testutil.RecordingsDir(t, "jev")
	if recs, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(recs) == 0 {
		t.Skip("NO JEV RECORDINGS YET: run `make record-jev`; review fixture tests skipped")
	}
	return &missingKeys{next: &jev.Replay{Dir: dir}}
}

// openFixture opens the fixture environment over the recorded Jev and
// embedding answers with the given reviewers and routing, and checks after
// the test that no Jev or embedding request missed its recording.
func openFixture(t *testing.T, tiers review.Tiers, routing *config.Routing) *pipelinetest.Fixture {
	t.Helper()
	jc := recordedJev(t)
	emb := embedReplay(t)
	opts := []pipelinetest.Option{pipelinetest.WithReviewers(tiers), embedOption(emb)}
	if routing != nil {
		opts = append(opts, pipelinetest.WithRouting(*routing))
	}
	f, err := pipelinetest.Open(context.Background(), testutil.FixtureRepo(t), testutil.FixturePRs(t), jc, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		f.Close()
		if len(jc.keys) > 0 {
			t.Errorf("%d Jev requests have no recording: %v", len(jc.keys), jc.keys)
		}
		if emb != nil && len(emb.misses) > 0 {
			t.Errorf("%d embedding texts have no recording: %v", len(emb.misses), emb.misses)
		}
	})
	return f
}

// runPR runs one fixture PR and fails the test on a hard error or a failed
// stage other than review.
func runPR(t *testing.T, f *pipelinetest.Fixture, n int, opt pipeline.RunOptions) pipelinetest.Outcome {
	t.Helper()
	o, err := f.Run(context.Background(), n, opt)
	if err != nil {
		t.Fatal(err)
	}
	for _, rl := range o.Failed() {
		t.Errorf("#%d: stage %s failed: %s", n, rl.Stage, rl.Err)
	}
	return o
}

// routing returns the built-in routing with a large cap and no
// calibration unless mutate changes them.
func routing(mutate func(*config.Routing)) *config.Routing {
	r := config.DefaultRouting()
	r.CostCapUSD, r.CalibrationSample = 100, 0
	if mutate != nil {
		mutate(&r)
	}
	return &r
}

// scripted is a fake reviewer pair driven by one function of the request;
// it counts calls by task and tier.
type scripted struct {
	mu     sync.Mutex
	calls  map[string]int // "<task>/<tier>"
	answer func(review.Request) (review.Result, error)
}

func newScripted(answer func(review.Request) (review.Result, error)) *scripted {
	return &scripted{calls: map[string]int{}, answer: answer}
}

// tiers returns the fast and deep fakes.
func (s *scripted) tiers() review.Tiers {
	return review.Tiers{
		Fast: &review.Fake{Name: fastSpec, Respond: s.respond},
		Deep: &review.Fake{Name: deepSpec, Respond: s.respond},
	}
}

func (s *scripted) respond(req review.Request) (review.Result, error) {
	s.mu.Lock()
	s.calls[string(req.Task)+"/"+string(req.Tier)]++
	s.mu.Unlock()
	return s.answer(req)
}

func (s *scripted) count(task review.Task, tier model.Tier) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[string(task)+"/"+string(tier)]
}

// reviewAnswer answers reviews with FakeReviewJSON (one low finding on the
// first unit) at confidence conf per tier, and prose with fixed text.
func reviewAnswer(fastConf, deepConf float64) func(review.Request) (review.Result, error) {
	return func(req review.Request) (review.Result, error) {
		if req.Task != review.TaskReview {
			return review.FakeRespond(0.9)(req)
		}
		conf := fastConf
		if req.Tier == model.TierDeep {
			conf = deepConf
		}
		return reviewResult(req, conf, nil), nil
	}
}

// reviewResult is a review response with the given findings (one low
// finding on the first unit when nil).
func reviewResult(req review.Request, conf float64, findings []model.Finding) review.Result {
	if findings == nil && len(req.Units) > 0 {
		u := req.Units[0]
		findings = []model.Finding{{Path: u.Path, Line: firstNewLine(u), Severity: model.SevLow, Claim: "Looks fine."}}
	}
	return review.Result{Text: review.FakeReviewJSON(findings, "comment", conf), InputTokens: 100, OutputTokens: 50}
}

// firstNewLine is the first new-side line of a unit (its start line).
func firstNewLine(u model.ChangeUnit) int {
	if lines := model.AddedLineNumbers(u); len(lines) > 0 {
		return lines[0]
	}
	return max(u.Range.New.Start, 1)
}

// tierCounts renders the rows' tiers and statuses for failure messages.
func tierCounts(rows []model.Review) string {
	m := map[string]int{}
	for _, r := range rows {
		m[string(r.Tier)+"/"+string(r.Status)]++
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, m[k])
	}
	return strings.Join(parts, " ")
}

// fastRows returns the rows routed fast (escalated rows included).
func fastRows(rows []model.Review) []model.Review {
	var out []model.Review
	for _, r := range rows {
		if r.Tier == model.TierFast || r.EscalatedOf != nil {
			out = append(out, r)
		}
	}
	return out
}

// TestFixtureReviewRouting runs the four fixture PRs with fake reviewers
// and checks the routing and review outcomes (ReviewExpectations).
func TestFixtureReviewRouting(t *testing.T) {
	s := newScripted(reviewAnswer(0.9, 0.9))
	f := openFixture(t, s.tiers(), routing(nil))
	outs, err := f.RunAll(context.Background(), pipeline.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reportAll(t, pipelinetest.Problems(outs))
	reportAll(t, pipelinetest.ReviewExpectations(outs))
	for _, n := range pipelinetest.PRNumbers {
		o := outs[n]
		t.Logf("#%d: %s · urgency %.2f (%s)", n, tierCounts(o.Reviews), o.Item.Urgency, o.Item.Why)
		if o.Agg.Review == nil || o.Agg.Calibration == nil {
			t.Errorf("#%d: Agg.Review %v / Agg.Calibration %v not set", n, o.Agg.Review, o.Agg.Calibration)
		}
	}
	if s.count(review.TaskReview, model.TierFast)+s.count(review.TaskReview, model.TierDeep) == 0 {
		t.Error("no review calls")
	}
	// Live progress: every routed row's stage row carries the call totals.
	for _, rl := range outs[101].RunLogs {
		if rl.Stage == model.StageReview && rl.Status == model.StageDone && rl.Calls == 0 {
			t.Errorf("#101 review row without calls: %+v", rl)
		}
	}
}

// TestFixtureUrgencyOrder pins the urgency order of the four fixture PRs
// after a full analysis (DECISIONS "Fixture urgency order (M3)").
func TestFixtureUrgencyOrder(t *testing.T) {
	f := openFixture(t, review.Tiers{}, routing(nil))
	outs, err := f.RunAll(context.Background(), pipeline.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	order := append([]int(nil), pipelinetest.PRNumbers...)
	sort.SliceStable(order, func(i, j int) bool { return outs[order[i]].Item.Urgency > outs[order[j]].Item.Urgency })
	var got []string
	for _, n := range order {
		got = append(got, fmt.Sprintf("#%d %.2f (%s)", n, outs[n].Item.Urgency, outs[n].Item.Why))
	}
	t.Logf("urgency order: %s", strings.Join(got, " > "))
	if outs[101].Item.Urgency <= outs[104].Item.Urgency {
		t.Errorf("#101 must rank above #104: %s", strings.Join(got, " > "))
	}
	// #101: asked you + breaking getUser + risk 5 + untested units; #102:
	// authored, breaking rename across callers; #104: CI red after your
	// approval but a low-risk dependency bump; #103: asked you, but dead
	// code removal (low risk, nothing breaking).
	if want := []int{101, 102, 104, 103}; fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("urgency order = %v, want %v: %s", order, want, strings.Join(got, " > "))
	}
}

// Escalation: a fast confidence below escalate_below re-runs the group on
// the deep tier (row deep, EscalatedOf fast, FastConf); above it nothing
// escalates.
func TestFixtureEscalation(t *testing.T) {
	for _, tc := range []struct {
		conf     float64
		escalate bool
	}{{0.3, true}, {0.9, false}} {
		t.Run(fmt.Sprint(tc.conf), func(t *testing.T) {
			s := newScripted(reviewAnswer(tc.conf, 0.95))
			f := openFixture(t, s.tiers(), routing(nil))
			o := runPR(t, f, 102, pipeline.RunOptions{}) // #101 routes every group deep
			fr := fastRows(o.Reviews)
			if len(fr) == 0 {
				t.Fatalf("no fast-routed rows: %s", tierCounts(o.Reviews))
			}
			escalated := 0
			for _, r := range fr {
				if r.EscalatedOf == nil {
					if tc.escalate {
						t.Errorf("row %q (%s %s) not escalated at fast confidence %.1f", r.GroupName, r.Tier, r.Status, tc.conf)
					}
					continue
				}
				escalated++
				if *r.EscalatedOf != model.TierFast || r.Tier != model.TierDeep || r.FastConf != tc.conf || r.Confidence != 0.95 || r.Status != model.ReviewDone {
					t.Errorf("escalated row = %+v", r)
				}
			}
			if !tc.escalate && escalated > 0 {
				t.Errorf("%d rows escalated at fast confidence %.1f", escalated, tc.conf)
			}
			if got := o.Agg.Review.Escalations; got != escalated {
				t.Errorf("ReviewStats.Escalations = %d, want %d", got, escalated)
			}
			calls, err := f.Pipeline.CallLog(context.Background(), o.Item.ID, fr[0].GroupID)
			if err != nil || len(calls) == 0 {
				t.Fatalf("call log = %v, %v", calls, err)
			}
			if tc.escalate && calls[len(calls)-1].Purpose != "escalation" {
				t.Errorf("last call of an escalated row = %+v", calls[len(calls)-1])
			}
		})
	}
}

// Retry: a call failing once is retried (done, Attempts 2); failing twice
// leaves the row failed with the error, and the item still aggregates.
func TestFixtureRetry(t *testing.T) {
	for _, tc := range []struct {
		failures int
		status   model.ReviewStatus
	}{{1, model.ReviewDone}, {2, model.ReviewFailed}} {
		t.Run(fmt.Sprint(tc.failures), func(t *testing.T) {
			var mu sync.Mutex
			seen := map[string]int{}
			ok := reviewAnswer(0.9, 0.9)
			s := newScripted(func(req review.Request) (review.Result, error) {
				if req.Task == review.TaskReview {
					mu.Lock()
					seen[req.GroupID]++
					n := seen[req.GroupID]
					mu.Unlock()
					if n <= tc.failures {
						return review.Result{}, fmt.Errorf("%w: boom %d", review.ErrUnavailable, n)
					}
				}
				return ok(req)
			})
			f := openFixture(t, s.tiers(), routing(nil))
			o := runPR(t, f, 101, pipeline.RunOptions{})
			routed := 0
			for _, r := range o.Reviews {
				if r.Tier == model.TierNone {
					continue
				}
				routed++
				if r.Status != tc.status || r.Attempts != 2 {
					t.Errorf("row %q: status %s attempts %d, want %s after 2", r.GroupName, r.Status, r.Attempts, tc.status)
				}
				if tc.status == model.ReviewFailed && !strings.Contains(r.Err, "boom 2") {
					t.Errorf("failed row err = %q", r.Err)
				}
			}
			if routed == 0 {
				t.Fatal("no routed rows")
			}
			if o.Agg.ItemID != o.Item.ID || o.Agg.Review == nil || o.Agg.Review.Status[tc.status] != routed {
				t.Errorf("aggregate after retries: %+v", o.Agg.Review)
			}
		})
	}
}

// noKey is a reviewer whose provider key is missing (review.Checker).
type noKey struct{ review.Fake }

func (n *noKey) Ready() error {
	return fmt.Errorf("%w: %w: %s not set", review.ErrUnavailable, review.ErrNoKey, review.AnthropicKeyEnv)
}

// No key: routed rows are skipped with the note, the review stage is
// skipped, no call is made and the item still aggregates.
func TestFixtureNoKey(t *testing.T) {
	fast, deep := &noKey{Fake: review.Fake{Name: fastSpec}}, &noKey{Fake: review.Fake{Name: deepSpec}}
	f := openFixture(t, review.Tiers{Fast: fast, Deep: deep}, routing(nil))
	o := runPR(t, f, 101, pipeline.RunOptions{})
	const note = "reviews skipped: PRT_ANTHROPIC_API_KEY not set"
	for _, r := range o.Reviews {
		if r.Tier != model.TierNone && (r.Status != model.ReviewSkipped || r.QueueNote != note) {
			t.Errorf("row %q: %s %q", r.GroupName, r.Status, r.QueueNote)
		}
	}
	if len(fast.Calls)+len(deep.Calls) != 0 {
		t.Errorf("%d calls without a key", len(fast.Calls)+len(deep.Calls))
	}
	for _, rl := range o.RunLogs {
		if rl.Stage == model.StageReview && rl.Status != model.StageRunning && (rl.Status != model.StageSkipped || !strings.HasPrefix(rl.Note, note)) {
			t.Errorf("review row = %+v", rl)
		}
	}
	if o.Agg.Review == nil || o.Agg.Review.Note != note || o.Agg.TLDR != "" {
		t.Errorf("agg review = %+v, TL;DR %q", o.Agg.Review, o.Agg.TLDR)
	}
	if o.Item.Urgency == 0 {
		t.Error("item not aggregated")
	}
}

// Cost cap: a tiny cap leaves routed rows queued with a note; raising the
// item's cap and re-running reviews them, reusing every cached review.
func TestFixtureCostCap(t *testing.T) {
	s := newScripted(reviewAnswer(0.9, 0.9))
	f := openFixture(t, s.tiers(), routing(func(r *config.Routing) { r.CostCapUSD = 0.000001 }))
	ctx := context.Background()
	o := runPR(t, f, 101, pipeline.RunOptions{})
	queued := 0
	for _, r := range o.Reviews {
		if r.Tier != model.TierNone {
			if r.Status != model.ReviewQueued || !strings.HasPrefix(r.QueueNote, "over cost cap") {
				t.Errorf("row %q: %s %q, want queued over the cap", r.GroupName, r.Status, r.QueueNote)
			}
			queued++
		}
	}
	if queued == 0 || o.Agg.Review.OverCap != queued {
		t.Fatalf("queued %d, ReviewStats.OverCap %d", queued, o.Agg.Review.OverCap)
	}
	if n := s.count(review.TaskReview, model.TierFast) + s.count(review.TaskReview, model.TierDeep); n != 0 {
		t.Fatalf("%d review calls over the cap", n)
	}

	it, err := f.Store.Item(ctx, o.Item.ID)
	if err != nil {
		t.Fatal(err)
	}
	it.CapUSD = 100
	if err := f.Store.UpsertItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	o = runPR(t, f, 101, pipeline.RunOptions{})
	calls := s.count(review.TaskReview, model.TierFast) + s.count(review.TaskReview, model.TierDeep)
	if calls != queued {
		t.Errorf("%d review calls after raising the cap, want one per queued row (%d)", calls, queued)
	}
	for _, r := range o.Reviews {
		if r.Tier != model.TierNone && r.Status != model.ReviewDone {
			t.Errorf("row %q after raising the cap: %s %q", r.GroupName, r.Status, r.QueueNote)
		}
	}
	if o.Agg.Review.CapUSD != 100 || o.Agg.Review.OverCap != 0 {
		t.Errorf("review stats = %+v", o.Agg.Review)
	}
	// A third run reviews nothing again: every review is cached.
	o = runPR(t, f, 101, pipeline.RunOptions{})
	if n := s.count(review.TaskReview, model.TierFast) + s.count(review.TaskReview, model.TierDeep); n != calls {
		t.Errorf("cached reviews were called again (%d → %d)", calls, n)
	}
	for _, rl := range o.RunLogs[len(o.RunLogs)-3:] {
		if rl.Stage == model.StageReview && !rl.CacheHit {
			t.Errorf("third review row = %+v, want a cache hit", rl)
		}
	}
}

// Calibration: with calibration_sample 1 every fast-routed group is also
// reviewed deep; a medium deep finding the fast review lacks is a miss,
// stored as a sample and counted in the calibration panel.
func TestFixtureCalibration(t *testing.T) {
	s := newScripted(func(req review.Request) (review.Result, error) {
		if req.Task != review.TaskReview {
			return review.FakeRespond(0.9)(req)
		}
		if req.Tier == model.TierFast {
			return reviewResult(req, 0.9, []model.Finding{}), nil
		}
		u := req.Units[0]
		return reviewResult(req, 0.9, []model.Finding{{Path: u.Path, Line: firstNewLine(u), Severity: model.SevMedium, Claim: "Missing nil check."}}), nil
	})
	f := openFixture(t, s.tiers(), routing(func(r *config.Routing) { r.CalibrationSample = 1 }))
	o := runPR(t, f, 102, pipeline.RunOptions{}) // #101 routes every group deep
	fr := fastRows(o.Reviews)
	if len(fr) == 0 {
		t.Fatalf("no fast rows: %s", tierCounts(o.Reviews))
	}
	for _, r := range fr {
		if r.Calibration == nil || r.Calibration.Missed != 1 || r.Calibration.Agree {
			t.Errorf("row %q calibration = %+v", r.GroupName, r.Calibration)
		}
	}
	samples, err := f.Store.Calibrations(context.Background(), pipelinetest.Now.AddDate(0, 0, -30))
	if err != nil || len(samples) != len(fr) {
		t.Fatalf("stored samples = %d (%v), want %d", len(samples), err, len(fr))
	}
	cs := o.Agg.Calibration
	if cs == nil || cs.Samples != len(fr) || cs.Misses != len(fr) || cs.MissRate != 1 || cs.LastAgree == nil || *cs.LastAgree {
		t.Errorf("calibration stats = %+v", cs)
	}
	if o.Agg.Review.Calibrations != len(fr) {
		t.Errorf("ReviewStats.Calibrations = %d", o.Agg.Review.Calibrations)
	}
	// A re-run reuses the cached deep reviews and appends no sample.
	runPR(t, f, 102, pipeline.RunOptions{})
	if again, _ := f.Store.Calibrations(context.Background(), pipelinetest.Now.AddDate(0, 0, -30)); len(again) != len(samples) {
		t.Errorf("re-run appended samples: %d → %d", len(samples), len(again))
	}
}

// TL;DR: written from the aggregate, cut to 160 runes, reused while the
// aggregate is unchanged, asked again on a forced run.
func TestFixtureTLDR(t *testing.T) {
	long := strings.Repeat("This change renames the profile client and updates every caller ", 5) + "."
	s := newScripted(func(req review.Request) (review.Result, error) {
		if req.Task == review.TaskTLDR {
			return review.Result{Text: long, InputTokens: 10, OutputTokens: 40}, nil
		}
		return reviewAnswer(0.9, 0.9)(req)
	})
	f := openFixture(t, s.tiers(), routing(nil))
	o := runPR(t, f, 101, pipeline.RunOptions{})
	if o.Agg.TLDR == "" || utf8.RuneCountInString(o.Agg.TLDR) > review.MaxTLDR || o.Agg.TLDRKey == "" {
		t.Fatalf("TL;DR = %q (%d runes), key %q", o.Agg.TLDR, utf8.RuneCountInString(o.Agg.TLDR), o.Agg.TLDRKey)
	}
	if n := s.count(review.TaskTLDR, model.TierFast); n != 1 {
		t.Fatalf("TL;DR calls = %d", n)
	}
	o2 := runPR(t, f, 101, pipeline.RunOptions{})
	if n := s.count(review.TaskTLDR, model.TierFast); n != 1 || o2.Agg.TLDR != o.Agg.TLDR {
		t.Errorf("unchanged run: %d TL;DR calls, text %q", n, o2.Agg.TLDR)
	}
	calls, err := f.Pipeline.CallLog(context.Background(), o.Item.ID, "")
	if err != nil || len(calls) != 1 || !calls[0].Cached || calls[0].Purpose != "tldr" {
		t.Errorf("TL;DR call log = %+v, %v", calls, err)
	}
	runPR(t, f, 101, pipeline.RunOptions{Force: true})
	if n := s.count(review.TaskTLDR, model.TierFast); n != 2 {
		t.Errorf("forced run: %d TL;DR calls, want 2", n)
	}
	runPR(t, f, 101, pipeline.RunOptions{FromStage: model.StageAggregate})
	if n := s.count(review.TaskTLDR, model.TierFast); n != 3 {
		t.Errorf("run from aggregate: %d TL;DR calls, want 3", n)
	}
}

// Group titles: groups Jev could not name get a fast-LLM title.
func TestFixtureGroupTitles(t *testing.T) {
	s := newScripted(reviewAnswer(0.9, 0.9))
	f := openFixture(t, s.tiers(), routing(nil))
	titled := 0
	for _, n := range pipelinetest.PRNumbers {
		o := runPR(t, f, n, pipeline.RunOptions{})
		for _, g := range o.Groups {
			switch {
			case g.NameSource == model.NameAI:
				titled++
				if g.Name != review.FakeTitle || g.Ungrouped || g.Mechanical {
					t.Errorf("#%d: titled group %+v", n, g)
				}
			case g.NameSource == model.NamePlaceholder && !g.Ungrouped && !g.Mechanical:
				t.Errorf("#%d: group %q kept its placeholder", n, g.Name)
			}
		}
	}
	if titled == 0 || s.count(review.TaskGroupTitle, model.TierFast) != titled {
		t.Errorf("%d groups titled with %d title calls", titled, s.count(review.TaskGroupTitle, model.TierFast))
	}
}

// TestRecordedReviewReplay replays the committed review recordings (both
// tiers) over the fixture PRs, strictly, and asserts ReviewExpectations.
func TestRecordedReviewReplay(t *testing.T) {
	dir := testutil.RecordingsDir(t, "review")
	if recs, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(recs) == 0 {
		t.Skip("NO REVIEW RECORDINGS YET in testdata/recordings/review: run the review recorder; review replay assertions skipped")
	}
	cfg := config.Defaults()
	fast := &reviewMisses{next: &review.Replay{Name: cfg.Models.Fast, Dir: dir}}
	deep := &reviewMisses{next: &review.Replay{Name: cfg.Models.Deep, Dir: dir}}
	f := openFixture(t, review.Tiers{Fast: fast, Deep: deep}, nil)
	outs, err := f.RunAll(context.Background(), pipeline.RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if misses := append(fast.list(), deep.list()...); len(misses) > 0 {
		t.Fatalf("%d review requests have no recording in %s: %v", len(misses), dir, misses)
	}
	reportAll(t, pipelinetest.Problems(outs))
	reportAll(t, pipelinetest.ReviewExpectations(outs))
}

// reviewMisses wraps a reviewer and records requests without a recording.
type reviewMisses struct {
	next   review.Reviewer
	mu     sync.Mutex
	misses []string
}

func (r *reviewMisses) Model() string { return r.next.Model() }

func (r *reviewMisses) Review(ctx context.Context, req review.Request) (review.Result, error) {
	res, err := r.next.Review(ctx, req)
	if errors.Is(err, review.ErrNoRecording) {
		r.mu.Lock()
		r.misses = append(r.misses, err.Error())
		r.mu.Unlock()
	}
	return res, err
}

func (r *reviewMisses) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.misses...)
}
