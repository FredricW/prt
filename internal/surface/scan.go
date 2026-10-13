package surface

import "strings"

// Bounds on the work done per item, so a huge diff or a minified file
// cannot stall stage 8.
const (
	maxUnitLines = 4000 // diff lines scanned per unit
	maxLineLen   = 1000 // longer lines (minified code, data blobs) are not scanned
	maxUnits     = 5000 // units scanned per item
)

// span is a byte range [start, end) of a line.
type span struct{ start, end int }

// line is one scanned source line of one side (old or new) of a unit or of
// a whole file.
type line struct {
	no   int    // 1-based line number on its side
	op   byte   // '+' added, '-' removed, ' ' context (or a whole-file line)
	text string // raw text, without the diff op
	// code is text with comments blanked out (same byte length, so offsets
	// match text); "" for a line too long to scan.
	code string
	strs []span // string literals, quotes included
}

// inString reports whether byte i of the line lies inside a string literal.
func (l line) inString(i int) bool {
	for _, s := range l.strs {
		if i >= s.start && i < s.end {
			return true
		}
	}
	return false
}

// strContents returns the contents of the line's string literals, quotes
// stripped (a literal that continues on the next line keeps its tail).
func (l line) strContents() []string {
	out := make([]string, 0, len(l.strs))
	for _, s := range l.strs {
		t := l.text[s.start:s.end]
		t = strings.Trim(t, "\"'`")
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// maskStrings returns the line's code with the contents of its string
// literals blanked (quotes kept), so keywords inside strings do not match.
func maskStrings(l line) string {
	if len(l.strs) == 0 {
		return l.code
	}
	b := []byte(l.code)
	for _, s := range l.strs {
		blank(b, s.start+1, s.end-1)
	}
	return string(b)
}

// lexer blanks comments and finds string literals line by line, carrying
// block comments and multi-line strings from one line to the next. It is a
// small approximation per language, good enough to keep a commented-out
// route or a route inside a string from matching.
type lexer struct {
	lang  string
	block bool   // inside a /* */ comment
	open  string // delimiter of a string still open at the end of the last line
}

// scanLine returns a scanned line.
func scanLine(lx *lexer, no int, op byte, text string) line {
	text = strings.TrimSuffix(text, "\r")
	code, strs := lx.scan(text)
	return line{no: no, op: op, text: text, code: code, strs: strs}
}

// scan blanks comments in text and returns it with its string literals.
func (lx *lexer) scan(text string) (string, []span) {
	if len(text) > maxLineLen {
		lx.block, lx.open = false, ""
		return "", nil
	}
	b := []byte(text)
	var strs []span
	i := 0
	if lx.open != "" {
		end := closeString(text, 0, lx.open, lx.lang)
		if end < 0 {
			return text, []span{{0, len(b)}}
		}
		strs = append(strs, span{0, end})
		lx.open, i = "", end
	}
	for i < len(b) {
		if lx.block {
			j := strings.Index(text[i:], "*/")
			if j < 0 {
				blank(b, i, len(b))
				return string(b), strs
			}
			blank(b, i, i+j+2)
			i, lx.block = i+j+2, false
			continue
		}
		switch {
		case lineComment(text, i, lx.lang):
			blank(b, i, len(b))
			return string(b), strs
		case blockComment(text, i, lx.lang):
			blank(b, i, i+2)
			i, lx.block = i+2, true
			continue
		}
		if delim := quoteAt(text, i, lx.lang); delim != "" {
			end := closeString(text, i+len(delim), delim, lx.lang)
			if end < 0 {
				if multiLine(delim, lx.lang) {
					lx.open = delim
				}
				strs = append(strs, span{i, len(b)})
				return string(b), strs
			}
			strs = append(strs, span{i, end})
			i = end
			continue
		}
		i++
	}
	return string(b), strs
}

func blank(b []byte, from, to int) {
	for k := from; k < to && k < len(b); k++ {
		b[k] = ' '
	}
}

// lineComment reports whether a line comment starts at text[i].
func lineComment(text string, i int, lang string) bool {
	switch lang {
	case langGo, langJS, langPrisma:
		return strings.HasPrefix(text[i:], "//")
	case langPy, langRuby, langEnv:
		return text[i] == '#'
	case langSQL:
		return strings.HasPrefix(text[i:], "--")
	}
	return false
}

// blockComment reports whether a block comment starts at text[i].
func blockComment(text string, i int, lang string) bool {
	switch lang {
	case langGo, langJS, langSQL:
		return strings.HasPrefix(text[i:], "/*")
	}
	return false
}

// quoteAt returns the string delimiter starting at text[i], or "".
func quoteAt(text string, i int, lang string) string {
	c := text[i]
	switch lang {
	case langGo, langJS:
		if c == '"' || c == '\'' || c == '`' {
			return string(c)
		}
	case langPy:
		if strings.HasPrefix(text[i:], `"""`) || strings.HasPrefix(text[i:], `'''`) {
			return text[i : i+3]
		}
		if c == '"' || c == '\'' {
			return string(c)
		}
	case langRuby, langEnv:
		if c == '"' || c == '\'' {
			return string(c)
		}
	case langSQL:
		// Double quotes delimit identifiers in SQL, not strings.
		if c == '\'' {
			return "'"
		}
	case langPrisma:
		if c == '"' {
			return `"`
		}
	}
	return ""
}

// multiLine reports whether a string opened with delim may span lines.
func multiLine(delim, lang string) bool {
	switch {
	case delim == "`":
		return lang == langGo || lang == langJS
	case len(delim) == 3:
		return true
	}
	return false
}

// closeString returns the offset just past the delimiter closing a string
// that starts at text[from:], or -1 when the string does not close on this
// line. Backslash escapes count except in Go raw strings; SQL doubles its
// quotes.
func closeString(text string, from int, delim, lang string) int {
	escapes := (delim != "`" || lang != langGo) && lang != langSQL
	for k := from; k < len(text); k++ {
		switch {
		case escapes && text[k] == '\\':
			k++
		case strings.HasPrefix(text[k:], delim):
			if lang == langSQL && strings.HasPrefix(text[k+1:], "'") {
				k++
				continue
			}
			return k + len(delim)
		}
	}
	return -1
}

// splitOp splits a unified diff line into its op and text. Hunk headers and
// "\ No newline" markers return op 0; anything else that is not "+" or "-"
// counts as context (an empty line included).
func splitOp(raw string) (byte, string) {
	switch {
	case strings.HasPrefix(raw, "@@"), strings.HasPrefix(raw, `\`):
		return 0, ""
	case raw == "":
		return ' ', ""
	}
	switch raw[0] {
	case '+', '-', ' ':
		return raw[0], raw[1:]
	}
	return ' ', raw
}
