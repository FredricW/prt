package pipeline

import (
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestAgeBranchInfo(t *testing.T) {
	probs := map[string]float64{model.BranchWIP: 0.7, model.BranchAbandoned: 0.2, model.BranchReady: 0.1}
	wip := model.BranchInfo{State: model.BranchWIP, P: 0.7, Probs: probs, Known: true, IdleDays: 3}
	cases := []struct {
		name  string
		in    model.BranchInfo
		idle  float64
		state string
		p     float64
		rule  string
		days  int
	}{
		{"still fresh", wip, 10, model.BranchWIP, 0.7, "", 10},
		{"crosses the threshold", wip, 14.5, model.BranchAbandoned, 0.2, "no commits for 14 days", 14},
		{"ready never ages", model.BranchInfo{State: model.BranchReady, P: 0.8, Known: true, IdleDays: 3}, 40, model.BranchReady, 0.8, "", 40},
		{"unknown ages with P 1", model.BranchInfo{IdleDays: 2}, 30, model.BranchAbandoned, 1, "no commits for 30 days", 30},
		{"already aged: count updated", model.BranchInfo{State: model.BranchAbandoned, P: 0.2, Probs: probs, Known: true, IdleDays: 15, Rule: "no commits for 15 days"},
			21, model.BranchAbandoned, 0.2, "no commits for 21 days", 21},
		{"clock went back", wip, 1, model.BranchWIP, 0.7, "", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AgeBranchInfo(tc.in, tc.idle, 14)
			if got.State != tc.state || got.P != tc.p || got.Rule != tc.rule || got.IdleDays != tc.days || got.Known != tc.in.Known {
				t.Errorf("got %+v", got)
			}
		})
	}
}
