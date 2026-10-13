package surface

import (
	"regexp"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// JS/TS registration patterns (express, fastify, hono, koa) and module
// facts, matched on comment-free code.
var (
	reJSVerb   = regexp.MustCompile("\\.(get|post|put|patch|delete|del|all|options|head)\\(\\s*([\"'`])(/[^\"'`]*)[\"'`]")
	reJSMount  = regexp.MustCompile("\\.(use|route)\\(\\s*[\"'`](/[^\"'`]*)[\"'`]\\s*,\\s*(?:require\\(\\s*[\"']([^\"']+)[\"']\\s*\\)|([A-Za-z_$][\\w$.]*)\\s*(\\(?))")
	reJSReg    = regexp.MustCompile("\\.register\\(\\s*([A-Za-z_$][\\w$]*)\\s*,\\s*\\{[^}]*?\\bprefix\\s*:\\s*[\"'`](/?[^\"'`]*)[\"'`]")
	reJSRoute  = regexp.MustCompile(`\.route\(\s*\{`)
	reJSMethod = regexp.MustCompile(`\bmethod\s*:\s*(\[[^\]]*\]|["'][A-Za-z]+["'])`)
	reJSURL    = regexp.MustCompile("\\b(?:url|path)\\s*:\\s*[\"'`](/[^\"'`]*)[\"'`]")
	reJSHdl    = regexp.MustCompile(`\bhandler\s*:\s*([A-Za-z_$][\w$.]*)`)
	reJSArg    = regexp.MustCompile(`,\s*([A-Za-z_$][\w$.]*)\s*(?:\)|,|$)`)

	reJSImportDefault = regexp.MustCompile(`^\s*import\s+([A-Za-z_$][\w$]*)\s*(?:,|from\b)`)
	reJSImportNamed   = regexp.MustCompile(`^\s*import\s+(?:[A-Za-z_$][\w$]*\s*,\s*)?(?:type\s+)?\{([^}]*)\}`)
	reJSImportFrom    = regexp.MustCompile(`\bfrom\s+["']([^"']+)["']`)
	reJSRequire       = regexp.MustCompile(`(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*require\(\s*["']([^"']+)["']\s*\)`)
	reJSRequireNamed  = regexp.MustCompile(`(?:const|let|var)\s*\{([^}]*)\}\s*=\s*require\(\s*["']([^"']+)["']`)
	reJSVar           = regexp.MustCompile(`(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::\s*[\w.<>]+\s*)?=\s*(express\(\s*\)|require\(\s*["'](?:express|fastify)["']\s*\)\(|(?:express\.)?Router\(|(?:Fastify|fastify)\(|new\s+Hono\(|new\s+(?:Koa)?Router\()`)
	reJSPrefixKw      = regexp.MustCompile(`\bprefix\s*:\s*["'](/[^"']*)["']`)
	reJSClient        = regexp.MustCompile(`\b(?:fetch|axios|ky|got|superagent)\s*[.(]`)
)

// Literal pre-filters for the JS patterns.
var (
	jsClientHints = []string{"fetch", "axios", "ky", "got", "superagent"}
	jsMountHints  = []string{".use(", ".route("}
	jsVerbHints   = []string{".get(", ".post(", ".put(", ".patch(", ".delete(", ".del(", ".all(", ".options(", ".head("}
)

// jsServerModules are import sources that make a file server code.
var jsServerModules = map[string]string{
	"express": fwExpress, "fastify": fwFastify, "hono": fwHono, "koa": fwKoa,
	"@koa/router": fwKoa, "koa-router": fwKoa, "next/server": fwNext,
}

// jsVar is a router variable of a JS module.
type jsVar struct {
	fw     string
	root   bool
	prefix string // koa Router({prefix})
}

// jsMount is a local mount of a router variable (app.use("/x", router)).
type jsMount struct {
	parent, prefix string
}

// jsInfo is what a JS doc says about routers.
type jsInfo struct {
	vars    map[string]jsVar
	mounts  map[string]jsMount
	imports map[string]string // local name → import source
	fws     map[string]bool
	server  bool // imports a server framework
	client  bool // calls fetch, axios, ...
}

// jsInfo analyses d's lines once.
func (d *doc) jsInfo() *jsInfo {
	if d.ji != nil {
		return d.ji
	}
	ji := &jsInfo{vars: map[string]jsVar{}, mounts: map[string]jsMount{}, imports: map[string]string{}, fws: map[string]bool{}}
	d.ji = ji
	for _, l := range d.lines {
		c := l.code
		if c == "" {
			continue
		}
		if strings.Contains(c, "import") || strings.Contains(c, "require") {
			ji.importLine(c)
		}
		if !ji.client && containsAny(c, jsClientHints) {
			ji.client = reJSClient.MatchString(c)
		}
		if strings.Contains(c, "=") && strings.Contains(c, "(") {
			for _, m := range reJSVar.FindAllStringSubmatch(c, -1) {
				ji.vars[m[1]] = jsVarOf(m[2], c)
			}
		}
	}
	for _, l := range d.lines {
		if !containsAny(l.code, jsMountHints) {
			continue
		}
		for _, m := range reJSMount.FindAllStringSubmatchIndex(l.code, -1) {
			target := sub(l.code, m, 4)
			if _, ok := ji.vars[target]; ok && sub(l.code, m, 5) == "" && !l.inString(m[0]) {
				ji.mounts[target] = jsMount{parent: receiverBefore(l.code, m[0]), prefix: sub(l.code, m, 2)}
			}
		}
	}
	return ji
}

// jsVarOf classifies a router constructor.
func jsVarOf(ctor, code string) jsVar {
	switch {
	case strings.Contains(strings.ToLower(ctor), "fastify"):
		return jsVar{fw: fwFastify, root: true}
	case strings.Contains(ctor, "Hono("):
		return jsVar{fw: fwHono, root: true}
	case strings.HasPrefix(ctor, "express(") || strings.HasPrefix(ctor, "require("):
		return jsVar{fw: fwExpress, root: true}
	case strings.Contains(ctor, "Router(") && !strings.HasPrefix(ctor, "new"):
		return jsVar{fw: fwExpress}
	}
	v := jsVar{fw: fwKoa}
	if m := reJSPrefixKw.FindStringSubmatch(code); m != nil {
		v.prefix = m[1]
	}
	return v
}

// importLine records the imports on line c.
func (ji *jsInfo) importLine(c string) {
	add := func(names, src string) {
		for n := range strings.SplitSeq(names, ",") {
			n = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(n), "type "))
			if _, alias, ok := strings.Cut(n, " as "); ok {
				n = strings.TrimSpace(alias)
			}
			if _, alias, ok := strings.Cut(n, ":"); ok {
				n = strings.TrimSpace(alias)
			}
			if n != "" {
				ji.imports[n] = src
			}
		}
		if fw, ok := jsServerModules[src]; ok {
			ji.server, ji.fws[fw] = true, true
		}
	}
	if from := reJSImportFrom.FindStringSubmatch(c); from != nil && strings.HasPrefix(strings.TrimSpace(c), "import") {
		if m := reJSImportDefault.FindStringSubmatch(c); m != nil {
			add(m[1], from[1])
		}
		if m := reJSImportNamed.FindStringSubmatch(c); m != nil {
			add(m[1], from[1])
		}
		add("", from[1])
	}
	for _, m := range reJSRequire.FindAllStringSubmatch(c, -1) {
		add(m[1], m[2])
	}
	for _, m := range reJSRequireNamed.FindAllStringSubmatch(c, -1) {
		add(m[1], m[2])
	}
}

// jsRouter is a receiver's resolution.
type jsRouter struct {
	fw     string
	root   bool
	known  bool
	prefix string
}

// chain resolves a router variable through local mounts.
func (ji *jsInfo) chain(name string, depth int) jsRouter {
	if depth > maxMountDepth {
		return jsRouter{}
	}
	if m, ok := ji.mounts[name]; ok && m.parent != name {
		up := ji.chain(m.parent, depth+1)
		up.prefix = joinPath(up.prefix, joinPath(m.prefix, ji.vars[name].prefix))
		if fw := ji.vars[name].fw; up.fw == "" {
			up.fw = fw
		}
		return up
	}
	v, ok := ji.vars[name]
	if !ok {
		return jsRouter{}
	}
	return jsRouter{fw: v.fw, root: v.root, known: true, prefix: v.prefix}
}

// reJSRouterName matches receiver names taken for routers without other
// evidence; reJSClientName names HTTP clients, never routers.
var (
	reJSRouterName = regexp.MustCompile(`^(app|router|server|fastify|routes|api[A-Z]\w*|\w+Router|\w+Routes)$`)
	reJSClientName = regexp.MustCompile(`^(axios|https?|request|client|\w+Client|ky|got|superagent|supertest|agent|cy|page|browser|\$http|fetcher|cache|map|params|searchParams|headers|cookies|store)$`)
)

// jsRegs returns the JS/TS registrations on the changed lines ls of v's
// side s: Next.js route exports in route files, else express-style calls.
func (b *builder) jsRegs(v *view, s side, ls []line) []reg {
	if p, ok := nextRoutePath(v.f.path); ok {
		return nextRegs(v, s, ls, p, false)
	}
	if p, ok := nextPagesAPIPath(v.f.path); ok {
		return nextRegs(v, s, ls, p, true)
	}
	if isJSX(v.f.path) {
		return nil
	}
	var (
		out  []reg
		d    *doc
		seen = map[int]bool{}
	)
	for _, l := range ls {
		if l.code == "" || !jsCandidate(l.code) {
			continue
		}
		if d == nil {
			d = b.sideDoc(v, s)
		}
		i := d.idx(l.no)
		if i < 0 {
			continue
		}
		for _, r := range d.jsLineRegs(i, seen) {
			r.a = at{v: v, l: l, s: s}
			out = append(out, r)
		}
	}
	return out
}

// jsCandidate is a cheap pre-filter for lines that may register a route (or
// be part of a fastify route block).
func jsCandidate(c string) bool {
	return containsAny(c, jsVerbHints) || containsAny(c, jsMountHints) || strings.Contains(c, ".register(") || fastifyCandidate(c)
}

// fastifyCandidate is a line that may start or belong to a fastify
// route({...}) block.
func fastifyCandidate(c string) bool {
	return containsAny(c, []string{".route(", "url:", "path:", "method:", "handler:"})
}

// jsLineRegs returns the registrations line i of d makes (seen dedups
// fastify route blocks by their first line).
func (d *doc) jsLineRegs(i int, seen map[int]bool) []reg {
	ji, l := d.jsInfo(), d.lines[i]
	var out []reg
	if containsAny(l.code, jsVerbHints) {
		out = append(out, ji.verbRegs(d, l)...)
	}
	if containsAny(l.code, jsMountHints) || strings.Contains(l.code, ".register(") {
		out = append(out, ji.mountRegs(d, l)...)
	}
	if fastifyCandidate(l.code) {
		out = append(out, d.fastifyRoute(i, seen)...)
	}
	return out
}

// verbRegs returns the express-style verb registrations on l
// (router.get("/x", h)).
func (ji *jsInfo) verbRegs(d *doc, l line) []reg {
	c := l.code
	var out []reg
	for _, m := range reJSVerb.FindAllStringSubmatchIndex(c, -1) {
		p := sub(c, m, 3)
		if l.inString(m[0]) || sub(c, m, 2) == "`" && strings.Contains(p, "${") {
			continue
		}
		if r, ok := ji.reg(d, receiverBefore(c, m[0]), methodOf(sub(c, m, 1)), p, jsHandler(c[m[1]:]), whatRoute); ok {
			out = append(out, r)
		}
	}
	return out
}

// mountRegs returns the mounts on l: use/route of a router, fastify
// register with a prefix.
func (ji *jsInfo) mountRegs(d *doc, l line) []reg {
	c := l.code
	var out []reg
	for _, m := range reJSMount.FindAllStringSubmatchIndex(c, -1) {
		target := sub(c, m, 4)
		if req := sub(c, m, 3); req != "" {
			target = "require(" + req + ")"
		}
		if l.inString(m[0]) || target == "" || sub(c, m, 5) != "" {
			continue
		}
		if r, ok := ji.reg(d, receiverBefore(c, m[0]), "", sub(c, m, 2), target, whatMount); ok {
			out = append(out, r)
		}
	}
	for _, m := range reJSReg.FindAllStringSubmatchIndex(c, -1) {
		if l.inString(m[0]) {
			continue
		}
		if r, ok := ji.reg(d, receiverBefore(c, m[0]), "", sub(c, m, 2), sub(c, m, 1), whatMount); ok {
			r.fw = fwFastify
			out = append(out, r)
		}
	}
	return out
}

// reg builds a JS registration on receiver recv; ok is false when recv is
// not a router.
func (ji *jsInfo) reg(d *doc, recv, method, p, handler, kind string) (reg, bool) {
	rt := ji.chain(recv, 0)
	if reJSClientName.MatchString(recv) && !rt.known {
		return reg{}, false
	}
	if !rt.known && !ji.server && !reJSRouterName.MatchString(recv) {
		return reg{}, false
	}
	r := reg{kind: kind, method: method, handler: handler, conf: confRouteExact}
	switch {
	case !rt.known && !ji.server:
		r.conf = confRouteName
	case !rt.known:
		r.conf = confRouteFile
	}
	if kind == whatMount {
		r.conf = min(r.conf, confMount)
	}
	r.fw = rt.fw
	if r.fw == "" {
		r.fw = ji.framework(recv)
	}
	r.local = joinPath(rt.prefix, p)
	r.rooted = rt.root
	r.id = ident{lang: langJS, stem: stem(d.path)}
	return r, true
}

// framework guesses the framework of a registration without a known router.
func (ji *jsInfo) framework(recv string) string {
	switch {
	case ji.fws[fwFastify] || recv == "fastify":
		return fwFastify
	case ji.fws[fwHono]:
		return fwHono
	case ji.fws[fwKoa]:
		return fwKoa
	}
	return fwExpress
}

// jsHandler is the handler of an express-style registration: the last
// plain argument after the path ("" for an inline function).
func jsHandler(rest string) string {
	if strings.Contains(rest, "=>") || strings.Contains(rest, "function") {
		return ""
	}
	ms := reJSArg.FindAllStringSubmatch(rest, -1)
	if len(ms) == 0 {
		return ""
	}
	return ms[len(ms)-1][1]
}

// fastifyWindow is how many lines a fastify route({...}) block is read over.
const fastifyWindow = 12

// fastifyRoute returns the routes of a fastify route({...}) block that
// starts at or up to fastifyWindow lines above line i.
func (d *doc) fastifyRoute(i int, seen map[int]bool) []reg {
	start := -1
	for k := i; k >= 0 && k > i-fastifyWindow; k-- {
		if !strings.Contains(d.lines[k].code, ".route(") {
			if k < i && strings.Contains(d.lines[k].code, "})") {
				break
			}
			continue
		}
		if m := reJSRoute.FindStringIndex(d.lines[k].code); m != nil && !d.lines[k].inString(m[0]) {
			start = k
			break
		}
		if k < i && strings.Contains(d.lines[k].code, "})") {
			break
		}
	}
	if start < 0 || seen[start] {
		return nil
	}
	seen[start] = true
	var methods []string
	url, handler := "", ""
	for k := start; k < len(d.lines) && k < start+fastifyWindow; k++ {
		c := d.lines[k].code
		if m := reJSMethod.FindStringSubmatch(c); m != nil && methods == nil {
			methods = quotedList(m[1])
		}
		if m := reJSURL.FindStringSubmatch(c); m != nil && url == "" {
			url = m[1]
		}
		if m := reJSHdl.FindStringSubmatch(c); m != nil && handler == "" {
			handler = m[1]
		}
		if k > start && strings.Contains(c, "})") {
			break
		}
	}
	if url == "" || len(methods) == 0 {
		return nil
	}
	c := d.lines[start].code
	m := reJSRoute.FindStringIndex(c)
	var out []reg
	for _, method := range methods {
		if r, ok := d.jsInfo().reg(d, receiverBefore(c, m[0]), method, url, handler, whatRoute); ok {
			r.fw = fwFastify
			out = append(out, r)
		}
	}
	return out
}

// jsBindings returns the cross-file mounts made in d: use/route of an
// imported router, fastify register with a prefix.
func (d *doc) jsBindings() []binding {
	ji := d.jsInfo()
	var out []binding
	for _, l := range d.lines {
		c := l.code
		if !containsAny(c, jsMountHints) && !strings.Contains(c, ".register(") {
			continue
		}
		for _, m := range reJSMount.FindAllStringSubmatchIndex(c, -1) {
			src := sub(c, m, 3)
			if src == "" && sub(c, m, 5) == "" {
				src = ji.imports[sub(c, m, 4)]
			}
			if bd, ok := ji.binding(d, l, m[0], src, sub(c, m, 2)); ok {
				out = append(out, bd)
			}
		}
		for _, m := range reJSReg.FindAllStringSubmatchIndex(c, -1) {
			if bd, ok := ji.binding(d, l, m[0], ji.imports[sub(c, m, 1)], sub(c, m, 2)); ok {
				out = append(out, bd)
			}
		}
	}
	return out
}

// binding is a mount of the module imported from src under p.
func (ji *jsInfo) binding(d *doc, l line, dot int, src, p string) (binding, bool) {
	target := jsModStem(src, d.path)
	if target == "" || l.inString(dot) {
		return binding{}, false
	}
	rt := ji.chain(receiverBefore(l.code, dot), 0)
	return binding{
		target: ident{lang: langJS, stem: target},
		prefix: joinPath(rt.prefix, p),
		owner:  ident{lang: langJS, stem: stem(d.path)},
		rooted: rt.root,
		file:   d.path, line: l.no,
	}, true
}

// Next.js route exports.
var (
	reNextFunc   = regexp.MustCompile(`^\s*export\s+(?:async\s+)?function\s+(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b`)
	reNextConst  = regexp.MustCompile(`^\s*export\s+(?:const|let|var)\s+(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\b`)
	reNextList   = regexp.MustCompile(`^\s*export\s*\{([^}]*)\}`)
	reNextMethod = regexp.MustCompile(`\b(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s*$`)
	reNextDef    = regexp.MustCompile(`^\s*export\s+default\b`)
	reNextRoute  = regexp.MustCompile(`^route\.(ts|js|mjs|mts)$`)
)

// nextRoutePath returns the URL path of an app-router route file
// ("web/app/api/users/[id]/route.ts" → "/api/users/[id]"): route groups and
// parallel-route slots are dropped.
func nextRoutePath(p string) (string, bool) {
	segs := strings.Split(p, "/")
	if !reNextRoute.MatchString(segs[len(segs)-1]) {
		return "", false
	}
	k := -1
	for i := len(segs) - 2; i >= 0; i-- {
		if segs[i] == "app" {
			k = i
			break
		}
	}
	if k < 0 {
		return "", false
	}
	var out []string
	for _, s := range segs[k+1 : len(segs)-1] {
		if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") || strings.HasPrefix(s, "@") {
			continue
		}
		out = append(out, s)
	}
	return "/" + strings.Join(out, "/"), true
}

// nextPagesAPIPath returns the URL path of a pages-router API file
// ("pages/api/users/[id].ts" → "/api/users/[id]").
func nextPagesAPIPath(p string) (string, bool) {
	i := strings.Index("/"+p, "/pages/api/")
	if i < 0 || isJSX(p) {
		return "", false
	}
	rest := stem(p[i+len("pages/"):])
	rest = strings.TrimSuffix(rest, "/index")
	return "/" + rest, true
}

// nextRegs returns the route exports on the changed lines of a Next.js
// route file (pages: the default export, any method).
func nextRegs(v *view, s side, ls []line, urlPath string, pages bool) []reg {
	var out []reg
	for _, l := range ls {
		for _, method := range nextMethods(l.code, pages) {
			out = append(out, reg{kind: whatRoute, fw: fwNext, method: method, local: urlPath, handler: method,
				conf: confRouteFile, rooted: true, a: at{v: v, l: l, s: s}})
		}
	}
	return out
}

// nextMethods returns the methods a line exports.
func nextMethods(c string, pages bool) []string {
	if pages {
		if reNextDef.MatchString(c) {
			return []string{methodAny}
		}
		return nil
	}
	for _, re := range []*regexp.Regexp{reNextFunc, reNextConst} {
		if m := re.FindStringSubmatch(c); m != nil {
			return []string{m[1]}
		}
	}
	m := reNextList.FindStringSubmatch(c)
	if m == nil {
		return nil
	}
	var out []string
	for item := range strings.SplitSeq(m[1], ",") {
		if mm := reNextMethod.FindStringSubmatch(strings.TrimSpace(item)); mm != nil {
			out = append(out, mm[1])
		}
	}
	return out
}

// nextRenames reports the routes of renamed Next.js route files as changed
// (their export lines are context, so line scanning does not see them).
func (b *builder) nextRenames() []model.SurfaceEntry {
	var out []model.SurfaceEntry
	for _, f := range b.files {
		if f.test || !f.file.Renamed || f.oldPath == "" || f.oldPath == f.path {
			continue
		}
		newURL, pages, ok := nextURL(f.path)
		oldURL, _, okOld := nextURL(f.oldPath)
		if !ok || !okOld || newURL == oldURL {
			continue
		}
		d := b.rd.fullDoc(f, sideNew)
		if d == nil {
			continue
		}
		var v *view
		if len(f.views) > 0 {
			v = f.views[0]
		}
		for _, l := range d.lines {
			for _, method := range nextMethods(l.code, pages) {
				e := newEntry(model.SurfaceAPI, model.SurfaceChanged, whatRoute, method+" "+newURL, at{v: v, l: l, s: sideNew}, confRouteFile, fwNext)
				e.Path, e.Detail = f.path, "was "+method+" "+oldURL
				out = append(out, e)
			}
		}
	}
	return out
}

// nextURL is the URL path of a Next.js route or pages API file.
func nextURL(p string) (string, bool, bool) {
	if u, ok := nextRoutePath(p); ok {
		return u, false, true
	}
	if u, ok := nextPagesAPIPath(p); ok {
		return u, true, true
	}
	return "", false, false
}
