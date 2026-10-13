package daemon

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// TestRunEmitsPatchesAndLabelFields: a run without Jev sends patch events
// for the stages that stored output, an item event carrying
// LabelsUnavailable once aggregate finished, and Detail serves item-subject
// labels alongside the unit labels.
func TestRunEmitsPatchesAndLabelFields(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	it := branchItem("a", 1)
	put(t, st, it)
	c := newClient(t, st, branchGit("a"), nil, nil)
	if err := c.RunSync(ctx, it.ID); err != nil {
		t.Fatal(err)
	}
	evs := drain(c.Events())
	patched := map[model.Stage]bool{}
	unavailable := false
	for _, e := range evs {
		switch e.Type {
		case proto.EventPatch:
			patched[e.Stage] = true
		case proto.EventItem:
			if e.Item != nil && e.Item.LabelsUnavailable {
				unavailable = true
			}
		}
	}
	for _, s := range []model.Stage{model.StageParse, model.StageJev, model.StageAggregate} {
		if !patched[s] {
			t.Errorf("no patch event for stage %s (events %+v)", s, evs)
		}
	}
	if !unavailable {
		t.Error("no item event with LabelsUnavailable after a run without Jev")
	}

	itemLabel := model.Label{UnitID: model.ItemSubject(it.ID), Question: model.QShape, Value: "mixed", P: 0.7, Confidence: 0.7}
	if err := st.PutLabels(ctx, []model.Label{itemLabel}); err != nil {
		t.Fatal(err)
	}
	det, err := c.Detail(ctx, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !det.Item.LabelsUnavailable || det.Agg == nil || !det.Agg.LabelsMissing {
		t.Errorf("item/agg not marked labels-missing: item %+v agg %+v", det.Item, det.Agg)
	}
	var haveItem, haveUnknownUnit bool
	for _, l := range det.Labels {
		haveItem = haveItem || (l.UnitID == itemLabel.UnitID && l.Question == model.QShape)
		haveUnknownUnit = haveUnknownUnit || (l.UnitID == det.Units[0].ID && model.IsUnknown(l))
	}
	if !haveItem || !haveUnknownUnit {
		t.Errorf("detail labels: item-subject %v, unknown unit label %v (%d labels)", haveItem, haveUnknownUnit, len(det.Labels))
	}
}
