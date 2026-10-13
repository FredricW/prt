package quality

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// span is an inclusive line range on one side of the diff.
type span struct {
	path        string
	first, last int
}

// region is where a ground-truth entry lives: its side and every location
// that counts as the same finding (the entry's own lines first, then each
// also_at location, all on the entry's side).
type region struct {
	side  string
	spans []span
}

// regionOf builds the region of a validated location. also_at entries take
// the entry's side (the README defines them as "other locations of the
// same finding").
func regionOf(l Loc) region {
	side := l.Side
	if side == "" {
		side = SideNew
	}
	r := region{side: side}
	if len(l.Lines) == 2 {
		r.spans = append(r.spans, span{path: l.Path, first: l.Lines[0], last: l.Lines[1]})
	}
	for _, a := range l.AlsoAt {
		if s, err := parseAlsoAt(a); err == nil {
			r.spans = append(r.spans, s)
		}
	}
	return r
}

// parseAlsoAt parses an also_at location "path:first-last" (or
// "path:line").
func parseAlsoAt(s string) (span, error) {
	i := strings.LastIndex(s, ":")
	if i <= 0 || i == len(s)-1 {
		return span{}, errors.New(`want "path:first-last"`)
	}
	path, rng := s[:i], s[i+1:]
	a, b, found := strings.Cut(rng, "-")
	if !found {
		b = a
	}
	first, err1 := strconv.Atoi(a)
	last, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || first < 1 || first > last {
		return span{}, fmt.Errorf("bad line range %q", rng)
	}
	return span{path: path, first: first, last: last}, nil
}

// unitSpans are a unit's changed spans on both sides of the diff. The
// new-side span runs from the first to the last added line; a unit that
// adds nothing uses its whole new range (the place the deletion happened,
// context included). The old side is the same over deleted lines and the
// old range. Context-only overlap therefore never matches a unit that has
// changed lines on that side.
type unitSpans struct {
	path     string
	new, old span
	hasNew   bool
	hasOld   bool
}

// spansOf computes a unit's changed spans from its diff body.
func spansOf(u model.ChangeUnit) unitSpans {
	us := unitSpans{path: u.Path}
	added, deleted := changedLines(u)
	us.new, us.hasNew = sideSpan(u.Path, added, u.Range.New)
	us.old, us.hasOld = sideSpan(u.Path, deleted, u.Range.Old)
	return us
}

// sideSpan is the span over changed lines, else the whole range; ok is
// false when both are empty.
func sideSpan(path string, lines []int, rng model.Span) (span, bool) {
	if len(lines) > 0 {
		return span{path: path, first: lines[0], last: lines[len(lines)-1]}, true
	}
	if rng.Count > 0 {
		return span{path: path, first: rng.Start, last: rng.Start + rng.Count - 1}, true
	}
	return span{}, false
}

// changedLines numbers a unit's added (new side) and deleted (old side)
// lines from its range starts, like aggregate does.
func changedLines(u model.ChangeUnit) (added, deleted []int) {
	if u.Diff == "" {
		return nil, nil
	}
	oldN, newN := u.Range.Old.Start, u.Range.New.Start
	for _, ln := range strings.Split(strings.TrimSuffix(u.Diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(ln, "@@"), strings.HasPrefix(ln, `\`):
		case strings.HasPrefix(ln, "+"):
			added = append(added, newN)
			newN++
		case strings.HasPrefix(ln, "-"):
			deleted = append(deleted, oldN)
			oldN++
		default:
			oldN++
			newN++
		}
	}
	return added, deleted
}

// overlaps reports whether a unit overlaps a region: same path and the
// unit's changed span on the region's side intersects any of the region's
// spans.
func (us unitSpans) overlaps(r region) bool {
	s, ok := us.new, us.hasNew
	if r.side == SideOld {
		s, ok = us.old, us.hasOld
	}
	if !ok {
		return false
	}
	for _, t := range r.spans {
		if t.path == s.path && s.first <= t.last && t.first <= s.last {
			return true
		}
	}
	return false
}

// anyOverlap reports whether any of the units overlaps r.
func anyOverlap(units []unitSpans, r region) bool {
	for _, u := range units {
		if u.overlaps(r) {
			return true
		}
	}
	return false
}
