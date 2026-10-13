package surface

import (
	"path"
	"regexp"
	"strings"
)

// Python registration patterns (flask, fastapi, django) and module facts.
var (
	rePyDeco      = regexp.MustCompile(`^\s*@([A-Za-z_]\w*)\.(route|get|post|put|patch|delete|head|options|api_route|websocket)\(\s*(?:(?:rule|path)\s*=\s*)?[rRbBuU]?(["'])([^"']*)["']`)
	rePyMethodsKw = regexp.MustCompile(`methods\s*=\s*[\[(]([^\])]*)[\])]`)
	rePyDef       = regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_]\w*)`)
	reDjangoPath  = regexp.MustCompile(`\b(path|re_path|url)\(\s*[rRuU]?(["'])([^"']*)["']\s*,\s*(.*)`)
	reDjangoIncl  = regexp.MustCompile(`^include\(\s*(?:[rRuU]?["']([\w.]+)["']|([A-Za-z_][\w.]*))`)
	reDjangoView  = regexp.MustCompile(`^([A-Za-z_][\w.]*)`)

	rePyFrom     = regexp.MustCompile(`^\s*from\s+([.\w]+)\s+import\s+\(?([^)#]*)`)
	rePyImport   = regexp.MustCompile(`^\s*import\s+([\w.]+)(?:\s+as\s+(\w+))?`)
	rePyVar      = regexp.MustCompile(`^\s*([A-Za-z_]\w*)\s*(?::\s*[\w.]+\s*)?=\s*(Flask|FastAPI|Blueprint|APIRouter)\((.*)`)
	rePyPrefixKw = regexp.MustCompile(`\b(?:url_prefix|prefix)\s*=\s*[rRuU]?["']([^"']*)["']`)
	rePyMount    = regexp.MustCompile(`\b([A-Za-z_]\w*)\.(register_blueprint|include_router)\(\s*([A-Za-z_][\w.]*)(.*)`)
)

// Literal pre-filters for the Python patterns.
var (
	pyRouterCtors = []string{"Flask(", "FastAPI(", "Blueprint(", "APIRouter("}
	pyMountHints  = []string{"register_blueprint(", "include_router("}
)

// pyVar is a Python router variable.
type pyVar struct {
	fw     string
	root   bool
	prefix string // the constructor's url_prefix / prefix
}

// pyMount is a local mount of a router variable.
type pyMount struct{ parent, prefix string }

// pyInfo is what a Python doc says about routers.
type pyInfo struct {
	vars    map[string]pyVar
	mounts  map[string]pyMount
	imports map[string]string // local name → dotted module path (".x" relative kept)
	fws     map[string]bool
}

// pyInfo analyses d's lines once.
func (d *doc) pyInfo() *pyInfo {
	if d.pi != nil {
		return d.pi
	}
	pi := &pyInfo{vars: map[string]pyVar{}, mounts: map[string]pyMount{}, imports: map[string]string{}, fws: map[string]bool{}}
	d.pi = pi
	for _, l := range d.lines {
		c := l.code
		if strings.Contains(c, "import") {
			pi.importLine(c)
		}
		if !containsAny(c, pyRouterCtors) {
			continue
		}
		if m := rePyVar.FindStringSubmatch(c); m != nil {
			v := pyVar{fw: fwFlask, root: m[2] == "Flask" || m[2] == "FastAPI"}
			if m[2] == "FastAPI" || m[2] == "APIRouter" {
				v.fw = fwFastAPI
			}
			if !v.root {
				if p := rePyPrefixKw.FindStringSubmatch(m[3]); p != nil {
					v.prefix = p[1]
				}
			}
			pi.vars[m[1]] = v
		}
	}
	for _, l := range d.lines {
		if !containsAny(l.code, pyMountHints) {
			continue
		}
		if m := rePyMount.FindStringSubmatch(l.code); m != nil {
			if _, ok := pi.vars[m[3]]; ok {
				pi.mounts[m[3]] = pyMount{parent: m[1], prefix: kwPrefix(m[4])}
			}
		}
	}
	return pi
}

func kwPrefix(rest string) string {
	if p := rePyPrefixKw.FindStringSubmatch(rest); p != nil {
		return p[1]
	}
	return ""
}

// importLine records the imports on line c.
func (pi *pyInfo) importLine(c string) {
	if m := rePyFrom.FindStringSubmatch(c); m != nil {
		pi.framework(m[1])
		for item := range strings.SplitSeq(m[2], ",") {
			name, alias, _ := strings.Cut(strings.TrimSpace(item), " as ")
			name, alias = strings.TrimSpace(name), strings.TrimSpace(alias)
			if alias == "" {
				alias = name
			}
			if name == "" || name == "*" {
				continue
			}
			mod := m[1] + "." + name
			if strings.HasSuffix(m[1], ".") {
				mod = m[1] + name
			}
			pi.imports[alias] = mod
		}
		return
	}
	if m := rePyImport.FindStringSubmatch(c); m != nil {
		pi.framework(m[1])
		name := m[2]
		if name == "" {
			name, _, _ = strings.Cut(m[1], ".")
			pi.imports[name] = name
			return
		}
		pi.imports[name] = m[1]
	}
}

func (pi *pyInfo) framework(mod string) {
	root, _, _ := strings.Cut(mod, ".")
	switch root {
	case "flask":
		pi.fws[fwFlask] = true
	case "fastapi":
		pi.fws[fwFastAPI] = true
	case "django":
		pi.fws[fwDjango] = true
	}
}

// pyRouter is a receiver's resolution.
type pyRouter struct {
	fw     string
	root   bool
	known  bool
	own    bool // the router has a prefix of its own
	prefix string
}

// chain resolves a router variable through local mounts.
func (pi *pyInfo) chain(name string, depth int) pyRouter {
	v, ok := pi.vars[name]
	if !ok || depth > maxMountDepth {
		return pyRouter{}
	}
	r := pyRouter{fw: v.fw, root: v.root, known: true, own: v.prefix != "", prefix: v.prefix}
	if m, ok := pi.mounts[name]; ok && m.parent != name {
		up := pi.chain(m.parent, depth+1)
		up.prefix = joinPath(joinPath(up.prefix, m.prefix), v.prefix)
		up.own = up.own || r.own
		if up.fw == "" {
			up.fw = r.fw
		}
		return up
	}
	return r
}

// rePyRouterName matches receiver names taken for routers without other
// evidence.
var rePyRouterName = regexp.MustCompile(`^(app|application|api|router|bp|blueprint|\w+_bp|\w+_router|\w+_blueprint|\w+_api|\w+_app)$`)

// pyRegs returns the Python registrations on the changed lines ls of v's
// side s: flask/fastapi decorators, django path() in urls modules.
func (b *builder) pyRegs(v *view, s side, ls []line) []reg {
	django := isDjangoURLs(v.f.path)
	var (
		out []reg
		d   *doc
	)
	for _, l := range ls {
		c := l.code
		deco := rePyDeco.FindStringSubmatchIndex(c)
		dj := django && reDjangoPath.MatchString(c)
		if deco == nil && !dj {
			continue
		}
		if d == nil {
			d = b.sideDoc(v, s)
		}
		i := d.idx(l.no)
		if i < 0 {
			continue
		}
		var rs []reg
		if deco != nil {
			rs = d.pyDecoRegs(i, deco)
		} else {
			rs = b.djangoRegs(v.f, s, d, i)
		}
		for _, r := range rs {
			r.a = at{v: v, l: l, s: s}
			out = append(out, r)
		}
	}
	return out
}

// pyDecoRegs returns the routes of a flask/fastapi decorator on line i (none
// when the match lies in a string, such as a docstring line).
func (d *doc) pyDecoRegs(i int, m []int) []reg {
	if d.lines[i].inString(m[0]) {
		return nil
	}
	pi, c := d.pyInfo(), d.lines[i].code
	recv, call, p := sub(c, m, 1), sub(c, m, 2), sub(c, m, 4)
	rt := pi.chain(recv, 0)
	if !rt.known && !pi.fws[fwFlask] && !pi.fws[fwFastAPI] && !rePyRouterName.MatchString(recv) {
		return nil
	}
	methods := []string{methodOf(call)}
	if call == "route" || call == "api_route" {
		methods = []string{"GET"}
		if mm := rePyMethodsKw.FindStringSubmatch(c); mm != nil {
			if list := quotedList(mm[1]); len(list) > 0 {
				methods = list
			}
		}
	}
	if call == "websocket" {
		methods = []string{"WS"}
	}
	fw := rt.fw
	switch {
	case fw != "":
	case pi.fws[fwFastAPI] && !pi.fws[fwFlask], call == "api_route", call == "websocket":
		fw = fwFastAPI
	case pi.fws[fwFlask] || call == "route":
		fw = fwFlask
	default:
		fw = fwFastAPI
	}
	conf := confRouteFile
	if !rt.known && !pi.fws[fw] {
		conf = confRouteName
	}
	handler := d.pyHandler(i)
	out := make([]reg, 0, len(methods))
	for _, method := range methods {
		out = append(out, reg{kind: whatRoute, fw: fw, method: method, local: joinPath(rt.prefix, p), handler: handler,
			conf: conf, rooted: rt.root, known: rt.own, id: ident{lang: langPy, stem: stem(d.path), name: recv}})
	}
	return out
}

// pyHandler is the function a decorator on line i decorates.
func (d *doc) pyHandler(i int) string {
	for k := i + 1; k < len(d.lines) && k <= i+6; k++ {
		if m := rePyDef.FindStringSubmatch(d.lines[k].code); m != nil {
			return m[1]
		}
	}
	return ""
}

// isDjangoURLs reports whether p is a django URLconf module.
func isDjangoURLs(p string) bool {
	return path.Base(p) == "urls.py" || dirBase(p) == "urls" && path.Ext(p) == ".py"
}

// djangoRegs returns the route or include on line i of a urls module.
func (b *builder) djangoRegs(f *fileInfo, s side, d *doc, i int) []reg {
	c := d.lines[i].code
	m := reDjangoPath.FindStringSubmatchIndex(c)
	if m == nil || d.lines[i].inString(m[0]) {
		return nil
	}
	route, rest := djangoRoute(sub(c, m, 3)), strings.TrimSpace(sub(c, m, 4))
	r := reg{kind: whatRoute, fw: fwDjango, method: methodAny, local: route, conf: confDjango,
		rooted: b.djangoRoot(f, s), id: ident{lang: langPy, stem: stem(d.path)}}
	if inc := reDjangoIncl.FindStringSubmatch(rest); inc != nil {
		r.kind, r.handler, r.conf = whatMount, inc[1]+inc[2], confMount
		return []reg{r}
	}
	if v := reDjangoView.FindStringSubmatch(rest); v != nil {
		r.handler = strings.TrimSuffix(v[1], ".as_view")
	}
	return []reg{r}
}

// djangoRoute is the display form of a path() or re_path() route.
func djangoRoute(r string) string {
	return strings.TrimSuffix(strings.TrimPrefix(r, "^"), "$")
}

// djangoRoot reports whether f is the root URLconf: a settings, wsgi or
// asgi module sits next to it.
func (b *builder) djangoRoot(f *fileInfo, s side) bool {
	dir := path.Dir(f.path)
	if root, ok := b.djangoRoots[s][dir]; ok {
		return root
	}
	root := false
	for _, n := range []string{"settings.py", "wsgi.py", "asgi.py"} {
		p := path.Join(dir, n)
		if _, ok := b.byPath[p]; ok || b.rd.exists(s, p) {
			root = true
			break
		}
	}
	if b.djangoRoots[s] == nil {
		b.djangoRoots[s] = map[string]bool{}
	}
	b.djangoRoots[s][dir] = root
	return root
}

// pyBindings returns the cross-file mounts made in d: register_blueprint and
// include_router of an imported router, django include().
func (b *builder) pyBindings(f *fileInfo, s side, d *doc) []binding {
	pi := d.pyInfo()
	var out []binding
	for _, l := range d.lines {
		c := l.code
		if !containsAny(c, pyMountHints) && !strings.Contains(c, "include(") {
			continue
		}
		if m := rePyMount.FindStringSubmatchIndex(c); m != nil && !l.inString(m[0]) {
			target := sub(c, m, 3)
			if _, local := pi.vars[target]; !local {
				rt := pi.chain(sub(c, m, 1), 0)
				st, name := pi.target(target, d.path)
				out = append(out, binding{target: ident{lang: langPy, stem: st, name: name},
					prefix: joinPath(rt.prefix, kwPrefix(sub(c, m, 4))),
					owner:  ident{lang: langPy, stem: stem(d.path), name: sub(c, m, 1)},
					rooted: rt.root, file: d.path, line: l.no})
			}
		}
		if !isDjangoURLs(d.path) {
			continue
		}
		m := reDjangoPath.FindStringSubmatch(c)
		if m == nil {
			continue
		}
		inc := reDjangoIncl.FindStringSubmatch(strings.TrimSpace(m[4]))
		if inc == nil {
			continue
		}
		mod := inc[1]
		if mod == "" {
			mod = pi.module(inc[2])
		}
		out = append(out, binding{target: ident{lang: langPy, stem: modToStem(mod, d.path)},
			prefix: djangoRoute(m[3]), owner: ident{lang: langPy, stem: stem(d.path)},
			rooted: b.djangoRoot(f, s), file: d.path, line: l.no})
	}
	return out
}

// module resolves a dotted name through the imports ("users.urls" with
// "from app import users" → "app.users.urls").
func (pi *pyInfo) module(name string) string {
	first, rest, _ := strings.Cut(name, ".")
	mod, ok := pi.imports[first]
	if !ok {
		return name
	}
	if rest == "" {
		return mod
	}
	return mod + "." + rest
}

// target resolves a mounted router expression ("users.router", "bp") to
// the module stem and variable that define it.
func (pi *pyInfo) target(expr, file string) (string, string) {
	full := pi.module(expr)
	i := strings.LastIndexByte(full, '.')
	if i < 0 {
		return modToStem(full, file), ""
	}
	return modToStem(full[:i], file), full[i+1:]
}
