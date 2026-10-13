package surface

import (
	"cmp"
	"slices"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Frameworks of API entries (SurfaceEntry.Framework).
const (
	fwNetHTTP = "net/http"
	fwChi     = "chi"
	fwGin     = "gin"
	fwEcho    = "echo"
	fwFiber   = "fiber"
	fwGorilla = "gorilla/mux"
	fwExpress = "express"
	fwFastify = "fastify"
	fwHono    = "hono"
	fwKoa     = "koa"
	fwNext    = "next"
	fwFlask   = "flask"
	fwFastAPI = "fastapi"
	fwDjango  = "django"
)

// Confidences of endpoint registrations.
const (
	confRouteExact = 0.97 // a registration on a known router with a literal path
	confRouteFile  = 0.95 // a Next.js route export, a decorator in a framework module
	confRouteName  = 0.85 // a registration recognised by the receiver's name only
	confMount      = 0.9
	confDjango     = 0.92
)

// methodAny is the method of a registration that takes every method.
const methodAny = "ANY"

// reg is an endpoint registration (or a mount) on one side of a unit.
type reg struct {
	kind    string // whatRoute or whatMount
	fw      string
	method  string // "GET", ...; methodAny
	local   string // the path with the prefixes resolved in its own file
	full    string // the path with every resolved prefix
	handler string // handler name (mount target for mounts); "" inline
	note    string // prefix note for Detail
	conf    float64
	a       at
	// rooted is set when local is complete; otherwise id names the router
	// for cross-file mounts, and known says the router carries a prefix of
	// its own (a blueprint's url_prefix), so a missing mount is no news.
	rooted bool
	known  bool
	id     ident
}

// label is the registration in words ("GET /x", or "/x" for a mount).
func (r reg) label() string {
	if r.kind == whatMount {
		return r.full
	}
	return r.method + " " + r.full
}

// detectAPI finds endpoint and response-shape changes.
func (b *builder) detectAPI() []model.SurfaceEntry {
	var regs [2][]reg
	for _, v := range b.live() {
		for _, s := range sides {
			regs[s] = append(regs[s], b.unitRegs(v, s)...)
		}
	}
	for _, s := range sides {
		regs[s] = b.resolveRegs(regs[s], s)
	}
	out := pairRegs(regs[sideOld], regs[sideNew])
	out = append(out, b.nextRenames()...)
	return append(out, b.detectShapes()...)
}

// unitRegs returns the registrations on v's changed lines on side s.
func (b *builder) unitRegs(v *view, s side) []reg {
	ls := v.changedLines(s)
	if len(ls) == 0 {
		return nil
	}
	switch v.f.lang {
	case langGo:
		return b.goRegs(v, s, ls)
	case langJS:
		return b.jsRegs(v, s, ls)
	case langPy:
		return b.pyRegs(v, s, ls)
	}
	return nil
}

// resolveRegs composes the cross-file mount prefixes of side s into each
// registration's full path, one registration per prefix found.
func (b *builder) resolveRegs(rs []reg, s side) []reg {
	var bs bindings
	loaded := false
	out := make([]reg, 0, len(rs))
	for _, r := range rs {
		if r.rooted {
			r.full = displayPath(r.local)
			out = append(out, r)
			continue
		}
		if !loaded {
			bs, loaded = b.bindings(s), true
		}
		out = append(out, expand(r, bs.resolve(r.id, 0))...)
	}
	return out
}

// expand applies resolved mount prefixes to r.
func expand(r reg, res []resolved) []reg {
	if len(res) == 0 {
		r.full = displayPath(r.local)
		if !r.known {
			r.note = strings.TrimSpace("mount prefix unknown " + r.id.label())
		}
		return []reg{r}
	}
	out := make([]reg, 0, len(res))
	for _, p := range res {
		c := r
		c.full = displayPath(joinPath(p.prefix, r.local))
		if !p.complete {
			c.note = "outer mount prefix unknown"
		}
		out = append(out, c)
	}
	return out
}

// bindings collects side s's mounts from every changed source file.
func (b *builder) bindings(s side) bindings {
	var bs bindings
	for _, f := range b.files {
		if f.test || b.ctx.Err() != nil {
			continue
		}
		switch f.lang {
		case langGo, langJS, langPy:
		default:
			continue
		}
		for _, d := range b.fileDocs(f, s) {
			switch f.lang {
			case langGo:
				bs = append(bs, d.goBindings()...)
			case langJS:
				bs = append(bs, d.jsBindings()...)
			case langPy:
				bs = append(bs, b.pyBindings(f, s, d)...)
			}
		}
	}
	return bs.sorted()
}

// pairRegs turns the removed (before) and added (after) registrations of the
// item into entries: the same endpoint on both sides is no change (or a
// handler change), the same handler or path with another method or path is
// a change, the rest are removals and additions.
func pairRegs(before, after []reg) []model.SurfaceEntry {
	sortRegs(before)
	sortRegs(after)
	usedOld, usedNew := make([]bool, len(before)), make([]bool, len(after))
	var out []model.SurfaceEntry
	pass := func(match func(o, n reg) bool, entry func(o, n reg) *model.SurfaceEntry) {
		for i, o := range before {
			if usedOld[i] {
				continue
			}
			for j, n := range after {
				if usedNew[j] || o.kind != n.kind || !match(o, n) {
					continue
				}
				usedOld[i], usedNew[j] = true, true
				if e := entry(o, n); e != nil {
					out = append(out, *e)
				}
				break
			}
		}
	}
	// The same endpoint on both sides: unchanged, or its handler changed.
	pass(func(o, n reg) bool { return o.method == n.method && normPath(o.full) == normPath(n.full) },
		func(o, n reg) *model.SurfaceEntry {
			if o.handler == n.handler || o.handler == "" || n.handler == "" {
				return nil
			}
			what := whatHandler
			if n.kind == whatMount {
				what = whatMount
			}
			e := regEntry(n, model.SurfaceChanged, what, "was "+o.handler)
			return &e
		})
	// The same handler in the same file under another method or path.
	pass(func(o, n reg) bool { return o.handler != "" && o.handler == n.handler && o.a.v.f == n.a.v.f },
		changedReg)
	// The same path in the same file under another method.
	pass(func(o, n reg) bool { return normPath(o.full) == normPath(n.full) && o.a.v.f == n.a.v.f },
		changedReg)
	for i, o := range before {
		if !usedOld[i] {
			out = append(out, regEntry(o, model.SurfaceRemoved, o.kind, ""))
		}
	}
	for j, n := range after {
		if !usedNew[j] {
			out = append(out, regEntry(n, model.SurfaceAdded, n.kind, ""))
		}
	}
	return out
}

// changedReg is the entry of a registration whose method or path changed.
func changedReg(o, n reg) *model.SurfaceEntry {
	e := regEntry(n, model.SurfaceChanged, n.kind, "was "+o.label())
	return &e
}

// regEntry is the entry of a registration.
func regEntry(r reg, change, what, detail string) model.SurfaceEntry {
	name := r.label()
	e := newEntry(model.SurfaceAPI, change, what, name, r.a, r.conf, r.fw)
	if r.kind == whatMount && r.handler != "" && change != model.SurfaceChanged {
		detail = joinDetail(detail, "→ "+r.handler)
	}
	e.Detail = joinDetail(r.note, detail)
	return e
}

// sortRegs orders registrations by place, so pairing is deterministic.
func sortRegs(rs []reg) {
	slices.SortStableFunc(rs, func(a, b reg) int {
		return cmp.Or(
			cmp.Compare(a.a.v.f.path, b.a.v.f.path),
			cmp.Compare(a.a.l.no, b.a.l.no),
			cmp.Compare(a.method, b.method),
			cmp.Compare(a.full, b.full),
		)
	})
}

// httpMethods are the methods a registration may name.
var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	"HEAD": true, "OPTIONS": true, "CONNECT": true, "TRACE": true,
}

// methodOf normalises a framework's method name ("Get", "del", "all").
func methodOf(name string) string {
	u := strings.ToUpper(name)
	switch u {
	case "DEL":
		return "DELETE"
	case "ALL", "ANY", "HANDLE", "HANDLEFUNC", "ROUTE", "API_ROUTE":
		return methodAny
	}
	if httpMethods[u] {
		return u
	}
	return ""
}

// quotedList returns the quoted words of s (`"GET", 'POST'`), upper-cased.
func quotedList(s string) []string {
	var out []string
	for _, m := range reQuoted.FindAllStringSubmatch(s, -1) {
		w := m[1] + m[2]
		if m := methodOf(w); m != "" && m != methodAny {
			out = append(out, m)
		}
	}
	return out
}
