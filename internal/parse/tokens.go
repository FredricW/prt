package parse

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/model"
)

// tokKind classifies a token for shape normalisation.
type tokKind uint8

const (
	tokOther   tokKind = iota // keywords, punctuation, numbers: kept verbatim
	tokIdent                  // identifiers: renamed $1, $2, … in shapes
	tokString                 // string/template/regex literals: $s in shapes
	tokComment                // comments: $c in shapes
)

// token is one lexical token of a source line.
type token struct {
	kind tokKind
	text string // verbatim for strings; whitespace-collapsed otherwise
}

// newToken builds a token from raw text; ok is false for blank text.
func newToken(kind tokKind, raw string) (token, bool) {
	raw = strings.TrimSuffix(raw, "\r")
	if kind == tokString {
		if strings.TrimSpace(raw) == "" {
			return token{}, false
		}
		return token{kind: kind, text: raw}, true
	}
	text := strings.Join(strings.Fields(raw), " ")
	if text == "" {
		return token{}, false
	}
	return token{kind: kind, text: text}, true
}

// scanLine is the fallback tokenizer for lines without a parse tree:
// identifiers, numbers, quoted strings, line comments (for known languages)
// and single-rune punctuation.
func scanLine(s, lang string) []token {
	var out []token
	cLike := lang == "go" || lang == "ts" || lang == "tsx" || lang == "js"
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		rest := s[i:]
		switch {
		case unicode.IsSpace(r):
			i += size
		case cLike && strings.HasPrefix(rest, "//"), lang == "python" && r == '#':
			out = appendTok(out, tokComment, rest)
			i = len(s)
		case cLike && strings.HasPrefix(rest, "/*"):
			end := len(s)
			if j := strings.Index(rest[2:], "*/"); j >= 0 {
				end = i + 2 + j + 2
			}
			out = appendTok(out, tokComment, s[i:end])
			i = end
		case r == '"' || r == '\'' || r == '`':
			end := closeQuote(s, i)
			out = appendTok(out, tokString, s[i:end])
			i = end
		case r == '_' || r == '$' || unicode.IsLetter(r):
			end := scanWhile(s, i, func(r rune) bool {
				return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
			})
			out = appendTok(out, tokIdent, s[i:end])
			i = end
		case unicode.IsDigit(r):
			end := scanWhile(s, i, func(r rune) bool {
				return r == '_' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r)
			})
			out = appendTok(out, tokOther, s[i:end])
			i = end
		default:
			out = appendTok(out, tokOther, s[i:i+size])
			i += size
		}
	}
	return out
}

func appendTok(out []token, kind tokKind, raw string) []token {
	if t, ok := newToken(kind, raw); ok {
		out = append(out, t)
	}
	return out
}

// closeQuote returns the index just past the string starting at s[i], or
// len(s) when it is not closed on this line.
func closeQuote(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case q:
			return j + 1
		}
	}
	return len(s)
}

func scanWhile(s string, i int, ok func(rune) bool) int {
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !ok(r) {
			break
		}
		i += size
	}
	return i
}

// changedLine is one +/- line of a unit with its tokens. block numbers the
// runs of adjacent +/- lines in the unit (a context line ends a run).
type changedLine struct {
	line  diff.Line
	toks  []token
	block int
}

// shapeHash hashes the unit's changed lines with identifiers renamed by
// first appearance, literals and comments replaced by placeholders, and
// whitespace dropped (DECISIONS: "Shape hash normalisation"). Lines with no
// tokens (blank lines) are left out.
func shapeHash(lines []changedLine) string {
	names := map[string]string{}
	var b strings.Builder
	for _, cl := range lines {
		if len(cl.toks) == 0 {
			continue
		}
		b.WriteByte(byte(cl.line.Op))
		for _, t := range cl.toks {
			b.WriteByte(' ')
			b.WriteString(shapeText(t, names))
		}
		b.WriteByte('\n')
	}
	return model.ContentHash("shape", b.String())
}

func shapeText(t token, names map[string]string) string {
	switch t.kind {
	case tokIdent:
		n, ok := names[t.text]
		if !ok {
			n = "$" + strconv.Itoa(len(names)+1)
			names[t.text] = n
		}
		return n
	case tokString:
		return "$s"
	case tokComment:
		return "$c"
	}
	return t.text
}

// isFormat reports a layout-only change: within every run of adjacent +/-
// lines (context lines separate runs), the deleted and added lines have equal
// token streams, and at least one run changes tokens at all. Comparing run
// by run means code moved past a context line (a reordered statement) is
// never formatting. Comments are tokens, so a comment edit is not
// formatting. In Python, where indentation is syntax, every non-blank line
// must keep its indentation and its tokens (no reflow across lines). In
// JS/TS an added line ending in a restricted keyword (return, throw, …) is
// never formatting, since automatic semicolon insertion changes its meaning.
func isFormat(lines []changedLine, lang string) bool {
	var blocks [][2][]changedLine // per run: dels, adds
	for _, cl := range lines {
		for len(blocks) <= cl.block {
			blocks = append(blocks, [2][]changedLine{})
		}
		if len(cl.toks) == 0 {
			continue
		}
		side := 1
		if cl.line.Op == diff.Del {
			side = 0
		}
		blocks[cl.block][side] = append(blocks[cl.block][side], cl)
		if side == 1 && asiHazard(cl.toks, lang) {
			return false
		}
	}
	relaid := false
	for _, b := range blocks {
		dels, adds := b[0], b[1]
		switch {
		case len(dels) == 0 && len(adds) == 0:
			continue // blank lines only
		case len(dels) == 0 || len(adds) == 0:
			return false
		case lang == "python" && !sameLines(dels, adds):
			return false
		case streamText(dels) != streamText(adds):
			return false
		}
		relaid = true
	}
	return relaid
}

// asiHazard reports a JS/TS line whose last token is a keyword after which
// a line break ends the statement (return, throw, break, continue, yield).
func asiHazard(toks []token, lang string) bool {
	if lang != "js" && lang != "ts" && lang != "tsx" || len(toks) == 0 {
		return false
	}
	switch toks[len(toks)-1].text {
	case "return", "throw", "break", "continue", "yield":
		return true
	}
	return false
}

func streamText(lines []changedLine) string {
	var b strings.Builder
	for _, cl := range lines {
		for _, t := range cl.toks {
			b.WriteString(t.text)
			b.WriteByte(0)
		}
	}
	return b.String()
}

func sameLines(dels, adds []changedLine) bool {
	if len(dels) != len(adds) {
		return false
	}
	for i := range dels {
		if indent(dels[i].line.Text) != indent(adds[i].line.Text) ||
			streamText(dels[i:i+1]) != streamText(adds[i:i+1]) {
			return false
		}
	}
	return true
}

func indent(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}
