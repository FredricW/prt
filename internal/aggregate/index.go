package aggregate

import (
	"sort"

	"github.com/fredricw/prt/internal/model"
)

// unitLabels holds one unit's labels: unit-scope labels by question and line
// labels by new-side line then question. A nil *unitLabels is a unit with no
// labels at all; every method is nil-safe.
type unitLabels struct {
	q     map[string]model.Label
	lines map[int]map[string]model.Label
	all   []model.Label
}

// labelIndex indexes an item's labels by subject.
type labelIndex struct {
	units map[string]*unitLabels
	item  map[string]model.Label // item-subject labels by question
	// anyKnown is true when at least one label is a real answer (not the
	// "Jev unreachable" placeholder).
	anyKnown bool
}

// newIndex indexes labels. Later duplicates of the same subject and question
// win (overrides are already applied by the caller). Line labels are stored
// by position among the unit's added lines; the index keys them by new-side
// line number instead (model.AddedLineNumbers), dropping positions past the
// unit's added lines. A unit without a diff body keeps positions as given.
func newIndex(itemID string, labels []model.Label, units []model.ChangeUnit) labelIndex {
	idx := labelIndex{units: map[string]*unitLabels{}, item: map[string]model.Label{}}
	subject := model.ItemSubject(itemID)
	added := make(map[string][]int, len(units))
	for _, u := range units {
		added[u.ID] = model.AddedLineNumbers(u)
	}
	for _, l := range labels {
		if nums := added[l.UnitID]; l.Line > 0 && len(nums) > 0 {
			if l.Line > len(nums) {
				continue
			}
			l.Line = nums[l.Line-1]
		}
		if !model.IsUnknown(l) {
			idx.anyKnown = true
		}
		if l.UnitID == subject {
			idx.item[l.Question] = l
			continue
		}
		ul := idx.units[l.UnitID]
		if ul == nil {
			ul = &unitLabels{q: map[string]model.Label{}, lines: map[int]map[string]model.Label{}}
			idx.units[l.UnitID] = ul
		}
		ul.add(l)
	}
	return idx
}

func (ul *unitLabels) add(l model.Label) {
	ul.all = append(ul.all, l)
	if l.Line <= 0 {
		ul.q[l.Question] = l
		return
	}
	m := ul.lines[l.Line]
	if m == nil {
		m = map[string]model.Label{}
		ul.lines[l.Line] = m
	}
	m[l.Question] = l
}

// known returns the unit-scope label for q when it is a real answer.
func (ul *unitLabels) known(q string) (model.Label, bool) {
	if ul == nil {
		return model.Label{}, false
	}
	l, ok := ul.q[q]
	if !ok || model.IsUnknown(l) {
		return model.Label{}, false
	}
	return l, true
}

// value returns the unit-scope label value for q ("unknown" included), or
// "" when the question was not asked.
func (ul *unitLabels) value(q string) string {
	if ul == nil {
		return ""
	}
	return ul.q[q].Value
}

// yesP returns P(yes) of a boolean unit-scope label, 0 when unknown or absent.
func (ul *unitLabels) yesP(q string) float64 {
	l, ok := ul.known(q)
	if !ok {
		return 0
	}
	return l.P
}

// lineNumbers returns the lines that carry line labels, ascending.
func (ul *unitLabels) lineNumbers() []int {
	if ul == nil {
		return nil
	}
	out := make([]int, 0, len(ul.lines))
	for n := range ul.lines {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// lineYes reports whether line n has a known boolean label q with P >= 0.5,
// and returns that label.
func (ul *unitLabels) lineYes(n int, q string) (model.Label, bool) {
	if ul == nil {
		return model.Label{}, false
	}
	l, ok := ul.lines[n][q]
	if !ok || model.IsUnknown(l) || l.P < 0.5 {
		return model.Label{}, false
	}
	return l, true
}

// linesYes returns the lines (ascending) whose boolean label q is yes.
func (ul *unitLabels) linesYes(q string) []model.Label {
	var out []model.Label
	for _, n := range ul.lineNumbers() {
		if l, ok := ul.lineYes(n, q); ok {
			out = append(out, l)
		}
	}
	return out
}

// unsureCount counts the unit's labels with confidence below threshold.
func (ul *unitLabels) unsureCount(threshold float64) int {
	if ul == nil {
		return 0
	}
	n := 0
	for _, l := range ul.all {
		if l.Confidence < threshold {
			n++
		}
	}
	return n
}
