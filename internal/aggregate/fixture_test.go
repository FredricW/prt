package aggregate

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

const tol = 1e-9

// fixture is a synthetic PR scenario under testdata/.
type fixture struct {
	Item   model.Item         `json:"item"`
	Owns   []string           `json:"owns"`
	Files  []model.File       `json:"files"`
	Units  []model.ChangeUnit `json:"units"`
	Groups []model.Group      `json:"groups"`
	Labels []shortLabel       `json:"labels"`
}

// shortLabel is a compact label: a boolean when V is empty (P is P(yes)),
// the Jev-unreachable placeholder when V is "unknown", otherwise a choice
// or score answer (Score defaults to the rung for numeric values).
// Confidence follows the label conventions unless C is set.
type shortLabel struct {
	U     string             `json:"u"`
	Q     string             `json:"q"`
	L     int                `json:"l"`
	V     string             `json:"v"`
	P     float64            `json:"p"`
	C     *float64           `json:"c"`
	S     float64            `json:"s"`
	Probs map[string]float64 `json:"probs"`
}

func (s shortLabel) label() model.Label {
	l := model.Label{UnitID: s.U, Question: s.Q, Line: s.L, Model: "typesafe-ai/jev", Probs: s.Probs}
	switch s.V {
	case "":
		l.P = s.P
		l.Value = model.LabelNo
		if s.P >= 0.5 {
			l.Value = model.LabelYes
		}
		l.Confidence = max(s.P, 1-s.P)
	case model.LabelUnknown:
		l.Value, l.Model = model.LabelUnknown, ""
	default:
		l.Value, l.P, l.Confidence, l.Score = s.V, s.P, s.P, s.S
		if n, err := strconv.Atoi(s.V); err == nil && l.Score == 0 {
			l.Score = float64(n)
		}
	}
	if s.C != nil {
		l.Confidence = *s.C
	}
	return l
}

func loadFixture(t *testing.T, name string) Input {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	in := Input{
		Item: f.Item, Files: f.Files, Units: f.Units, Groups: f.Groups, Owns: f.Owns,
		Weights: config.Defaults().Rank, Pipeline: config.Defaults().Pipeline,
	}
	for i := range in.Units {
		in.Units[i].ItemID = f.Item.ID
	}
	// Fixtures write line labels by new-side line number for readability;
	// stored labels use the position among the unit's added lines.
	added := map[string][]int{}
	for _, u := range in.Units {
		added[u.ID] = model.AddedLineNumbers(u)
	}
	for _, s := range f.Labels {
		l := s.label()
		if nums := added[l.UnitID]; l.Line > 0 && len(nums) > 0 {
			pos := slices.Index(nums, l.Line)
			if pos < 0 {
				t.Fatalf("fixture %s: label %+v is not on an added line of %s", name, s, l.UnitID)
			}
			l.Line = pos + 1
		}
		in.Labels = append(in.Labels, l)
	}
	return in
}

func near(a, b float64) bool { return math.Abs(a-b) < tol }

func unitByID(t *testing.T, agg model.ItemAgg, id string) model.UnitAgg {
	t.Helper()
	for _, u := range agg.Units {
		if u.UnitID == id {
			return u
		}
	}
	t.Fatalf("unit %s missing", id)
	return model.UnitAgg{}
}

func assertComposition(t *testing.T, got, want map[string]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("composition = %v, want %v", got, want)
	}
	for k, v := range want {
		if !near(got[k], v) {
			t.Errorf("composition[%s] = %v, want %v", k, got[k], v)
		}
	}
}

func assertEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

func TestProfileUnits(t *testing.T) {
	agg := Item(loadFixture(t, "profile.json"))
	type want struct {
		kind      string
		attn      model.Attention
		effective int
		minutes   float64
		minConf   float64
		unsure    bool
		noise     string
		riskLevel int
		tags      []string
		untested  bool
		undoc     bool
		bullet    int
		contract  string
		owned     bool
	}
	tests := []struct {
		id string
		w  want
	}{
		{"u1", want{kind: "feature", attn: model.AttnEyes, effective: 4, minutes: 2, minConf: 0.6, riskLevel: 4,
			tags: []string{"data_write"}, untested: true, bullet: 1, contract: "none", owned: true}},
		{"u2", want{kind: "test", attn: model.AttnSkim, effective: 3, minutes: 0.3, minConf: 0.7, riskLevel: 1,
			contract: "none", owned: true}},
		{"u3", want{kind: "bugfix", attn: model.AttnEyes, effective: 5, minutes: 2.5, minConf: 0.5, unsure: true, riskLevel: 5,
			tags: []string{"auth", "error_path", "pii"}, untested: true, undoc: true, contract: "signature"}},
		{"u4", want{kind: "feature", attn: model.AttnEyes, effective: 4, minutes: 2, minConf: 0.6, riskLevel: 3,
			bullet: 1, contract: "api shape"}},
		{"u5", want{kind: "docs", attn: model.AttnSkip, effective: 2, minutes: 0, minConf: 0.6, riskLevel: 1, bullet: 3}},
		{"u6", want{kind: "", attn: model.AttnSkim, effective: 0, minutes: 0, minConf: 0, unsure: true,
			noise: model.NoiseMoved, owned: true}},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			u, w := unitByID(t, agg, tc.id), tc.w
			if u.Kind != w.kind || u.Attention != w.attn || u.EffectiveLines != w.effective || u.Unsure != w.unsure ||
				u.NoiseReason != w.noise || u.Noise != (w.noise != "") || u.RiskLevel != w.riskLevel ||
				u.Untested != w.untested || u.Undocumented != w.undoc || u.DescBullet != w.bullet ||
				u.Contract != w.contract || u.Owned != w.owned {
				t.Errorf("unit = %+v, want %+v", u, w)
			}
			if !near(u.Minutes, w.minutes) || !near(u.MinConfidence, w.minConf) {
				t.Errorf("minutes, minConf = %v, %v; want %v, %v", u.Minutes, u.MinConfidence, w.minutes, w.minConf)
			}
			assertEqual(t, "risk tags", u.RiskTags, w.tags)
		})
	}

	u1 := unitByID(t, agg, "u1")
	if !u1.Behaviour || !near(u1.BehaviourP, 0.9) || u1.Breaking || !near(u1.BreakingP, 0.1) || u1.Covered ||
		!near(u1.CoveredP, 0.2) || !near(u1.Risk, 4.3) || !near(u1.Relevance, 4.6) || u1.FailMode != "wrong data" {
		t.Errorf("u1 facts = %+v", u1)
	}
	assertEqual(t, "u1 lines", u1.Lines, []model.LineMark{
		{Line: 10, Mark: model.MarkSuspicious, Score: 3.8, P: 0.7},
		{Line: 11, Mark: model.MarkValue, P: 0.8},
		{Line: 12, Mark: model.MarkLeftover, P: 0.7},
		{Line: 13, Mark: model.MarkNone, Fmt: true},
	})
	if u3 := unitByID(t, agg, "u3"); !u3.Breaking || u3.FailMode != "crash" || len(u3.Lines) != 2 {
		t.Errorf("u3 = %+v", u3)
	}
	assertEqual(t, "unit order", unitIDs(agg.Units), []string{"u1", "u2", "u3", "u4", "u5", "u6"})
}

// Regression: line labels are cached by unit id, so a unit that keeps its
// content but moves (rebase, another item) must carry its marks along.
func TestLineLabelsFollowShiftedUnit(t *testing.T) {
	in := loadFixture(t, "profile.json")
	const shift = 25
	for i := range in.Units {
		if in.Units[i].ID == "u1" || in.Units[i].ID == "u3" {
			in.Units[i].Range.New.Start += shift
		}
	}
	agg := Item(in)
	assertEqual(t, "u1 lines", unitByID(t, agg, "u1").Lines, []model.LineMark{
		{Line: 10 + shift, Mark: model.MarkSuspicious, Score: 3.8, P: 0.7},
		{Line: 11 + shift, Mark: model.MarkValue, P: 0.8},
		{Line: 12 + shift, Mark: model.MarkLeftover, P: 0.7},
		{Line: 13 + shift, Mark: model.MarkNone, Fmt: true},
	})
	found := false
	for _, h := range agg.Headlines {
		if h.Kind == "pii or secret" {
			found = true
			if h.UnitID != "u3" || h.Line != 43+shift {
				t.Errorf("pii headline = %+v, want u3 line %d", h, 43+shift)
			}
		}
	}
	if !found {
		t.Error("pii headline missing")
	}
}

func unitIDs(us []model.UnitAgg) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		out = append(out, u.UnitID)
	}
	return out
}

func TestProfileItem(t *testing.T) {
	in := loadFixture(t, "profile.json")
	agg := Item(in)

	if agg.Added != 15 || agg.Removed != 6 || agg.EffectiveLines != 18 || agg.NoiseLines != 3 || agg.UnitsTotal != 6 {
		t.Errorf("lines = +%d -%d eff %d noise %d units %d", agg.Added, agg.Removed, agg.EffectiveLines, agg.NoiseLines, agg.UnitsTotal)
	}
	assertComposition(t, agg.Composition, map[string]float64{"feature": 8.0 / 18, "test": 3.0 / 18, "bugfix": 5.0 / 18, "docs": 2.0 / 18})
	if !near(agg.Minutes, 6.8) {
		t.Errorf("minutes = %v, want 6.8 (label-based)", agg.Minutes)
	}
	if agg.UnitsEyes != 3 || agg.UnitsSkim != 2 || agg.UnitsSkip != 1 {
		t.Errorf("attention = %d/%d/%d", agg.UnitsEyes, agg.UnitsSkim, agg.UnitsSkip)
	}
	if agg.RiskMax != 5 || !near(agg.RiskAny, 1-0.2*0.2) || agg.TestGap != 2 || agg.RiskyUnits != 3 {
		t.Errorf("risk max %v any %v gap %d risky %d", agg.RiskMax, agg.RiskAny, agg.TestGap, agg.RiskyUnits)
	}
	assertEqual(t, "risk tags", agg.RiskTags, map[string]int{"data_write": 1, "auth": 1, "error_path": 1, "pii": 1})
	assertEqual(t, "undocumented", agg.Undocumented, []string{"u3"})
	if agg.Unsure != 2 {
		t.Errorf("unsure = %d, want 2", agg.Unsure)
	}
	assertEqual(t, "unsure units", agg.UnsureUnits, []string{"u3", "u6"})
	if agg.Shape != "mixed" || !near(agg.ShapeP, 0.55) {
		t.Errorf("shape = %q %v", agg.Shape, agg.ShapeP)
	}
	if agg.ItemID != "pr:101" || agg.LabelsMissing {
		t.Errorf("id/labels missing = %q %v", agg.ItemID, agg.LabelsMissing)
	}
	if c, _ := contrib(agg.UrgencyTerms, "w_minutes"); !near(c, -0.02*6.8) {
		t.Errorf("urgency minutes term = %v, want label-based minutes", c)
	}
}

func TestProfileHotSpots(t *testing.T) {
	agg := Item(loadFixture(t, "profile.json"))
	want := []model.HotSpot{
		{UnitID: "u1", Path: "web/profile/cache.ts", Line: 10, Score: 4.3 * 4.6 * 0.8 * 0.6, Reason: "data-write, wrong data, untested"},
		{UnitID: "u3", Path: "server/auth/session.go", Line: 41, Score: 4.8 * 2.0 * 0.6 * 0.5, Reason: "auth, crash, untested"},
		{UnitID: "u4", Path: "server/api/user.go", Line: 7, Score: 3.1 * 1.4 * 0.4 * 0.6, Reason: "risk 3 of 5"},
	}
	if len(agg.HotSpots) != len(want) {
		t.Fatalf("hot spots = %+v", agg.HotSpots)
	}
	for i, w := range want {
		g := agg.HotSpots[i]
		if g.UnitID != w.UnitID || g.Path != w.Path || g.Line != w.Line || g.Reason != w.Reason || !near(g.Score, w.Score) {
			t.Errorf("hot spot %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestProfileHeadlines(t *testing.T) {
	agg := Item(loadFixture(t, "profile.json"))
	want := []model.Headline{
		{UnitID: "u4", Path: "server/api/user.go", Line: 7, Kind: "breaking", Text: "func GetUser(id string, opts Opts) (*User, error)", P: 0.9, Confidence: 0.9},
		{UnitID: "u3", Path: "server/auth/session.go", Line: 41, Kind: "breaking", Text: "Logout", P: 0.7, Confidence: 0.7},
		{UnitID: "u3", Path: "server/auth/session.go", Line: 41, Kind: "signature", Text: "Logout", P: 0.8, Confidence: 0.8},
		{UnitID: "u4", Path: "server/api/user.go", Line: 7, Kind: "api shape", Text: "func GetUser(id string, opts Opts) (*User, error)", P: 0.75, Confidence: 0.75},
		{UnitID: "u2", Path: "web/profile/cache.test.ts", Line: 5, Kind: "test weakened", Text: "web/profile/cache.test.ts", P: 0.8, Confidence: 0.8},
		{UnitID: "u3", Path: "server/auth/session.go", Line: 43, Kind: "pii or secret", Text: "Logout", P: 0.9, Confidence: 0.9},
	}
	if len(agg.Headlines) != len(want) {
		t.Fatalf("headlines = %+v", agg.Headlines)
	}
	for i, w := range want {
		g := agg.Headlines[i]
		if g.UnitID != w.UnitID || g.Path != w.Path || g.Line != w.Line || g.Kind != w.Kind || g.Text != w.Text ||
			!near(g.P, w.P) || !near(g.Confidence, w.Confidence) {
			t.Errorf("headline %d = %+v, want %+v", i, g, w)
		}
	}
}

func TestProfileBullets(t *testing.T) {
	agg := Item(loadFixture(t, "profile.json"))
	want := []model.BulletAgg{
		{Index: 1, Text: "Cache user profiles for 5 minutes", Implemented: true, UnitIDs: []string{"u1", "u4"}, P: 0.9},
		{Index: 2, Text: "Invalidate the profile cache on logout"},
		{Index: 3, Text: "Document the cache in the README", Implemented: true, UnitIDs: []string{"u5"}, P: 0.6},
	}
	assertEqual(t, "bullets", agg.Bullets, want)
	if !near(agg.Fidelity, 2.0/3) {
		t.Errorf("fidelity = %v, want 2/3", agg.Fidelity)
	}
}

func TestProfileFilesAndSlice(t *testing.T) {
	agg := Item(loadFixture(t, "profile.json"))
	assertEqual(t, "files", agg.Files, []model.FileAgg{
		{Path: "web/profile/cache.ts", Owned: true, Attention: 1, Eyes: 1, Skim: 1, UnitIDs: []string{"u6", "u1"}},
		{Path: "web/profile/cache.test.ts", Owned: true, Skim: 1, UnitIDs: []string{"u2"}},
		{Path: "web/profile/index.ts", Owned: true, UnitIDs: []string{}},
		{Path: "server/api/user.go", Attention: 1, Eyes: 1, UnitIDs: []string{"u4"}},
		{Path: "server/auth/session.go", Attention: 1, Eyes: 1, UnitIDs: []string{"u3"}},
		{Path: "README.md", Skip: 1, UnitIDs: []string{"u5"}},
	})
	assertEqual(t, "slice", agg.Slice, []model.SliceRow{
		{Module: "web/profile/*.test.ts", Owned: true, Units: 1},
		{Module: "web/**", Owned: true, Units: 2, Eyes: 1},
		{Module: "server/*", Units: 2, Eyes: 2},
		{Module: rootModule, Units: 1},
	})
}

func TestProfileGroups(t *testing.T) {
	agg := Item(loadFixture(t, "profile.json"))
	if len(agg.Groups) != 1 {
		t.Fatalf("groups = %+v", agg.Groups)
	}
	g := agg.Groups[0]
	if g.GroupID != "g1" || g.EffectiveLines != 8 || !near(g.Minutes, 4) || g.RiskMax != 4 || g.TestGap != 1 || g.Unsure != 0 {
		t.Errorf("group = %+v", g)
	}
	assertComposition(t, g.Composition, map[string]float64{"feature": 1})
	assertEqual(t, "group tags", g.RiskTags, map[string]int{"data_write": 1})
	assertEqual(t, "group breaking", g.Breaking, []string{"u4"})
}

func TestNoiseAndMechanical(t *testing.T) {
	agg := Item(loadFixture(t, "noise.json"))
	tests := []struct {
		id        string
		noise     string
		attn      model.Attention
		effective int
		minutes   float64
		unsure    bool
	}{
		{"n1", model.NoiseMoved, model.AttnSkip, 0, 0, false},      // moved beats kind formatting
		{"n2", model.NoiseFormatting, model.AttnSkip, 0, 0, false}, // parse Format beats kind generated
		{"n3", model.NoiseFormatting, model.AttnSkip, 0, 0, false}, // kind formatting
		{"n4", model.NoiseGenerated, model.AttnSkip, 0, 0, false},  // generated beats fmt lines
		{"n5", model.NoiseFmtLines, model.AttnSkip, 0, 0, false},   // every added line fmt
		{"n6", "", model.AttnSkim, 2, 0.2, false},                  // one added line not fmt
		{"n7", model.NoiseGenerated, model.AttnSkim, 0, 0, true},   // unsure noise is skim, not skip
		{"n8", "", model.AttnSkim, 10, 1.0, true},                  // unsure depth skip becomes skim
		{"n9", "", model.AttnEyes, 1, 1.0, false},                  // eyes floor of one minute
		{"m1", "", model.AttnSkim, 0, 0, false},                    // mechanical repeat
		{"m2", "", model.AttnSkim, 2, 0.2, false},                  // mechanical example counted once
		{"m3", "", model.AttnSkim, 0, 0, false},                    // mechanical repeat
		{"n10", model.NoiseFmtLines, model.AttnSkip, 0, 0, false},  // no diff: fmt line count covers adds
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			u := unitByID(t, agg, tc.id)
			if u.NoiseReason != tc.noise || u.Noise != (tc.noise != "") || u.Attention != tc.attn ||
				u.EffectiveLines != tc.effective || !near(u.Minutes, tc.minutes) || u.Unsure != tc.unsure {
				t.Errorf("unit = %+v", u)
			}
		})
	}
	if agg.NoiseLines != 18 || agg.EffectiveLines != 15 || !near(agg.Minutes, 2.4) {
		t.Errorf("noise %d effective %d minutes %v", agg.NoiseLines, agg.EffectiveLines, agg.Minutes)
	}
	if agg.UnitsEyes != 1 || agg.UnitsSkim != 6 || agg.UnitsSkip != 6 {
		t.Errorf("attention = %d/%d/%d", agg.UnitsEyes, agg.UnitsSkim, agg.UnitsSkip)
	}
	assertComposition(t, agg.Composition, map[string]float64{"refactor": 4.0 / 15, "feature": 10.0 / 15, "bugfix": 1.0 / 15})
	assertEqual(t, "unsure units", agg.UnsureUnits, []string{"n7", "n8"})
	if agg.Bullets != nil || agg.Fidelity != 0 {
		t.Errorf("bullets = %+v fidelity %v, want none and 0", agg.Bullets, agg.Fidelity)
	}
	if len(agg.Groups) != 1 || agg.Groups[0].EffectiveLines != 2 || !near(agg.Groups[0].Minutes, 0.2) {
		t.Errorf("mechanical group = %+v", agg.Groups)
	}
	assertEqual(t, "slice", agg.Slice, []model.SliceRow{{Module: "src/*", Units: 13, Eyes: 1}})
	if len(agg.Files) != 9 {
		t.Errorf("files = %d, want 9 (unit paths missing from Files are listed)", len(agg.Files))
	}
}

func TestUnknownAndMissingLabels(t *testing.T) {
	unknown := loadFixture(t, "unknown.json")
	unknown.LabelsMissing = true
	none := loadFixture(t, "unknown.json")
	none.Labels = nil
	tests := []struct {
		name   string
		in     Input
		kind   string
		unsure int
	}{
		{"all unknown", unknown, model.LabelUnknown, 21},
		{"no labels", none, "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agg := Item(tc.in)
			for _, u := range agg.Units {
				if u.Attention != model.AttnEyes || !u.Unsure || u.MinConfidence != 0 || u.Kind != tc.kind || u.Noise ||
					u.Untested || u.Undocumented || u.RiskLevel != 0 || len(u.RiskTags) != 0 {
					t.Errorf("unit = %+v", u)
				}
				// Regression: unknown booleans read as a confident "no".
				if !u.BehaviourUnknown || !u.BreakingUnknown || !u.CoveredUnknown {
					t.Errorf("unit %s: unknown booleans not flagged: %+v", u.UnitID, u)
				}
				for _, m := range u.Lines {
					if m.Mark != model.MarkNone || m.Fmt {
						t.Errorf("line mark = %+v", m)
					}
				}
			}
			if len(agg.Headlines) != 0 || len(agg.HotSpots) != 0 || len(agg.Undocumented) != 0 {
				t.Errorf("headlines %v hot spots %v undocumented %v", agg.Headlines, agg.HotSpots, agg.Undocumented)
			}
			if agg.Headlines == nil || agg.HotSpots == nil || agg.Undocumented == nil || agg.RiskTags == nil || agg.Composition == nil {
				t.Errorf("empty collections must be non-nil")
			}
			if agg.UnitsEyes != 3 || agg.Unsure != tc.unsure || len(agg.UnsureUnits) != 3 {
				t.Errorf("eyes %d unsure %d units %v", agg.UnitsEyes, agg.Unsure, agg.UnsureUnits)
			}
			if !near(agg.Minutes, StandInMinutes(9)) {
				t.Errorf("minutes = %v, want stand-in %v", agg.Minutes, StandInMinutes(9))
			}
			if agg.RiskMax != 0 || agg.RiskAny != 0 || agg.TestGap != 0 || agg.Fidelity != 0 || len(agg.Bullets) != 2 {
				t.Errorf("risk %v/%v gap %d fidelity %v bullets %d", agg.RiskMax, agg.RiskAny, agg.TestGap, agg.Fidelity, len(agg.Bullets))
			}
			assertComposition(t, agg.Composition, map[string]float64{unknownKind: 1})
			if agg.LabelsMissing != tc.in.LabelsMissing {
				t.Errorf("labels missing = %v", agg.LabelsMissing)
			}
			assertEqual(t, "slice", agg.Slice, []model.SliceRow{
				{Module: "lib/**", Owned: true, Units: 2, Eyes: 2},
				{Module: "cmd/*", Units: 1, Eyes: 1},
			})
		})
	}
}

func TestItemDeterministic(t *testing.T) {
	in := loadFixture(t, "profile.json")
	first := Item(in)
	for range 20 {
		if got := Item(in); !reflect.DeepEqual(got, first) {
			t.Fatal("Item is not deterministic")
		}
	}
}
