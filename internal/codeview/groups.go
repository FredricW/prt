package codeview

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// groupEntry is one group of the by-change list (mode B).
type groupEntry struct {
	g   *model.Group
	agg *model.GroupAgg // nil when Detail.Agg has no row for the group
	// units are the group's UnitIDs present in Detail.Units, in review order.
	units []string
	files int // distinct paths among units
	// bullet is the bullet chip text ("b2") for NameSource bullet: the index
	// of the Agg bullet whose text equals the group's bullet, else "bullet".
	bullet string
}

// mechanical reports whether the group renders as one example plus a count.
func (ge *groupEntry) mechanical() bool { return ge.g.Mechanical && len(ge.units) > 0 }

// example is the unit a mechanical group shows (ExampleUnit when present,
// else its first unit).
func (ge *groupEntry) example() string {
	for _, id := range ge.units {
		if id == ge.g.ExampleUnit {
			return id
		}
	}
	if len(ge.units) == 0 {
		return ""
	}
	return ge.units[0]
}

// paneUnits are the units the group's pane renders, in order: only the
// example for a mechanical group, else every unit.
func (ge *groupEntry) paneUnits() []string {
	if ge.mechanical() {
		return []string{ge.example()}
	}
	return ge.units
}

// count is the number shown next to a mechanical group ("×N"): Group.Count,
// or the unit count when Count is unset.
func (ge *groupEntry) count() int {
	if ge.g.Count > 0 {
		return ge.g.Count
	}
	return len(ge.units)
}

// placeholder reports whether the name is code-derived, not a Jev bullet.
func (ge *groupEntry) placeholder() bool { return ge.g.NameSource != model.NameBullet }

// displayName is the group name as shown: placeholder names are wrapped in
// ‹ › so they read as stand-ins even without colour.
func (ge *groupEntry) displayName() string {
	name := strings.TrimSpace(ge.g.Name)
	if name == "" {
		name = "unnamed"
	}
	if ge.placeholder() {
		return "‹" + name + "›"
	}
	return name
}

// countText is "3 units · 2 files", or "×5 · 5 files" for a mechanical group.
func (ge *groupEntry) countText() string {
	n := plural(len(ge.units), "unit")
	if ge.mechanical() {
		n = "×" + strconv.Itoa(ge.count())
	}
	return n + " · " + plural(ge.files, "file")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// buildGroups indexes d.Groups into c: groups in review order (Order, with
// ungrouped singletons last), units filtered to those present, and a
// unit → group index. A unit that no group lists (a partial detail) gets a
// singleton placeholder group so mode B still reaches it.
func buildGroups(c *content, d proto.Detail) {
	if len(d.Groups) == 0 || len(c.units) == 0 {
		return
	}
	order := make([]int, len(d.Groups))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		ga, gb := &d.Groups[order[a]], &d.Groups[order[b]]
		if ga.Ungrouped != gb.Ungrouped {
			return !ga.Ungrouped
		}
		return ga.Order < gb.Order
	})
	aggs := map[string]*model.GroupAgg{}
	bullets := map[string]int{}
	if d.Agg != nil {
		for i := range d.Agg.Groups {
			aggs[d.Agg.Groups[i].GroupID] = &d.Agg.Groups[i]
		}
		for _, b := range d.Agg.Bullets {
			bullets[strings.TrimSpace(b.Text)] = b.Index
		}
	}
	c.groupOf = map[string]int{}
	for _, i := range order {
		g := &d.Groups[i]
		c.addGroup(g, aggs[g.ID], bullets)
	}
	c.addOrphans(d.Units)
}

// addGroup appends g unless none of its units are present.
func (c *content) addGroup(g *model.Group, agg *model.GroupAgg, bullets map[string]int) {
	ge := groupEntry{g: g, agg: agg, bullet: "bullet"}
	paths := map[string]bool{}
	for _, id := range g.UnitIDs {
		if c.units[id] == nil {
			continue
		}
		if _, dup := c.groupOf[id]; dup {
			continue // every unit is in exactly one group; keep the first
		}
		ge.units = append(ge.units, id)
		paths[c.units[id].Path] = true
	}
	if len(ge.units) == 0 {
		return
	}
	ge.files = len(paths)
	text := strings.TrimSpace(g.DescBullet)
	if text == "" {
		text = strings.TrimSpace(g.Name)
	}
	if i, ok := bullets[text]; ok {
		ge.bullet = model.BulletValue(i)
	}
	for _, id := range ge.units {
		c.groupOf[id] = len(c.groups)
	}
	c.groups = append(c.groups, ge)
}

// addOrphans appends a placeholder singleton for every unit no group lists.
func (c *content) addOrphans(units []model.ChangeUnit) {
	for i := range units {
		u := &units[i]
		if _, ok := c.groupOf[u.ID]; ok {
			continue
		}
		name := u.Symbol
		if name == "" {
			name = path.Base(u.Path)
		}
		g := &model.Group{ID: "orphan:" + u.ID, UnitIDs: []string{u.ID}, Name: name,
			NameSource: model.NamePlaceholder, Ungrouped: true, Order: len(c.groups)}
		c.addGroup(g, nil, nil)
	}
}

// firstUngrouped is the index of the first ungrouped group, or -1.
func (c *content) firstUngrouped() int {
	for i := range c.groups {
		if c.groups[i].g.Ungrouped {
			return i
		}
	}
	return -1
}

// groupIndex returns the index of the group with id, or -1.
func (c *content) groupIndex(id string) int {
	for i := range c.groups {
		if c.groups[i].g.ID == id {
			return i
		}
	}
	return -1
}

// splitTarget returns the group a split-suggestion row jumps to: the first
// listed group among the file's GroupIDs, else the first group with a unit
// in that file; -1 when none.
func (c *content) splitTarget(sf model.SplitFile) int {
	best := -1
	for _, id := range sf.GroupIDs {
		if gi := c.groupIndex(id); gi >= 0 && (best < 0 || gi < best) {
			best = gi
		}
	}
	if best >= 0 {
		return best
	}
	for gi := range c.groups {
		for _, id := range c.groups[gi].units {
			if c.units[id].Path == sf.Path {
				return gi
			}
		}
	}
	return -1
}

// unitLabel names a unit in link lines: path base, then the symbol.
func (c *content) unitLabel(id string) string {
	u := c.units[id]
	if u == nil {
		return id
	}
	s := path.Base(u.Path)
	if u.Symbol != "" {
		s += " " + u.Symbol
	}
	return s
}

// whyLines lists the links that bind a group, one line each, in words.
// Mechanical links collapse into one "same shape ×N (verified)" line.
func (c *content) whyLines(ge *groupEntry) []string {
	var out []string
	mech := false
	for _, l := range ge.g.Links {
		if l.Kind == model.LinkMechanical {
			if !mech {
				out = append(out, "same shape ×"+strconv.Itoa(ge.count())+" (verified)")
				mech = true
			}
			continue
		}
		out = append(out, c.linkText(l))
	}
	if ge.g.Mechanical && !mech {
		out = append(out, "same shape ×"+strconv.Itoa(ge.count())+" (verified)")
	}
	return out
}

// linkText is one "why" line: "calls  a → b", "jev caller update .82 · cos
// .79  a → b", "near   cos .71  a → b".
func (c *content) linkText(l model.Link) string {
	ends := c.unitLabel(l.From) + " → " + c.unitLabel(l.To)
	switch {
	case l.Source == model.LinkJev:
		s := "jev"
		if l.Relation != "" {
			s += " " + l.Relation
		}
		s += " " + ProbText(l.P)
		if l.Kind == model.LinkEmbedNeighbor {
			s += " · cos " + ProbText(l.Weight)
		} else {
			s += " · " + kindWord(l.Kind)
		}
		return s + "  " + ends
	case l.Kind == model.LinkEmbedNeighbor:
		return "near   cos " + ProbText(l.Weight) + "  " + ends
	}
	return fmt.Sprintf("%-6s %s", kindWord(l.Kind), ends)
}

// kindWord is a link kind in words ("same symbol" for same-symbol).
func kindWord(k model.LinkKind) string { return strings.ReplaceAll(string(k), "-", " ") }

// groupChips builds a group header's chips: bullet with P (bullet-named
// groups), mechanical/ungrouped/edited markers, then — only with labels —
// GroupAgg facts (risk max, tags, test gap, breaking, unsure), and the
// routing placeholder.
func groupChips(ge *groupEntry, labelled bool) []chip {
	var cs []chip
	if !ge.placeholder() {
		cs = append(cs, chip{text: ge.bullet, prob: ProbText(ge.g.NameP)})
	}
	switch {
	case ge.mechanical():
		cs = append(cs, chip{text: "mechanical ×" + strconv.Itoa(ge.count())})
	case ge.g.Ungrouped:
		cs = append(cs, chip{text: "ungrouped"})
	}
	if ge.g.UserEdited {
		cs = append(cs, chip{text: "edited by you"})
	}
	if labelled && ge.agg != nil {
		cs = append(cs, aggChips(ge.agg)...)
	}
	return append(cs, chip{text: "routing: — (M3)"})
}

// aggChips renders GroupAgg facts; zero values are left out.
func aggChips(a *model.GroupAgg) []chip {
	var cs []chip
	if a.RiskMax > 0 {
		cs = append(cs, chip{text: "risk " + strconv.FormatFloat(a.RiskMax, 'f', -1, 64), warn: a.RiskMax >= 4})
	}
	for _, t := range riskTagOrder(a.RiskTags) {
		s := t
		if n := a.RiskTags[t]; n > 1 {
			s += " ×" + strconv.Itoa(n)
		}
		cs = append(cs, chip{text: s})
	}
	if a.TestGap > 0 {
		cs = append(cs, chip{text: "test gap " + strconv.Itoa(a.TestGap), warn: true})
	}
	if n := len(a.Breaking); n > 0 {
		cs = append(cs, chip{text: "breaking " + strconv.Itoa(n), warn: true})
	}
	if a.Unsure > 0 {
		cs = append(cs, chip{text: "? " + strconv.Itoa(a.Unsure) + " unsure"})
	}
	return cs
}

// riskTagOrder lists the tags with a count in model.RiskTags order, then
// any other tag by name.
func riskTagOrder(tags map[string]int) []string {
	var out []string
	known := map[string]bool{}
	for _, t := range model.RiskTags {
		known[t] = true
		if tags[t] > 0 {
			out = append(out, t)
		}
	}
	var rest []string
	for t, n := range tags {
		if !known[t] && n > 0 {
			rest = append(rest, t)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}
