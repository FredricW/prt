package pipelinetest

import (
	"fmt"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Fixture paths the grouping expectations name.
const (
	clientPath        = "web/src/api/client.ts"
	errorsPath        = "web/src/api/errors.ts"
	cacheHookPath     = "web/src/hooks/useProfileCache.ts"
	cacheHookTestPath = "web/src/__tests__/useProfileCache.test.ts"
	clientTestPath    = "web/src/__tests__/client.test.ts"
)

// GroupExpectations returns how the M2 grouping of a fixture run deviates
// from what the fixture PRs were designed for:
//
//	(a) #102: exactly one mechanical group with Count ≥ 5 (smaller
//	    mechanical classes, e.g. call sites of one shape ×3, may exist), its
//	    ExampleUnit set, every member sharing the example's shape hash, and
//	    every unit of that shape collapsed into it (the rename, ×N);
//	(b) #101: one group holds the client.ts getUser unit and ≥ 2 caller
//	    units in other files whose diff contains "getUser(", with getUser
//	    before them (interface first);
//	(c) #101: a moved link from a client.ts unit to an errors.ts unit
//	    (ApiError);
//	(d) #101: tests links useProfileCache.test.ts → useProfileCache.ts and
//	    client.test.ts → client.ts;
//	(e) every PR: every unit in exactly one group, ungrouped groups last,
//	    Agg.Grouping set and consistent (Units == number of units).
func GroupExpectations(outs map[int]Outcome) []string {
	var out []string
	for _, n := range PRNumbers {
		out = append(out, everyUnitGrouped(n, outs[n])...)
	}
	out = append(out, expectMechanical102(outs[102])...)
	out = append(out, expectInterfaceGroup101(outs[101])...)
	return append(out, expectLinks101(outs[101])...)
}

// hasStructural reports whether any link came from stage 2.
func hasStructural(ls []model.Link) bool {
	for _, l := range ls {
		if l.Source == model.LinkStructural {
			return true
		}
	}
	return false
}

// everyUnitGrouped checks (e).
func everyUnitGrouped(n int, o Outcome) []string {
	var out []string
	seen := map[string]int{}
	sawUngrouped := false
	for _, g := range o.Groups {
		if sawUngrouped && !g.Ungrouped {
			out = append(out, fmt.Sprintf("#%d: group %q (order %d) listed after an ungrouped group", n, g.Name, g.Order))
		}
		sawUngrouped = sawUngrouped || g.Ungrouped
		for _, id := range g.UnitIDs {
			seen[id]++
		}
	}
	for _, u := range o.Units {
		if c := seen[u.ID]; c != 1 {
			out = append(out, fmt.Sprintf("#%d: unit %s %s in %d groups, want exactly 1", n, u.Path, u.Symbol, c))
		}
	}
	switch g := o.Agg.Grouping; {
	case g == nil:
		out = append(out, fmt.Sprintf("#%d: Agg.Grouping not set", n))
	case g.Units != len(o.Units):
		out = append(out, fmt.Sprintf("#%d: Agg.Grouping.Units = %d, want %d", n, g.Units, len(o.Units)))
	}
	return out
}

// expectMechanical102 checks (a).
func expectMechanical102(o Outcome) []string {
	units := unitMap(o)
	var mech []model.Group
	for _, g := range o.Groups {
		if g.Mechanical && g.Count >= 5 {
			mech = append(mech, g)
		}
	}
	if len(mech) != 1 {
		return []string{fmt.Sprintf("#102: %d mechanical groups of 5 or more, want exactly 1 (the rename)", len(mech))}
	}
	g := mech[0]
	ex, ok := units[g.ExampleUnit]
	if !ok || ex.ShapeHash == "" {
		return []string{fmt.Sprintf("#102: mechanical group example %q is not a unit with a shape hash", g.ExampleUnit)}
	}
	var out []string
	if g.Count < 5 {
		out = append(out, fmt.Sprintf("#102: mechanical group count %d, want >= 5", g.Count))
	}
	for _, id := range g.UnitIDs {
		if units[id].ShapeHash != ex.ShapeHash {
			out = append(out, fmt.Sprintf("#102: mechanical member %s %s does not share the example's shape", units[id].Path, units[id].Symbol))
		}
	}
	same := 0
	for _, u := range o.Units {
		if u.ShapeHash == ex.ShapeHash {
			same++
		}
	}
	if same != g.Count || len(g.UnitIDs) != g.Count {
		out = append(out, fmt.Sprintf("#102: %d units share the rename's shape but the group counts %d (%d members)", same, g.Count, len(g.UnitIDs)))
	}
	return out
}

// expectInterfaceGroup101 checks (b).
func expectInterfaceGroup101(o Outcome) []string {
	units := unitMap(o)
	for _, g := range o.Groups {
		pos := -1
		for i, id := range g.UnitIDs {
			if u := units[id]; u.Path == clientPath && strings.Contains(u.Symbol, "getUser") {
				pos = i
			}
		}
		if pos < 0 {
			continue
		}
		callers, early := 0, 0
		for i, id := range g.UnitIDs {
			if u := units[id]; u.Path != clientPath && strings.Contains(u.Diff, "getUser(") {
				callers++
				if i < pos {
					early++
				}
			}
		}
		var out []string
		if callers < 2 {
			out = append(out, fmt.Sprintf("#101: getUser group %q has %d caller units in other files, want >= 2", g.Name, callers))
		}
		if early > 0 {
			out = append(out, fmt.Sprintf("#101: %d getUser callers come before getUser in group %q (interface first)", early, g.Name))
		}
		return out
	}
	return []string{"#101: no group holds the client.ts getUser unit"}
}

// expectLinks101 checks (c) and (d).
func expectLinks101(o Outcome) []string {
	units := unitMap(o)
	has := func(kind model.LinkKind, from, to string, toMatch func(model.ChangeUnit) bool) bool {
		for _, l := range o.Links {
			f, t := units[l.From], units[l.To]
			if l.Kind == kind && f.Path == from && t.Path == to && (toMatch == nil || toMatch(t)) {
				return true
			}
		}
		return false
	}
	var out []string
	apiError := func(u model.ChangeUnit) bool {
		return strings.Contains(u.Symbol, "ApiError") || strings.Contains(u.Diff, "class ApiError")
	}
	if !has(model.LinkMoved, clientPath, errorsPath, apiError) {
		out = append(out, "#101: no moved link "+clientPath+" → "+errorsPath+" (ApiError)")
	}
	if !has(model.LinkTests, cacheHookTestPath, cacheHookPath, nil) {
		out = append(out, "#101: no tests link "+cacheHookTestPath+" → "+cacheHookPath)
	}
	if !has(model.LinkTests, clientTestPath, clientPath, nil) {
		out = append(out, "#101: no tests link "+clientTestPath+" → "+clientPath)
	}
	return out
}

func unitMap(o Outcome) map[string]model.ChangeUnit {
	m := make(map[string]model.ChangeUnit, len(o.Units))
	for _, u := range o.Units {
		m[u.ID] = u
	}
	return m
}
