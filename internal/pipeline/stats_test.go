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
