package pipeview

import (
	"testing"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// While a run is in flight over a snapshot (M5.2), the routing table shows
// the run's live rows with a dim "updating · <stage>" line; a row of a
// group the snapshot does not know falls back to its GroupName.
func TestLiveRowsDuringSnapshot(t *testing.T) {
	d := load(t, "full")
	d.Run = &proto.RunStatus{Stage: model.StageReview, Snapshot: true, SnapshotSHA: "abc1234",
		Reviews: []model.Review{
			{GroupID: "g1aaaaaaaaaa", GroupName: "Cache getUser results", Tier: model.TierFast, Status: model.ReviewRunning},
			{GroupID: "gNEWnewnewnew", GroupName: "Brand new group", Tier: model.TierDeep, Status: model.ReviewQueued},
		}}
	m := newModel(t, d, 160, 50)
	v := m.View()
	contains(t, v, "updating · review")
	contains(t, v, "Brand new group")
	lacks(t, v, "Logger cleanup") // a snapshot row the live table no longer has
	if len(m.rows) != 2 || m.rows[0].rev.Status != model.ReviewRunning {
		t.Fatalf("rows %+v", m.rows)
	}

	// Without the snapshot flag (or without live rows) nothing changes.
	for _, run := range []*proto.RunStatus{
		nil,
		{Stage: model.StageReview, Reviews: d.Run.Reviews},
		{Stage: model.StageJev, Snapshot: true},
	} {
		d2 := load(t, "full")
		d2.Run = run
		m := newModel(t, d2, 160, 50)
		v := m.View()
		lacks(t, v, "updating")
		contains(t, v, "Logger cleanup")
	}
}

// A run that has not reached its first stage says "updating" alone.
func TestLiveRowsNoteWithoutStage(t *testing.T) {
	d := load(t, "full")
	d.Run = &proto.RunStatus{Snapshot: true, Reviews: []model.Review{}}
	if got := updatingNote(d); got != "updating" {
		t.Errorf("note %q", got)
	}
	m := newModel(t, d, 160, 50)
	contains(t, m.View(), "no routing decisions yet")
}
