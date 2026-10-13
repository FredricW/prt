package daemon

import (
	"context"
	"fmt"
	"slices"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
)

// analysis is the part of a Detail that one complete pipeline run
// produces. Detail serves it from the store, or from the item's snapshot
// while a re-run rewrites the store (stale-while-revalidate, M5.2).
type analysis struct {
	// head is the head the analysis describes (the item's AnalysedSHA when
	// it was read).
	head    string
	diff    string
	files   []model.File
	units   []model.ChangeUnit
	links   []model.Link
	groups  []model.Group
	reviews []model.Review // the routing table
	agg     *model.ItemAgg
	// subjects are the label subjects of the item, its files and units
	// (labelSubjects); labels are their stored labels, without the user's
	// overrides (applied when served). Both nil when labels were not read.
	subjects []string
	labels   []model.Label
}

// Detail implements Client: the full Detail (DetailOf with every part).
func (c *InProcess) Detail(ctx context.Context, itemID string) (proto.Detail, error) {
	return c.DetailOf(ctx, proto.DetailParams{ItemID: itemID})
}

// DetailOf implements Client (M5.2). With p.NoLabels it reads no label and
// no override at all. While the item has a snapshot (a re-run of an
// analysed item is in flight, or one was stopped or failed), the analysis
// parts come from it; the item, comments, finding marks, routing rules and
// run log are always read live. Detail.Run describes a run in flight.
func (c *InProcess) DetailOf(ctx context.Context, p proto.DetailParams) (proto.Detail, error) {
	it, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return proto.Detail{}, fmt.Errorf("daemon: detail %s: %w", p.ItemID, err)
	}
	det := proto.Detail{Item: it, LabelsOmitted: p.NoLabels}
	snap := c.snaps.get(it.ID)
	a := snap
	if a == nil {
		if a, err = c.loadAnalysis(ctx, it, !p.NoLabels); err != nil {
			return det, fmt.Errorf("daemon: detail %s: %w", it.ID, err)
		}
	}
	if err := c.serveAnalysis(ctx, &det, a, snap != nil, !p.NoLabels); err != nil {
		return det, fmt.Errorf("daemon: detail %s: %w", it.ID, err)
	}
	if err := c.serveLive(ctx, &det); err != nil {
		return det, fmt.Errorf("daemon: detail %s: %w", it.ID, err)
	}
	if det.Run, err = c.runStatus(ctx, it.ID, snap); err != nil {
		return det, fmt.Errorf("daemon: detail %s: %w", it.ID, err)
	}
	return det, nil
}

// Labels implements Client (M5.2): the labels of p.UnitIDs, or of every
// subject of the analysis Detail serves (its units, files and the item),
// with the user's overrides applied as Detail applies them. Only those
// subjects are read.
func (c *InProcess) Labels(ctx context.Context, p proto.LabelsParams) ([]model.Label, error) {
	if _, err := c.d.Store.Item(ctx, p.ItemID); err != nil {
		return nil, fmt.Errorf("daemon: labels %s: %w", p.ItemID, err)
	}
	subjects, ls, err := c.rawLabels(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("daemon: labels %s: %w", p.ItemID, err)
	}
	out, err := c.withOverrides(ctx, subjects, ls)
	if err != nil {
		return nil, fmt.Errorf("daemon: labels %s: %w", p.ItemID, err)
	}
	return out, nil
}

// rawLabels returns the subjects p asks for and their labels without
// overrides: from the item's snapshot when it has one, else from the store
// (ItemSubjects reads the subjects from document ids alone).
func (c *InProcess) rawLabels(ctx context.Context, p proto.LabelsParams) ([]string, []model.Label, error) {
	if snap := c.snaps.get(p.ItemID); snap != nil {
		if len(p.UnitIDs) == 0 {
			return snap.subjects, slices.Clone(snap.labels), nil
		}
		return p.UnitIDs, labelsOf(snap.labels, p.UnitIDs), nil
	}
	subjects := p.UnitIDs
	if len(subjects) == 0 {
		var err error
		if subjects, err = c.d.Store.ItemSubjects(ctx, p.ItemID); err != nil {
			return nil, nil, err
		}
	}
	ls, err := c.d.Store.Labels(ctx, subjects)
	return subjects, ls, err
}

// labelsOf returns the labels whose subject is one of subjects, in order.
func labelsOf(ls []model.Label, subjects []string) []model.Label {
	want := make(map[string]bool, len(subjects))
	for _, s := range subjects {
		want[s] = true
	}
	var out []model.Label
	for _, l := range ls {
		if want[l.UnitID] {
			out = append(out, l)
		}
	}
	return out
}

// withOverrides applies the stored overrides of subjects to ls: the Code
// page shows labels as the aggregate uses them (Overridden set, Jev's
// answer in Original). Overrides are user data and always read live, also
// over a snapshot. ls must not be shared (ApplyOverrides may return it).
func (c *InProcess) withOverrides(ctx context.Context, subjects []string, ls []model.Label) ([]model.Label, error) {
	ovs, err := c.d.Store.Overrides(ctx, subjects)
	if err != nil {
		return nil, err
	}
	return pipeline.ApplyOverrides(ls, ovs), nil
}

// loadAnalysis reads the item's stored analysis parts, its labels only
// when withLabels.
func (c *InProcess) loadAnalysis(ctx context.Context, it model.Item, withLabels bool) (*analysis, error) {
	s := c.d.Store
	a := &analysis{head: it.AnalysedSHA, diff: c.storedDiff(ctx, it)}
	var err error
	if a.files, err = s.Files(ctx, it.ID); err != nil {
		return nil, err
	}
	if a.units, err = s.Units(ctx, it.ID); err != nil {
		return nil, err
	}
	if withLabels {
		a.subjects = labelSubjects(it.ID, a.files, a.units)
		if a.labels, err = s.Labels(ctx, a.subjects); err != nil {
			return nil, err
		}
	}
	if a.links, err = s.Links(ctx, it.ID); err != nil {
		return nil, err
	}
	if a.groups, err = s.Groups(ctx, it.ID); err != nil {
		return nil, err
	}
	if a.reviews, err = s.ItemReviews(ctx, it.ID); err != nil {
		return nil, err
	}
	if agg, ok, err := s.Agg(ctx, it.ID); err == nil && ok {
		a.agg = &agg
	}
	return a, nil
}

// storedDiff is the diff the stored files and units were parsed from: the
// fetch of UnitsSHA (set by stage 1 with the units), else of the analysed
// head, else of the current head. A Stale item's new head has no analysis
// (or fetch) yet, and during a run the units move to the new head before
// AnalysedSHA does.
func (c *InProcess) storedDiff(ctx context.Context, it model.Item) string {
	for _, sha := range []string{it.UnitsSHA, it.AnalysedSHA, it.HeadSHA} {
		if sha == "" {
			continue
		}
		if f, ok, err := c.d.Store.Fetch(ctx, it.ID, sha); err == nil && ok {
			return f.Diff
		}
	}
	return ""
}

// serveAnalysis copies a into det (slices cloned when a is a shared
// snapshot), labels with the live overrides applied when withLabels, and
// the aggregate with the branch block aged to now.
func (c *InProcess) serveAnalysis(ctx context.Context, det *proto.Detail, a *analysis, shared, withLabels bool) error {
	det.Diff, det.Files, det.Units = a.diff, a.files, a.units
	det.Links, det.Groups, det.Reviews = a.links, a.groups, a.reviews
	if shared {
		det.Files, det.Units = slices.Clone(det.Files), slices.Clone(det.Units)
		det.Links, det.Groups, det.Reviews = slices.Clone(det.Links), slices.Clone(det.Groups), slices.Clone(det.Reviews)
	}
	if withLabels {
		ls := a.labels
		if shared {
			ls = slices.Clone(ls)
		}
		var err error
		if det.Labels, err = c.withOverrides(ctx, a.subjects, ls); err != nil {
			return err
		}
	}
	det.Agg = c.agedAgg(det.Item, a.agg)
	return nil
}

// agedAgg returns a copy of agg whose Branch state box ages like the
// picker line (setUrgency): an idle branch is never re-analysed.
func (c *InProcess) agedAgg(it model.Item, agg *model.ItemAgg) *model.ItemAgg {
	if agg == nil {
		return nil
	}
	out := *agg
	if out.Branch != nil && it.Kind == model.KindBranch && (it.AnalysedSHA == "" || it.AnalysedSHA == it.HeadSHA) {
		b := pipeline.AgeBranchInfo(*out.Branch, idleDays(it, c.now()), c.cfg().Pipeline.AbandonedDays)
		out.Branch = &b
	}
	return &out
}

// serveLive fills the parts of det that are never served from a snapshot:
// comments (sorted), finding marks, the active routing rules and the
// current run's rows (Store.CurrentRunLogs; older runs are pruned, M5.2).
func (c *InProcess) serveLive(ctx context.Context, det *proto.Detail) error {
	s, id := c.d.Store, det.Item.ID
	var err error
	if det.Comments, err = s.Comments(ctx, id); err != nil {
		return err
	}
	sortComments(det.Comments)
	if det.FindingMarks, err = s.FindingMarks(ctx, id); err != nil {
		return err
	}
	info := c.routingInfo()
	det.Routing = &info
	det.RunLogs, err = s.CurrentRunLogs(ctx, id)
	return err
}

// runStatus is Detail.Run: nil unless a run of the item is in flight. With
// a snapshot it says so and carries the live routing rows (the Pipeline
// page follows the run's reviews while the Overview keeps the snapshot).
func (c *InProcess) runStatus(ctx context.Context, itemID string, snap *analysis) (*proto.RunStatus, error) {
	stage, running := c.runningStage(itemID)
	if !running {
		return nil, nil
	}
	rs := &proto.RunStatus{Stage: stage}
	if snap == nil {
		return rs, nil
	}
	rs.Snapshot, rs.SnapshotSHA = true, snap.head
	var err error
	rs.Reviews, err = c.d.Store.ItemReviews(ctx, itemID)
	return rs, err
}

// runningStage reports whether a run of the item is in flight and the
// stage it is in ("" before its first stage row).
func (c *InProcess) runningStage(itemID string) (model.Stage, bool) {
	c.runMu.Lock()
	defer c.runMu.Unlock()
	e := c.runs[itemID]
	if e == nil {
		return "", false
	}
	return e.stage, true
}

// labelSubjects lists every Label.UnitID an item's labels can carry: its
// units, its files (model.FileSubject) and the item itself
// (model.ItemSubject).
func labelSubjects(itemID string, files []model.File, units []model.ChangeUnit) []string {
	ids := make([]string, 0, len(units)+len(files)+1)
	for _, u := range units {
		ids = append(ids, u.ID)
	}
	for _, f := range files {
		ids = append(ids, model.FileSubject(itemID, f.Path))
	}
	return append(ids, model.ItemSubject(itemID))
}
