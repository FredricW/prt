package surface

import (
	"regexp"
	"strings"
)

// Go registration patterns, matched on comment-free code. Each match starts
// at the "." before the method, so the receiver is read backwards from it.
var (
	reGoHandle       = regexp.MustCompile(`\.(Handle|HandleFunc)\(\s*"([^"]*)"\s*,\s*([\w.]*)`)
	reGoMethodHandle = regexp.MustCompile(`\.(Handle|Add|Method|MethodFunc)\(\s*"([A-Za-z]+)"\s*,\s*"(/[^"]*)"\s*,\s*([\w.]*)`)
	reGoTitle        = regexp.MustCompile(`\.(Get|Post|Put|Patch|Delete|Head|Options|Connect|Trace|All)\(\s*"(/[^"]*)"\s*,\s*([\w.]*)`)
	reGoUpper        = regexp.MustCompile(`\.(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|CONNECT|TRACE|Any)\(\s*"(/[^"]*)"\s*,\s*([\w.]*)`)
	reGoMount        = regexp.MustCompile(`\.Mount\(\s*"([^"]*)"\s*,\s*([\w.]+)(\(?)`)
	reGoMethods      = regexp.MustCompile(`\.Methods\(([^)]*)\)`)
	reQuoted         = regexp.MustCompile(`"([^"]*)"|'([^']*)'`)

	reGoFunc     = regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_]\w*)`)
	reGoCtor     = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*:?=\s*(chi\.NewRouter|chi\.NewMux|gin\.New|gin\.Default|echo\.New|fiber\.New|mux\.NewRouter|http\.NewServeMux)\(`)
	reGoGroup    = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*:?=\s*([A-Za-z_]\w*)\.Group\(\s*"([^"]*)"`)
	reGoSubRtr   = regexp.MustCompile(`\b([A-Za-z_]\w*)\s*:?=\s*([A-Za-z_]\w*)\.PathPrefix\(\s*"([^"]*)"\s*\)\.Subrouter\(`)
	reGoParam    = regexp.MustCompile(`([A-Za-z_]\w*)\s+\*?(chi\.Router|chi\.Mux|gin\.IRouter|gin\.IRoutes|gin\.RouterGroup|gin\.Engine|echo\.Echo|echo\.Group|fiber\.Router|fiber\.App|mux\.Router|http\.ServeMux)\b`)
	reChiScope   = regexp.MustCompile(`\.(Route|Group)\(\s*(?:"([^"]*)"\s*,\s*)?func\s*\(\s*([A-Za-z_]\w*)\s+chi\.Router\s*\)`)
	reGoImport1  = regexp.MustCompile(`^import\s+(?:([A-Za-z_]\w*|\.)\s+)?"([^"]+)"`)
	reGoImportIn = regexp.MustCompile(`^\s*(?:([A-Za-z_]\w*|\.)\s+)?"([^"]+)"`)
	reGoPass     = regexp.MustCompile(`\b([A-Za-z_]\w*)\.([A-Za-z_]\w*)\(\s*([A-Za-z_]\w*)(?:\.Group\(\s*"([^"]*)"\s*\))?\s*[,)]`)
	reGoMountPkg = regexp.MustCompile(`\.Mount\(\s*"([^"]*)"\s*,\s*([A-Za-z_]\w*)\.([A-Za-z_]\w*)\(`)
	reVersionEl  = regexp.MustCompile(`^v\d+$`)
)

// goCtorKinds maps router constructors to their framework and whether the
// router they make is a root (nothing mounts it).
var goCtorKinds = map[string]struct {
	fw   string
	root bool
}{
	"chi.NewRouter": {fwChi, false}, "chi.NewMux": {fwChi, false},
	"gin.New": {fwGin, true}, "gin.Default": {fwGin, true},
	"echo.New": {fwEcho, true}, "fiber.New": {fwFiber, true},
	"mux.NewRouter": {fwGorilla, true}, "http.NewServeMux": {fwNetHTTP, true},
}

// goParamKinds maps router parameter types to their framework and root-ness.
var goParamKinds = map[string]struct {
	fw   string
	root bool
}{
	"chi.Router": {fwChi, false}, "chi.Mux": {fwChi, false},
	"gin.IRouter": {fwGin, false}, "gin.IRoutes": {fwGin, false}, "gin.RouterGroup": {fwGin, false}, "gin.Engine": {fwGin, true},
	"echo.Echo": {fwEcho, true}, "echo.Group": {fwEcho, false},
	"fiber.Router": {fwFiber, false}, "fiber.App": {fwFiber, true},
	"mux.Router": {fwGorilla, false}, "http.ServeMux": {fwNetHTTP, true},
}

// goImportFws maps import path fragments to router frameworks.
var goImportFws = map[string]string{
	"go-chi/chi": fwChi, "gin-gonic/gin": fwGin, "labstack/echo": fwEcho,
	"gofiber/fiber": fwFiber, "gorilla/mux": fwGorilla,
}

// goVar is a router variable, parameter or locally mounted router.
type goVar struct {
	idx    int    // defining line index
	fn     string // enclosing function
	name   string
	fw     string
	root   bool
	parent string // Group/PathPrefix/Mount receiver ("" for constructors and params)
	prefix string // Group/PathPrefix path, or the local mount's full prefix
}

// goInfo is what a Go doc says about routers.
type goInfo struct {
	fn      []string // enclosing top-level function per line index ("" unknown)
	scope   []string // composed chi Route prefix per line index
	vars    []goVar  // definitions in line order
	mounts  []goVar  // local chi mounts of router variables
	imports map[string]string
	fws     map[string]bool
}

// goRouter is a receiver's resolution.
type goRouter struct {
	fw     string
	root   bool
	known  bool
	prefix string // Group/mount prefixes between the receiver and its base router
}

// goInfo analyses d's lines once: functions, chi scopes, router variables
// and imports.
func (d *doc) goInfo() *goInfo {
	if d.gi != nil {
		return d.gi
	}
	gi := &goInfo{fn: make([]string, len(d.lines)), scope: make([]string, len(d.lines)),
		imports: map[string]string{}, fws: map[string]bool{}}
	d.gi = gi
	type scopeEnt struct {
		prefix string
		level  int
	}
	var stack []scopeEnt
	cur, depth, inImport := symbolName(d.sym), 0, false
	for i, l := range d.lines {
		c := l.code
		inImport = gi.importLine(c, inImport)
		if strings.HasPrefix(c, "func") {
			if m := reGoFunc.FindStringSubmatch(c); m != nil {
				cur, depth, stack = m[1], 0, nil
				gi.params(i, cur, c)
			}
		}
		for len(stack) > 0 && stack[len(stack)-1].level > depth {
			stack = stack[:len(stack)-1]
		}
		gi.fn[i] = cur
		if len(stack) > 0 {
			parts := make([]string, 0, len(stack))
			for _, e := range stack {
				parts = append(parts, e.prefix)
			}
			gi.scope[i] = joinAll(parts)
		}
		gi.defs(i, cur, l)
		if strings.Contains(c, "chi.Router") {
			if m := reChiScope.FindStringSubmatchIndex(c); m != nil && !l.inString(m[0]) {
				stack = append(stack, scopeEnt{prefix: sub(c, m, 2), level: depth + 1})
				gi.vars = append(gi.vars, goVar{idx: i, fn: cur, name: sub(c, m, 3), fw: fwChi, root: isMainFunc(cur)})
			}
		}
		depth += braceDelta(l)
	}
	return gi
}

// sub returns submatch k of an index match, "" when it did not take part.
func sub(s string, m []int, k int) string {
	if 2*k+1 >= len(m) || m[2*k] < 0 {
		return ""
	}
	return s[m[2*k]:m[2*k+1]]
}

func joinAll(parts []string) string {
	out := ""
	for _, p := range parts {
		out = joinPath(out, p)
	}
	return out
}

func isMainFunc(fn string) bool { return fn == "main" || fn == "init" }

// importLine records an import on line c and returns whether an import
// block is still open.
func (gi *goInfo) importLine(c string, in bool) bool {
	switch {
	case in && strings.HasPrefix(strings.TrimSpace(c), ")"):
		return false
	case in:
		if m := reGoImportIn.FindStringSubmatch(c); m != nil {
			gi.addImport(m[1], m[2])
		}
		return true
	case strings.HasPrefix(c, "import ("), strings.HasPrefix(c, "import("):
		return true
	case !strings.HasPrefix(c, "import"):
		return false
	}
	if m := reGoImport1.FindStringSubmatch(c); m != nil {
		gi.addImport(m[1], m[2])
	}
	return false
}

func (gi *goInfo) addImport(alias, p string) {
	name := alias
	if name == "" || name == "." || name == "_" {
		name = goPkgName(p)
	}
	gi.imports[name] = p
	for frag, fw := range goImportFws {
		if strings.Contains(p, frag) {
			gi.fws[fw] = true
		}
	}
}

// goPkgName is the default package name of an import path
// ("github.com/go-chi/chi/v5" → "chi", "gopkg.in/yaml.v3" → "yaml").
func goPkgName(p string) string {
	els := strings.Split(p, "/")
	name := els[len(els)-1]
	if reVersionEl.MatchString(name) && len(els) > 1 {
		name = els[len(els)-2]
	}
	if i := strings.Index(name, ".v"); i > 0 {
		name = name[:i]
	}
	return name
}

// params records router-typed parameters of a function signature.
func (gi *goInfo) params(i int, fn, c string) {
	for _, m := range reGoParam.FindAllStringSubmatch(c, -1) {
		k := goParamKinds[m[2]]
		gi.vars = append(gi.vars, goVar{idx: i, fn: fn, name: m[1], fw: k.fw, root: k.root || isMainFunc(fn)})
	}
}

// defs records router variables defined on line l.
func (gi *goInfo) defs(i int, fn string, l line) {
	c := l.code
	if !strings.Contains(c, "(") || !strings.Contains(c, "=") && !strings.Contains(c, ".Mount(") {
		return
	}
	for _, m := range reGoCtor.FindAllStringSubmatchIndex(c, -1) {
		if l.inString(m[0]) {
			continue
		}
		k := goCtorKinds[sub(c, m, 2)]
		gi.vars = append(gi.vars, goVar{idx: i, fn: fn, name: sub(c, m, 1), fw: k.fw, root: k.root || isMainFunc(fn)})
	}
	for _, re := range []*regexp.Regexp{reGoGroup, reGoSubRtr} {
		for _, m := range re.FindAllStringSubmatchIndex(c, -1) {
			if !l.inString(m[0]) {
				gi.vars = append(gi.vars, goVar{idx: i, fn: fn, name: sub(c, m, 1), parent: sub(c, m, 2), prefix: sub(c, m, 3)})
			}
		}
	}
	for _, m := range reGoMount.FindAllStringSubmatchIndex(c, -1) {
		target := sub(c, m, 2)
		if l.inString(m[0]) || strings.Contains(target, ".") || sub(c, m, 3) != "" {
			continue
		}
		gi.mounts = append(gi.mounts, goVar{idx: i, fn: fn, name: target,
			parent: receiverBefore(c, m[0]), prefix: joinPath(gi.scope[i], sub(c, m, 1))})
	}
}

// chain resolves a router variable used on line i: its framework, root-ness
// and the Group/mount prefixes above it.
func (gi *goInfo) chain(name string, i, depth int) goRouter {
	if depth > maxMountDepth || i < 0 || i >= len(gi.fn) {
		return goRouter{}
	}
	fn := gi.fn[i]
	for _, m := range gi.mounts {
		if m.name == name && m.fn == fn && m.parent != name {
			up := gi.chain(m.parent, m.idx, depth+1)
			up.prefix = joinPath(up.prefix, m.prefix)
			return up
		}
	}
	v, ok := gi.lookup(fn, name, i)
	if !ok {
		return goRouter{}
	}
	if v.parent == "" {
		return goRouter{fw: v.fw, root: v.root, known: true}
	}
	up := gi.chain(v.parent, v.idx, depth+1)
	up.prefix = joinPath(up.prefix, v.prefix)
	return up
}

// lookup returns the last definition of name in function fn at or before
// line i.
func (gi *goInfo) lookup(fn, name string, i int) (goVar, bool) {
	for k := len(gi.vars) - 1; k >= 0; k-- {
		v := gi.vars[k]
		if v.name == name && v.idx <= i && (v.fn == fn || v.fn == "" || fn == "") {
			return v, true
		}
	}
	return goVar{}, false
}

// goRouterNames are receiver names taken for routers when nothing in the
// file defines them. Names as often used for maps or HTTP clients ("m",
// "api", "app", "client") are left out: a router so named is normally
// recognised by its constructor or parameter type instead.
var goRouterNames = map[string]bool{
	"r": true, "rt": true, "router": true, "mux": true, "g": true, "grp": true,
	"group": true, "rg": true, "sub": true, "subrouter": true, "e": true, "admin": true,
	"public": true, "private": true, "authed": true, "protected": true, "engine": true, "routes": true,
	"v1": true, "v2": true, "v3": true,
}

// isRouterName reports whether a receiver name reads as a router.
func isRouterName(name string) bool {
	if goRouterNames[name] {
		return true
	}
	ln := strings.ToLower(name)
	return strings.HasSuffix(ln, "router") || strings.HasSuffix(ln, "mux") || strings.HasSuffix(ln, "group") || strings.HasSuffix(ln, "routes")
}

// goMatch is one registration-shaped call on a line.
type goMatch struct {
	kind      string // whatRoute or whatMount
	call      string // the method called ("Get", "HandleFunc", "GET", "Mount")
	method    string
	methodArg bool // the method is the first argument (gin Handle, echo Add, chi Method)
	path      string
	handler   string
	at        int // offset of the "." before the call
}

// goRegHints are the literals a Go registration line contains (a cheap
// pre-filter for goMatches).
var goRegHints = []string{
	"Handle", ".Get(", ".Post(", ".Put(", ".Patch(", ".Delete(", ".Head(", ".Options(", ".Connect(", ".Trace(", ".All(",
	".GET(", ".POST(", ".PUT(", ".PATCH(", ".DELETE(", ".HEAD(", ".OPTIONS(", ".CONNECT(", ".TRACE(", ".Any(",
	".Add(", ".Method", ".Mount(",
}

// goMatches returns the registration-shaped calls on l.
func goMatches(l line) []goMatch {
	c := l.code
	if !strings.Contains(c, `"`) || !containsAny(c, goRegHints) {
		return nil
	}
	var out []goMatch
	add := func(m goMatch) {
		if !l.inString(m.at) {
			out = append(out, m)
		}
	}
	seen := map[int]bool{}
	for _, m := range reGoMethodHandle.FindAllStringSubmatchIndex(c, -1) {
		seen[m[0]] = true
		add(goMatch{kind: whatRoute, call: sub(c, m, 1), method: methodOf(sub(c, m, 2)), methodArg: true,
			path: sub(c, m, 3), handler: goHandler(sub(c, m, 4)), at: m[0]})
	}
	for _, m := range reGoHandle.FindAllStringSubmatchIndex(c, -1) {
		if seen[m[0]] {
			continue
		}
		method, p, ok := splitPattern(sub(c, m, 2))
		if !ok {
			continue
		}
		if ms := reGoMethods.FindStringSubmatch(c[m[1]:]); method == methodAny && ms != nil {
			if list := quotedList(ms[1]); len(list) > 0 {
				method = strings.Join(list, ",")
			}
		}
		add(goMatch{kind: whatRoute, call: sub(c, m, 1), method: method, path: p, handler: goHandler(sub(c, m, 3)), at: m[0]})
	}
	for _, re := range []*regexp.Regexp{reGoTitle, reGoUpper} {
		for _, m := range re.FindAllStringSubmatchIndex(c, -1) {
			add(goMatch{kind: whatRoute, call: sub(c, m, 1), method: methodOf(sub(c, m, 1)), path: sub(c, m, 2), handler: goHandler(sub(c, m, 3)), at: m[0]})
		}
	}
	for _, m := range reGoMount.FindAllStringSubmatchIndex(c, -1) {
		add(goMatch{kind: whatMount, call: "Mount", path: sub(c, m, 1), handler: sub(c, m, 2), at: m[0]})
	}
	return out
}

// splitPattern splits a net/http pattern ("GET /x", "/x", "GET example.com/x")
// into method and path; ok is false for anything that is not a pattern.
func splitPattern(p string) (method, rest string, ok bool) {
	method, rest = methodAny, strings.TrimSpace(p)
	if m, r, found := strings.Cut(rest, " "); found && httpMethods[m] {
		method, rest = m, strings.TrimSpace(r)
	}
	if i := strings.IndexByte(rest, '/'); i > 0 && !strings.ContainsAny(rest[:i], " \t") {
		rest = rest[i:] // host pattern: keep the path
	}
	return method, rest, strings.HasPrefix(rest, "/")
}

// goHandler is the handler name of a registration argument ("" for an
// inline func literal).
func goHandler(arg string) string {
	if arg == "func" {
		return ""
	}
	return arg
}

// goRegs returns the Go registrations on the changed lines ls of v's side s.
func (b *builder) goRegs(v *view, s side, ls []line) []reg {
	var (
		out []reg
		d   *doc
	)
	for _, l := range ls {
		if l.code == "" {
			continue
		}
		ms := goMatches(l)
		if len(ms) == 0 {
			continue
		}
		if d == nil {
			d = b.sideDoc(v, s)
		}
		i := d.idx(l.no)
		if i < 0 {
			continue
		}
		for _, m := range ms {
			if r, ok := d.goInfo().reg(d, i, m); ok {
				r.a = at{v: v, l: l, s: s}
				out = append(out, r)
			}
		}
	}
	return out
}

// reg builds a registration from a match on line i of d; ok is false when
// the receiver is not a router.
func (gi *goInfo) reg(d *doc, i int, m goMatch) (reg, bool) {
	recv := receiverBefore(d.lines[i].code, m.at)
	rt := gi.chain(recv, i, 0)
	isHTTP := recv == "http" && (m.call == "Handle" || m.call == "HandleFunc")
	if !rt.known && !isHTTP && !isRouterName(recv) {
		return reg{}, false
	}
	r := reg{kind: m.kind, method: m.method, handler: m.handler, conf: confRouteExact}
	if !rt.known && !isHTTP {
		r.conf = confRouteName
	}
	if m.kind == whatMount {
		r.conf = min(r.conf, confMount)
	}
	r.fw = gi.framework(m, rt, isHTTP, d.lines[i].code)
	r.local = joinPath(joinPath(gi.scope[i], rt.prefix), m.path)
	fn := gi.fn[i]
	r.rooted = rt.root || isHTTP || r.fw == fwNetHTTP || isMainFunc(fn)
	r.id = ident{lang: langGo, stem: dirBase(d.path), name: fn}
	return r, true
}

// framework names the router framework of a match.
func (gi *goInfo) framework(m goMatch, rt goRouter, isHTTP bool, code string) string {
	if isHTTP {
		return fwNetHTTP
	}
	switch m.call {
	case "Method", "MethodFunc", "Mount":
		return fwChi
	case "Add":
		return fwEcho
	}
	if rt.fw != "" {
		return rt.fw
	}
	switch m.call {
	case "Handle", "HandleFunc":
		switch {
		case m.methodArg:
			return fwGin
		case gi.fws[fwGorilla] || reGoMethods.MatchString(code):
			return fwGorilla
		case gi.fws[fwChi]:
			return fwChi
		}
		return fwNetHTTP
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "TRACE", "Any":
		if gi.fws[fwEcho] && !gi.fws[fwGin] {
			return fwEcho
		}
		return fwGin
	}
	if gi.fws[fwFiber] && !gi.fws[fwChi] {
		return fwFiber
	}
	return fwChi
}

// goBindings returns the cross-file mounts made in d: chi Mount of a package
// function, and a router passed to a package function
// (users.Register(v1.Group("/users"))).
func (d *doc) goBindings() []binding {
	gi := d.goInfo()
	// A router passed as an argument: "(" followed by a router's name.
	var passed []string
	for _, v := range gi.vars {
		passed = append(passed, "("+v.name)
	}
	var out []binding
	for i, l := range d.lines {
		c := l.code
		if c == "" {
			continue
		}
		if strings.Contains(c, ".Mount(") {
			for _, m := range reGoMountPkg.FindAllStringSubmatchIndex(c, -1) {
				if l.inString(m[0]) {
					continue
				}
				rt := gi.chain(receiverBefore(c, m[0]), i, 0)
				out = append(out, gi.binding(d, i, rt, gi.pkgOf(sub(c, m, 2)), sub(c, m, 3), sub(c, m, 1)))
			}
		}
		if len(passed) == 0 || !containsAny(c, passed) {
			continue
		}
		for _, m := range reGoPass.FindAllStringSubmatchIndex(c, -1) {
			if l.inString(m[0]) {
				continue
			}
			rt := gi.chain(sub(c, m, 3), i, 0)
			if !rt.known {
				continue
			}
			out = append(out, gi.binding(d, i, rt, gi.pkgOf(sub(c, m, 1)), sub(c, m, 2), sub(c, m, 4)))
		}
	}
	return out
}

// pkgOf is the package name a qualifier refers to: its import's package
// name, else the qualifier itself (a unit's lines rarely include the
// imports, and an alias usually is the package name).
func (gi *goInfo) pkgOf(qual string) string {
	if imp, ok := gi.imports[qual]; ok {
		return goPkgName(imp)
	}
	return qual
}

// binding is a mount of package function pkg.fn under p on line i of d.
func (gi *goInfo) binding(d *doc, i int, rt goRouter, pkg, fn, p string) binding {
	owner := gi.fn[i]
	return binding{
		target: ident{lang: langGo, stem: pkg, name: fn},
		prefix: joinPath(joinPath(gi.scope[i], rt.prefix), p),
		owner:  ident{lang: langGo, stem: dirBase(d.path), name: owner},
		rooted: rt.root || isMainFunc(owner),
		file:   d.path, line: d.lines[i].no,
	}
}
