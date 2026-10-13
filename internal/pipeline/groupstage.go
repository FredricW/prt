package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/cluster"
	"github.com/fredricw/prt/internal/judge"
	"github.com/fredricw/prt/internal/model"
)

// groupOut is the outcome of stage 5 as aggregate consumes it.
type groupOut struct {
	groups []model.Group
	// links are the item's kept links (structural + Jev-confirmed +
	// mechanical candidates), never nil.
	links []model.Link
	// noStructural is set when stage 2 failed: aggregate then gets nil
	// links (wiring fidelity unknown) although clustering still used the
	// embed and mechanical links.
	noStructural bool
	stats        *model.GroupingStats
	split        *model.SplitSuggestion
}

// aggLinks are the links aggregate sees: nil when stage 2 failed, so the
// fidelity rule does not count every unit as unwired.
func (g groupOut) aggLinks() []model.Link {
	if g.noStructural {
		return nil
	}
	return g.links
}

// clusterIn is what stage 5 reads from the earlier stages.
type clusterIn struct {
	parsed     parseOut
	structural []model.Link // nil when stage 2 failed
	em         embedOut
	pairs      pairOut
}

// keptLinks are the links clustering uses and the store keeps: structural,
// Jev-confirmed and mechanical candidates. Never nil: when stage 2 failed
// the Jev-confirmed and mechanical links still group units.
func (c clusterIn) keptLinks() []model.Link {
	out := make([]model.Link, 0, len(c.structural)+len(c.pairs.confirmed)+len(c.em.mech))
	out = append(out, c.structural...)
	out = append(out, c.pairs.confirmed...)
	return append(out, c.em.mech...)
}

// clusterStage is stage 5: cluster.Cluster over the kept links with the
// user's group edits, then Jev names for real groups and the split
// suggestion. Jev failures in naming and split are soft (placeholder
// names, no split) and only noted; store errors fail the stage.
func (p *Pipeline) clusterStage(ctx context.Context, it model.Item, in clusterIn, rl *model.RunLog) (groupOut, error) {
	units := in.parsed.units
	links := in.keptLinks()
	edits, err := p.d.Store.GroupEdits(ctx, it.ID)
	if err != nil {
		return groupOut{}, fmt.Errorf("pipeline: load group edits: %w", err)
	}
	ls, err := p.itemLabels(ctx, it, in.parsed.files, units)
	if err != nil {
		return groupOut{}, err
	}
	pc := p.d.Config.Pipeline
	res := p.fn.cluster(cluster.Input{
		ItemID: it.ID, Units: units, Links: links, Labels: ls, Edits: edits,
		Options: cluster.Options{MaxGroupUnits: pc.MaxGroupUnits, MechanicalMin: pc.MechanicalMin},
	})
	groups, nameNote, err := p.nameGroups(ctx, it, units, res.Groups, rl)
	if err != nil {
		return groupOut{}, err
	}
	split, splitNote, err := p.splitSuggestion(ctx, it, units, groups, rl)
	if err != nil {
		return groupOut{}, err
	}
	if err := p.d.Store.PutGroups(ctx, it.ID, groups); err != nil {
		return groupOut{}, fmt.Errorf("pipeline: store groups: %w", err)
	}
	if err := p.d.Store.PutLinks(ctx, it.ID, links); err != nil {
		return groupOut{}, fmt.Errorf("pipeline: store links: %w", err)
	}
	stats := groupingStats(res.Stats, in, units)
	rl.In, rl.Out = len(units), len(groups)
	rl.Note = joinNotes(fmt.Sprintf("%d groups · %d mechanical · %d ungrouped", stats.Groups, stats.Mechanical, stats.Ungrouped), nameNote, splitNote)
	return groupOut{groups: groups, links: links, noStructural: in.structural == nil, stats: &stats, split: split}, nil
}

// groupingStats completes cluster's stats with the link and Jev counts.
func groupingStats(s model.GroupingStats, in clusterIn, units []model.ChangeUnit) model.GroupingStats {
	s.Units = len(units)
	s.Structural = countKinds(in.structural)
	s.EmbedCandidates = len(in.em.cands)
	s.MechanicalCandidates = len(in.em.mech)
	s.PairsAsked = in.pairs.asked
	s.JevKept = map[string]int{}
	for _, l := range in.pairs.confirmed {
		s.JevKept[l.Relation]++
	}
	s.JevRejected = in.pairs.rejected
	s.EmbedNote = in.em.note
	return s
}

// nameGroups asks name_bullet for every real (not ungrouped) group whose
// stored name label is not current (unknown, or asked for another title
// and bullets), stores the answers and applies the names. Ungrouped groups
// are never sent. Only cancellation and store errors are returned; a Jev
// failure keeps placeholder names and is noted.
func (p *Pipeline) nameGroups(ctx context.Context, it model.Item, units []model.ChangeUnit, groups []model.Group, rl *model.RunLog) ([]model.Group, string, error) {
	bullets := model.DescBullets(it.Body)
	nctx := p.fn.nameCtx(it.Title, bullets)
	byID := unitsByID(units)
	texts := map[string]judge.GroupText{}
	var subjects []string
	for _, g := range groups {
		if g.Ungrouped {
			continue
		}
		s := model.GroupSubject(g.ID)
		subjects = append(subjects, s)
		texts[s] = judge.GroupText{ID: g.ID, Units: lookupUnits(byID, g.UnitIDs)}
	}
	if len(subjects) == 0 {
		return groups, "", nil
	}
	sort.Strings(subjects)
	stored, err := p.labelsFor(ctx, subjects, model.QNameBullet)
	if err != nil {
		return groups, "", err
	}
	cur := knownSubjects(stored, &nctx)
	var need []judge.GroupText
	for _, s := range subjects {
		if !cur[s] {
			need = append(need, texts[s])
		}
	}
	note := fmt.Sprintf("names: %d asked, %d cached", len(subjects), len(subjects)-len(need))
	fresh, suffix, err := p.askNames(ctx, it.Title, bullets, need, stored, rl)
	if err != nil {
		if ctx.Err() != nil || !errors.Is(err, errJudge) {
			return groups, note, err
		}
		p.d.Log.Warn("group names failed; placeholders kept", "item", it.ID, "err", err)
		return groups, note + " (failed: " + err.Error() + ")", nil
	}
	return p.fn.applyNames(groups, mergeLabels(stored, fresh), bullets, nctx), note + suffix, nil
}

// errJudge marks a failed judge call (soft), as opposed to a store error.
var errJudge = errors.New("judge failed")

func (p *Pipeline) askNames(ctx context.Context, title string, bullets []string, need []judge.GroupText, stored []model.Label, rl *model.RunLog) ([]model.Label, string, error) {
	if len(need) == 0 {
		return nil, "", nil
	}
	if p.d.Jev == nil {
		return nil, " (Jev unavailable)", nil
	}
	res, err := p.fn.names(ctx, p.d.Jev, judge.NameInput{
		Title: title, Bullets: bullets, Groups: need, Catalogue: p.catalogue(), Concurrency: p.d.Config.Pipeline.Concurrency,
	})
	rl.Calls, rl.Cost = rl.Calls+res.Calls, rl.Cost+res.Cost
	if err != nil {
		return nil, "", fmt.Errorf("%w: names: %w", errJudge, err)
	}
	fresh := keepKnown(res.Labels, stored)
	return fresh, failedNote(res), p.putLabels(ctx, fresh)
}

// splitSuggestion asks separate_pr for each changed file without a current
// label (Ctx = judge.FileCtx of the group list hash and the file's units)
// and builds the split
// suggestion. It runs only when the item has at least two real groups; a
// single group has nothing to split off.
func (p *Pipeline) splitSuggestion(ctx context.Context, it model.Item, units []model.ChangeUnit, groups []model.Group, rl *model.RunLog) (*model.SplitSuggestion, string, error) {
	sums := groupSummaries(units, groups)
	if len(sums) < 2 {
		return nil, "", nil
	}
	sctx := p.fn.splitCtx(sums)
	files := fileTexts(units)
	subjects := make([]string, len(files))
	for i, f := range files {
		subjects[i] = model.FileSubject(it.ID, f.Path)
	}
	stored, err := p.labelsFor(ctx, subjects, model.QSeparatePR)
	if err != nil {
		return nil, "", err
	}
	want := make(map[string]string, len(files))
	for i, f := range files {
		want[subjects[i]] = judge.FileCtx(sctx, f.Units)
	}
	cur := map[string]bool{}
	for _, l := range stored {
		if !model.IsUnknown(l) && l.Ctx == want[l.UnitID] {
			cur[l.UnitID] = true
		}
	}
	var need []judge.FileText
	for i, f := range files {
		if !cur[subjects[i]] {
			need = append(need, f)
		}
	}
	note := fmt.Sprintf("split: %d files, %d cached", len(files), len(files)-len(need))
	fresh, suffix, err := p.askSplit(ctx, it, need, sums, stored, rl)
	if err != nil {
		if ctx.Err() != nil || !errors.Is(err, errJudge) {
			return nil, note, err
		}
		p.d.Log.Warn("split suggestion failed", "item", it.ID, "err", err)
		return nil, note + " (failed: " + err.Error() + ")", nil
	}
	return p.fn.suggestion(it.ID, units, groups, mergeLabels(stored, fresh), sctx), note + suffix, nil
}

func (p *Pipeline) askSplit(ctx context.Context, it model.Item, need []judge.FileText, sums []judge.GroupSummary, stored []model.Label, rl *model.RunLog) ([]model.Label, string, error) {
	if len(need) == 0 {
		return nil, "", nil
	}
	if p.d.Jev == nil {
		return nil, " (Jev unavailable)", nil
	}
	res, err := p.fn.separatePR(ctx, p.d.Jev, judge.SplitInput{
		ItemID: it.ID, Title: it.Title, Files: need, Groups: sums, Catalogue: p.catalogue(), Concurrency: p.d.Config.Pipeline.Concurrency,
	})
	rl.Calls, rl.Cost = rl.Calls+res.Calls, rl.Cost+res.Cost
	if err != nil {
		return nil, "", fmt.Errorf("%w: separate_pr: %w", errJudge, err)
	}
	fresh := keepKnown(res.Labels, stored)
	return fresh, failedNote(res), p.putLabels(ctx, fresh)
}

// groupSummaries lists the real (not ungrouped) groups in group order with
// their sorted file paths and unit counts.
func groupSummaries(units []model.ChangeUnit, groups []model.Group) []judge.GroupSummary {
	byID := unitsByID(units)
	var out []judge.GroupSummary
	for _, g := range groups {
		if g.Ungrouped {
			continue
		}
		seen := map[string]bool{}
		var paths []string
		for _, id := range g.UnitIDs {
			if u, ok := byID[id]; ok && !seen[u.Path] {
				seen[u.Path] = true
				paths = append(paths, u.Path)
			}
		}
		sort.Strings(paths)
		out = append(out, judge.GroupSummary{ID: g.ID, Name: g.Name, Paths: paths, Units: len(g.UnitIDs)})
	}
	return out
}

// fileTexts groups units by path, paths sorted, units in item order.
func fileTexts(units []model.ChangeUnit) []judge.FileText {
	byPath := map[string][]model.ChangeUnit{}
	var paths []string
	for _, u := range units {
		if _, ok := byPath[u.Path]; !ok {
			paths = append(paths, u.Path)
		}
		byPath[u.Path] = append(byPath[u.Path], u)
	}
	sort.Strings(paths)
	out := make([]judge.FileText, len(paths))
	for i, p := range paths {
		us := byPath[p]
		sort.SliceStable(us, func(a, b int) bool { return us[a].Order < us[b].Order })
		out[i] = judge.FileText{Path: p, Units: us}
	}
	return out
}

// labelsFor loads the stored labels of subjects for one question.
func (p *Pipeline) labelsFor(ctx context.Context, subjects []string, question string) ([]model.Label, error) {
	all, err := p.d.Store.Labels(ctx, subjects)
	if err != nil {
		return nil, fmt.Errorf("pipeline: load %s labels: %w", question, err)
	}
	out := all[:0]
	for _, l := range all {
		if l.Question == question {
			out = append(out, l)
		}
	}
	return out, nil
}

// putLabels stores ls (no-op when empty).
func (p *Pipeline) putLabels(ctx context.Context, ls []model.Label) error {
	if len(ls) == 0 {
		return nil
	}
	if err := p.d.Store.PutLabels(ctx, ls); err != nil {
		return fmt.Errorf("pipeline: store labels: %w", err)
	}
	return nil
}

// knownSubjects returns the subjects with a known label; with ctx set, only
// labels whose Ctx equals *ctx count (current answers).
func knownSubjects(ls []model.Label, ctx *string) map[string]bool {
	m := map[string]bool{}
	for _, l := range ls {
		if !model.IsUnknown(l) && (ctx == nil || l.Ctx == *ctx) {
			m[l.UnitID] = true
		}
	}
	return m
}

type labelKey struct {
	subject, q string
	line       int
}

func keyOf(l model.Label) labelKey { return labelKey{l.UnitID, l.Question, l.Line} }

// keepKnown drops from fresh every unknown label whose (subject, question,
// line) has a known label in stored, so an outage never erases an answer.
func keepKnown(fresh, stored []model.Label) []model.Label {
	known := map[labelKey]bool{}
	for _, l := range stored {
		if !model.IsUnknown(l) {
			known[keyOf(l)] = true
		}
	}
	out := make([]model.Label, 0, len(fresh))
	for _, l := range fresh {
		if model.IsUnknown(l) && known[keyOf(l)] {
			continue
		}
		out = append(out, l)
	}
	return out
}

// mergeLabels returns stored with fresh labels replacing the same (subject,
// question, line), sorted by subject then question then line.
func mergeLabels(stored, fresh []model.Label) []model.Label {
	m := map[labelKey]model.Label{}
	for _, l := range stored {
		m[keyOf(l)] = l
	}
	for _, l := range fresh {
		m[keyOf(l)] = l
	}
	out := make([]model.Label, 0, len(m))
	for _, l := range m {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.UnitID != b.UnitID {
			return a.UnitID < b.UnitID
		}
		if a.Question != b.Question {
			return a.Question < b.Question
		}
		return a.Line < b.Line
	})
	return out
}

func unitsByID(units []model.ChangeUnit) map[string]model.ChangeUnit {
	m := make(map[string]model.ChangeUnit, len(units))
	for _, u := range units {
		m[u.ID] = u
	}
	return m
}

// lookupUnits returns the units of ids in that order, skipping unknown ids.
func lookupUnits(byID map[string]model.ChangeUnit, ids []string) []model.ChangeUnit {
	out := make([]model.ChangeUnit, 0, len(ids))
	for _, id := range ids {
		if u, ok := byID[id]; ok {
			out = append(out, u)
		}
	}
	return out
}

// joinNotes joins the non-empty notes with " · ".
func joinNotes(notes ...string) string {
	var parts []string
	for _, n := range notes {
		if n != "" {
			parts = append(parts, n)
		}
	}
	return strings.Join(parts, " · ")
}

// sortedKinds returns the keys of m sorted.
func sortedKinds(m map[model.LinkKind]int) []model.LinkKind {
	out := make([]model.LinkKind, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
