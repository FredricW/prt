package link

import (
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// linkKey identifies a link: one per (From, To, Kind).
type linkKey struct {
	from, to string
	kind     model.LinkKind
}

// linkSet accumulates deduplicated links between known units.
type linkSet struct {
	order map[string]int // unit id → Order
	links map[linkKey]model.Link
}

func newLinkSet(units []model.ChangeUnit) *linkSet {
	s := &linkSet{order: make(map[string]int, len(units)), links: map[linkKey]model.Link{}}
	for _, u := range units {
		s.order[u.ID] = u.Order
	}
	return s
}

// add records a link unless it is a self link, an endpoint is unknown, or
// the (From, To, Kind) triple is already present (the first one wins).
func (s *linkSet) add(from, to string, kind model.LinkKind, source model.LinkSource, weight float64) {
	if from == to {
		return
	}
	if _, ok := s.order[from]; !ok {
		return
	}
	if _, ok := s.order[to]; !ok {
		return
	}
	k := linkKey{from: from, to: to, kind: kind}
	if _, ok := s.links[k]; ok {
		return
	}
	s.links[k] = model.Link{From: from, To: to, Kind: kind, Weight: weight, Source: source}
}

// structural adds a structural link of weight 1.
func (s *linkSet) structural(from, to string, kind model.LinkKind) {
	s.add(from, to, kind, model.LinkStructural, 1)
}

// sorted returns the links ordered by (From Order, To Order, Kind), ties
// by ids, so the output never depends on map iteration.
func (s *linkSet) sorted() []model.Link {
	out := make([]model.Link, 0, len(s.links))
	for _, l := range s.links {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if oa, ob := s.order[a.From], s.order[b.From]; oa != ob {
			return oa < ob
		}
		if oa, ob := s.order[a.To], s.order[b.To]; oa != ob {
			return oa < ob
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if c := strings.Compare(a.From, b.From); c != 0 {
			return c < 0
		}
		return a.To < b.To
	})
	return out
}

// sortedUnits returns a copy of units ordered by (Order, ID).
func sortedUnits(units []model.ChangeUnit) []model.ChangeUnit {
	out := append([]model.ChangeUnit(nil), units...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order != out[j].Order {
			return out[i].Order < out[j].Order
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// unitLines are the changed rows of one unit, 0-based, per side.
type unitLines struct {
	added   map[int]bool // head rows of + lines
	deleted map[int]bool // base rows of - lines
}

// changedLines walks a unit's diff from its range starts.
func changedLines(u model.ChangeUnit) unitLines {
	ul := unitLines{added: map[int]bool{}, deleted: map[int]bool{}}
	oldNo, newNo := u.Range.Old.Start, u.Range.New.Start
	for _, l := range strings.Split(u.Diff, "\n") {
		if l == "" {
			continue
		}
		switch l[0] {
		case '+':
			ul.added[newNo-1] = true
			newNo++
		case '-':
			ul.deleted[oldNo-1] = true
			oldNo++
		case ' ':
			oldNo++
			newNo++
		}
	}
	return ul
}

// baseSide reports whether a unit's own lines live on the base side only
// (pure deletions, including the origin half of a move).
func baseSide(u model.ChangeUnit) bool { return u.Adds == 0 && u.Dels > 0 }

// spanRows returns the 0-based rows of a span.
func spanRows(s model.Span) map[int]bool {
	rows := make(map[int]bool, s.Count)
	for i := 0; i < s.Count; i++ {
		rows[s.Start-1+i] = true
	}
	return rows
}

// isTestPath reports whether p is a test file: *_test.go, test_*.py,
// *_test.py, *.test.* / *.spec.* (ts/tsx/js/jsx/mjs/cjs), or any file under
// a __tests__, test or tests directory. Same rule as pipeline's isTestPath.
func isTestPath(p string) bool {
	base := baseName(p)
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasSuffix(base, ".py") && (strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py")):
		return true
	}
	ext := extOf(base)
	switch ext {
	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs":
		s := strings.TrimSuffix(base, ext)
		if strings.HasSuffix(s, ".test") || strings.HasSuffix(s, ".spec") {
			return true
		}
	}
	for _, dir := range strings.Split(dirOf(p), "/") {
		if testDirs[dir] {
			return true
		}
	}
	return false
}

// testDirs are directory names whose files are tests.
var testDirs = map[string]bool{"__tests__": true, "tests": true, "test": true}
