package pipeview

import (
	"sort"
	"time"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// stripStages are the stage strip's eight boxes (fetch is the prefix line).
var stripStages = []model.Stage{
	model.StageParse, model.StageLink, model.StageEmbed, model.StageJev,
	model.StageCluster, model.StageRoute, model.StageReview, model.StageAggregate,
}

// stageName is a stage's label in the strip ("agg" for aggregate).
func stageName(s model.Stage) string {
	if s == model.StageAggregate {
		return "agg"
	}
	return string(s)
}

// glyph is a stage status's mark: done ✓, running ⟳, queued ·, failed ✗,
// skipped-cache ≡, skipped –, stopped ■ (M5.1: the user stopped the run).
func glyph(s model.StageStatus) string {
	switch s {
	case model.StageStopped:
		return "■"
	case model.StageDone:
		return "✓"
	case model.StageRunning:
		return "⟳"
	case model.StageFailed:
		return "✗"
	case model.StageCached:
		return "≡"
	case model.StageSkipped:
		return "–"
	}
	return "·"
}

// runView is the current run of an item: the rows since the last fetch
// row's StartedAt (every row when there is no fetch row), latest per stage.
type runView struct {
	start  time.Time
	fetch  *model.RunLog
	latest map[model.Stage]model.RunLog
	all    []model.RunLog // every row, oldest first
}

// currentRun splits rls into the current run (see runView).
func currentRun(rls []model.RunLog) runView {
	all := append([]model.RunLog(nil), rls...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].StartedAt.Before(all[j].StartedAt) })
	v := runView{latest: map[model.Stage]model.RunLog{}, all: all}
	from := 0
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Stage == model.StageFetch {
			from, v.start = i, all[i].StartedAt
			f := all[i]
			v.fetch = &f
			break
		}
	}
	for _, rl := range all[from:] {
		if rl.Stage != model.StageFetch {
			v.latest[rl.Stage] = rl
		}
	}
	return v
}

// inRun reports whether rl belongs to the current run.
func (v runView) inRun(rl model.RunLog) bool {
	return v.fetch == nil || !rl.StartedAt.Before(v.start)
}

// stageRows returns every row of stage s, newest first.
func (v runView) stageRows(s model.Stage) []model.RunLog {
	var out []model.RunLog
	for i := len(v.all) - 1; i >= 0; i-- {
		if v.all[i].Stage == s {
			out = append(out, v.all[i])
		}
	}
	return out
}

// row is one routing-table row: the review row and its display name.
type row struct {
	rev         model.Review
	name        string
	ai          bool // NameSource ai: model-written title
	placeholder bool
}

// liveRows reports whether the routing table shows the run in flight's
// rows (M5.2, stale-while-revalidate): the daemon serves the last complete
// analysis (Run.Snapshot) and the live routing rows in Run.Reviews.
func liveRows(d proto.Detail) bool {
	return d.Run != nil && d.Run.Snapshot && d.Run.Reviews != nil
}

// updatingNote is the routing box's dim line while it shows live rows:
// "updating · <stage>" ("updating" before the run's first stage).
func updatingNote(d proto.Detail) string {
	if !liveRows(d) {
		return ""
	}
	if d.Run.Stage == "" {
		return "updating"
	}
	return "updating · " + string(d.Run.Stage)
}

// buildRows orders the routing rows (d.Reviews, or the live Run.Reviews
// while a run is in flight over a snapshot) by d.Groups (rows of unknown
// groups follow in their own order, named by their GroupName) and
// resolves names.
func buildRows(d proto.Detail) []row {
	groups := make(map[string]model.Group, len(d.Groups))
	pos := make(map[string]int, len(d.Groups))
	for i, g := range d.Groups {
		groups[g.ID], pos[g.ID] = g, i
	}
	src := d.Reviews
	if liveRows(d) {
		src = d.Run.Reviews
	}
	revs := append([]model.Review(nil), src...)
	sort.SliceStable(revs, func(i, j int) bool {
		pi, iok := pos[revs[i].GroupID]
		pj, jok := pos[revs[j].GroupID]
		switch {
		case iok && jok:
			return pi < pj
		case iok != jok:
			return iok
		}
		return false
	})
	out := make([]row, len(revs))
	for i, r := range revs {
		g, ok := groups[r.GroupID]
		rw := row{rev: r, name: r.GroupName}
		if ok {
			if rw.name == "" {
				rw.name = g.Name
			}
			rw.ai = g.NameSource == model.NameAI
			rw.placeholder = g.NameSource == model.NamePlaceholder
		}
		if rw.name == "" {
			rw.name = shortID(r.GroupID)
		}
		out[i] = rw
	}
	return out
}

// shortID is the first 8 characters of an id.
func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// capNow is the item's effective review cost cap: the totals' CapUSD, else
// the item's own cap, else the rules' cost_cap_usd (0 when unknown).
func capNow(d proto.Detail) float64 {
	switch {
	case d.Agg != nil && d.Agg.Review != nil && d.Agg.Review.CapUSD > 0:
		return d.Agg.Review.CapUSD
	case d.Item.CapUSD > 0:
		return d.Item.CapUSD
	case d.Routing != nil:
		return d.Routing.CostCapUSD
	}
	return 0
}
