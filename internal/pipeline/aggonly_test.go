package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
)

// markAnalysed sets the stored item's AnalysedSHA as the daemon does after
// a run.
func markAnalysed(t *testing.T, e *labelEnv) {
	t.Helper()
	it := e.storedItem(t)
	it.AnalysedSHA = it.HeadSHA
	if err := e.st.UpsertItem(context.Background(), it); err != nil {
		t.Fatal(err)
	}
	e.item = it
}

// groupOf returns the id of the group holding the unit at path, and the
// unit.
func groupOf(t *testing.T, e *labelEnv, path string) (string, model.ChangeUnit) {
	t.Helper()
	var u model.ChangeUnit
	for _, x := range e.units(t) {
		if x.Path == path {
			u = x
		}
	}
	groups, err := e.st.Groups(context.Background(), e.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		for _, id := range g.UnitIDs {
			if id == u.ID {
				return g.ID, u
			}
		}
	}
	t.Fatalf("no group holds %s", path)
	return "", u
}

func rowOf(t *testing.T, e *labelEnv, groupID string) model.Review {
	t.Helper()
	for _, r := range itemRows(t, e) {
		if r.GroupID == groupID {
			return r
		}
	}
	t.Fatalf("no routing row for %s", groupID)
	return model.Review{}
}

func allReviewCalls(p *Pipeline) int {
	return reviewCalls(p.d.Reviewers.Fast) + reviewCalls(p.d.Reviewers.Deep)
}

func TestAggregateOnlyMarksStaleLabels(t *testing.T) {
	e, p, f := m3Env(t, review.FakeRespond(0.9))
	ctx := context.Background()
	e.run(t, p)
	markAnalysed(t, e)
	jc := p.d.Jev.(*jev.Fake)
	jevBefore, pairsBefore, namesBefore, splitBefore := len(jc.Calls), len(f.pairCalls), len(f.nameCalls), len(f.splitCalls)
	reviewsBefore := allReviewCalls(p)

	stale, u := groupOf(t, e, pClient)
	other, _ := groupOf(t, e, pUsers)
	if rowOf(t, e, stale).Status != model.ReviewDone || rowOf(t, e, other).Status != model.ReviewDone {
		t.Fatalf("rows not done before the override: %+v", itemRows(t, e))
	}
	if err := e.st.PutOverride(ctx, model.Override{UnitID: u.ID, Question: model.QRisk, Value: "4", At: labelNow.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	e.events = nil
	if err := p.RunWith(ctx, e.item, RunOptions{AggregateOnly: true}); err != nil {
		t.Fatal(err)
	}
	for _, rl := range e.events {
		if rl.Stage != model.StageAggregate {
			t.Errorf("aggregate-only run emitted a %s row", rl.Stage)
		}
	}
	if rl := e.stage(t, model.StageAggregate); rl.Status == model.StageFailed || !strings.Contains(rl.Note, "aggregate only: 1 review(s) stale-labels") {
		t.Errorf("aggregate row %+v", rl)
	}
	if len(jc.Calls) != jevBefore || len(f.pairCalls) != pairsBefore || len(f.nameCalls) != namesBefore || len(f.splitCalls) != splitBefore {
		t.Errorf("aggregate-only run asked Jev")
	}
	if n := allReviewCalls(p); n != reviewsBefore {
		t.Errorf("aggregate-only run made %d review calls", n-reviewsBefore)
	}
	row := rowOf(t, e, stale)
	want := "label overridden: risk 1→4 (" + pClient + ":1)"
	if row.Status != model.ReviewStaleLabels || row.StaleNote != want || row.Verdict == "" || len(row.Findings) == 0 {
		t.Errorf("stale row %+v, want note %q", row, want)
	}
	if r := rowOf(t, e, other); r.Status != model.ReviewDone || r.StaleNote != "" {
		t.Errorf("untouched row %+v", r)
	}
	agg := e.agg(t)
	if agg.Review == nil || agg.Review.Status[model.ReviewStaleLabels] != 1 || agg.Review.Groups == 0 {
		t.Errorf("review stats %+v", agg.Review)
	}
	if ua := unitAgg(agg, u.ID); ua.Risk != 4 {
		t.Errorf("override not aggregated: risk %v", ua.Risk)
	}
	calls, err := p.CallLog(ctx, e.item.ID, stale)
	if err != nil || len(calls) == 0 {
		t.Errorf("call log of the stale row lost: %v %v", calls, err)
	}

	// A later normal run serves the cached review and keeps stale-labels.
	e.run(t, p)
	if n := allReviewCalls(p); n != reviewsBefore {
		t.Errorf("normal run made %d review calls", n-reviewsBefore)
	}
	if r := rowOf(t, e, stale); r.Status != model.ReviewStaleLabels || r.StaleNote != want {
		t.Errorf("normal run cleared stale-labels: %+v", r)
	}
	if r := rowOf(t, e, other); r.Status != model.ReviewDone {
		t.Errorf("normal run: untouched row %+v", r)
	}

	// A forced deep re-review (D/R) clears it.
	if err := p.SetGroupOverride(ctx, e.item.ID, stale, model.TierDeep); err != nil {
		t.Fatal(err)
	}
	e.run(t, p)
	if r := rowOf(t, e, stale); r.Status != model.ReviewDone || r.StaleNote != "" || r.Tier != model.TierDeep {
		t.Errorf("forced deep: %+v", r)
	}
}

func unitAgg(agg model.ItemAgg, id string) model.UnitAgg {
	for _, u := range agg.Units {
		if u.UnitID == id {
			return u
		}
	}
	return model.UnitAgg{}
}

// Overrides older than the review leave it done; a row without a time
// counts every override.
func TestMarkStaleTimes(t *testing.T) {
	t0 := labelNow
	groups := []model.Group{{ID: "g1", UnitIDs: []string{"u1", "u2"}}, {ID: "g2", UnitIDs: []string{"u3"}}}
	units := []model.ChangeUnit{
		{ID: "u1", Path: "a.go", Range: model.Range{New: model.Span{Start: 10, Count: 2}}},
		{ID: "u2", Path: "b.go", Range: model.Range{Old: model.Span{Start: 4, Count: 1}}},
		{ID: "u3", Path: "c.go", Range: model.Range{New: model.Span{Start: 1, Count: 1}}},
	}
	ovs := []model.Override{
		{UnitID: "u2", Question: model.QBreaking, Value: model.LabelNo, At: t0.Add(2 * time.Hour)},
		{UnitID: "u1", Question: model.QRisk, Value: "2", At: t0.Add(time.Hour)},
		{UnitID: "u3", Question: model.QRisk, Value: "5", At: t0.Add(-time.Hour)},
	}
	labels := []model.Label{
		{UnitID: "u1", Question: model.QRisk, Value: "4"},
		{UnitID: "u1", Question: model.QRisk, Line: 2, Value: "9"}, // line labels are not the old value
	}
	rows := []model.Review{
		{GroupID: "g1", Tier: model.TierFast, Status: model.ReviewDone, StartedAt: t0},
		{GroupID: "g2", Tier: model.TierFast, Status: model.ReviewDone, StartedAt: t0},
	}
	got := markStale(rows, staleIn{groups: groups, units: units, overrides: ovs, labels: labels})
	want := "label overridden: risk 4→2 (a.go:10); label overridden: breaking unknown→no (b.go:4)"
	if got[0].Status != model.ReviewStaleLabels || got[0].StaleNote != want {
		t.Errorf("g1 = %+v\nwant %q", got[0], want)
	}
	if got[1].Status != model.ReviewDone {
		t.Errorf("g2 (override older than the review) = %+v", got[1])
	}
	if rows[0].Status != model.ReviewDone {
		t.Error("markStale changed its input")
	}
	rows[1].StartedAt = time.Time{}
	if got := markStale(rows, staleIn{groups: groups, units: units, overrides: ovs}); got[1].Status != model.ReviewStaleLabels {
		t.Errorf("row without a time = %+v", got[1])
	}
	queued := []model.Review{{GroupID: "g1", Tier: model.TierFast, Status: model.ReviewQueued}, {GroupID: "g1", Tier: model.TierNone, Status: model.ReviewSkipped}}
	for _, r := range markStale(queued, staleIn{groups: groups, units: units, overrides: ovs}) {
		if r.Status == model.ReviewStaleLabels {
			t.Errorf("unfinished row marked stale: %+v", r)
		}
	}
}

// With nothing stored for the analysed head an aggregate-only run is a
// normal run.
func TestAggregateOnlyFallsBack(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil)
	e.events = nil
	if err := p.RunWith(context.Background(), e.item, RunOptions{AggregateOnly: true}); err != nil {
		t.Fatal(err)
	}
	if rl := e.stage(t, model.StageFetch); rl.Status == model.StageFailed {
		t.Errorf("fetch row %+v", rl)
	}
	e.agg(t)
}
