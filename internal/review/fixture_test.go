package review

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/")

// testKey is a dummy API key; no test may find it in an error, a
// recording or any output.
const testKey = "sk-ant-test-DO-NOT-LEAK-0123456789"

// sampleUnits are three units of a small synthetic group: a modified
// function, a pure deletion and an added test.
func sampleUnits() []model.ChangeUnit {
	return []model.ChangeUnit{
		{
			ID: "u-client", Path: "web/src/api/client.ts", Kind: model.UnitModified, Order: 1,
			Range:  model.Range{Old: model.Span{Start: 10, Count: 3}, New: model.Span{Start: 10, Count: 4}},
			Symbol: "getUser", Signature: "export async function getUser(id: string, opts: GetUserOptions)",
			Diff: "@@ -10,3 +10,4 @@\n-export async function getUser(id: string) {\n+export async function getUser(id: string, opts: GetUserOptions) {\n+  if (opts.cache === false) invalidate(id);\n   return fetchJSON(`/users/${id}`);\n }\n",
			Adds: 2, Dels: 1,
		},
		{
			ID: "u-legacy", Path: "server/internal/billing/legacy.go", Kind: model.UnitDeleted, Order: 2,
			Range:  model.Range{Old: model.Span{Start: 40, Count: 2}, New: model.Span{Start: 39, Count: 0}},
			Symbol: "LegacyExport", Diff: "-func LegacyExport() {}\n-\n", Dels: 2,
		},
		{
			ID: "u-test", Path: "web/src/api/client.test.ts", Kind: model.UnitAdded, Order: 3,
			Range: model.Range{New: model.Span{Start: 1, Count: 3}},
			Diff:  "+it(\"passes opts\", async () => {\n+  expect(await getUser(\"1\", {})).toBeDefined();\n+});\n", Adds: 3,
		},
	}
}

// sampleInput is the synthetic GroupInput the golden review prompt is
// built from: an agg for the first unit, raw labels for the test unit and
// nothing for the deletion.
func sampleInput(tier model.Tier) GroupInput {
	return GroupInput{
		Item: model.Item{
			Title: "Profile options for getUser",
			Body:  "Adds request options.\n\n- `getUser` takes a required `GetUserOptions` argument\n- Invalidate the profile cache on logout\n",
		},
		Group: model.Group{ID: "g-profile", Name: "`getUser` takes a required `GetUserOptions` argument", DescBullet: "`getUser` takes a required `GetUserOptions` argument", NameSource: model.NameBullet},
		Units: sampleUnits(),
		Aggs: []model.UnitAgg{{
			UnitID: "u-client", Kind: "feature", RiskLevel: 4, RiskTags: []string{"auth", "data_write"},
			BreakingP: 0.82, Breaking: true, Contract: "signature", CoveredP: 0.1, Untested: true,
		}},
		Labels: []model.Label{
			{UnitID: "u-test", Question: model.QKind, Value: "test", P: 0.9},
			{UnitID: "u-test", Question: model.QRisk, Value: "1", P: 0.8},
			{UnitID: "u-test", Question: model.QBreaking, Value: "no", P: 0.05},
			{UnitID: "u-test", Question: model.QCovered, Value: "unknown"},
			{UnitID: "u-test", Question: "risk_tags.async", Value: "yes", P: 0.7},
		},
		Context: []model.ContextBlock{{Kind: model.CtxCallers, Title: "caller web/src/ProfilePage.tsx ProfilePage", Body: "const u = await getUser(id);\n"}},
		Prompts: []string{model.PromptTests, "bogus", model.PromptSecurity},
		Tier:    tier,
	}
}

// sampleAgg is a synthetic aggregate record with every field set,
// including the ones BuildTLDR must ignore.
func sampleAgg() model.ItemAgg {
	return model.ItemAgg{
		ItemID:      "pr:101",
		Composition: map[string]float64{"feature": 0.62, "refactor": 0.3, "test": 0.08, "config": 0.001},
		NoiseLines:  14, Added: 120, Removed: 40, EffectiveLines: 96, Minutes: 7.6,
		UnitsTotal: 9, UnitsEyes: 3, UnitsSkim: 4, UnitsSkip: 2,
		Headlines: []model.Headline{
			{UnitID: "u-client", Path: "web/src/api/client.ts", Line: 10, Kind: "breaking", Text: "getUser(id, opts) — breaks existing callers", P: 0.86, Confidence: 0.86},
		},
		HotSpots: []model.HotSpot{{UnitID: "u-client", Path: "web/src/api/client.ts", Line: 11, Score: 9.1, Reason: "cache invalidation skips auth check"}},
		RiskMax:  4, RiskAny: 0.7, RiskTags: map[string]int{"data_write": 1, "auth": 2},
		TestGap: 1, RiskyUnits: 2,
		Bullets:  []model.BulletAgg{{Index: 1, Text: "`getUser` takes options", Implemented: true}, {Index: 2, Text: "Invalidate cache on logout"}},
		Fidelity: 0.5, Undocumented: []string{"u-legacy"}, Shape: "mixed", ShapeP: 0.7,
		Urgency: 3.2, Why: "asked you · breaking · age", UrgencyTerms: []model.UrgencyTerm{{Name: "w_age", Value: 2}},
		TLDR: "old", TLDRKey: "k",
		Review:      &model.ReviewStats{Groups: 3, ElapsedMs: 1200, Cost: 0.02},
		Calibration: &model.CalibrationStats{Samples: 1},
	}
}

// golden compares got with testdata/<name>, rewriting it with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs (run go test -update to accept):\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

// renderRequest is the golden-file view of a request.
func renderRequest(r Request) string {
	var b strings.Builder
	b.WriteString("TASK: " + string(r.Task) + "\nTIER: " + string(r.Tier) + "\nGROUP: " + r.GroupID + "\n")
	b.WriteString("MAX_TOKENS: " + strconv.Itoa(r.MaxTokens) + "\n\n=== SYSTEM ===\n" + r.System + "\n\n=== USER (as sent) ===\n" + UserContent(r))
	return b.String()
}
