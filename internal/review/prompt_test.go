package review

import (
	"fmt"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestBuildReviewGolden(t *testing.T) {
	for _, tier := range []model.Tier{model.TierFast, model.TierDeep} {
		req := BuildReview(sampleInput(tier))
		golden(t, "review_"+string(tier)+".golden", renderRequest(req))
	}
}

func TestBuildReviewShape(t *testing.T) {
	in := sampleInput(model.TierDeep)
	req := BuildReview(in)
	if req.Task != TaskReview || req.Tier != model.TierDeep || req.GroupID != "g-profile" || req.MaxTokens != ReviewMaxTokensDeep {
		t.Fatalf("request fields: %+v", req)
	}
	if got := BuildReview(sampleInput(model.TierFast)).MaxTokens; got != ReviewMaxTokensFast {
		t.Errorf("fast MaxTokens = %d", got)
	}
	if len(req.Units) != 3 || len(req.Context) != 1 || !strings.HasPrefix(req.Context[0], "### callers: caller web/src/ProfilePage.tsx ProfilePage\n") {
		t.Errorf("units %d, context %q", len(req.Units), req.Context)
	}
	for _, want := range []string{
		"Title: Profile options for getUser",
		"- Invalidate the profile cache on logout",
		"hints: kind=feature risk=4 tags=auth,data_write breaking=0.82 contract=signature covered=0.10 untested=yes",
		"hints: kind=test risk=1 tags=async breaking=0.05 contract=? covered=? untested=no",
		"(deleted, old lines 40-41)",
		"   40       -func LegacyExport() {}",
		"         11 +  if (opts.cache === false) invalidate(id);",
		"   11    12    return fetchJSON",
	} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	// Security before tests, unknown ids ignored, each once.
	sec, tst := strings.Index(req.System, "Security focus"), strings.Index(req.System, "Tests focus")
	if sec < 0 || tst < sec || strings.Contains(req.System, "Performance focus") {
		t.Errorf("specialists in system prompt: sec %d tests %d", sec, tst)
	}
	// Deterministic.
	if again := BuildReview(sampleInput(model.TierDeep)); renderRequest(again) != renderRequest(req) {
		t.Error("BuildReview is not deterministic")
	}
}

func TestBuildReviewElidesLongDiffs(t *testing.T) {
	var diff strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&diff, "+line %d of a very long generated block\n", i)
	}
	in := sampleInput(model.TierFast)
	in.Units = []model.ChangeUnit{{ID: "big", Path: "big.go", Kind: model.UnitAdded, Range: model.Range{New: model.Span{Start: 1, Count: 3000}}, Diff: diff.String()}}
	req := BuildReview(in)
	if !strings.Contains(req.Prompt, "lines elided …") {
		t.Fatal("no elision marker")
	}
	if len(req.Prompt) > MaxReviewDiffBytes+4096 {
		t.Errorf("prompt %d bytes exceeds the cap", len(req.Prompt))
	}
	if !strings.Contains(req.Prompt, "+line 0 of") || !strings.Contains(req.Prompt, "+line 2999 of") {
		t.Error("head or tail missing")
	}
	if BuildReview(in).Prompt != req.Prompt {
		t.Error("elision is not deterministic")
	}
}

func TestFenceAvoidsBackticksInDiff(t *testing.T) {
	if got := fenceFor([]string{"+```go", "+x"}); got != "````" {
		t.Errorf("fence = %q", got)
	}
}

func TestBuildTLDRGolden(t *testing.T) {
	golden(t, "tldr.golden", renderRequest(BuildTLDR(sampleAgg())))
}

func TestBuildTLDRKeyInputs(t *testing.T) {
	base := BuildTLDR(sampleAgg())
	key := func(r Request) string {
		k, err := RequestKey("anthropic:claude-haiku-5-5", r)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	if base.Task != TaskTLDR || base.Tier != model.TierFast || base.MaxTokens != TLDRMaxTokens {
		t.Fatalf("request: %+v", base)
	}
	ignored := map[string]func(*model.ItemAgg){
		"urgency":     func(a *model.ItemAgg) { a.Urgency = 9; a.UrgencyTerms = nil },
		"why":         func(a *model.ItemAgg) { a.Why = "age" },
		"review":      func(a *model.ItemAgg) { a.Review = &model.ReviewStats{ElapsedMs: 99999, Cost: 1} },
		"calibration": func(a *model.ItemAgg) { a.Calibration = nil },
		"tldr":        func(a *model.ItemAgg) { a.TLDR, a.TLDRKey = "new", "k2" },
		"units":       func(a *model.ItemAgg) { a.Units = []model.UnitAgg{{UnitID: "x"}} },
	}
	for name, mut := range ignored {
		a := sampleAgg()
		mut(&a)
		if key(BuildTLDR(a)) != key(base) {
			t.Errorf("%s changed the TL;DR key", name)
		}
	}
	read := map[string]func(*model.ItemAgg){
		"headlines": func(a *model.ItemAgg) {
			a.Headlines = append(a.Headlines, model.Headline{Kind: "env/flag", Path: "x", Text: "new flag"})
		},
		"hot spots":   func(a *model.ItemAgg) { a.HotSpots = nil },
		"risk":        func(a *model.ItemAgg) { a.RiskMax = 2 },
		"test gap":    func(a *model.ItemAgg) { a.TestGap = 2 },
		"composition": func(a *model.ItemAgg) { a.Composition["bugfix"] = 0.2 },
		"labels":      func(a *model.ItemAgg) { a.LabelsMissing = true },
		"bullets":     func(a *model.ItemAgg) { a.Bullets[1].Implemented = true },
	}
	for name, mut := range read {
		a := sampleAgg()
		mut(&a)
		if key(BuildTLDR(a)) == key(base) {
			t.Errorf("%s did not change the TL;DR key", name)
		}
	}
}

func TestBuildTLDRCapsHeadlines(t *testing.T) {
	a := sampleAgg()
	for i := range 12 {
		a.Headlines = append(a.Headlines, model.Headline{Kind: "breaking", Path: fmt.Sprintf("f%d.go", i), Line: i, Text: "x"})
	}
	p := BuildTLDR(a).Prompt
	if !strings.Contains(p, "and 5 more") || strings.Contains(p, "f8.go") {
		t.Errorf("headlines not capped:\n%s", p)
	}
}

func TestBuildGroupTitleGolden(t *testing.T) {
	g := model.Group{ID: "g-misc", Name: "client.ts getUser", NameSource: model.NamePlaceholder}
	req := BuildGroupTitle(g, sampleUnits())
	if req.Task != TaskGroupTitle || req.Tier != model.TierFast || req.MaxTokens != TitleMaxTokens || req.GroupID != "g-misc" {
		t.Fatalf("request: %+v", req)
	}
	golden(t, "title.golden", renderRequest(req))
}

func TestBuildGroupTitleCaps(t *testing.T) {
	var units []model.ChangeUnit
	var diff strings.Builder
	for i := range 40 {
		fmt.Fprintf(&diff, "+%s\n", strings.Repeat("x", 300+i))
	}
	for i := range 11 {
		units = append(units, model.ChangeUnit{ID: fmt.Sprint(i), Path: fmt.Sprintf("p%d.go", i), Diff: diff.String()})
	}
	p := BuildGroupTitle(model.Group{ID: "g"}, units).Prompt
	if !strings.Contains(p, "(and 3 more units)") || strings.Contains(p, "p8.go") {
		t.Error("units not capped")
	}
	if len(p) > titleUnits*(titleLines+2)*(titleLineBytes+8)+1024 {
		t.Errorf("prompt %d bytes", len(p))
	}
}
