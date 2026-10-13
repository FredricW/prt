package pipeline

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/labels"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

// HeadlineBreaking is the model.Headline.Kind counted into Item.Breaking.
const HeadlineBreaking = "breaking"

// jevOut is the outcome of stage 4 (labels) for one item.
type jevOut struct {
	// missing is true when some units could not be labelled this run (Jev
	// nil or unavailable, or labels.Run failed): ItemAgg.LabelsMissing.
	missing bool
}

// catalogue returns the question catalogue (built-in when Deps has none).
func (p *Pipeline) catalogue() questions.Catalogue {
	if len(p.d.Questions.Questions) == 0 {
		return questions.Default()
	}
	return p.d.Questions
}

// labelStage is stage 4 (labels half; pairs arrive in M2). Units whose
// stored labels already answer every live question are cache hits; the rest
// go to labels.Run. Without Jev (nil client, ErrUnavailable) the affected
// units get unknown labels and the item is marked labels-missing. A
// labels.Run error fails the stage (soft: the caller carries on with unknown
// labels) and is returned so the RunLog row records it.
func (p *Pipeline) labelStage(ctx context.Context, it model.Item, files []model.File, units []model.ChangeUnit, rl *model.RunLog) (jevOut, error) {
	cat := p.catalogue()
	stored, err := p.d.Store.Labels(ctx, unitIDs(units))
	if err != nil {
		return jevOut{}, fmt.Errorf("pipeline: load labels: %w", err)
	}
	need := unitsNeedingLabels(units, stored, cat)
	rl.In = len(units)
	rl.Note = fmt.Sprintf("%d of %d cached", len(units)-len(need), len(units))
	if len(need) == 0 {
		rl.CacheHit = true
		return jevOut{}, nil
	}
	if p.d.Jev == nil {
		rl.Note = "Jev unavailable"
		return jevOut{missing: true}, p.storeLabels(ctx, withUnknowns(nil, stored, need, cat), rl)
	}
	res, runErr := labels.Run(ctx, p.d.Jev, labels.Input{
		Ctx:         p.labelContext(it, files),
		Units:       need,
		Catalogue:   cat,
		LineBatch:   p.d.Config.Pipeline.LineBatch,
		Concurrency: p.d.Config.Pipeline.Concurrency,
	})
	rl.Calls, rl.Cost = res.Calls, res.Cost
	if runErr != nil {
		// Keep any labels that did arrive; fill the rest with unknowns.
		ls := withUnknowns(res.Labels, stored, need, cat)
		if err := p.storeLabels(ctx, ls, rl); err != nil {
			p.d.Log.Warn("store labels after failure", "item", it.ID, "err", err)
		}
		return jevOut{missing: true}, fmt.Errorf("pipeline: labels %s: %w", it.ID, runErr)
	}
	out := jevOut{missing: res.Unavailable}
	if res.Unavailable {
		rl.Note = "Jev unavailable"
	}
	return out, p.storeLabels(ctx, dropShadowedUnknowns(res.Labels, stored), rl)
}

// dropShadowedUnknowns removes unknown labels from ls whose (unit, question,
// line) already has a known stored label, so an outage never erases an
// earlier answer.
func dropShadowedUnknowns(ls, stored []model.Label) []model.Label {
	type key struct {
		unit, q string
		line    int
	}
	known := map[key]bool{}
	for _, l := range stored {
		if !model.IsUnknown(l) {
			known[key{l.UnitID, l.Question, l.Line}] = true
		}
	}
	out := make([]model.Label, 0, len(ls))
	for _, l := range ls {
		if model.IsUnknown(l) && known[key{l.UnitID, l.Question, l.Line}] {
			continue
		}
		out = append(out, l)
	}
	return out
}

func (p *Pipeline) storeLabels(ctx context.Context, ls []model.Label, rl *model.RunLog) error {
	rl.Out = len(ls)
	if len(ls) == 0 {
		return nil
	}
	if err := p.d.Store.PutLabels(ctx, ls); err != nil {
		return fmt.Errorf("pipeline: store labels: %w", err)
	}
	return nil
}

// labelContext is the per-item context sent with unit questions.
func (p *Pipeline) labelContext(it model.Item, files []model.File) labels.Context {
	return labels.Context{
		ItemID:       it.ID,
		Title:        it.Title,
		Bullets:      model.DescBullets(it.Body),
		Role:         p.d.Config.User.Role,
		Owns:         p.d.Config.User.Owns,
		TestsTouched: testsTouched(files),
	}
}

// testsTouched lists the changed paths that look like tests, in diff order.
func testsTouched(files []model.File) []string {
	var out []string
	for _, f := range files {
		if isTestPath(f.Path) {
			out = append(out, f.Path)
		}
	}
	return out
}

// isTestPath reports whether a path looks like a test file: Go "_test.go",
// Python "test_*.py" / "*_test.py", JS/TS "*.test.*" / "*.spec.*" (ts, tsx,
// js, jsx, mjs, cjs), or anything under a "__tests__", "test" or "tests"
// directory.
func isTestPath(p string) bool {
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasSuffix(base, ".py") && (strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")):
		return true
	}
	ext := path.Ext(base)
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		stem := strings.TrimSuffix(base, ext)
		if strings.HasSuffix(stem, ".test") || strings.HasSuffix(stem, ".spec") {
			return true
		}
	}
	for _, dir := range strings.Split(path.Dir(p), "/") {
		if dir == "__tests__" || dir == "tests" || dir == "test" {
			return true
		}
	}
	return false
}

// unitIDs returns the ids of units, in order.
func unitIDs(units []model.ChangeUnit) []string {
	ids := make([]string, len(units))
	for i, u := range units {
		ids[i] = u.ID
	}
	return ids
}

// liveLabelIDs returns the label ids of the live questions of one scope.
func liveLabelIDs(cat questions.Catalogue, s questions.Scope) []string {
	var out []string
	for _, q := range cat.Live(s) {
		out = append(out, q.LabelIDs()...)
	}
	return out
}

// unitsNeedingLabels returns the units whose stored labels are incomplete:
// a unit is complete when every live unit-scope label id has a known
// (non-unknown) unit label and, if the unit adds lines, every live
// line-scope label id has at least one known line label. Unknown line labels
// on other lines are not inspected (the labeller may legitimately skip lines
// such as blank ones, so "every added line" cannot be required).
func unitsNeedingLabels(units []model.ChangeUnit, stored []model.Label, cat questions.Catalogue) []model.ChangeUnit {
	type key struct {
		unit, q string
		line    bool
	}
	known := map[key]bool{}
	for _, l := range stored {
		if !model.IsUnknown(l) {
			known[key{l.UnitID, l.Question, l.Line > 0}] = true
		}
	}
	unitQs, lineQs := liveLabelIDs(cat, questions.ScopeUnit), liveLabelIDs(cat, questions.ScopeLine)
	var need []model.ChangeUnit
	for _, u := range units {
		complete := true
		for _, q := range unitQs {
			complete = complete && known[key{u.ID, q, false}]
		}
		if u.Adds > 0 {
			for _, q := range lineQs {
				complete = complete && known[key{u.ID, q, true}]
			}
		}
		if !complete {
			need = append(need, u)
		}
	}
	return need
}

// unknownLabels is what labels.Run produces for units when Jev is
// unavailable: an unknown label (confidence 0) for every live unit-scope
// label id, and for every live line-scope label id on every added line.
func unknownLabels(units []model.ChangeUnit, cat questions.Catalogue) []model.Label {
	unitQs, lineQs := liveLabelIDs(cat, questions.ScopeUnit), liveLabelIDs(cat, questions.ScopeLine)
	var out []model.Label
	for _, u := range units {
		for _, q := range unitQs {
			out = append(out, model.Label{UnitID: u.ID, Question: q, Value: model.LabelUnknown})
		}
		for _, ln := range addedLines(u) {
			for _, q := range lineQs {
				out = append(out, model.Label{UnitID: u.ID, Question: q, Line: ln, Value: model.LabelUnknown})
			}
		}
	}
	return out
}

// withUnknowns returns ls plus an unknown label for every (unit, question,
// line) of units that neither ls nor a known stored label answers, so a
// failed or skipped run never overwrites an earlier real answer.
func withUnknowns(ls, stored []model.Label, units []model.ChangeUnit, cat questions.Catalogue) []model.Label {
	type key struct {
		unit, q string
		line    int
	}
	have := map[key]bool{}
	for _, l := range ls {
		have[key{l.UnitID, l.Question, l.Line}] = true
	}
	for _, l := range stored {
		if !model.IsUnknown(l) {
			have[key{l.UnitID, l.Question, l.Line}] = true
		}
	}
	out := append([]model.Label(nil), ls...)
	for _, l := range unknownLabels(units, cat) {
		if !have[key{l.UnitID, l.Question, l.Line}] {
			out = append(out, l)
		}
	}
	return out
}

// addedLines returns the new-side line numbers of a unit's added lines.
func addedLines(u model.ChangeUnit) []int {
	if u.Adds == 0 || u.Diff == "" {
		return nil
	}
	var out []int
	n := u.Range.New.Start
	for _, ln := range strings.Split(u.Diff, "\n") {
		switch {
		case strings.HasPrefix(ln, "+"):
			out = append(out, n)
			n++
		case strings.HasPrefix(ln, " "):
			n++
		}
	}
	return out
}

// applyOverrides replaces label values with the user's overrides: Value =
// override, Confidence 1, P 1 (P(yes) = 0 for a boolean overridden to "no",
// following the label conventions), Overridden set. Overrides apply to
// unit-scope labels (Line 0); an override for a (unit, question) with no
// stored label at all adds a unit-scope label.
func applyOverrides(ls []model.Label, ovs []model.Override) []model.Label {
	if len(ovs) == 0 {
		return ls
	}
	type key struct{ unit, q string }
	byKey := map[key]model.Override{}
	for _, o := range ovs {
		byKey[key{o.UnitID, o.Question}] = o
	}
	out := make([]model.Label, 0, len(ls)+len(ovs))
	used := map[key]bool{}
	for _, l := range ls {
		k := key{l.UnitID, l.Question}
		if o, ok := byKey[k]; ok && l.Line == 0 {
			l = overridden(l, o)
		}
		used[k] = true // line-only questions get no unit-scope label
		out = append(out, l)
	}
	var extra []model.Label
	for k, o := range byKey {
		if !used[k] {
			extra = append(extra, overridden(model.Label{UnitID: o.UnitID, Question: o.Question}, o))
		}
	}
	sort.Slice(extra, func(i, j int) bool {
		if extra[i].UnitID != extra[j].UnitID {
			return extra[i].UnitID < extra[j].UnitID
		}
		return extra[i].Question < extra[j].Question
	})
	return append(out, extra...)
}

func overridden(l model.Label, o model.Override) model.Label {
	v := o.Value
	l.Value, l.Overridden, l.Confidence, l.P = v, &v, 1, 1
	if v == model.LabelNo {
		l.P = 0
	}
	if lvl := model.ScoreLevel(l); lvl > 0 {
		l.Score = float64(lvl)
	}
	if l.Probs != nil {
		l.Probs = map[string]float64{v: 1}
	}
	return l
}
