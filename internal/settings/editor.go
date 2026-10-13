package settings

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
)

// doc is a TOML file held as lines so single values can be replaced without
// touching anything else (DECISIONS "M5.1 settings page and command line":
// a line-level editor, no new dependency). It understands just enough TOML
// to find key/value lines: table and array-table headers, bare and quoted
// (dotted) keys, multi-line arrays and inline tables, all four string kinds
// (multi-line ones included) and comments. Every line keeps its own line
// ending, so CRLF, LF and a missing final newline survive byte for byte.
type doc struct {
	lines []string // line text without its terminator
	eols  []string // each line's terminator: "\n", "\r\n" or "" (last line)
	eol   string   // terminator for new lines (the file's first one)

	entries []entry // key/value lines, rebuilt after every edit
	headers []header
}

// entry is one `key = value` statement.
type entry struct {
	path      []string // table path + dotted key parts
	inArray   bool     // under a [[...]] header: never edited
	start     int      // first line
	end       int      // last line (multi-line values)
	valStart  int      // value's first byte in lines[start]
	valEnd    int      // byte after the value in lines[end]
	indent    string   // leading whitespace of the key line
	headerIdx int      // index into headers of the enclosing table, -1 top level
}

// header is a table or array-table header line.
type header struct {
	path  []string
	array bool
	line  int
}

// parseDoc splits text into lines and indexes its keys. It fails only on
// syntax it cannot step over (an unterminated string or array, a key line
// without "="), so a value is never edited in a misread file.
func parseDoc(text string) (*doc, error) {
	d := &doc{eol: "\n"}
	if i := strings.IndexByte(text, '\n'); i > 0 && text[i-1] == '\r' {
		d.eol = "\r\n"
	}
	for text != "" {
		line, rest, found := strings.Cut(text, "\n")
		eol := ""
		if found {
			eol = "\n"
			if strings.HasSuffix(line, "\r") {
				line, eol = line[:len(line)-1], "\r\n"
			}
		}
		d.lines = append(d.lines, line)
		d.eols = append(d.eols, eol)
		text = rest
	}
	return d, d.index()
}

// String renders the document.
func (d *doc) String() string {
	var b strings.Builder
	for i, l := range d.lines {
		b.WriteString(l)
		b.WriteString(d.eols[i])
	}
	return b.String()
}

// index rebuilds entries and headers from lines.
func (d *doc) index() error {
	d.entries, d.headers = nil, nil
	table, array, hdr := []string(nil), false, -1
	for i := 0; i < len(d.lines); i++ {
		line := d.lines[i]
		body := strings.TrimLeft(line, " \t")
		switch {
		case body == "" || body[0] == '#':
			continue
		case body[0] == '[':
			path, isArray, err := parseHeader(body)
			if err != nil {
				return fmt.Errorf("line %d: %w", i+1, err)
			}
			d.headers = append(d.headers, header{path: path, array: isArray, line: i})
			table, array, hdr = path, isArray, len(d.headers)-1
		default:
			e, err := d.parseEntry(i, len(line)-len(body))
			if err != nil {
				return fmt.Errorf("line %d: %w", i+1, err)
			}
			e.path = append(slices.Clone(table), e.path...)
			e.inArray, e.headerIdx = array, hdr
			d.entries = append(d.entries, e)
			i = e.end
		}
	}
	return nil
}

// parseHeader reads "[a.b]" or "[[a.b]]" (a trailing comment allowed).
func parseHeader(s string) ([]string, bool, error) {
	array := strings.HasPrefix(s, "[[")
	open := 1
	if array {
		open = 2
	}
	parts, n, err := parseKey(s[open:])
	if err != nil {
		return nil, false, err
	}
	rest := strings.TrimLeft(s[open+n:], " \t")
	closing := strings.Repeat("]", open)
	if !strings.HasPrefix(rest, closing) {
		return nil, false, fmt.Errorf("bad table header %q", s)
	}
	return parts, array, nil
}

// parseEntry reads the key/value statement starting at line i, column col.
func (d *doc) parseEntry(i, col int) (entry, error) {
	line := d.lines[i]
	parts, n, err := parseKey(line[col:])
	if err != nil {
		return entry{}, err
	}
	p := col + n
	for p < len(line) && (line[p] == ' ' || line[p] == '\t') {
		p++
	}
	if p >= len(line) || line[p] != '=' {
		return entry{}, fmt.Errorf("expected \"=\" after key %q", strings.Join(parts, "."))
	}
	p++
	for p < len(line) && (line[p] == ' ' || line[p] == '\t') {
		p++
	}
	end, endCol, err := d.scanValue(i, p)
	if err != nil {
		return entry{}, fmt.Errorf("key %q: %w", strings.Join(parts, "."), err)
	}
	return entry{path: parts, start: i, end: end, valStart: p, valEnd: endCol, indent: line[:col]}, nil
}

// parseKey reads a (dotted) key from the start of s, skipping surrounding
// whitespace, and returns its unquoted parts and the bytes consumed.
func parseKey(s string) ([]string, int, error) {
	var parts []string
	p := 0
	for {
		for p < len(s) && (s[p] == ' ' || s[p] == '\t') {
			p++
		}
		if p >= len(s) {
			return nil, 0, fmt.Errorf("missing key")
		}
		part, n, err := parseKeyPart(s[p:])
		if err != nil {
			return nil, 0, err
		}
		parts = append(parts, part)
		p += n
		q := p
		for q < len(s) && (s[q] == ' ' || s[q] == '\t') {
			q++
		}
		if q >= len(s) || s[q] != '.' {
			return parts, p, nil
		}
		p = q + 1
	}
}

var bareKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+`)

// parseKeyPart reads one bare, "basic" or 'literal' key.
func parseKeyPart(s string) (string, int, error) {
	switch s[0] {
	case '"':
		end := closingQuote(s, 1)
		if end < 0 {
			return "", 0, fmt.Errorf("unterminated quoted key")
		}
		v, err := strconv.Unquote(s[:end+1])
		if err != nil {
			// TOML escapes are a subset of Go's except \e and \x; keep the raw
			// text so the key still compares with itself.
			v = s[1:end]
		}
		return v, end + 1, nil
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return "", 0, fmt.Errorf("unterminated quoted key")
		}
		return s[1 : end+1], end + 2, nil
	}
	m := bareKeyRe.FindString(s)
	if m == "" {
		return "", 0, fmt.Errorf("bad key at %q", s)
	}
	return m, len(m), nil
}

// closingQuote returns the index of the '"' closing a basic string whose
// body starts at s[from], or -1.
func closingQuote(s string, from int) int {
	for p := from; p < len(s); p++ {
		switch s[p] {
		case '\\':
			p++
		case '"':
			return p
		}
	}
	return -1
}

// scanValue finds the end of the value that starts at line i, column c: it
// returns the last line of the value and the byte after the value on it
// (before trailing whitespace and comment).
func (d *doc) scanValue(i, c int) (int, int, error) {
	depth := 0
	for i < len(d.lines) {
		line := d.lines[i]
		for c < len(line) {
			ch := line[c]
			switch {
			case strings.HasPrefix(line[c:], `"""`) || strings.HasPrefix(line[c:], `'''`):
				ni, nc, err := d.scanMultiline(i, c)
				if err != nil {
					return 0, 0, err
				}
				i, c, line = ni, nc, d.lines[ni]
			case ch == '"':
				end := closingQuote(line, c+1)
				if end < 0 {
					return 0, 0, fmt.Errorf("unterminated string")
				}
				c = end + 1
			case ch == '\'':
				end := strings.IndexByte(line[c+1:], '\'')
				if end < 0 {
					return 0, 0, fmt.Errorf("unterminated string")
				}
				c += end + 2
			case ch == '[' || ch == '{':
				depth++
				c++
				continue
			case ch == ']' || ch == '}':
				depth--
				c++
			case ch == '#':
				if depth == 0 {
					return i, trimRightSpace(line, c), nil
				}
				c = len(line) // comment inside a multi-line array
				continue
			default:
				c++
				continue
			}
			if depth <= 0 {
				return i, c, nil
			}
		}
		if depth <= 0 {
			return i, trimRightSpace(line, c), nil
		}
		i, c = i+1, 0
	}
	return 0, 0, fmt.Errorf("unterminated array or inline table")
}

// scanMultiline skips a """ or ”' string starting at line i, column c and
// returns the position after its closing delimiter.
func (d *doc) scanMultiline(i, c int) (int, int, error) {
	delim := d.lines[i][c : c+3]
	c += 3
	for i < len(d.lines) {
		line := d.lines[i]
		for c < len(line) {
			if delim == `"""` && line[c] == '\\' {
				c += 2
				continue
			}
			if strings.HasPrefix(line[c:], delim) {
				c += 3
				for c < len(line) && line[c] == delim[0] { // up to two quotes may end the body
					c++
				}
				return i, c, nil
			}
			c++
		}
		i, c = i+1, 0
	}
	return 0, 0, fmt.Errorf("unterminated multi-line string")
}

func trimRightSpace(line string, end int) int {
	for end > 0 && (line[end-1] == ' ' || line[end-1] == '\t') {
		end--
	}
	return end
}

// set gives table.key the value lit (a TOML literal; an existing value
// that decodes to the same thing is left as written). An existing
// key is replaced in place, keeping the line's indentation and trailing
// comment; a missing key is appended after the table's last key; a missing
// table is appended to the file. Table "" means the top level (before the
// first header). Keys under array tables are never touched.
func (d *doc) set(table, key, lit string) error {
	path := []string{key}
	if table != "" {
		path = []string{table, key}
	}
	if e, ok := d.find(path); ok {
		return d.replace(e, lit)
	}
	d.insert(table, key, lit)
	return d.index()
}

// find returns the first non-array-table entry with this full path.
func (d *doc) find(path []string) (entry, bool) {
	for _, e := range d.entries {
		if !e.inArray && slices.Equal(e.path, path) {
			return e, true
		}
	}
	return entry{}, false
}

// replace swaps e's value text for lit unless both decode to the same value.
func (d *doc) replace(e entry, lit string) error {
	old := d.valueText(e)
	if sameValue(old, lit) {
		return nil
	}
	nl := d.lines[e.start][:e.valStart] + lit + d.lines[e.end][e.valEnd:]
	eol := d.eols[e.end]
	d.lines = slices.Replace(d.lines, e.start, e.end+1, nl)
	d.eols = slices.Replace(d.eols, e.start, e.end+1, eol)
	return d.index()
}

// valueText returns e's value as written (multi-line values joined by \n).
func (d *doc) valueText(e entry) string {
	if e.start == e.end {
		return d.lines[e.start][e.valStart:e.valEnd]
	}
	parts := []string{d.lines[e.start][e.valStart:]}
	parts = append(parts, d.lines[e.start+1:e.end]...)
	parts = append(parts, d.lines[e.end][:e.valEnd])
	return strings.Join(parts, "\n")
}

// sameValue reports whether two value texts decode to equal values.
func sameValue(a, b string) bool {
	if a == b {
		return true
	}
	var va, vb map[string]any
	if _, err := toml.Decode("v = "+a+"\n", &va); err != nil {
		return false
	}
	if _, err := toml.Decode("v = "+b+"\n", &vb); err != nil {
		return false
	}
	return reflect.DeepEqual(va, vb)
}

// insert adds `key = lit` to table, creating the table when missing.
func (d *doc) insert(table, key, lit string) {
	kv := tomlKey(key) + " = " + lit
	if table == "" {
		d.insertTopLevel(kv)
		return
	}
	for hi, h := range d.headers {
		if h.array || !slices.Equal(h.path, []string{table}) {
			continue
		}
		at, indent := h.line+1, ""
		for _, e := range d.entries {
			if e.headerIdx == hi {
				at, indent = e.end+1, e.indent
			}
		}
		d.insertLines(at, indent+kv)
		return
	}
	d.appendLines("["+tomlKey(table)+"]", kv)
}

// insertTopLevel adds a top-level key after the last top-level key, else
// before the first header (and the comment block attached to it), else at
// the end of the file.
func (d *doc) insertTopLevel(kv string) {
	at, indent := -1, ""
	for _, e := range d.entries {
		if e.headerIdx == -1 {
			at, indent = e.end+1, e.indent
		}
	}
	if at >= 0 {
		d.insertLines(at, indent+kv)
		return
	}
	if len(d.headers) == 0 {
		d.appendLines(kv)
		return
	}
	at = d.headers[0].line
	for at > 0 && strings.HasPrefix(strings.TrimLeft(d.lines[at-1], " \t"), "#") {
		at--
	}
	d.insertLines(at, kv, "")
}

// insertLines inserts new lines before index at.
func (d *doc) insertLines(at int, lines ...string) {
	if at > 0 && d.eols[at-1] == "" {
		d.eols[at-1] = d.eol
	}
	eols := make([]string, len(lines))
	for i := range eols {
		eols[i] = d.eol
	}
	d.lines = slices.Insert(d.lines, at, lines...)
	d.eols = slices.Insert(d.eols, at, eols...)
}

// appendLines appends lines at the end, separated from non-blank content
// by one blank line.
func (d *doc) appendLines(lines ...string) {
	if n := len(d.lines); n > 0 && strings.TrimSpace(d.lines[n-1]) != "" {
		lines = append([]string{""}, lines...)
	}
	d.insertLines(len(d.lines), lines...)
}

// tomlKey renders a key, quoting it unless it is bare.
func tomlKey(k string) string {
	if k != "" && bareKeyRe.FindString(k) == k {
		return k
	}
	return tomlString(k)
}

// tomlString renders s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlFloat renders f so TOML reads it back as the same float (always with
// a decimal point). f must be finite (Validate).
func tomlFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

// tomlInt renders an integer.
func tomlInt(n int) string { return strconv.Itoa(n) }

// tomlStrings renders a one-line string array.
func tomlStrings(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = tomlString(x)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// validText reports whether s can be written as a TOML string.
func validText(s string) bool { return utf8.ValidString(s) }

// finite reports whether f is neither NaN nor infinite.
func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
