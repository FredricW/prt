package surface

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// prefixInput builds testdata/prefix/<name>: change.diff over the base/
// tree, with a Source (base tree, head = base with the diff applied) or
// without one.
func prefixInput(t *testing.T, name string, withSrc bool) Input {
	t.Helper()
	dir := filepath.Join("testdata", "prefix", name)
	text := readFile(t, filepath.Join(dir, "change.diff"))
	in := patchInput(t, text)
	if withSrc {
		in.Src = treeSource(t, filepath.Join(dir, "base"), parsePatch(t, text))
	}
	return in
}

// TestPrefixResolution checks router prefixes composed from the base and
// head files: chi Route and Mount, gin Group passed to a package function,
// django include, a renamed Next.js route file; and the diff-only
// degradation without a Source.
func TestPrefixResolution(t *testing.T) {
	tests := []struct {
		name    string
		withSrc bool
		want    []string
		detail  map[string]string // entry words → Detail
		note    string            // substring of ChangeSurface.Note ("" none)
	}{
		{name: "chi-mount", withSrc: true, want: []string{"added route: GET /api/v1/users/{id}/avatar"},
			detail: map[string]string{"added route: GET /api/v1/users/{id}/avatar": ""}},
		{name: "chi-mount", want: []string{"added route: GET /{id}/avatar"},
			detail: map[string]string{"added route: GET /{id}/avatar": "mount prefix unknown (users)"}, note: "file contents unavailable"},
		{name: "chi-route", withSrc: true, want: []string{"added route: GET /api/v2/d", "removed route: GET /api/c"}},
		{name: "chi-route", want: []string{"added route: GET /d", "removed route: GET /c"},
			detail: map[string]string{"added route: GET /d": "mount prefix unknown"}, note: "file contents unavailable"},
		{name: "gin-group", withSrc: true, want: []string{"added route: DELETE /api/users/:id"},
			detail: map[string]string{"added route: DELETE /api/users/:id": ""}},
		{name: "gin-group", want: []string{"added route: DELETE /:id"},
			detail: map[string]string{"added route: DELETE /:id": "mount prefix unknown (users)"}},
		{name: "django-include", withSrc: true, want: []string{"added route: ANY /api/users/<int:pk>/avatar/"}},
		{name: "next-rename", withSrc: true, want: []string{"changed route: GET /api/users"},
			detail: map[string]string{"changed route: GET /api/users": "was GET /api/user"}},
		{name: "next-rename", want: nil},
	}
	for _, tc := range tests {
		label := tc.name + "/no-src"
		if tc.withSrc {
			label = tc.name + "/src"
		}
		t.Run(label, func(t *testing.T) {
			cs := build(context.Background(), prefixInput(t, tc.name, tc.withSrc))
			assertEntries(t, cs, tc.want)
			for words, detail := range tc.detail {
				if got := find(t, cs, words).Detail; got != detail {
					t.Errorf("%s detail = %q, want %q", words, got, detail)
				}
			}
			if tc.note == "" && tc.withSrc && cs.Note != "" {
				t.Errorf("note = %q, want none", cs.Note)
			}
			if tc.note != "" && !strings.Contains(cs.Note, tc.note) {
				t.Errorf("note = %q, want it to mention %q", cs.Note, tc.note)
			}
		})
	}
}

// TestFixturePR106DiffOnly checks #106 without a Source: the users routes
// still get their prefix from the mount in main.go's hunk, which the item
// changes too.
func TestFixturePR106DiffOnly(t *testing.T) {
	in := fixtureInput(t, "06-user-preferences-api.patch")
	in.Src = nil
	cs := build(context.Background(), in)
	find(t, cs, "added route: PUT /api/v1/users/{id}/preferences")
	find(t, cs, "removed route: GET /api/v1/health")
}

func TestJoinAndNormPath(t *testing.T) {
	joins := []struct{ a, b, want string }{
		{"", "/x", "/x"}, {"/api", "", "/api"}, {"/api/", "/v1", "/api/v1"}, {"/api", "v1", "/api/v1"},
		{"api/", "users/", "api/users/"}, {"/api", "/", "/api/"},
	}
	for _, j := range joins {
		if got := joinPath(j.a, j.b); got != j.want {
			t.Errorf("joinPath(%q, %q) = %q, want %q", j.a, j.b, got, j.want)
		}
	}
	norms := []struct{ in, want string }{
		{"/users/{id}", "/users/{}"}, {"/users/:id/", "/users/{}"}, {"/users/<int:pk>/", "/users/{}"},
		{"/users/[id]", "/users/{}"}, {"/", "/"},
	}
	for _, n := range norms {
		if got := normPath(n.in); got != n.want {
			t.Errorf("normPath(%q) = %q, want %q", n.in, got, n.want)
		}
	}
}

func TestIdentLabel(t *testing.T) {
	tests := []struct {
		id   ident
		want string
	}{
		{ident{langGo, "users", "Routes"}, "(users.Routes)"},
		{ident{langGo, "users", ""}, "(users)"},
		{ident{langGo, "", "Register"}, "(Register)"},
		{ident{langGo, "", ""}, ""},
		{ident{langJS, "server/routes/users", ""}, "(routes/users)"},
		{ident{langPy, "app/routers/users", "router"}, "(users.router)"},
	}
	for _, tc := range tests {
		if got := tc.id.label(); got != tc.want {
			t.Errorf("%+v.label() = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestReceiverBefore(t *testing.T) {
	tests := []struct{ code, want string }{
		{`r.Get("/x", h)`, "r"},
		{`r.With(auth).Get("/x", h)`, "r"},
		{`r.With(mw(a, b)).With(c).Get("/x", h)`, "r"},
		{`s.router.Get("/x", h)`, "router"},
		{`chi.NewRouter().Get("/x", h)`, "chi"},
		{`(x).Get("/x", h)`, ""},
		{`.Get("/x")`, ""},
	}
	for _, tc := range tests {
		dot := strings.Index(tc.code, ".Get(")
		if got := receiverBefore(tc.code, dot); got != tc.want {
			t.Errorf("receiverBefore(%q) = %q, want %q", tc.code, got, tc.want)
		}
	}
}
