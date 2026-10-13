// Package shape decides which change units make the same mechanical edit.
// The shape hash (set by parse) maps identifiers and literals to
// placeholders, so it alone cannot tell `"old"` → `"new"` from `"alt"` →
// `"neu"`; the edit signature adds the literal tokens that changed. Two
// units are one mechanical edit when their Keys are equal. link (mechanical
// candidates), pipeline (which candidates Jev judges) and cluster
// (verification) share this key so they agree on the classes.
package shape

import (
	"sort"
	"strings"
	"unicode"

	"github.com/fredricw/prt/internal/model"
)

// Key is a unit's mechanical class key: its shape hash plus its edit
// signature, or "" when the unit has no shape hash (it never verifies).
func Key(u model.ChangeUnit) string {
	if u.ShapeHash == "" {
		return ""
	}
	return u.ShapeHash + "\x02" + EditSig(u.Diff)
}

// EditSig is the literal token edit a unit makes: the tokens its removed
// lines have that its added lines lack, and the reverse, as sorted
// multisets joined into one string. Two units with equal shape hashes make
// the same mechanical edit only when their edit signatures are equal too:
// a rename getUser → fetchUser at two call sites with different arguments
// matches, while `"k": "old"` → `"new"` and `"k": "alt"` → `"neu"` (same
// shape, different literals) do not.
func EditSig(diff string) string {
	var old, nu []string
	for _, line := range strings.Split(diff, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case '-':
			old = append(old, tokens(line[1:])...)
		case '+':
			nu = append(nu, tokens(line[1:])...)
		}
	}
	removed, added := multisetDiff(old, nu), multisetDiff(nu, old)
	return strings.Join(removed, "\x00") + "\x01" + strings.Join(added, "\x00")
}

// tokens splits s into words (letters, digits, _ and $), quoted strings
// (kept whole, quotes included) and single other non-space runes.
func tokens(s string) []string {
	var out []string
	rs := []rune(s)
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsSpace(r):
			i++
		case isWord(r):
			j := i
			for j < len(rs) && isWord(rs[j]) {
				j++
			}
			out = append(out, string(rs[i:j]))
			i = j
		case r == '"' || r == '\'' || r == '`':
			j := i + 1
			for j < len(rs) && rs[j] != r {
				if rs[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(rs))
			out = append(out, string(rs[i:j]))
			i = j
		default:
			out = append(out, string(r))
			i++
		}
	}
	return out
}

func isWord(r rune) bool { return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// multisetDiff returns a minus b as a sorted multiset.
func multisetDiff(a, b []string) []string {
	n := make(map[string]int, len(b))
	for _, t := range b {
		n[t]++
	}
	var out []string
	for _, t := range a {
		if n[t] > 0 {
			n[t]--
			continue
		}
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
