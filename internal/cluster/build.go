package cluster

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Unit roles inside a group, in review order.
const (
	roleInterface = iota
	roleOther
	roleTest
)

// Group review categories, in review order.
const (
	catInterface = iota
	catCode
	catTest
	catMechanical
	catUngrouped
)

// builder turns groups under construction into model groups.
type builder struct {
	g     *graph
	cut   map[int]bool
	iface map[string]bool // units whose "interface" label says yes
}

// built is a model group with its sort key.
type built struct {
	group    model.Group
	category int
	first    int // smallest unit position
}

// build computes roles, unit order, links, placeholder names, the review
// order and the ids of every group.
func (b builder) build(itemID string, groups []*group) []model.Group {
	tests := b.testUnits()
	bs := make([]built, 0, len(groups))
	for _, gr := range groups {
		bs = append(bs, b.one(itemID, gr, tests))
	}
	sort.SliceStable(bs, func(i, j int) bool {
		if bs[i].category != bs[j].category {
			return bs[i].category < bs[j].category
		}
		return bs[i].first < bs[j].first
	})
	out := make([]model.Group, len(bs))
	for i, x := range bs {
		x.group.Order = i
		out[i] = x.group
	}
	return out
}

// one builds a single group.
func (b builder) one(itemID string, gr *group, tests []bool) built {
	roles := b.roles(gr, tests)
	order := orderUnits(gr, roles)
	mg := model.Group{
		ItemID:     itemID,
		UnitIDs:    b.ids(order),
		NameSource: model.NamePlaceholder,
		Ungrouped:  !gr.mechanical && len(gr.units) == 1,
		UserEdited: gr.userEdited,
	}
	mg.ID = model.GroupID(mg.UnitIDs)
	if gr.mechanical {
		mg.Mechanical = true
		mg.ExampleUnit = b.g.units[gr.example].ID
		mg.Count = len(gr.units)
		mg.Links = sortedLinks(gr.mechLinks)
		mg.Ejected = b.ids(gr.ejected)
	} else if !mg.Ungrouped {
		mg.Links = b.poolLinks(gr.units)
	}
	mg.Name = b.placeholder(mg, order)
	return built{group: mg, category: category(gr, mg, roles), first: gr.units[0]}
}

// ids maps positions to unit ids.
func (b builder) ids(ps []int) []string {
	if len(ps) == 0 {
		return nil
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = b.g.units[p].ID
	}
	return out
}

// testUnits marks units on a test path or at the From end of a tests link.
func (b builder) testUnits() []bool {
	out := make([]bool, len(b.g.units))
	for i, u := range b.g.units {
		out[i] = isTestPath(u.Path)
	}
	for _, l := range b.g.links {
		if l.l.Kind == model.LinkTests && graphEdge(l) {
			out[l.from] = true
		}
	}
	return out
}

// roles assigns each member its role. Test wins over interface, so a test
// helper called by a test stays with the tests. A unit is an interface when
// its "interface" label says yes or another member calls or imports it.
func (b builder) roles(gr *group, tests []bool) map[int]int {
	in := make(map[int]bool, len(gr.units))
	for _, u := range gr.units {
		in[u] = true
	}
	called := map[int]bool{}
	for _, l := range b.g.links {
		if graphEdge(l) && (l.l.Kind == model.LinkCalls || l.l.Kind == model.LinkImports) && in[l.from] && in[l.to] {
			called[l.to] = true
		}
	}
	roles := make(map[int]int, len(gr.units))
	for _, u := range gr.units {
		switch {
		case tests[u]:
			roles[u] = roleTest
		case called[u] || b.iface[b.g.units[u].ID]:
			roles[u] = roleInterface
		default:
			roles[u] = roleOther
		}
	}
	return roles
}

// orderUnits sorts members by role then position; a mechanical group keeps
// its example first.
func orderUnits(gr *group, roles map[int]int) []int {
	out := append([]int(nil), gr.units...)
	sort.SliceStable(out, func(i, j int) bool {
		a, c := out[i], out[j]
		if gr.mechanical && (a == gr.example) != (c == gr.example) {
			return a == gr.example
		}
		if roles[a] != roles[c] {
			return roles[a] < roles[c]
		}
		return a < c
	})
	return out
}

// category is a group's review category.
func category(gr *group, mg model.Group, roles map[int]int) int {
	switch {
	case mg.Ungrouped:
		return catUngrouped
	case gr.mechanical:
		return catMechanical
	}
	allTests := true
	for _, u := range gr.units {
		if roles[u] == roleInterface {
			return catInterface
		}
		allTests = allTests && roles[u] == roleTest
	}
	if allTests {
		return catTest
	}
	return catCode
}

// poolLinks returns the kept structural and Jev links among units: every
// usable link that was not cut, sorted by (From Order, To Order, Kind).
func (b builder) poolLinks(units []int) []model.Link {
	in := make(map[int]bool, len(units))
	for _, u := range units {
		in[u] = true
	}
	var ls []link
	for i, l := range b.g.links {
		if graphEdge(l) && !b.cut[i] && in[l.from] && in[l.to] {
			ls = append(ls, l)
		}
	}
	return sortedLinks(ls)
}

// sortedLinks returns the model links in link order (already the order of
// g.links, re-sorted here for groups whose links were filtered).
func sortedLinks(ls []link) []model.Link {
	if len(ls) == 0 {
		return nil
	}
	s := append([]link(nil), ls...)
	sort.Slice(s, func(i, j int) bool { return linkLess(s[i], s[j]) })
	out := make([]model.Link, len(s))
	for i, l := range s {
		out[i] = l.l
	}
	return out
}

// placeholder is the code-derived group name: "<base> ×<Count>" for
// mechanical groups, "<base>" for ungrouped units and "<base> (+N)"
// otherwise, where base is the first unit's symbol, else its file name.
func (b builder) placeholder(mg model.Group, order []int) string {
	first := b.g.units[order[0]]
	switch {
	case mg.Mechanical:
		return fmt.Sprintf("%s ×%d", baseName(first), mg.Count)
	case mg.Ungrouped:
		return baseName(first)
	}
	return fmt.Sprintf("%s (+%d)", baseName(first), len(order)-1)
}

// baseName is a unit's symbol, else the base name of its path.
func baseName(u model.ChangeUnit) string {
	if s := strings.TrimSpace(u.Symbol); s != "" {
		return s
	}
	return path.Base(u.Path)
}

// interfaceLabels returns the units whose unit-scope "interface" label is a
// real answer with P(yes) ≥ 0.5; later labels for the same unit win.
func interfaceLabels(labels []model.Label) map[string]bool {
	out := map[string]bool{}
	for _, l := range labels {
		if l.Question != model.QInterface || l.Line != 0 || model.IsUnknown(l) {
			continue
		}
		out[l.UnitID] = l.P >= 0.5
	}
	return out
}

// isTestPath reports whether p is a test file: *_test.go, test_*.py,
// *_test.py, *.test.* / *.spec.* for ts/tsx/js/jsx/mjs/cjs, or any path
// under __tests__, tests or test (copy of the pipeline's rule).
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
