package surface

import (
	"cmp"
	"path"
	"regexp"
	"slices"
	"strings"
)

// ident names a router for cross-file mount lookups.
//
//   - Go: stem is the base name of the package directory and name the
//     function that registers the routes (chi's users.Routes, gin's
//     users.Register(group)).
//   - JS/TS: stem is the module path without extension; name is unused
//     (every router of the module is served under the mount).
//   - Python: stem is the module path without extension and name the router
//     variable ("" matches any; django include).
type ident struct {
	lang, stem, name string
}

// label names the router for a Detail: "(users.Routes)", "(users.router)",
// "(routes/users)"; "" when nothing names it.
func (t ident) label() string {
	name := path.Base(t.stem)
	if t.lang == langJS {
		name = path.Join(path.Base(path.Dir(t.stem)), path.Base(t.stem))
	}
	if t.stem == "" || name == "." {
		name = ""
	}
	if t.name != "" {
		name = strings.TrimPrefix(name+"."+t.name, ".")
	}
	if name == "" {
		return ""
	}
	return "(" + name + ")"
}

// matches reports whether a registration's router id is the target t of a
// mount.
func (t ident) matches(id ident) bool {
	if t.lang != id.lang || t.stem == "" {
		return false
	}
	switch t.lang {
	case langGo:
		return t.stem == id.stem && t.name == id.name
	case langJS:
		return id.stem == t.stem || id.stem == t.stem+"/index"
	}
	return (id.stem == t.stem || strings.HasSuffix(id.stem, "/"+t.stem)) && (t.name == "" || t.name == id.name)
}

// binding is a mount found at one revision: everything target registers is
// served under prefix, itself under the prefixes of owner unless rooted.
type binding struct {
	target ident
	prefix string
	owner  ident
	rooted bool   // the mount is made on a root router: prefix is complete
	file   string // where the mount is (for a deterministic order)
	line   int
}

// resolved is one resolved mount prefix of a router.
type resolved struct {
	prefix   string
	complete bool // every mount up to a root router was found
}

// maxMountDepth bounds mount chains (and cycles); maxPrefixes the prefixes
// one router is reported under.
const (
	maxMountDepth = 6
	maxPrefixes   = 3
)

// bindings are one side's mounts, in file and line order.
type bindings []binding

func (bs bindings) sorted() bindings {
	slices.SortStableFunc(bs, func(a, b binding) int {
		return cmp.Or(cmp.Compare(a.file, b.file), cmp.Compare(a.line, b.line), cmp.Compare(a.prefix, b.prefix))
	})
	return bs
}

// resolve returns the prefixes the router id is mounted under; empty when
// no mount of it was found.
func (bs bindings) resolve(id ident, depth int) []resolved {
	if depth > maxMountDepth {
		return nil
	}
	var out []resolved
	for _, b := range bs {
		if !b.target.matches(id) || b.owner == id {
			continue
		}
		if b.rooted {
			out = append(out, resolved{b.prefix, true})
			continue
		}
		up := bs.resolve(b.owner, depth+1)
		if len(up) == 0 {
			out = append(out, resolved{b.prefix, false})
			continue
		}
		for _, r := range up {
			out = append(out, resolved{joinPath(r.prefix, b.prefix), r.complete})
		}
	}
	return uniqResolved(out)
}

// uniqResolved drops repeated prefixes (keeping a complete one) and caps the
// list.
func uniqResolved(rs []resolved) []resolved {
	var out []resolved
	for _, r := range rs {
		i := slices.IndexFunc(out, func(o resolved) bool { return o.prefix == r.prefix })
		switch {
		case i >= 0:
			out[i].complete = out[i].complete || r.complete
		case len(out) < maxPrefixes:
			out = append(out, r)
		}
	}
	return out
}

// joinPath joins URL path parts without doubling or losing slashes
// ("/api/v1" + "/users" → "/api/v1/users"; "/api" + "/" → "/api/").
func joinPath(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return strings.TrimRight(a, "/") + "/" + strings.TrimLeft(b, "/")
}

// displayPath makes a joined path absolute ("" → "/").
func displayPath(p string) string {
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// reParam matches a path parameter in any framework's syntax: {id}, :id,
// <int:id>, [id], [...slug].
var reParam = regexp.MustCompile(`\{[^}]*\}|:[A-Za-z_]\w*|<[^>]*>|\[[^\]]*\]`)

// normPath is a path's comparison form: parameters as "{}", no trailing
// slash.
func normPath(p string) string {
	p = reParam.ReplaceAllString(p, "{}")
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// isIdentByte reports whether c can be part of an identifier ($ for JS).
func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// identBefore returns the identifier ending at code[end].
func identBefore(code string, end int) string {
	start := end
	for start > 0 && isIdentByte(code[start-1]) {
		start--
	}
	return code[start:end]
}

// receiverBefore returns the receiver of a method call whose "." is at
// code[dot]: the identifier before it, skipping chained calls such as
// r.With(mw).Get or s.router.Get ("router"). "" when there is none.
func receiverBefore(code string, dot int) string {
	end := dot
	for range 8 {
		if end <= 0 || end > len(code) {
			return ""
		}
		if code[end-1] != ')' {
			return identBefore(code, end)
		}
		open := matchingOpen(code, end-1)
		if open <= 0 {
			return ""
		}
		name := identBefore(code, open)
		start := open - len(name)
		if name == "" || start == 0 || code[start-1] != '.' {
			return ""
		}
		end = start - 1
	}
	return ""
}

// matchingOpen returns the index of the "(" matching the ")" at code[close],
// or -1.
func matchingOpen(code string, closeAt int) int {
	depth := 0
	for k := closeAt; k >= 0; k-- {
		switch code[k] {
		case ')':
			depth++
		case '(':
			depth--
			if depth == 0 {
				return k
			}
		}
	}
	return -1
}

// braceDelta is the net count of "{" minus "}" in a line's code, outside
// string literals.
func braceDelta(l line) int {
	n := 0
	for i := 0; i < len(l.code); i++ {
		switch l.code[i] {
		case '{':
			if !l.inString(i) {
				n++
			}
		case '}':
			if !l.inString(i) {
				n--
			}
		}
	}
	return n
}

// symbolName is a unit symbol's bare name ("Store.Get" → "Get").
func symbolName(sym string) string {
	if i := strings.LastIndexByte(sym, '.'); i >= 0 {
		return sym[i+1:]
	}
	return sym
}

// modToStem turns a Python module path into a repo path stem, resolving a
// relative module ("..routers.users") against the importing file.
func modToStem(mod, file string) string {
	n := len(mod) - len(strings.TrimLeft(mod, "."))
	rest := strings.ReplaceAll(mod[n:], ".", "/")
	if n == 0 {
		return rest
	}
	base := path.Dir(file)
	for k := 1; k < n; k++ {
		base = path.Dir(base)
	}
	if base == "." {
		return rest
	}
	return path.Join(base, rest)
}

// jsModStem resolves a relative JS import source against the importing
// file ("./routes/users" from "server/app.ts" → "server/routes/users");
// "" for a package import.
func jsModStem(src, file string) string {
	if !strings.HasPrefix(src, ".") {
		return ""
	}
	p := path.Join(path.Dir(file), src)
	switch path.Ext(p) {
	case ".js", ".ts", ".mjs", ".cjs", ".mts", ".cts":
		p = stem(p)
	}
	return p
}
