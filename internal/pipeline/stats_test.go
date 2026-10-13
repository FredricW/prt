package pipeline

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

func TestQuestionRates(t *testing.T) {
	got := questionRates(
		map[string]int{model.QRisk: 16, model.QKind: 15, model.QBreaking: 1, "gone": 2, "zero": 0},
		map[string]int{model.QRisk: 100, model.QKind: 100, model.QBreaking: 50},
		overrideFlagAbove,
	)
	want := []model.QuestionRate{
		{Question: model.QRisk, Overrides: 16, Labels: 100, Rate: 0.16, Flagged: true},
		{Question: model.QKind, Overrides: 15, Labels: 100, Rate: 0.15, Flagged: false},
		{Question: model.QBreaking, Overrides: 1, Labels: 50, Rate: 0.02},
		{Question: "gone", Overrides: 2},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rates\n got %+v\nwant %+v", got, want)
	}
}

func TestCalibrationStatsQuestionsAndLastGroup(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil)
	ctx := context.Background()
	var ls []model.Label
	for i := range 100 {
		ls = append(ls, model.Label{UnitID: fmt.Sprintf("u%03d", i), Question: model.QRisk, Value: "2"})
	}
	if err := e.st.PutLabels(ctx, ls); err != nil {
		t.Fatal(err)
	}
	for i := range 17 {
		at := labelNow.Add(-time.Duration(i) * time.Hour)
		if i == 16 {
			at = labelNow.AddDate(0, 0, -31) // outside the window
		}
		if err := e.st.PutOverride(ctx, model.Override{UnitID: fmt.Sprintf("u%03d", i), Question: model.QRisk, Value: "4", At: at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.PutItemReviews(ctx, e.item.ID, []model.Review{{GroupID: "g1", GroupName: "Validate input"}}); err != nil {
		t.Fatal(err)
	}
	for i, g := range []string{"g0", "g1"} {
		s := model.CalibrationSample{ItemID: e.item.ID, GroupID: g, At: labelNow.Add(time.Duration(i-2) * time.Hour), Agree: i == 1}
		if err := e.st.AppendCalibration(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	cs := p.calibrationStats(ctx)
	if cs.FlagAbove != 0.15 || cs.Overrides[model.QRisk] != 16 {
		t.Errorf("stats %+v", cs)
	}
	want := []model.QuestionRate{{Question: model.QRisk, Overrides: 16, Labels: 100, Rate: 0.16, Flagged: true}}
	if !reflect.DeepEqual(cs.Questions, want) {
		t.Errorf("questions %+v", cs.Questions)
	}
	if cs.Samples != 2 || cs.LastGroup != "Validate input" || cs.LastAgree == nil || !*cs.LastAgree {
		t.Errorf("last sample: %+v", cs)
	}
}

// Pruning the labels of a removed item keeps its overrides (user data), but
// the per-question rate counts only overrides whose label is still stored,
// so it does not climb with every merged PR.
func TestCalibrationRatesIgnorePrunedLabels(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil)
	ctx := context.Background()
	var ls []model.Label
	for i := range 40 {
		ls = append(ls, model.Label{UnitID: fmt.Sprintf("u%02d", i), Question: model.QBreaking, P: 0.6})
	}
	var gone []string
	for i := range 5 {
		gone = append(gone, fmt.Sprintf("merged%d", i))
		ls = append(ls, model.Label{UnitID: gone[i], Question: model.QBreaking, P: 0.7})
	}
	if err := e.st.PutLabels(ctx, ls); err != nil {
		t.Fatal(err)
	}
	for _, id := range append([]string{"u00", "u01", "u02", "u03", "u04"}, gone...) {
		if err := e.st.PutOverride(ctx, model.Override{UnitID: id, Question: model.QBreaking, Value: "no", At: labelNow}); err != nil {
			t.Fatal(err)
		}
	}
	before := p.calibrationStats(ctx).Questions
	// The merged item is removed: its labels are pruned, its overrides kept.
	if n, err := e.st.PruneLabels(ctx, gone); err != nil || n != len(gone) {
		t.Fatalf("pruned %d (%v)", n, err)
	}
	cs := p.calibrationStats(ctx)
	if cs.Overrides[model.QBreaking] != 10 {
		t.Errorf("overrides in the window: %d, want 10", cs.Overrides[model.QBreaking])
	}
	want := []model.QuestionRate{{Question: model.QBreaking, Overrides: 5, Labels: 40, Rate: 0.125}}
	if !reflect.DeepEqual(cs.Questions, want) {
		t.Errorf("after pruning: %+v, want %+v (before: %+v)", cs.Questions, want, before)
	}
}
