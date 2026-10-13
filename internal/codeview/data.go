package codeview

import (
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// fileEntry is one row of the file list.
type fileEntry struct {
	path       string
	owned      bool
	units      []string // unit ids in line order (only units present in Detail.Units)
	adds, dels int      // from Detail.Files (0 when the file is not listed there)
}

// content is the indexed form of a proto.Detail.
type content struct {
	itemID string
	files  []fileEntry
	units  map[string]*model.ChangeUnit
	aggs   map[string]*model.UnitAgg
	labels []model.Label
	// hasAgg is true when Detail.Agg carries units: chips, minimap cells
	// and noise collapsing come from it. Without it every unit is expanded.
	hasAgg bool
	// labelled is hasAgg without LabelsMissing: label chips, gutter marks
	// and filters apply only then.
	labelled      bool
	labelsMissing bool
	// anyOwned is true when at least one file is owned; without owned files
	// the "mine" filter is ignored (it would hide everything).
	anyOwned bool
}

// plain reports whether there are no units to lay out, so the component
// falls back to the plain diff viewer.
func (c *content) plain() bool { return len(c.units) == 0 }

// buildContent indexes d. Numbers and order come from d.Agg when it has
// units; otherwise files come from d.Files/d.Units in path and line order.
func buildContent(d proto.Detail) content {
	c := content{
		itemID: d.Item.ID,
		units:  make(map[string]*model.ChangeUnit, len(d.Units)),
		aggs:   map[string]*model.UnitAgg{},
		labels: d.Labels,
	}
	for i := range d.Units {
		c.units[d.Units[i].ID] = &d.Units[i]
	}
	if len(c.units) == 0 {
		return c
	}
	stats := make(map[string]model.File, len(d.Files))
	for _, f := range d.Files {
		stats[f.Path] = f
	}
	if d.Agg != nil && len(d.Agg.Units) > 0 {
		c.hasAgg = true
		c.labelsMissing = d.Agg.LabelsMissing
		c.labelled = !d.Agg.LabelsMissing
		for i := range d.Agg.Units {
			c.aggs[d.Agg.Units[i].UnitID] = &d.Agg.Units[i]
		}
	}
	if c.hasAgg && len(d.Agg.Files) > 0 {
		c.files = filesFromAgg(d.Agg.Files, c.units, stats)
	} else {
		c.files = filesFromUnits(d.Units, d.Files)
	}
	for _, f := range c.files {
		c.anyOwned = c.anyOwned || f.owned
	}
	return c
}

// filesFromAgg keeps the aggregate's order (owned first, then attention),
// stable-partitioned so the `not yours` divider is a single boundary.
func filesFromAgg(fas []model.FileAgg, units map[string]*model.ChangeUnit, stats map[string]model.File) []fileEntry {
	out := make([]fileEntry, 0, len(fas))
	for _, fa := range fas {
		fe := fileEntry{path: fa.Path, owned: fa.Owned, adds: stats[fa.Path].Adds, dels: stats[fa.Path].Dels}
		for _, id := range fa.UnitIDs {
			if units[id] != nil {
				fe.units = append(fe.units, id)
			}
		}
		out = append(out, fe)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].owned && !out[j].owned })
	return out
}

// filesFromUnits lists Detail.Files (plus any unit path missing there),
// owned first, each group in path order, units in line order.
func filesFromUnits(units []model.ChangeUnit, files []model.File) []fileEntry {
	byPath := map[string]*fileEntry{}
	var out []*fileEntry
	add := func(path string, owned bool, adds, dels int) *fileEntry {
		if fe, ok := byPath[path]; ok {
			return fe
		}
		fe := &fileEntry{path: path, owned: owned, adds: adds, dels: dels}
		byPath[path] = fe
		out = append(out, fe)
		return fe
	}
	for _, f := range files {
		add(f.Path, f.Owned, f.Adds, f.Dels)
	}
	sorted := make([]*model.ChangeUnit, len(units))
	for i := range units {
		sorted[i] = &units[i]
	}
	sort.SliceStable(sorted, func(i, j int) bool { return unitLess(sorted[i], sorted[j]) })
	for _, u := range sorted {
		fe := add(u.Path, false, 0, 0)
		fe.units = append(fe.units, u.ID)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].owned != out[j].owned {
			return out[i].owned
		}
		return out[i].path < out[j].path
	})
	res := make([]fileEntry, len(out))
	for i, fe := range out {
		res[i] = *fe
	}
	return res
}

func unitLess(a, b *model.ChangeUnit) bool {
	if a.Range.New.Start != b.Range.New.Start {
		return a.Range.New.Start < b.Range.New.Start
	}
	if a.Range.Old.Start != b.Range.Old.Start {
		return a.Range.Old.Start < b.Range.Old.Start
	}
	return a.Order < b.Order
}

// divider returns the index of the first not-owned file, or -1 when every
// file is owned.
func (c *content) divider() int {
	for i, f := range c.files {
		if !f.owned {
			return i
		}
	}
	return -1
}

// fileOf returns the index of the file listing unitID, or -1.
func (c *content) fileOf(unitID string) int {
	for i, f := range c.files {
		for _, id := range f.units {
			if id == unitID {
				return i
			}
		}
	}
	return -1
}

// unitLines parses a unit's diff body ("+x", "-y", " z" lines, no hunk
// header) into lines numbered from the unit's range.
func unitLines(u *model.ChangeUnit) []diff.Line {
	if u.Diff == "" {
		return nil
	}
	raw := strings.Split(strings.TrimSuffix(u.Diff, "\n"), "\n")
	out := make([]diff.Line, 0, len(raw))
	oldNo, newNo := u.Range.Old.Start, u.Range.New.Start
	for _, s := range raw {
		if strings.HasPrefix(s, `\ `) {
			if len(out) > 0 {
				out[len(out)-1].NoEOL = true
			}
			continue
		}
		l := diff.Line{Op: diff.Context}
		if s != "" {
			l.Op, l.Text = diff.Op(s[0]), s[1:]
		}
		switch l.Op {
		case diff.Add:
			l.NewNo = newNo
			newNo++
		case diff.Del:
			l.OldNo = oldNo
			oldNo++
		default:
			l.Op = diff.Context
			if s != "" && s[0] != ' ' {
				l.Text = s // no op prefix: keep the text whole
			}
			l.OldNo, l.NewNo = oldNo, newNo
			oldNo++
			newNo++
		}
		out = append(out, l)
	}
	return out
}
