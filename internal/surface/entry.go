package surface

import (
	"cmp"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// What values of the entries this package writes (SurfaceEntry.What).
const (
	whatRoute    = "route"
	whatHandler  = "handler"
	whatMount    = "mount"
	whatResponse = "response shape"
	whatRequest  = "request shape"

	whatMigration = "migration"
	whatTable     = "table"
	whatColumn    = "column"
	whatIndex     = "index"
	whatView      = "view"

	whatFlag   = "flag"
	whatEnv    = "env"
	whatConfig = "config key"

	whatContract = "contract change" // Jev-only entries
)

// Detail words shared by detectors.
const (
	detailOneWay = "one-way"
)

// at is where an entry sits: a unit and one of its lines on one side.
type at struct {
	v *view
	l line
	s side
}

// newEntry returns a code-detected entry at a.
func newEntry(area, change, what, name string, a at, conf float64, fw string) model.SurfaceEntry {
	e := model.SurfaceEntry{
		Area: area, Change: change, What: what, Name: name,
		Line: a.l.no, Confidence: conf, Source: model.SurfaceFromCode, Framework: fw,
	}
	if a.v != nil {
		e.Path, e.UnitID = a.v.u.Path, a.v.u.ID
	}
	if a.s == sideOld {
		e.Side = string(model.SideOld)
	}
	return e
}

// joinDetail joins non-empty detail parts with " · ".
func joinDetail(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " · ")
}

// changeRank orders changes by importance: removed, changed, added.
func changeRank(c string) int {
	switch c {
	case model.SurfaceRemoved:
		return 0
	case model.SurfaceChanged:
		return 1
	}
	return 2
}

// sourceRank prefers confirmed code entries, then code, then Jev-only.
func sourceRank(s string) int {
	switch s {
	case model.SurfaceFromBoth:
		return 0
	case model.SurfaceFromCode:
		return 1
	}
	return 2
}

// finish rounds, deduplicates and sorts an area's entries; the result is
// never nil.
func finish(es []model.SurfaceEntry) []model.SurfaceEntry {
	for i := range es {
		es[i].Confidence = round2(es[i].Confidence)
	}
	es = dedup(es)
	slices.SortStableFunc(es, compareImportance)
	if es == nil {
		es = []model.SurfaceEntry{}
	}
	return es
}

// compareImportance is the block order: removed, changed, added; then
// confidence descending, path, line; then the rest for determinism.
func compareImportance(a, b model.SurfaceEntry) int {
	return cmp.Or(
		cmp.Compare(changeRank(a.Change), changeRank(b.Change)),
		cmp.Compare(b.Confidence, a.Confidence),
		cmp.Compare(a.Path, b.Path),
		cmp.Compare(a.Line, b.Line),
		cmp.Compare(a.Name, b.Name),
		cmp.Compare(a.What, b.What),
		cmp.Compare(a.Side, b.Side),
		cmp.Compare(a.UnitID, b.UnitID),
		cmp.Compare(a.Detail, b.Detail),
	)
}

// compareBest orders duplicates: the one kept comes first.
func compareBest(a, b model.SurfaceEntry) int {
	return cmp.Or(
		cmp.Compare(b.Confidence, a.Confidence),
		cmp.Compare(sourceRank(a.Source), sourceRank(b.Source)),
		cmp.Compare(a.Path, b.Path),
		cmp.Compare(a.Line, b.Line),
		cmp.Compare(a.Side, b.Side),
		cmp.Compare(a.UnitID, b.UnitID),
		cmp.Compare(a.Detail, b.Detail),
	)
}

// dedup keeps one entry per (change, what, name): the most confident one,
// with the other places named in its Detail.
func dedup(es []model.SurfaceEntry) []model.SurfaceEntry {
	type key struct{ change, what, name string }
	groups := map[key][]model.SurfaceEntry{}
	var order []key
	for _, e := range es {
		k := key{e.Change, e.What, e.Name}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}
	out := make([]model.SurfaceEntry, 0, len(order))
	for _, k := range order {
		g := groups[k]
		slices.SortStableFunc(g, compareBest)
		best := g[0]
		if extra := otherPlaces(best, g[1:]); extra != "" {
			best.Detail = joinDetail(best.Detail, extra)
		}
		out = append(out, best)
	}
	return out
}

// otherPlaces names the places of an entry's duplicates ("also at a.go:3",
// "also at a.go:3 and 2 more"); "" when they are all at the same place.
func otherPlaces(best model.SurfaceEntry, rest []model.SurfaceEntry) string {
	seen := map[string]bool{loc(best): true}
	var places []string
	for _, e := range rest {
		if l := loc(e); !seen[l] {
			seen[l] = true
			places = append(places, l)
		}
	}
	switch len(places) {
	case 0:
		return ""
	case 1:
		return "also at " + places[0]
	}
	return "also at " + places[0] + " and " + strconv.Itoa(len(places)-1) + " more"
}

func round2(f float64) float64 {
	if math.IsNaN(f) {
		return 0
	}
	return math.Round(min(max(f, 0), 1)*100) / 100
}
