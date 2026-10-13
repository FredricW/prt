package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/store"
)

// reviewEnv is an InProcess over one analysed branch ("feat": one unit, so
// one ungrouped group) with fake reviewers (nil when withReviewers is
// false) and a config dir in a temp git dir.
type reviewEnv struct {
	c    *InProcess
	st   store.Store
	id   string
	dirs []string
	// deepReviews counts review calls on the deep tier.
	deepReviews atomic.Int64
	fast, deep  *review.Fake
}

func newReviewEnv(t *testing.T, withReviewers bool) *reviewEnv {
	t.Helper()
	e := &reviewEnv{st: newTestStore(t), id: model.BranchItemID("feat"), dirs: []string{filepath.Join(t.TempDir(), "prt")}}
	answer := review.FakeRespond(0.9)
	e.fast = &review.Fake{Name: "fake:fast", Respond: answer}
	e.deep = &review.Fake{Name: "fake:deep", Respond: func(req review.Request) (review.Result, error) {
		if req.Task == review.TaskReview {
			e.deepReviews.Add(1)
		}
		return answer(req)
	}}
	e.c = newClient(t, e.st, branchGit("feat"), nil, func(d *pipeline.Deps) {
		d.Routing, d.ConfigDirs = config.DefaultRouting(), e.dirs
		if withReviewers {
			d.Reviewers = review.Tiers{Fast: e.fast, Deep: e.deep}
		}
	})
	put(t, e.st, branchItem("feat", 1))
	if err := e.c.RunSync(context.Background(), e.id); err != nil {
		t.Fatal(err)
	}
	return e
}

// rows returns the item's routing table.
func (e *reviewEnv) rows(t *testing.T) []model.Review {
	t.Helper()
	rows, err := e.st.ItemReviews(context.Background(), e.id)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// waitRow polls the single routing row until pred holds (5 s).
func (e *reviewEnv) waitRow(t *testing.T, what string, pred func(model.Review) bool) model.Review {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows := e.rows(t)
		if len(rows) == 1 && pred(rows[0]) {
			return rows[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; rows = %+v", what, rows)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitAgg polls the stored aggregate until pred holds (5 s).
func (e *reviewEnv) waitAgg(t *testing.T, what string, pred func(model.ItemAgg) bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		agg, ok, err := e.st.Agg(context.Background(), e.id)
		if err != nil {
			t.Fatal(err)
		}
		if ok && pred(agg) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; agg review = %+v", what, agg.Review)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReviewActionForcesAndSkips(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t, true)
	row := e.rows(t)[0]
	// No Jev: labels unknown, so the rules route the group deep.
	if row.Tier != model.TierDeep || row.Forced || row.Status != model.ReviewDone {
		t.Fatalf("first run row = %+v", row)
	}
	for _, p := range []proto.ReviewActionParams{
		{ItemID: e.id, GroupID: row.GroupID, Action: "explode"},
		{ItemID: e.id, Action: proto.ActionDeep},
		{ItemID: "branch:missing", GroupID: row.GroupID, Action: proto.ActionDeep},
	} {
		if err := e.c.ReviewAction(ctx, p); err == nil {
			t.Errorf("ReviewAction(%+v): want error", p)
		}
	}

	if err := e.c.ReviewAction(ctx, proto.ReviewActionParams{ItemID: e.id, GroupID: row.GroupID, Action: proto.ActionDeep}); err != nil {
		t.Fatal(err)
	}
	got := e.waitRow(t, "forced deep row", func(r model.Review) bool { return r.Forced && r.Status == model.ReviewDone })
	if got.Tier != model.TierDeep || got.RuleWords != "forced deep (D)" || got.RuleIndex != 0 {
		t.Errorf("forced row = %+v", got)
	}

	if err := e.c.ReviewAction(ctx, proto.ReviewActionParams{ItemID: e.id, GroupID: row.GroupID, Action: proto.ActionSkip}); err != nil {
		t.Fatal(err)
	}
	got = e.waitRow(t, "skipped row", func(r model.Review) bool { return r.UserSkipped })
	if got.Tier != model.TierNone || got.Forced || got.RuleWords != "skipped by user (s)" || got.Status != model.ReviewSkipped {
		t.Errorf("skipped row = %+v", got)
	}

	if err := e.c.ReviewAction(ctx, proto.ReviewActionParams{ItemID: e.id, GroupID: row.GroupID, Action: proto.ActionReset}); err != nil {
		t.Fatal(err)
	}
	got = e.waitRow(t, "reset row", func(r model.Review) bool { return !r.UserSkipped && r.Status == model.ReviewDone })
	if got.Tier != model.TierDeep || got.Forced {
		t.Errorf("reset row = %+v", got)
	}
	// D re-reviews on deep once, bypassing the cached deep review; skip and
	// reset re-ran routing only and served the cache.
	if n := e.deepReviews.Load(); n != 2 {
		t.Errorf("deep review calls = %d, want 2 (first run + one fresh re-review)", n)
	}
}

func TestSetCapUpdatesItemAndReruns(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t, true)
	drain(e.c.Events())
	for _, p := range []proto.CapParams{{ItemID: e.id, CapUSD: -1}, {ItemID: "branch:missing", CapUSD: 1}} {
		if err := e.c.SetCap(ctx, p); err == nil {
			t.Errorf("SetCap(%+v): want error", p)
		}
	}
	if err := e.c.SetCap(ctx, proto.CapParams{ItemID: e.id, CapUSD: 1.5}); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, e.c.Events(), func(ev proto.Event) bool {
		return ev.Type == proto.EventItem && ev.ItemID == e.id && ev.Item.CapUSD == 1.5
	})
	if it, err := e.st.Item(ctx, e.id); err != nil || it.CapUSD != 1.5 {
		t.Fatalf("stored item cap = %v, %v", it.CapUSD, err)
	}
	e.waitAgg(t, "cap in review stats", func(a model.ItemAgg) bool { return a.Review != nil && a.Review.CapUSD == 1.5 })
	if err := e.c.SetCap(ctx, proto.CapParams{ItemID: e.id}); err != nil {
		t.Fatal(err)
	}
	e.waitAgg(t, "default cap", func(a model.ItemAgg) bool {
		return a.Review != nil && a.Review.CapUSD == config.DefaultRouting().CostCapUSD
	})
}

func TestRoutingEnsureWritesBuiltinFile(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t, false)
	path := filepath.Join(e.dirs[0], config.RoutingFile)
	info, err := e.c.Routing(ctx, proto.RoutingParams{})
	if err != nil || info.Path != path || info.Source != "builtin" || len(info.Rules) != len(config.DefaultRouting().Rules) {
		t.Fatalf("info = %+v, %v", info, err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("routing file written without Ensure")
	}
	info, err = e.c.Routing(ctx, proto.RoutingParams{Ensure: true})
	if err != nil || info.Path != path {
		t.Fatalf("ensure = %+v, %v", info, err)
	}
	loaded, err := config.LoadRouting(e.dirs)
	if err != nil {
		t.Fatal(err)
	}
	def := config.DefaultRouting()
	if loaded.Source != path || !reflect.DeepEqual(loaded.Rules, def.Rules) || loaded.DeepSlots != def.DeepSlots ||
		loaded.CostCapUSD != def.CostCapUSD || loaded.EscalateBelow != def.EscalateBelow || loaded.CalibrationSample != def.CalibrationSample || len(loaded.Warnings) > 0 {
		t.Errorf("written rules read back as %+v", loaded)
	}
	for _, r := range info.Rules {
		if r.Err != "" || !strings.HasPrefix(r.Words, "if ") {
			t.Errorf("rule %+v", r)
		}
	}
	// Ensure never overwrites an existing file.
	if err := os.WriteFile(path, []byte("deep_slots = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.c.Routing(ctx, proto.RoutingParams{Ensure: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "deep_slots = 1\n" {
		t.Errorf("Ensure overwrote the file: %q", b)
	}
	// Detail carries the active rules.
	det, err := e.c.Detail(ctx, e.id)
	if err != nil || det.Routing == nil || len(det.Routing.Rules) != len(def.Rules) {
		t.Errorf("detail routing = %+v, %v", det.Routing, err)
	}
}

func TestRoutingReload(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t, false) // no reviewers: the routed row stays skipped (not done)
	path := filepath.Join(e.dirs[0], config.RoutingFile)
	if err := os.MkdirAll(e.dirs[0], 0o755); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"[[rule]]\nwhen = \"risk >>> 4\"\ntier = \"deep\"\n", // rule does not parse
		"deep_slots = \n", // TOML syntax error
	} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := e.c.Routing(ctx, proto.RoutingParams{Reload: true})
		if err != nil || info.Err == "" || info.Rerouted != 0 {
			t.Errorf("reload of %q = %+v, %v; want Err", bad, info, err)
		}
		if src := e.c.p.Routing().Config.Source; src != "builtin" {
			t.Errorf("bad reload replaced the rules (source %q)", src)
		}
	}

	if err := os.WriteFile(path, []byte("cost_cap_usd = 0.5\n\n[[rule]]\nwhen = \"true\"\ntier = \"fast\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := e.c.Routing(ctx, proto.RoutingParams{Reload: true})
	if err != nil || info.Err != "" || info.Source != path || info.Rerouted != 1 || len(info.Rules) != 1 || info.CostCapUSD != 0.5 {
		t.Fatalf("good reload = %+v, %v", info, err)
	}
	got := e.waitRow(t, "re-routed row", func(r model.Review) bool { return r.RuleWords == "if true → fast" })
	if got.Tier != model.TierFast || got.RuleIndex != 1 {
		t.Errorf("re-routed row = %+v", got)
	}
}

// A reload whose items are all reviewed re-routes nothing: done groups keep
// their tier and no review is asked again.
func TestRoutingReloadKeepsReviewed(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t, true)
	before := e.rows(t)[0]
	if before.Status != model.ReviewDone || before.Tier != model.TierDeep {
		t.Fatalf("first run row = %+v", before)
	}
	path := filepath.Join(e.dirs[0], config.RoutingFile)
	if err := os.MkdirAll(e.dirs[0], 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[[rule]]\nwhen = \"true\"\ntier = \"fast\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := e.c.Routing(ctx, proto.RoutingParams{Reload: true})
	if err != nil || info.Err != "" || info.Rerouted != 0 {
		t.Fatalf("reload = %+v, %v; want nothing re-routed", info, err)
	}
	// Run the item as a reload would and check the done row is kept.
	if _, err := e.c.p.RunHead(ctx, mustItem(t, e), pipeline.RunOptions{KeepReviewed: true}); err != nil {
		t.Fatal(err)
	}
	if got := e.rows(t)[0]; got.Tier != model.TierDeep || got.Status != model.ReviewDone || reviewCalls(e.fast) != 0 {
		t.Errorf("row after reload run = %+v (fast review calls %d)", got, reviewCalls(e.fast))
	}
	if n := e.deepReviews.Load(); n != 1 {
		t.Errorf("deep review calls = %d, want 1 (cached)", n)
	}
}

func mustItem(t *testing.T, e *reviewEnv) model.Item {
	t.Helper()
	it, err := e.st.Item(context.Background(), e.id)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func reviewCalls(f *review.Fake) int {
	n := 0
	for _, req := range f.Calls {
		if req.Task == review.TaskReview {
			n++
		}
	}
	return n
}

func TestCallLog(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t, true)
	row := e.rows(t)[0]
	calls, err := e.c.CallLog(ctx, proto.CallLogParams{ItemID: e.id, GroupID: row.GroupID})
	if err != nil || len(calls) == 0 {
		t.Fatalf("call log = %+v, %v", calls, err)
	}
	if c := calls[0]; c.Purpose != "routed" || c.Tier != model.TierDeep || c.Prompt == "" || c.Response == "" || c.GroupID != row.GroupID {
		t.Errorf("call = %+v", c)
	}
	tldr, err := e.c.CallLog(ctx, proto.CallLogParams{ItemID: e.id})
	if err != nil || len(tldr) != 1 || tldr[0].Purpose != "tldr" {
		t.Errorf("TL;DR call log = %+v, %v", tldr, err)
	}
	if _, err := e.c.CallLog(ctx, proto.CallLogParams{}); err == nil {
		t.Error("CallLog without an item: want error")
	}
}

func TestRefreshFromStage(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t, true)
	if err := e.c.Refresh(ctx, proto.ItemParams{ItemID: e.id, FromStage: "bogus"}); err == nil {
		t.Error("unknown stage: want error")
	}
	// From the review stage the cached review is ignored: asked again.
	if err := e.c.Refresh(ctx, proto.ItemParams{ItemID: e.id, FromStage: model.StageReview}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.deepReviews.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("FromStage review did not re-call the reviewer (%d calls)", e.deepReviews.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEarlierStage(t *testing.T) {
	for _, tc := range []struct{ a, b, want model.Stage }{
		{"", "", ""},
		{"", model.StageReview, model.StageReview},
		{model.StageAggregate, "", model.StageAggregate},
		{model.StageAggregate, model.StageRoute, model.StageRoute},
		{model.StageFetch, model.StageReview, model.StageFetch},
	} {
		if got := earlierStage(tc.a, tc.b); got != tc.want {
			t.Errorf("earlierStage(%q, %q) = %q, want %q", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestRoutingTOMLRoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := config.DefaultRouting()
	r.CostCapUSD, r.DeepSlots = 2, 5
	if err := os.WriteFile(filepath.Join(dir, config.RoutingFile), []byte(RoutingTOML(r)), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadRouting([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if got.CostCapUSD != 2 || got.DeepSlots != 5 || !reflect.DeepEqual(got.Rules, r.Rules) {
		t.Errorf("round trip = %+v", got)
	}
}

// The four M3 methods go Socket → Serve → InProcess and back.
func TestSocketReviewMethods(t *testing.T) {
	ctx := context.Background()
	e := newReviewEnv(t, true)
	s, _, _ := startServer(t, e.c)
	row := e.rows(t)[0]

	info, err := s.Routing(ctx, proto.RoutingParams{Ensure: true})
	if err != nil || info.Path != filepath.Join(e.dirs[0], config.RoutingFile) || len(info.Rules) == 0 {
		t.Fatalf("routing = %+v, %v", info, err)
	}
	calls, err := s.CallLog(ctx, proto.CallLogParams{ItemID: e.id, GroupID: row.GroupID})
	if err != nil || len(calls) == 0 || calls[0].Prompt == "" {
		t.Fatalf("call log = %+v, %v", calls, err)
	}
	if err := s.SetCap(ctx, proto.CapParams{ItemID: e.id, CapUSD: 2}); err != nil {
		t.Fatal(err)
	}
	if it, _ := e.st.Item(ctx, e.id); it.CapUSD != 2 {
		t.Errorf("cap over the socket = %v", it.CapUSD)
	}
	if err := s.ReviewAction(ctx, proto.ReviewActionParams{ItemID: e.id, GroupID: row.GroupID, Action: proto.ActionDeep}); err != nil {
		t.Fatal(err)
	}
	e.waitRow(t, "forced row", func(r model.Review) bool { return r.Forced })
	err = s.ReviewAction(ctx, proto.ReviewActionParams{ItemID: e.id, Action: proto.ActionDeep})
	var re *RemoteError
	if !errors.As(err, &re) || re.Method != proto.MethodReviewAction {
		t.Fatalf("bad review action err = %#v", err)
	}
}

// Discovery keeps the label urgency terms of the stored aggregate while it
// describes the current head, and drops them when the head moves.
func TestDiscoveryKeepsLabelUrgencyTerms(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	it := branchItem("a", 0)
	it.State, it.AnalysedSHA = model.StateReady, it.HeadSHA
	put(t, st, it)
	agg := model.ItemAgg{ItemID: it.ID, Minutes: 2, RiskMax: 5, TestGap: 2, Units: []model.UnitAgg{{UnitID: "u1", Breaking: true, Relevance: 4}}}
	if err := st.PutAgg(ctx, agg); err != nil {
		t.Fatal(err)
	}
	c := newClient(t, st, branchGit("a"), nil, nil)
	pass := func(sha string) model.Item {
		t.Helper()
		if err := c.upsertDiscovered(ctx, it.ID, func(x *model.Item) {
			applyBranch(x, gitx.Ref{Name: "a", SHA: sha}, "main", 1, 0)
		}); err != nil {
			t.Fatal(err)
		}
		got, err := st.Item(ctx, it.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	same := pass(it.HeadSHA)
	if !strings.Contains(same.Why, "breaking") || !strings.Contains(same.Why, "risk") {
		t.Errorf("same head: why = %q (urgency %.2f), want the label terms", same.Why, same.Urgency)
	}
	moved := pass("h-moved")
	if strings.Contains(moved.Why, "breaking") || moved.Urgency >= same.Urgency {
		t.Errorf("moved head: why = %q urgency %.2f (same head %.2f), want no label terms", moved.Why, moved.Urgency, same.Urgency)
	}
}
