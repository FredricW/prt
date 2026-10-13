package surface

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/fredricw/prt/internal/model"
)

// Shape confidences: a field change of a type named like a response is a
// heuristic, one found through its use with a JSON writer weaker still.
const (
	confShapeNamed = 0.7
	confShapeReq   = 0.65
	confShapeUsed  = 0.6
)

// shapeScan is how far back the enclosing type declaration is searched.
const shapeScan = 300

var (
	reGoField    = regexp.MustCompile("^\\s*([A-Za-z_]\\w*)\\s+([^\\s`]+)\\s+`([^`]*)`")
	reGoJSONTag  = regexp.MustCompile(`\bjson:"([^"]*)"`)
	reGoStruct   = regexp.MustCompile(`^\s*type\s+([A-Za-z_]\w*)\s+struct\s*\{`)
	reGoJSONUse  = regexp.MustCompile(`json\.NewEncoder|\.JSON\(|[wW]riteJSON|render\.JSON|respondJSON|json\.Marshal`)
	reTSField    = regexp.MustCompile(`^\s*(?:readonly\s+)?([A-Za-z_$][\w$]*)(\??)\s*:\s*([^;]+?)\s*[;,]?\s*$`)
	reTSType     = regexp.MustCompile(`^\s*(?:export\s+)?(?:interface\s+([A-Za-z_$][\w$]*)|type\s+([A-Za-z_$][\w$]*)\s*=\s*\{)`)
	reTSJSONUse  = regexp.MustCompile(`(?:NextResponse|Response)\.json|\bres\.(?:json|send)\(|\breply\.send\(|\bc\.json\(`)
	respSuffixes = []string{"Response", "Resp", "Reply", "Result", "Payload", "DTO", "Dto", "View", "Output", "Out"}
	reqSuffixes  = []string{"Request", "Req", "Input", "Params", "Body", "Form", "Command"}
)

// shapeField is one json field of a response or request type on one side.
type shapeField struct {
	path, typ, field string // file, type name, field name in code
	name             string // the wire name (json tag, TS property)
	sig              string // the field's type and tag, for change detection
	what             string
	conf             float64
	fw               string
	a                at
}

// detectShapes finds added, removed and changed fields of Go structs and
// server-side TS types used as API responses or requests.
func (b *builder) detectShapes() []model.SurfaceEntry {
	var fields [2][]shapeField
	for _, v := range b.live() {
		for _, s := range sides {
			switch v.f.lang {
			case langGo:
				fields[s] = append(fields[s], b.goShapes(v, s)...)
			case langJS:
				fields[s] = append(fields[s], b.tsShapes(v, s)...)
			}
		}
	}
	return pairShapes(fields[sideOld], fields[sideNew])
}

// goShapes returns the json-tagged fields changed on v's side s, in structs
// that existed on both sides.
func (b *builder) goShapes(v *view, s side) []shapeField {
	var (
		out []shapeField
		d   *doc
	)
	for _, l := range v.changedLines(s) {
		m := reGoField.FindStringSubmatch(l.code)
		if m == nil {
			continue
		}
		tag := reGoJSONTag.FindStringSubmatch(m[3])
		if tag == nil {
			continue
		}
		name, _, _ := strings.Cut(tag[1], ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = m[1]
		}
		if d == nil {
			d = b.sideDoc(v, s)
		}
		typ, hdr := enclosingType(d, d.idx(l.no), reGoStruct)
		if typ == "" || v.f.isChanged(s, hdr) {
			continue
		}
		what, conf := shapeKind(typ, d, reGoJSONUse)
		if what == "" {
			continue
		}
		out = append(out, shapeField{path: v.f.path, typ: typ, field: m[1], name: name, sig: m[2] + " `" + m[3] + "`",
			what: what, conf: conf, fw: "go", a: at{v: v, l: l, s: s}})
	}
	return out
}

// tsShapes returns the properties changed on v's side s in TS interfaces
// and object types of server modules (route files, framework modules; never
// UI or HTTP client code).
func (b *builder) tsShapes(v *view, s side) []shapeField {
	if isJSX(v.f.path) {
		return nil
	}
	var (
		out    []shapeField
		d      *doc
		server bool
	)
	for _, l := range v.changedLines(s) {
		m := reTSField.FindStringSubmatch(l.code)
		if m == nil {
			continue
		}
		if d == nil {
			d = b.sideDoc(v, s)
			_, next, _ := nextURL(v.f.path)
			ji := d.jsInfo()
			server = next || ji.server && !ji.client
		}
		if !server {
			return nil
		}
		typ, hdr := enclosingType(d, d.idx(l.no), reTSType)
		if typ == "" || v.f.isChanged(s, hdr) {
			continue
		}
		what, conf := shapeKind(typ, d, reTSJSONUse)
		if what == "" {
			continue
		}
		out = append(out, shapeField{path: v.f.path, typ: typ, field: m[1], name: m[1], sig: m[2] + m[3],
			what: what, conf: conf - 0.05, fw: "ts", a: at{v: v, l: l, s: s}})
	}
	return out
}

// enclosingType returns the name and header line number of the type
// declaration enclosing line i of d ("" when none is in reach).
func enclosingType(d *doc, i int, header *regexp.Regexp) (string, int) {
	if i < 0 {
		return "", 0
	}
	for k := i - 1; k >= 0 && k >= i-shapeScan; k-- {
		c := d.lines[k].code
		if m := header.FindStringSubmatch(c); m != nil {
			for _, g := range m[1:] {
				if g != "" {
					return g, d.lines[k].no
				}
			}
			return "", 0
		}
		if strings.HasPrefix(c, "}") || strings.HasPrefix(c, ")") || isDeclStart(c) {
			return "", 0
		}
	}
	return "", 0
}

// declStarts open a declaration that cannot sit inside a type body.
var declStarts = []string{"func ", "function ", "class ", "async function ", "export function ", "export class ",
	"export async function ", "export default "}

// isDeclStart reports whether a line opens a function or class.
func isDeclStart(c string) bool {
	for _, p := range declStarts {
		if strings.HasPrefix(c, p) {
			return true
		}
	}
	return false
}

// shapeKind says whether a type is a response or a request shape: by its
// name, else by its use with a JSON writer in the same file.
func shapeKind(typ string, d *doc, use *regexp.Regexp) (string, float64) {
	switch {
	case hasWordSuffix(typ, respSuffixes):
		return whatResponse, confShapeNamed
	case hasWordSuffix(typ, reqSuffixes):
		return whatRequest, confShapeReq
	}
	if !d.full {
		return "", 0
	}
	used, ok := d.jsonUse[typ]
	if !ok {
		for _, l := range d.lines {
			if strings.Contains(l.code, typ) && use.MatchString(l.code) {
				used = true
				break
			}
		}
		if d.jsonUse == nil {
			d.jsonUse = map[string]bool{}
		}
		d.jsonUse[typ] = used
	}
	if used {
		return whatResponse, confShapeUsed
	}
	return "", 0
}

// hasWordSuffix reports whether name ends in one of the words as a word of
// its own: CamelCase ("userResponse"), snake_case ("user_response") or the
// whole name.
func hasWordSuffix(name string, words []string) bool {
	ln := strings.ToLower(name)
	for _, w := range words {
		lw := strings.ToLower(w)
		if strings.HasSuffix(name, w) && len(name) > len(w) || ln == lw || strings.HasSuffix(ln, "_"+lw) {
			return true
		}
	}
	return false
}

// pairShapes turns the fields of both sides into entries: a field with the
// same code name on both sides is unchanged (a realignment) or changed; the
// rest are added or removed.
func pairShapes(before, after []shapeField) []model.SurfaceEntry {
	key := func(f shapeField) string { return f.path + "\x00" + f.typ + "\x00" + f.field }
	order := func(a, b shapeField) int {
		return cmp.Or(cmp.Compare(key(a), key(b)), cmp.Compare(a.a.l.no, b.a.l.no))
	}
	slices.SortStableFunc(before, order)
	slices.SortStableFunc(after, order)
	byKey := map[string][]int{}
	for i, o := range before {
		byKey[key(o)] = append(byKey[key(o)], i)
	}
	usedOld := make([]bool, len(before))
	var out []model.SurfaceEntry
	for _, n := range after {
		idx := byKey[key(n)]
		if len(idx) == 0 {
			out = append(out, shapeEntry(n, model.SurfaceAdded, ""))
			continue
		}
		byKey[key(n)] = idx[1:]
		usedOld[idx[0]] = true
		o := before[idx[0]]
		switch {
		case o.name != n.name:
			out = append(out, shapeEntry(n, model.SurfaceChanged, `was "`+o.name+`"`))
		case normSpace(o.sig) != normSpace(n.sig):
			out = append(out, shapeEntry(n, model.SurfaceChanged, "was "+clip(normSpace(o.sig), 60)))
		}
	}
	for i, o := range before {
		if !usedOld[i] {
			out = append(out, shapeEntry(o, model.SurfaceRemoved, ""))
		}
	}
	return out
}

func shapeEntry(f shapeField, change, detail string) model.SurfaceEntry {
	e := newEntry(model.SurfaceAPI, change, f.what, f.typ+"."+f.name, f.a, f.conf, f.fw)
	e.Detail = detail
	return e
}

// normSpace collapses runs of white space.
func normSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// clip shortens s to at most n bytes (on a rune boundary) with an ellipsis.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
