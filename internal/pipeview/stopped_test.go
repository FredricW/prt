package pipeview

import (
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// stoppedDetail is the full fixture after a user stop during review
// (M5.1): the running stage and review rows end "stopped", the item is
// StateStopped at review.
func stoppedDetail(t *testing.T) Model {
	t.Helper()
	d := load(t, "full")
	for i := range d.RunLogs {
		if d.RunLogs[i].Status == model.StageRunning {
			d.RunLogs[i].Status = model.StageStopped
		}
	}
	for i := range d.Reviews {
		if d.Reviews[i].Status == model.ReviewRunning || d.Reviews[i].Status == model.ReviewQueued {
			d.Reviews[i].Status = model.ReviewStopped
		}
	}
	d.Item.State, d.Item.StateNote = model.StateStopped, "review"
	return newModel(t, d, 200, 80)
}

func TestStoppedRun(t *testing.T) {
	m := stoppedDetail(t)
	v := m.View()
	contains(t, v, "■ stopped at review")
	contains(t, v, "■ review")    // the stage box
	contains(t, v, "■ stopped  ") // a routing row, not "✗ failed"
	contains(t, v, "7→0 · stopped")
	lacks(t, v, "▶ running")
	lacks(t, v, "◌ queued ")
	lacks(t, v, "stopped (review)") // the run state says it once
}

func TestStoppedStageLog(t *testing.T) {
	m := stoppedDetail(t)
	press(t, &m, "K")
	for range 6 {
		press(t, &m, "j")
	}
	press(t, &m, "enter")
	contains(t, m.View(), "• 10-13 14:03:04 stopped")
}

func TestStageStripStopped(t *testing.T) {
	rls := load(t, "full").RunLogs
	for i := range rls {
		if rls[i].Status == model.StageRunning {
			rls[i].Status = model.StageStopped
		}
	}
	if got := StageStrip(rls, 0); !strings.Contains(got, "review ■") {
		t.Errorf("StageStrip = %q, want review ■", got)
	}
}

func TestNotAnalysedRunState(t *testing.T) {
	d := load(t, "nokey")
	d.Item.State = model.StateNotAnalysed
	m := newModel(t, d, 200, 80)
	contains(t, m.View(), "◌ not analysed")
	d.RunLogs = nil
	m = newModel(t, d, 200, 80)
	contains(t, m.View(), "◌ not analysed")
	// A resumed run under way is not "stopped" any more.
	d = load(t, "full")
	d.Item.State, d.Item.StateNote = model.StateAnalysing, ""
	m = newModel(t, d, 200, 80)
	lacks(t, m.View(), "stopped at")
}
