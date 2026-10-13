package pipeline

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/route"
)

// m3Env is m2Env with fake reviewers, a routing that sends every group
// fast, and placeholder group names (Jev name_bullet "none").
func m3Env(t *testing.T, answer func(review.Request) (review.Result, error), mutate ...func(*Deps)) (*labelEnv, *Pipeline, *fakeM2) {
	t.Helper()
	f := &fakeM2{nameValue: model.BulletNone}
	e := newLabelEnv(t)
	p := e.pipeline(&jev.Fake{}, append([]func(*Deps){func(d *Deps) {
		d.Embedder = &embed.Fake{}
		d.Reviewers = review.Tiers{
			Fast: &review.Fake{Name: "fake:fast", Respond: answer},
			Deep: &review.Fake{Name: "fake:deep", Respond: answer},
		}
		d.Routing = config.Routing{CostCapUSD: 100, EscalateBelow: 0.5, DeepSlots: 1, Rules: []config.Rule{{When: "true", Tier: "fast"}}}
	}}, mutate...)...)
	f.install(p)
	return e, p, f
}

func TestReviewProgressEmittedNotStored(t *testing.T) {
	e, p, _ := m3Env(t, review.FakeRespond(0.9))
	e.run(t, p)
	var progress []model.RunLog
	for _, rl := range e.events {
		if IsProgress(rl) {
			progress = append(progress, rl)
		}
	}
	if len(progress) == 0 {
		t.Fatal("no progress rows emitted")
	}
	last := progress[len(progress)-1]
	if last.Stage != model.StageReview || last.Out != last.In || !strings.HasSuffix(last.Note, " reviewed") {
		t.Errorf("last progress row = %+v", last)
	}
	logs, err := e.st.RunLogs(context.Background(), e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, rl := range logs {
		if IsProgress(rl) {
			t.Errorf("progress row stored: %+v", rl)
		}
	}
	if rl := e.stage(t, model.StageReview); rl.Status != model.StageDone || rl.Calls != last.In {
		t.Errorf("review row = %+v", rl)
	}
}

func TestGroupTitlesAfterSplitAndCached(t *testing.T) {
	e, p, f := m3Env(t, review.FakeRespond(0.9))
	e.run(t, p)
	groups, err := e.st.Groups(context.Background(), e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	titled := 0
	for _, g := range groups {
		if g.NameSource == model.NameAI {
			titled++
			if g.Name != review.FakeTitle {
				t.Errorf("titled group %+v", g)
			}
		}
	}
	if titled != 2 {
		t.Fatalf("%d groups titled, want the 2 real groups", titled)
	}
	// The split question saw the placeholder names, never the titles.
	if len(f.splitCalls) != 1 {
		t.Fatalf("split calls = %d", len(f.splitCalls))
	}
	if rl := e.stage(t, model.StageCluster); rl.Calls < 2 || !strings.Contains(rl.Note, "titles: 2 asked, 0 cached") {
		t.Errorf("cluster row = %+v", rl)
	}
	e.run(t, p)
	if rl := e.stage(t, model.StageCluster); !strings.Contains(rl.Note, "titles: 2 asked, 2 cached") {
		t.Errorf("second cluster row = %+v", rl)
	}
	e.events = nil
	if err := p.RunWith(context.Background(), e.item, RunOptions{FromStage: model.StageCluster}); err != nil {
		t.Fatal(err)
	}
	if rl := e.stage(t, model.StageCluster); !strings.Contains(rl.Note, "titles: 2 asked, 0 cached") {
		t.Errorf("cluster row from cluster = %+v", rl)
	}
}

func TestNoFastReviewerKeepsPlaceholders(t *testing.T) {
	e, p, _ := m3Env(t, review.FakeRespond(0.9), func(d *Deps) { d.Reviewers = review.Tiers{} })
	e.run(t, p)
	groups, _ := e.st.Groups(context.Background(), e.item.ID)
	for _, g := range groups {
		if g.NameSource == model.NameAI {
			t.Errorf("group titled without a reviewer: %+v", g)
		}
	}
	if agg := e.agg(t); agg.TLDR != "" || agg.Review == nil || agg.Review.Note != "reviews skipped: no reviewer configured" {
		t.Errorf("agg TL;DR %q review %+v", agg.TLDR, agg.Review)
	}
}

func TestSetRoutingKeepsRulesOnError(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil)
	before := p.Routing()
	bad := config.Routing{Source: "bad.toml", Rules: []config.Rule{{When: "risk >>> 4", Tier: "deep"}}}
	if err := p.SetRouting(bad); err == nil {
		t.Fatal("bad rules: want error")
	}
	if err := p.SetRouting(config.Routing{}); err == nil {
		t.Fatal("no rules: want error")
	}
	if got := p.Routing(); got.Config.Source != before.Config.Source || len(got.Rules) != len(before.Rules) {
		t.Errorf("rules changed after a failed SetRouting: %+v", got.Config)
	}
	good := config.Routing{Source: "good.toml", DeepSlots: 1, Rules: []config.Rule{{When: "true", Tier: "fast"}}}
	if err := p.SetRouting(good); err != nil {
		t.Fatal(err)
	}
	if got := p.Routing(); got.Config.Source != "good.toml" || len(got.Rules) != 1 {
		t.Errorf("rules after SetRouting = %+v", got)
	}
}

func TestZeroRoutingMeansBuiltin(t *testing.T) {
	e := newLabelEnv(t)
	if got := e.pipeline(nil).Routing(); got.Config.Source != "builtin" || len(got.Rules) != len(config.DefaultRouting().Rules) {
		t.Errorf("zero Deps.Routing compiled to %+v", got.Config)
	}
}

func TestSetGroupOverrideValidates(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil)
	ctx := context.Background()
	if err := p.SetGroupOverride(ctx, e.item.ID, "g", model.TierFast); err == nil {
		t.Error("fast override: want error")
	}
	if err := p.SetGroupOverride(ctx, e.item.ID, "", model.TierDeep); err == nil {
		t.Error("no group: want error")
	}
	if err := p.SetGroupOverride(ctx, e.item.ID, "g", model.TierDeep); err != nil {
		t.Fatal(err)
	}
	ovs, err := p.groupOverrides(ctx, e.item.ID, []model.Group{{ID: "g"}, {ID: "h"}})
	if err != nil || len(ovs) != 1 || ovs["g"] != model.TierDeep {
		t.Fatalf("overrides = %v, %v", ovs, err)
	}
	if err := p.SetGroupOverride(ctx, e.item.ID, "g", ""); err != nil {
		t.Fatal(err)
	}
	if ovs, _ := p.groupOverrides(ctx, e.item.ID, []model.Group{{ID: "g"}}); len(ovs) != 0 {
		t.Errorf("after reset = %v", ovs)
	}
}

func TestApplyUserTiers(t *testing.T) {
	decs := []route.Decision{
		{GroupID: "a", Tier: model.TierFast, RuleIndex: 3, Rule: "breaking", Words: "if breaking → fast", Context: []string{"callers"}},
		{GroupID: "b", Tier: model.TierDeep, RuleIndex: 2, Prompts: []string{"security"}, Context: []string{"tests"}},
		{GroupID: "c", Tier: model.TierNone, RuleIndex: 1},
		{GroupID: "d", Tier: model.TierNone, Words: "no rule matched"},
	}
	auto := func(id string) []string { return []string{model.CtxCallers, "auto-" + id} }
	out, flags := applyUserTiers(decs, map[string]model.Tier{"a": model.TierDeep, "b": model.TierNone, "d": model.TierDeep}, auto)
	if a := out[0]; a.Tier != model.TierDeep || a.RuleIndex != 0 || a.Words != wordsForcedDeep || len(a.Context) != 1 || !flags["a"].forced {
		t.Errorf("forced = %+v", a)
	}
	// A group routed none carries no context: forcing it deep attaches the
	// stage-6 automatic context.
	if d := out[3]; d.Tier != model.TierDeep || len(d.Context) != 2 || d.Context[1] != "auto-d" {
		t.Errorf("forced from none = %+v", d)
	}
	if b := out[1]; b.Tier != model.TierNone || b.Words != wordsSkipped || b.Prompts != nil || b.Context != nil || !flags["b"].skipped {
		t.Errorf("skipped = %+v", b)
	}
	if c := out[2]; c.RuleIndex != 1 || flags["c"] != (userFlags{}) {
		t.Errorf("untouched = %+v", c)
	}
}

func TestSampled(t *testing.T) {
	for _, tc := range []struct {
		id   string
		rate float64
		want bool
	}{
		{"00000000ff", 0.05, true},
		{"0ccccccc00", 0.05, true},  // 0.05 - ε
		{"0cccccce00", 0.05, false}, // just above 0.05
		{"ffffffff00", 1, true},     // below 1 always
		{"80000000", 0.5, false},    // exactly 0.5 is not below
		{"7fffffff", 0.5, true},     // just below
		{"zzzzzzzz", 1, false},      // not hex
		{"abc", 1, false},           // too short
		{"00000000", 0, false},      // sampling off
	} {
		if got := sampled(tc.id, tc.rate); got != tc.want {
			t.Errorf("sampled(%q, %v) = %v, want %v", tc.id, tc.rate, got, tc.want)
		}
	}
}

func TestMissedFindings(t *testing.T) {
	fast := []model.Finding{
		{UnitID: "u1", Path: "a.go", Line: 10, Severity: model.SevLow},
		{Path: "b.go", Line: 5, Severity: model.SevHigh},
	}
	deep := []model.Finding{
		{UnitID: "u1", Path: "a.go", Line: 13, Severity: model.SevHigh},   // within 3 lines: matched
		{UnitID: "u1", Path: "a.go", Line: 20, Severity: model.SevMedium}, // missed
		{UnitID: "u2", Path: "a.go", Line: 10, Severity: model.SevHigh},   // other unit: missed
		{Path: "b.go", Line: 99, Severity: model.SevMedium},               // unanchored, same path: matched
		{Path: "c.go", Line: 1, Severity: model.SevMedium},                // unanchored, other path: missed
		{UnitID: "u3", Path: "d.go", Line: 1, Severity: model.SevLow},     // low: never counted
	}
	if got := missedFindings(fast, deep); got != 3 {
		t.Errorf("missed = %d, want 3", got)
	}
	if got := missedFindings(nil, nil); got != 0 {
		t.Errorf("missed of nothing = %d", got)
	}
}

func TestCapRunes(t *testing.T) {
	long := strings.Repeat("word ", 50)
	got := capRunes(long, review.MaxTLDR)
	if n := utf8.RuneCountInString(got); n > review.MaxTLDR || !strings.HasSuffix(got, "…") || strings.Contains(got, "wor…") {
		t.Errorf("capRunes = %q (%d runes)", got, n)
	}
	if got := capRunes("  short.  ", 160); got != "short." {
		t.Errorf("short = %q", got)
	}
	if got := capRunes(strings.Repeat("é", 200), 10); utf8.RuneCountInString(got) != 10 {
		t.Errorf("no spaces = %q", got)
	}
}

func TestDeepEstMs(t *testing.T) {
	rev := string(review.TaskReview)
	for _, tc := range []struct {
		calls []model.ReviewCall
		want  int64
	}{
		{nil, deepMsPerGroup},
		{[]model.ReviewCall{{Task: rev, Tier: model.TierFast, Ms: 500}}, 500 * deepMsFactor},
		{[]model.ReviewCall{{Task: rev, Tier: model.TierFast, Ms: 500}, {Task: rev, Tier: model.TierDeep, Ms: 3000}}, 3000},
		{[]model.ReviewCall{{Task: rev, Tier: model.TierDeep, Ms: 3000, Err: "boom"}}, deepMsPerGroup},
		{[]model.ReviewCall{{Task: "group-title", Tier: model.TierFast, Ms: 100}}, deepMsPerGroup},
	} {
		if got := deepEstMs(tc.calls); got != tc.want {
			t.Errorf("deepEstMs(%+v) = %d, want %d", tc.calls, got, tc.want)
		}
	}
}

func TestRunOptionsIgnoresCache(t *testing.T) {
	o := RunOptions{FromStage: model.StageReview}
	for s, want := range map[model.Stage]bool{
		model.StageFetch: false, model.StageCluster: false, model.StageRoute: false,
		model.StageReview: true, model.StageAggregate: true,
	} {
		if got := o.ignoresCache(s); got != want {
			t.Errorf("ignoresCache(%s) = %v", s, got)
		}
	}
	if (RunOptions{}).ignoresCache(model.StageAggregate) || (RunOptions{FromStage: "bogus"}).ignoresCache(model.StageAggregate) {
		t.Error("no FromStage must ignore nothing")
	}
	if !ValidStage(model.StageJev) || ValidStage("bogus") {
		t.Error("ValidStage")
	}
}
