package diff

import "github.com/sergi/go-diff/diffmatchpatch"

// Span is a half-open byte range [Start, End) within a line.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// IntraLine returns the changed byte ranges of a paired old/new line, from a
// character-level diff with semantic cleanup (spec: no third model call).
func IntraLine(oldLine, newLine string) (oldSpans, newSpans []Span) {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(oldLine, newLine, false)
	diffs = dmp.DiffCleanupSemantic(diffs)
	oi, ni := 0, 0
	for _, d := range diffs {
		n := len(d.Text)
		switch d.Type {
		case diffmatchpatch.DiffEqual:
			oi += n
			ni += n
		case diffmatchpatch.DiffDelete:
			oldSpans = appendSpan(oldSpans, Span{oi, oi + n})
			oi += n
		case diffmatchpatch.DiffInsert:
			newSpans = appendSpan(newSpans, Span{ni, ni + n})
			ni += n
		}
	}
	return oldSpans, newSpans
}

func appendSpan(ss []Span, s Span) []Span {
	if len(ss) > 0 && ss[len(ss)-1].End == s.Start {
		ss[len(ss)-1].End = s.End
		return ss
	}
	return append(ss, s)
}

// Pair pairs deleted and added lines of a hunk for intra-line highlighting:
// within each run of consecutive -/+ lines, the i-th deletion pairs with the
// i-th addition. It returns index pairs into h.Lines.
func Pair(h Hunk) [][2]int {
	var pairs [][2]int
	for i := 0; i < len(h.Lines); {
		if h.Lines[i].Op != Del {
			i++
			continue
		}
		var dels, adds []int
		for i < len(h.Lines) && h.Lines[i].Op == Del {
			dels = append(dels, i)
			i++
		}
		for i < len(h.Lines) && h.Lines[i].Op == Add {
			adds = append(adds, i)
			i++
		}
		for k := 0; k < len(dels) && k < len(adds); k++ {
			pairs = append(pairs, [2]int{dels[k], adds[k]})
		}
	}
	return pairs
}
