package codeview

import (
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// RiskMin is N in the "risk ≥ N" filter: a unit passes when its integer
// risk rung (UnitAgg.RiskLevel) is at least RiskMin.
const RiskMin = 3

// defaultFilters returns the filters that are on when the component is
// created (spec: needs eyes, behaviour changes, mine).
func defaultFilters() [numFilters]bool { return [numFilters]bool{true, true, true} }

// numFilters is len(AllFilters), the number of toggle keys.
const numFilters = 10

// filterIndex returns f's position in AllFilters, or -1.
func filterIndex(f Filter) int {
	for i, g := range AllFilters {
		if g == f {
			return i
		}
	}
	return -1
}

// hasData reports whether a filter can be evaluated yet; comments and
// last-look marks arrive in M4.
func hasData(f Filter) bool { return f != FilterComments && f != FilterSince }

// filterLabel is the bar text of f ("risk ≥ 3" for FilterRisk).
func filterLabel(f Filter) string {
	if f == FilterRisk {
		return strings.Replace(string(f), "N", strconv.Itoa(RiskMin), 1)
	}
	return string(f)
}

// matches reports whether unit aggregate a satisfies filter f.
func matches(f Filter, a *model.UnitAgg) bool {
	switch f {
	case FilterNeedsEyes:
		return a.Attention == model.AttnEyes
	case FilterBehaviour:
		// An unanswered behaviour label is no claim either way: keep the unit.
		return a.Behaviour || a.BehaviourUnknown
	case FilterMine:
		return a.Owned
	case FilterRisk:
		return a.RiskLevel >= RiskMin
	case FilterUntested:
		return a.Untested
	case FilterUndocumented:
		return a.Undocumented
	case FilterUnsure:
		return a.Unsure
	}
	return true
}

// filtersApply reports whether filters hide anything: only with labels.
func (m *Model) filtersApply() bool { return m.c.labelled }

// visible reports whether a unit passes every active filter (AND). Units
// revealed by FocusUnit, and every unit when labels are missing, pass.
func (m *Model) visible(id string) bool {
	if !m.filtersApply() || m.revealed[id] {
		return true
	}
	a := m.c.aggs[id]
	if a == nil {
		return true
	}
	for i, f := range AllFilters {
		if !m.filters[i] || !m.filterActive(f) {
			continue
		}
		ok := matches(f, a)
		if f == FilterFindings {
			ok = m.c.hasFindings(id) // findings live on the routing rows, not the aggregate
		}
		if !ok {
			return false
		}
	}
	return true
}

// filterActive reports whether an enabled filter takes effect: it has data,
// and for "mine" the item has at least one owned file (with none, "mine"
// would hide every unit, so it is ignored and the bar says so).
func (m *Model) filterActive(f Filter) bool {
	return hasData(f) && (f != FilterMine || m.c.anyOwned)
}

// collapsed reports whether a unit is drawn as a one-line summary: noise
// per the aggregate (or, by file, a mechanical repeat: a non-example member
// of a mechanical group), not unsure, without review findings, and not
// expanded by `z` (on its file by file, on its group by change) or
// FocusUnit. By change a mechanical group already shows only its example.
func (m *Model) collapsed(id string) bool {
	a := m.c.aggs[id]
	if !m.c.hasAgg || a == nil || a.Unsure || m.c.hasFindings(id) || m.expandedUnits[id] {
		return false
	}
	if m.byChange() {
		if !a.Noise {
			return false
		}
		gi, ok := m.c.groupOf[id]
		return !ok || !m.expandedGroups[m.c.groups[gi].g.ID]
	}
	if !a.Noise && m.c.repeatGroup(id) == nil {
		return false
	}
	return !m.expandedFiles[m.c.units[id].Path]
}

// hiddenIn counts the units of ids that filters hide.
func (m *Model) hiddenIn(ids []string) int {
	n := 0
	for _, id := range ids {
		if !m.visible(id) {
			n++
		}
	}
	return n
}

// SetFilter turns filter f on or off (filters without data yet are
// ignored). It keeps the selected file and clamps the scroll.
func (m *Model) SetFilter(f Filter, on bool) {
	i := filterIndex(f)
	if i < 0 || !hasData(f) {
		return
	}
	m.filters[i] = on
	m.revealed = map[string]bool{}
	m.rebuild()
}

// FilterOn reports whether filter f is on.
func (m Model) FilterOn(f Filter) bool {
	i := filterIndex(f)
	return i >= 0 && m.filters[i]
}
