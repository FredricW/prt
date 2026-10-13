package parse

import (
	"strings"

	"github.com/fredricw/prt/internal/diff"
)

// segment is a run of hunk lines [start, end) that becomes one unit.
type segment struct {
	start, end   int
	firstChanged int
	lastChanged  int
	sym, sig     string
	addDecl      *decl // first enclosing declaration of an added line
	delDecl      *decl // first enclosing declaration of a deleted line
}

// segments splits a hunk at function boundaries. Consecutive changed lines
// with the same symbol form one segment; context between two segments
// stays with the preceding one while it is still inside that symbol, so
// leading context joins the first segment and trailing context the last.
// A hunk with no changed lines yields no segments.
func (fc *fileCtx) segments(h diff.Hunk) []segment {
	if !hasChanges(h.Lines) {
		return nil
	}
	if !fc.ok {
		return []segment{{start: 0, end: len(h.Lines), sym: fc.fallbackSymbol(h)}}
	}
	syms := fc.changedSymbols(h.Lines)
	var segs []segment
	for i, l := range h.Lines {
		if l.Op == diff.Context {
			continue
		}
		s := syms[i]
		if n := len(segs); n == 0 || segs[n-1].sym != s.name {
			segs = append(segs, segment{sym: s.name, firstChanged: i})
		}
		sg := &segs[len(segs)-1]
		sg.lastChanged = i
		switch {
		case l.Op == diff.Add && sg.addDecl == nil:
			sg.addDecl = s.d
		case l.Op == diff.Del && sg.delDecl == nil:
			sg.delDecl = s.d
		}
	}
	for k := range segs {
		if k > 0 {
			split := segs[k-1].lastChanged + 1
			for split < segs[k].firstChanged && fc.contextSymbol(h.Lines[split]) == segs[k-1].sym {
				split++
			}
			segs[k-1].end, segs[k].start = split, split
		}
		segs[k].sig = signatureOf(segs[k])
	}
	segs[len(segs)-1].end = len(h.Lines)
	return segs
}

func hasChanges(lines []diff.Line) bool {
	for _, l := range lines {
		if l.Op != diff.Context {
			return true
		}
	}
	return false
}

// fallbackSymbol is the symbol of a hunk-level unit: git's section heading
// for supported languages, none for unknown ones.
func (fc *fileCtx) fallbackSymbol(h diff.Hunk) string {
	if fc.lang == "" {
		return ""
	}
	return h.Section
}

// signatureOf prefers the head-side declaration of a segment.
func signatureOf(sg segment) string {
	switch {
	case sg.sym == "":
		return ""
	case sg.addDecl != nil:
		return sg.addDecl.sig
	case sg.delDecl != nil:
		return sg.delDecl.sig
	}
	return ""
}

// changedSymbols attributes each changed line to its enclosing symbol on
// its own side (+ head, - base); context entries are left empty.
func (fc *fileCtx) changedSymbols(lines []diff.Line) []lineSym {
	out := make([]lineSym, len(lines))
	for i, l := range lines {
		switch l.Op {
		case diff.Add:
			out[i] = fc.new.at(l.NewNo - 1)
		case diff.Del:
			out[i] = fc.old.at(l.OldNo - 1)
		}
	}
	mergeRenames(lines, out)
	attachBlanks(lines, out)
	return out
}

// attachBlanks gives each blank changed line the symbol of the previous
// changed line in the hunk (or the next one, for leading blanks), so a
// blank line next to a deleted or added function never forms a unit of its
// own. The declaration is not copied, so a blank line never picks the
// signature.
func attachBlanks(lines []diff.Line, syms []lineSym) {
	prev, have := "", false
	var pending []int
	for i, l := range lines {
		if l.Op == diff.Context {
			continue
		}
		if strings.TrimSpace(l.Text) != "" {
			for _, k := range pending {
				syms[k] = lineSym{name: syms[i].name}
			}
			pending = pending[:0]
			prev, have = syms[i].name, true
			continue
		}
		if have {
			syms[i] = lineSym{name: prev}
		} else {
			pending = append(pending, i)
		}
	}
}

// contextSymbol is the symbol enclosing a context line (head side when
// present).
func (fc *fileCtx) contextSymbol(l diff.Line) string {
	if fc.new != nil {
		return fc.new.at(l.NewNo - 1).name
	}
	return fc.old.at(l.OldNo - 1).name
}

// mergeRenames keeps a renamed declaration in one unit: within a block of
// adjacent changed lines, when every deleted line is in one base
// declaration, every added line in one head declaration with a different
// name, and the block rewrites both headers, the deleted lines take the
// head symbol.
func mergeRenames(lines []diff.Line, syms []lineSym) {
	for i := 0; i < len(lines); {
		if lines[i].Op == diff.Context {
			i++
			continue
		}
		j := i
		for j < len(lines) && lines[j].Op != diff.Context {
			j++
		}
		mergeBlock(lines[i:j], syms[i:j])
		i = j
	}
}

func mergeBlock(lines []diff.Line, syms []lineSym) {
	oldD, oldHead, oldOK := sameDecl(lines, syms, diff.Del)
	newD, newHead, newOK := sameDecl(lines, syms, diff.Add)
	if !oldOK || !newOK || !oldHead || !newHead || oldD.name == newD.name {
		return
	}
	for k, l := range lines {
		if l.Op == diff.Del {
			syms[k] = lineSym{name: newD.name, d: newD}
		}
	}
}

// sameDecl reports whether all lines with op share one declaration, and
// whether one of them is that declaration's header row.
func sameDecl(lines []diff.Line, syms []lineSym, op diff.Op) (d *decl, header, ok bool) {
	for k, l := range lines {
		if l.Op != op {
			continue
		}
		if syms[k].d == nil || (d != nil && syms[k].d != d) {
			return nil, false, false
		}
		d = syms[k].d
		row := l.NewNo - 1
		if op == diff.Del {
			row = l.OldNo - 1
		}
		if row == d.sigRow {
			header = true
		}
	}
	return d, header, d != nil
}
