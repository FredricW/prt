package aggregate

import (
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// labelsOf indexes short labels for one unit.
func labelsOf(ls ...shortLabel) *unitLabels {
	all := make([]model.Label, 0, len(ls))
	for _, s := range ls {
		if s.U == "" {
			s.U = "u"
		}
		all = append(all, s.label())
	}
	return newIndex("pr:1", all, nil).units["u"]
}

func TestAttention(t *testing.T) {
	// unknownAll is a unit whose risk and behaviour are unknown too.
	unknownAll := model.UnitAgg{BehaviourUnknown: true}
	tests := []struct {
		name  string
		depth string
		a     model.UnitAgg
		test  bool
		want  model.Attention
	}{
		{"read", model.DepthRead, model.UnitAgg{}, false, model.AttnEyes},
		{"run locally", model.DepthRun, model.UnitAgg{}, false, model.AttnEyes},
		{"skim", model.DepthSkim, model.UnitAgg{}, false, model.AttnSkim},
		{"skip", model.DepthSkip, model.UnitAgg{}, false, model.AttnSkip},
		{"unsure skip is skim", model.DepthSkip, model.UnitAgg{Unsure: true}, false, model.AttnSkim},
		{"noise is skip", model.DepthRead, model.UnitAgg{Noise: true}, false, model.AttnSkip},
		{"unsure noise is skim", model.DepthRead, model.UnitAgg{Noise: true, Unsure: true}, false, model.AttnSkim},
		{"unknown depth, nothing known", model.LabelUnknown, unknownAll, false, model.AttnEyes},
		{"missing depth, nothing known", "", unknownAll, false, model.AttnEyes},
		{"unknown depth, risky", model.LabelUnknown, model.UnitAgg{RiskLevel: 3}, false, model.AttnEyes},
		{"unknown depth, behaviour", model.LabelUnknown, model.UnitAgg{RiskLevel: 1, Behaviour: true}, false, model.AttnEyes},
		{"unknown depth, low risk, no behaviour", model.LabelUnknown, model.UnitAgg{RiskLevel: 2}, false, model.AttnSkim},
		{"unknown depth, behaviour no, risk unknown", "", model.UnitAgg{}, false, model.AttnSkim},
		{"test read caps at skim", model.DepthRead, model.UnitAgg{}, true, model.AttnSkim},
		{"test skip stays skip", model.DepthSkip, model.UnitAgg{}, true, model.AttnSkip},
		{"test unknown risky caps at skim", model.LabelUnknown, model.UnitAgg{RiskLevel: 4}, true, model.AttnSkim},
	}
	for _, tc := range tests {
		if got := attention(tc.depth, tc.a, tc.test); got != tc.want {
			t.Errorf("%s: attention = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestIsTestUnit(t *testing.T) {
	for _, tc := range []struct {
		path, kind string
		want       bool
	}{
		{"web/src/__tests__/a.test.ts", "feature", true},
		{"server/users_test.go", "", true},
		{"web/src/helpers.ts", "test", true},
		{"web/src/helpers.ts", "feature", false},
	} {
		if got := isTestUnit(model.ChangeUnit{Path: tc.path}, tc.kind); got != tc.want {
			t.Errorf("isTestUnit(%q, %q) = %v", tc.path, tc.kind, got)
		}
	}
}

func TestDecisionConfidence(t *testing.T) {
	c := func(f float64) *float64 { return &f }
	tests := []struct {
		name string
		l    shortLabel
		want float64
	}{
		{"boolean keeps confidence", shortLabel{Q: "breaking", P: 0.3}, 0.7},
		{"kind keeps its P", shortLabel{Q: "kind", V: "feature", P: 0.55, Probs: map[string]float64{"feature": 0.55, "refactor": 0.45}}, 0.55},
		{"depth read counts run locally", shortLabel{Q: "depth", V: "read", P: 0.49, Probs: map[string]float64{"read": 0.49, "run locally": 0.11, "skim": 0.38, "skip": 0.02}}, 0.60},
		{"depth skim is its own bucket", shortLabel{Q: "depth", V: "skim", P: 0.41, Probs: map[string]float64{"read": 0.33, "run locally": 0.11, "skim": 0.41, "skip": 0.15}}, 0.41},
		{"risk 3 sides", shortLabel{Q: "risk", V: "3", P: 0.55, Probs: map[string]float64{"2": 0.36, "3": 0.55, "4": 0.08, "5": 0.01}}, 0.64},
		{"risk 4 sides", shortLabel{Q: "risk", V: "4", P: 0.53, Probs: map[string]float64{"2": 0.15, "3": 0.30, "4": 0.53, "5": 0.02}}, 0.55},
		{"risk 1 sides", shortLabel{Q: "risk", V: "1", P: 0.5, Probs: map[string]float64{"1": 0.5, "2": 0.3, "3": 0.2}}, 0.8},
		{"no probs keeps confidence", shortLabel{Q: "depth", V: "read", P: 0.45, C: c(0.45)}, 0.45},
		{"unknown is 0", shortLabel{Q: "risk", V: "unknown"}, 0},
	}
	for _, tc := range tests {
		if got := decisionConfidence(tc.l.label()); !near(got, tc.want) {
			t.Errorf("%s: decisionConfidence = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestMinutes(t *testing.T) {
	tests := []struct {
		at   model.Attention
		n    int
		want float64
	}{
		{model.AttnEyes, 1, 1},
		{model.AttnEyes, 2, 1},
		{model.AttnEyes, 10, 5},
		{model.AttnSkim, 10, 1},
		{model.AttnSkim, 3, 0.3},
		{model.AttnSkip, 10, 0},
		{model.AttnEyes, 0, 0},
	}
	for _, tc := range tests {
		if got := minutes(tc.at, tc.n); !near(got, tc.want) {
			t.Errorf("minutes(%s, %d) = %v, want %v", tc.at, tc.n, got, tc.want)
		}
	}
}

func TestLineMarkPrecedence(t *testing.T) {
	tests := []struct {
		name   string
		labels []shortLabel
		want   model.LineMark
	}{
		{"suspicious beats value and leftover", []shortLabel{
			{Q: "suspicious", L: 1, V: "3", S: 3.4, P: 0.6}, {Q: "value_tweak", L: 1, P: 0.9}, {Q: "leftover", L: 1, P: 0.9},
		}, model.LineMark{Line: 1, Mark: model.MarkSuspicious, Score: 3.4, P: 0.6}},
		{"low suspicious falls through to value", []shortLabel{
			{Q: "suspicious", L: 1, V: "2", S: 2.4, P: 0.6}, {Q: "value_tweak", L: 1, P: 0.7}, {Q: "leftover", L: 1, P: 0.9},
		}, model.LineMark{Line: 1, Mark: model.MarkValue, P: 0.7}},
		{"value below half falls through to leftover", []shortLabel{
			{Q: "value_tweak", L: 1, P: 0.4}, {Q: "leftover", L: 1, P: 0.5},
		}, model.LineMark{Line: 1, Mark: model.MarkLeftover, P: 0.5}},
		{"fmt only", []shortLabel{{Q: "fmt", L: 1, P: 0.8}}, model.LineMark{Line: 1, Mark: model.MarkNone, Fmt: true}},
		{"unknown suspicious", []shortLabel{{Q: "suspicious", L: 1, V: "unknown"}}, model.LineMark{Line: 1, Mark: model.MarkNone}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			marks := lineMarks(labelsOf(tc.labels...))
			assertEqual(t, "marks", marks, []model.LineMark{tc.want})
		})
	}
}

func TestRiskTagsDedupe(t *testing.T) {
	ul := labelsOf(
		shortLabel{Q: "risk_tags.pii", P: 0.7},
		shortLabel{Q: "risk_tags.error_path", P: 0.2},
		shortLabel{Q: "risk_tags.auth", P: 0.5},
		shortLabel{Q: "swallow", L: 3, P: 0.9},
		shortLabel{Q: "pii_or_secret", L: 4, P: 0.6},
		shortLabel{Q: "risk_tags.money", V: "unknown"},
	)
	assertEqual(t, "tags", riskTags(ul), []string{"auth", "error_path", "pii"})
}

func facts(id string, risk, relevance, covered, conf float64) unitFacts {
	return unitFacts{
		unit: model.ChangeUnit{ID: id, Path: id + ".go"},
		agg:  model.UnitAgg{UnitID: id, Path: id + ".go", Risk: risk, Relevance: relevance, CoveredP: covered, MinConfidence: conf, RiskLevel: int(risk)},
	}
}

func TestHotSpotsOrderAndCut(t *testing.T) {
	us := []unitFacts{
		facts("a", 2, 2, 0, 1),   // 4
		facts("b", 4, 2, 0, 1),   // 8
		facts("c", 2, 2, 0, 1),   // 4, tie with a: unit order keeps a first
		facts("d", 5, 5, 1, 1),   // fully covered: 0, skipped
		facts("e", 3, 3, 0.5, 1), // 4.5
		facts("f", 0, 5, 0, 1),   // unknown risk: skipped
	}
	got := hotSpots(us, nil)
	ids := make([]string, 0, len(got))
	for _, h := range got {
		ids = append(ids, h.UnitID)
	}
	assertEqual(t, "hot spots", ids, []string{"b", "e", "a"})
	if len(hotSpots(nil, nil)) != 0 || hotSpots(nil, nil) == nil {
		t.Error("no units: want empty, non-nil hot spots")
	}
}

func TestHotSpotReason(t *testing.T) {
	tests := []struct {
		a    model.UnitAgg
		want string
	}{
		{model.UnitAgg{RiskTags: []string{"io_in_loop", "money"}, FailMode: "slowdown", Untested: true}, "io-in-loop, slowdown, untested"},
		{model.UnitAgg{RiskTags: []string{"date_tz"}, FailMode: "wrong data"}, "date/tz, wrong data"},
		{model.UnitAgg{FailMode: "a b c d e f"}, "a b c d e"},
		{model.UnitAgg{RiskLevel: 4, Untested: true}, "untested"},
		{model.UnitAgg{RiskLevel: 4}, "risk 4 of 5"},
	}
	for _, tc := range tests {
		got := hotSpotReason(tc.a)
		if got != tc.want {
			t.Errorf("reason(%+v) = %q, want %q", tc.a, got, tc.want)
		}
		if n := len(strings.Fields(got)); n > maxReasonWords {
			t.Errorf("reason %q has %d words", got, n)
		}
	}
}

func TestRiskAny(t *testing.T) {
	withRisk := func(level string, p float64, probs map[string]float64) unitFacts {
		ul := labelsOf(shortLabel{Q: "risk", V: level, P: p, Probs: probs})
		return unitFacts{labels: ul, agg: model.UnitAgg{RiskLevel: model.ScoreLevel(ul.q["risk"])}}
	}
	tests := []struct {
		name string
		us   []unitFacts
		want float64
	}{
		{"none risky", []unitFacts{withRisk("3", 0.9, map[string]float64{"3": 0.9, "4": 0.1})}, 0},
		{"one from probs", []unitFacts{withRisk("4", 0.5, map[string]float64{"4": 0.5, "5": 0.3})}, 0.8},
		{"probs fallback to P", []unitFacts{withRisk("5", 0.7, nil)}, 0.7},
		{"two combine", []unitFacts{
			withRisk("4", 0.5, map[string]float64{"4": 0.5}),
			withRisk("5", 0.6, map[string]float64{"5": 0.6}),
			withRisk("2", 0.9, nil),
		}, 1 - 0.5*0.4},
	}
	for _, tc := range tests {
		if got := riskAny(tc.us); !near(got, tc.want) {
			t.Errorf("%s: riskAny = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHeadlineText(t *testing.T) {
	long := strings.Repeat("x", 80)
	tests := []struct {
		u    model.ChangeUnit
		want string
	}{
		{model.ChangeUnit{Symbol: "Logout", Signature: "func Logout()", Path: "a.go"}, "Logout"},
		{model.ChangeUnit{Signature: "func Logout()", Path: "a.go"}, "func Logout()"},
		{model.ChangeUnit{Path: "a.go"}, "a.go"},
		{model.ChangeUnit{Symbol: long}, strings.Repeat("x", maxHeadlineRunes-1) + "…"},
	}
	for _, tc := range tests {
		if got := headlineText(tc.u); got != tc.want {
			t.Errorf("headlineText = %q, want %q", got, tc.want)
		}
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		name string
		u    model.ChangeUnit
		want int
	}{
		{"context then add", model.ChangeUnit{Diff: " a\n b\n+c", Range: model.Range{New: model.Span{Start: 10}}}, 12},
		{"pure deletion uses old side", model.ChangeUnit{Diff: " a\n-b", Dels: 1, Range: model.Range{Old: model.Span{Start: 20}, New: model.Span{Start: 30}}}, 21},
		{"header and no-newline marker skipped", model.ChangeUnit{Diff: "@@ -1 +1 @@\n\\ No newline at end of file\n+x", Range: model.Range{New: model.Span{Start: 5}}}, 5},
		{"no diff, deletions", model.ChangeUnit{Dels: 2, Range: model.Range{Old: model.Span{Start: 8}, New: model.Span{Start: 7}}}, 8},
		{"no diff, additions", model.ChangeUnit{Adds: 2, Range: model.Range{Old: model.Span{Start: 8}, New: model.Span{Start: 7}}}, 7},
	}
	for _, tc := range tests {
		if got := firstLine(tc.u); got != tc.want {
			t.Errorf("%s: firstLine = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestConfidenceThreshold(t *testing.T) {
	tests := []struct {
		name     string
		ul       *unitLabels
		below    float64
		wantConf float64
		unsure   bool
	}{
		{"no labels", nil, 0.6, 0, true},
		{"at threshold is sure", labelsOf(shortLabel{Q: "kind", V: "feature", P: 0.6}), 0.6, 0.6, false},
		{"below threshold", labelsOf(shortLabel{Q: "kind", V: "feature", P: 0.59}), 0.6, 0.59, true},
		{"line label does not count", labelsOf(shortLabel{Q: "kind", V: "feature", P: 0.9}, shortLabel{Q: "fmt", L: 2, P: 0.45}), 0.6, 0.9, false},
		{"risk tag does not count", labelsOf(shortLabel{Q: "kind", V: "feature", P: 0.9}, shortLabel{Q: "risk_tags.auth", P: 0.5}), 0.6, 0.9, false},
		{"relevance does not count", labelsOf(shortLabel{Q: "covered", P: 0.2}, shortLabel{Q: "relevance", V: "3", P: 0.3}), 0.6, 0.8, false},
		{"only other labels", labelsOf(shortLabel{Q: "relevance", V: "3", P: 0.9}), 0.6, 0, true},
		{"unknown counts as 0", labelsOf(shortLabel{Q: "kind", V: "feature", P: 0.9}, shortLabel{Q: "depth", V: "unknown"}), 0.6, 0, true},
		{"breaking near half", labelsOf(shortLabel{Q: "kind", V: "feature", P: 0.9}, shortLabel{Q: "breaking", P: 0.55}), 0.6, 0.55, true},
		{"custom threshold", labelsOf(shortLabel{Q: "kind", V: "feature", P: 0.7}), 0.8, 0.7, true},
	}
	for _, tc := range tests {
		conf, unsure := confidence(tc.ul, tc.below)
		if !near(conf, tc.wantConf) || unsure != tc.unsure {
			t.Errorf("%s: confidence = %v, %v; want %v, %v", tc.name, conf, unsure, tc.wantConf, tc.unsure)
		}
	}
}

func TestUnsureBelowDefault(t *testing.T) {
	in := loadFixture(t, "profile.json")
	in.Pipeline.UnsureBelow = 0
	if got := Item(in); got.Unsure != 0 || len(got.UnsureUnits) != 1 {
		t.Errorf("unsure with default threshold = %d units %v, want 0 and [u6]", got.Unsure, got.UnsureUnits)
	}
	in.Pipeline.UnsureBelow = 0.95
	if got := Item(in); got.Unsure == 0 || len(got.UnsureUnits) != 6 {
		t.Errorf("unsure with 0.95 = %d units %v", got.Unsure, got.UnsureUnits)
	}
}
