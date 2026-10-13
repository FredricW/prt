package route

import (
	"slices"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

func defaultRules(t *testing.T) Rules {
	t.Helper()
	rs, err := Compile(config.DefaultRouting())
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestRouteDefaultRules(t *testing.T) {
	two := []model.ChangeUnit{unit("a", 1), unit("c", 2)}
	quiet := func(kind string) []model.Label {
		return append(fullLabels("a", kind), fullLabels("c", kind)...)
	}
	tests := []struct {
		name      string
		facts     func() GroupFacts
		tier      model.Tier
		ruleIndex int
		unknown   bool
		prompts   []string
		context   []string // exact, unless unknown
		words     string
	}{
		{
			name: "mechanical",
			facts: func() GroupFacts {
				f := group(two, quiet("feature"))
				f.Group.Mechanical = true
				return f
			},
			tier: model.TierNone, ruleIndex: 1, words: "if mechanical || kind in [formatting, moved, generated, dependency] → none",
		},
		{
			name:  "all dependency",
			facts: func() GroupFacts { return group(two, quiet("dependency")) },
			tier:  model.TierNone, ruleIndex: 1,
		},
		{
			name: "risk 4 with auth",
			facts: func() GroupFacts {
				ls := set(quiet("feature"), score("a", model.QRisk, 4, 4.1))
				ls = set(ls, yes("c", "risk_tags.auth"))
				ls = set(ls, yes("c", model.QBreaking))
				f := group(two, ls)
				f.Aggs = []model.UnitAgg{{UnitID: "a", Untested: true}, {UnitID: "c", RiskTags: []string{"auth"}}}
				return f
			},
			tier: model.TierDeep, ruleIndex: 2, prompts: []string{"security"}, context: []string{"callers", "tests"},
		},
		{
			name:  "risk 4 without security tags",
			facts: func() GroupFacts { return group(two, set(quiet("feature"), score("a", model.QRisk, 4, 4.1))) },
			tier:  model.TierDeep, ruleIndex: 2, prompts: nil, context: []string{"callers", "tests"},
		},
		{
			name:  "async tag without security tags",
			facts: func() GroupFacts { return group(two, set(quiet("feature"), yes("a", "risk_tags.async"))) },
			tier:  model.TierDeep, ruleIndex: 2, prompts: []string{"performance"}, context: []string{"callers", "tests"},
		},
		{
			name: "pii on the catch-all rule gets security",
			facts: func() GroupFacts {
				return group(two, set(set(quiet("feature"), yes("a", "risk_tags.pii")), score("a", model.QRisk, 3, 3)))
			},
			tier: model.TierFast, ruleIndex: 5, prompts: []string{"security"},
		},
		{
			name:  "breaking only",
			facts: func() GroupFacts { return group(two, set(quiet("feature"), yes("a", model.QBreaking))) },
			tier:  model.TierFast, ruleIndex: 3, context: []string{"callers"},
		},
		{
			name:  "kind test",
			facts: func() GroupFacts { return group(two, quiet("test")) },
			tier:  model.TierFast, ruleIndex: 4, prompts: []string{"tests"}, context: []string{"code_under_test"},
		},
		{
			name:  "otherwise fast",
			facts: func() GroupFacts { return group(two, quiet("feature")) },
			tier:  model.TierFast, ruleIndex: 5, words: "if true → fast",
		},
		{
			name: "auto context: signature, env/flag",
			facts: func() GroupFacts {
				ls := set(quiet("feature"), choice("a", model.QContract, "signature"))
				return group(two, set(ls, choice("c", model.QContract, "env/flag")))
			},
			tier: model.TierFast, ruleIndex: 5, context: []string{"types", "config"},
		},
		{
			// The kind is feature or mixed, never a noise kind: rule 1 is False.
			name:  "unknown kind beside a known feature",
			facts: func() GroupFacts { return group(two, set(quiet("feature"), unknownLabel("a", model.QKind))) },
			tier:  model.TierFast, ruleIndex: 5,
		},
		{
			// Two known kinds differ: mixed whatever the third says.
			name: "feature, test, unknown kind",
			facts: func() GroupFacts {
				three := []model.ChangeUnit{unit("a", 1), unit("c", 2), unit("d", 3)}
				ls := append(append(fullLabels("a", "feature"), fullLabels("c", "test")...), fullLabels("d", "feature")...)
				return group(three, set(ls, unknownLabel("d", model.QKind)))
			},
			tier: model.TierFast, ruleIndex: 5, words: "if true → fast", prompts: []string{"tests"},
		},
		{
			name:  "unknown kind beside a known dependency",
			facts: func() GroupFacts { return group(two, set(quiet("dependency"), unknownLabel("a", model.QKind))) },
			tier:  model.TierDeep, unknown: true, words: "labels unknown → deep (rule 1)",
		},
		{
			name: "labels missing, not mechanical",
			facts: func() GroupFacts {
				f := group(two, nil)
				f.LabelsMissing = true
				return f
			},
			tier: model.TierDeep, unknown: true, words: "labels unknown → deep (rule 1)",
		},
		{
			name: "mechanical with unknown labels",
			facts: func() GroupFacts {
				f := group(two, nil)
				f.Group.Mechanical = true
				f.LabelsMissing = true
				return f
			},
			tier: model.TierNone, ruleIndex: 1,
		},
		{
			name:  "unknown risk",
			facts: func() GroupFacts { return group(two, set(quiet("feature"), unknownLabel("c", model.QRisk))) },
			tier:  model.TierDeep, unknown: true, words: "labels unknown → deep (rule 2)",
		},
	}
	rs := defaultRules(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.facts()
			d := Route(rs, []GroupFacts{f})[0]
			if d.GroupID != f.Group.ID || d.Tier != tt.tier || d.RuleIndex != tt.ruleIndex || d.Unknown != tt.unknown || d.Err != "" {
				t.Fatalf("decision = %+v, want tier %s rule %d unknown %v", d, tt.tier, tt.ruleIndex, tt.unknown)
			}
			if !slices.Equal(d.Prompts, tt.prompts) {
				t.Errorf("prompts = %v, want %v", d.Prompts, tt.prompts)
			}
			if !tt.unknown && !slices.Equal(d.Context, tt.context) {
				t.Errorf("context = %v, want %v", d.Context, tt.context)
			}
			if tt.words != "" && d.Words != tt.words {
				t.Errorf("words = %q, want %q", d.Words, tt.words)
			}
			if tt.unknown && d.Rule != "" {
				t.Errorf("rule = %q, want empty", d.Rule)
			}
			if !tt.unknown && d.RuleIndex > 0 && d.Rule != rs.Rules[d.RuleIndex-1].When.String() {
				t.Errorf("rule = %q", d.Rule)
			}
			if d.Tier == model.TierNone && (d.Prompts != nil || d.Context != nil) {
				t.Errorf("tier none with prompts %v context %v", d.Prompts, d.Context)
			}
		})
	}
}

func TestRouteNoMatchAndTypeErrors(t *testing.T) {
	rs, err := Compile(config.Routing{Rules: []config.Rule{
		{When: "risk && kind", Tier: "deep"},
		{When: "units > 5", Tier: "fast"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	f := group([]model.ChangeUnit{unit("a", 1)}, fullLabels("a", "feature"))
	d := Route(rs, []GroupFacts{f})[0]
	if d.Tier != model.TierNone || d.Words != "no rule matched" || d.RuleIndex != 0 {
		t.Errorf("decision = %+v, want no rule matched", d)
	}
	if !strings.HasPrefix(d.Err, "rule 1: ") || !strings.Contains(d.Err, "not a condition") {
		t.Errorf("err = %q", d.Err)
	}
}

// A known unit that already settles a max comparison decides it even when
// another unit's label is unknown (Kleene: max ≥ 5 ≥ 4).
func TestRouteKnownMaxSettlesComparison(t *testing.T) {
	rs, err := Compile(config.Routing{Rules: []config.Rule{
		{When: "risk >= 4 || tag.auth", Tier: "deep", Context: []string{"tests"}},
		{When: "true", Tier: "fast"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, c := unit("a", 1), unit("c", 2)
	ls := append(set(fullLabels("a", "feature"), score("a", model.QRisk, 5, 5)), fullLabels("c", "feature")...)
	ls = set(ls, unknownLabel("c", model.QRisk))
	d := Route(rs, []GroupFacts{group([]model.ChangeUnit{a, c}, ls)})[0]
	if d.Tier != model.TierDeep || d.Unknown || d.RuleIndex != 1 || !slices.Contains(d.Context, model.CtxTests) {
		t.Errorf("{5, unknown} vs risk >= 4: %+v, want rule 1 deep with its context", d)
	}
	for _, tc := range []struct {
		when string
		want Tri
	}{
		{"risk >= 4", True}, {"risk > 4", True}, {"risk >= 6", Unknown},
		{"risk < 5", False}, {"risk <= 4", False}, {"risk <= 5", Unknown},
		{"4 <= risk", True}, {"6 < risk", Unknown}, {"5 > risk", False},
		{"risk == 5", Unknown}, {"risk in [5]", Unknown},
	} {
		e, err := Parse(tc.when)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := e.Eval(NewEnv(group([]model.ChangeUnit{a, c}, ls))); err != nil || got != tc.want {
			t.Errorf("%s = %v (%v), want %v", tc.when, got, err, tc.want)
		}
	}
}

// A choice whose known units agree on v and whose other units are unknown
// can only be v or "mixed": comparisons both candidates answer alike decide.
func TestEvalEitherChoice(t *testing.T) {
	a, c := unit("a", 1), unit("c", 2)
	ls := set(append(fullLabels("a", "feature"), fullLabels("c", "feature")...), unknownLabel("c", model.QKind))
	env := NewEnv(group([]model.ChangeUnit{a, c}, ls))
	for _, tc := range []struct {
		when string
		want Tri
	}{
		{"kind in [formatting, moved]", False},
		{"kind in [feature, mixed]", True},
		{"kind in [feature]", Unknown},
		{"kind == test", False},
		{"kind != test", True},
		{"kind == feature", Unknown},
		{`"test" == kind`, False},
	} {
		e, err := Parse(tc.when)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := e.Eval(env); err != nil || got != tc.want {
			t.Errorf("%s = %v (%v), want %v", tc.when, got, err, tc.want)
		}
	}
}

func TestRoutePrompts(t *testing.T) {
	rs, err := Compile(config.Routing{Rules: []config.Rule{
		{When: "true", Tier: "deep", Prompts: []string{"security", "performance", "tests", "security"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	u := []model.ChangeUnit{unit("a", 1)}
	tests := []struct {
		name   string
		labels []model.Label
		aggs   []model.UnitAgg
		want   []string
	}{
		{"none", fullLabels("a", "feature"), nil, nil},
		{"pii tag from agg", fullLabels("a", "feature"), []model.UnitAgg{{UnitID: "a", RiskTags: []string{"pii"}}}, []string{"security"}},
		{"money tag", set(fullLabels("a", "feature"), yes("a", "risk_tags.money")), nil, []string{"security"}},
		{"io in loop", set(fullLabels("a", "feature"), yes("a", "risk_tags.io_in_loop")), nil, []string{"performance"}},
		{"perf kind", fullLabels("a", "perf"), nil, []string{"performance"}},
		{"performance effect", set(fullLabels("a", "feature"), choice("a", model.QEffect, "performance")), nil, []string{"performance"}},
		{"test weakened", set(fullLabels("a", "feature"), yes("a", model.QTestWeakened)), nil, []string{"tests"}},
		{"test kind and auth", set(fullLabels("a", "test"), yes("a", "risk_tags.auth")), nil, []string{"security", "tests"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := group(u, tt.labels)
			f.Aggs = tt.aggs
			d := Route(rs, []GroupFacts{f})[0]
			if !slices.Equal(d.Prompts, tt.want) {
				t.Errorf("prompts = %v, want %v", d.Prompts, tt.want)
			}
		})
	}
}

func TestRouteHotSpot(t *testing.T) {
	rs := defaultRules(t)
	f := group([]model.ChangeUnit{unit("a", 1), unit("c", 2)}, append(fullLabels("a", "feature"), fullLabels("c", "feature")...))
	f.Aggs = []model.UnitAgg{
		{UnitID: "a", Risk: 2, Relevance: 3, CoveredP: 0.5, MinConfidence: 1}, // 3
		{UnitID: "c", Risk: 4, Relevance: 2, CoveredP: 0, MinConfidence: 0.5}, // 4
	}
	if d := Route(rs, []GroupFacts{f})[0]; d.HotSpot != 4 {
		t.Errorf("hot spot = %v, want 4", d.HotSpot)
	}
	f.GroupAgg.HotSpot = 7
	if d := Route(rs, []GroupFacts{f})[0]; d.HotSpot != 7 {
		t.Errorf("hot spot = %v, want GroupAgg 7", d.HotSpot)
	}
}
