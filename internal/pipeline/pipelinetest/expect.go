package pipelinetest

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Problems returns the ways a fixture run fails the hard requirements: a
// missing PR, a failed stage (with its exact error) or labels missing.
func Problems(outs map[int]Outcome) []string {
	var out []string
	for _, n := range PRNumbers {
		o, ok := outs[n]
		if !ok {
			out = append(out, fmt.Sprintf("#%d: no outcome", n))
			continue
		}
		for _, rl := range o.Failed() {
			out = append(out, fmt.Sprintf("#%d: stage %s failed: %s", n, rl.Stage, rl.Err))
		}
		if o.Agg.LabelsMissing {
			out = append(out, fmt.Sprintf("#%d: labels missing (Jev unavailable)", n))
		}
	}
	return out
}

// Expectations returns how the labelled fixture run deviates from the
// outcomes the fixture PRs were designed for (tolerant of model variance):
//
//   - every unit of every PR has a known "kind" label;
//   - #101: a breaking headline (or breaking unit in web/src/api/client.ts)
//     on getUser, and the "Invalidate the profile cache on logout" bullet is
//     present and claimed by no unit except the clearProfileCache helper;
//   - #102: at least 5 units share one shape hash and refactor is the
//     largest composition share;
//   - #103: every unit is deleted (or moved) and nothing is added;
//   - #104: dependency is the largest composition share, or every non-noise
//     unit is labelled dependency.
func Expectations(outs map[int]Outcome) []string {
	var out []string
	for _, n := range PRNumbers {
		out = append(out, everyUnitHasKind(n, outs[n])...)
	}
	out = append(out, expect101(outs[101])...)
	out = append(out, expect102(outs[102])...)
	out = append(out, expect103(outs[103])...)
	return append(out, expect104(outs[104])...)
}

func everyUnitHasKind(n int, o Outcome) []string {
	var out []string
	kinds := kindByUnit(o)
	for _, u := range o.Units {
		if k, ok := kinds[u.ID]; !ok || k == model.LabelUnknown {
			out = append(out, fmt.Sprintf("#%d %s %s: kind label %q", n, u.Path, u.Symbol, k))
		}
	}
	return out
}

func kindByUnit(o Outcome) map[string]string {
	m := map[string]string{}
	for _, l := range o.Labels {
		if l.Question == model.QKind && l.Line == 0 {
			m[l.UnitID] = l.Value
		}
	}
	return m
}

// The only unit allowed to claim #101's logout bullet (see expect101).
const (
	logoutHelperPath   = "web/src/hooks/useProfileCache.ts"
	logoutHelperSymbol = "clearProfileCache"
)

func expect101(o Outcome) []string {
	var out []string
	units := map[string]model.ChangeUnit{}
	for _, u := range o.Units {
		units[u.ID] = u
	}
	found := false
	for _, h := range o.Agg.Headlines {
		found = found || (h.Kind == "breaking" && strings.Contains(units[h.UnitID].Symbol, "getUser"))
	}
	for _, ua := range o.Agg.Units {
		u := units[ua.UnitID]
		found = found || (ua.Breaking && u.Path == "web/src/api/client.ts" && strings.Contains(u.Symbol, "getUser"))
	}
	if !found {
		out = append(out, fmt.Sprintf("#101: no breaking headline or unit for getUser (headlines %+v)", o.Agg.Headlines))
	}
	var logout *model.BulletAgg
	for i, b := range o.Agg.Bullets {
		if strings.Contains(b.Text, "Invalidate the profile cache on logout") {
			logout = &o.Agg.Bullets[i]
		}
	}
	if logout == nil {
		return append(out, "#101: logout bullet missing from the aggregate")
	}
	// The PR adds clearProfileCache but no logout handler calls it. Seen one
	// unit at a time, Jev may fairly map the helper itself to the bullet;
	// proving "not wired" needs caller links (M2). So M1 only requires that
	// no unit other than the cache helper claims the bullet.
	for _, id := range logout.UnitIDs {
		if u := units[id]; u.Path != logoutHelperPath || u.Symbol != logoutHelperSymbol {
			out = append(out, fmt.Sprintf("#101: logout bullet claimed by %s %s, want not implemented (or only by %s)", u.Path, u.Symbol, logoutHelperSymbol))
		}
	}
	return out
}

func expect102(o Outcome) []string {
	var out []string
	shapes := map[string]int{}
	best := 0
	for _, u := range o.Units {
		if u.ShapeHash != "" {
			shapes[u.ShapeHash]++
			best = max(best, shapes[u.ShapeHash])
		}
	}
	if best < 5 {
		out = append(out, fmt.Sprintf("#102: largest shape group = %d units, want >= 5", best))
	}
	if top := TopShare(o.Agg.Composition); top != "refactor" {
		out = append(out, fmt.Sprintf("#102: largest composition share = %q, want refactor (%v)", top, o.Agg.Composition))
	}
	return out
}

func expect103(o Outcome) []string {
	var out []string
	for _, u := range o.Units {
		if u.Kind != model.UnitDeleted && u.Kind != model.UnitMoved {
			out = append(out, fmt.Sprintf("#103: unit %s %s kind %q, want deleted or moved", u.Path, u.Symbol, u.Kind))
		}
	}
	if o.Agg.Added != 0 {
		out = append(out, fmt.Sprintf("#103: added = %d, want 0", o.Agg.Added))
	}
	return out
}

func expect104(o Outcome) []string {
	if TopShare(o.Agg.Composition) == model.KindDependency {
		return nil
	}
	var out []string
	kinds := kindByUnit(o)
	noise := map[string]bool{}
	for _, ua := range o.Agg.Units {
		noise[ua.UnitID] = ua.Noise
	}
	for _, u := range o.Units {
		if !noise[u.ID] && kinds[u.ID] != model.KindDependency {
			out = append(out, fmt.Sprintf("#104: unit %s kind %q, want dependency (composition %v)", u.Path, kinds[u.ID], o.Agg.Composition))
		}
	}
	return out
}

// TopShare returns the composition kind with the largest share ("" if
// empty; ties broken by name).
func TopShare(c map[string]float64) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	top := ""
	for _, k := range keys {
		if top == "" || c[k] > c[top] {
			top = k
		}
	}
	return top
}
