package surface

import (
	"context"
	"testing"
)

// caseTest is one fixture case: testdata/<dir>/cases.txt section name, and
// every entry Build must return (in fmtEntry words).
type caseTest struct {
	dir, name string
	want      []string
}

// runCases builds each case without a Source and compares all entries.
func runCases(t *testing.T, cases []caseTest) {
	t.Helper()
	loaded := map[string]map[string]string{}
	for _, c := range cases {
		t.Run(c.dir+"/"+c.name, func(t *testing.T) {
			if loaded[c.dir] == nil {
				loaded[c.dir] = loadCases(t, c.dir)
			}
			text, ok := loaded[c.dir][c.name]
			if !ok {
				t.Fatalf("no case %q in testdata/%s/cases.txt", c.name, c.dir)
			}
			in := patchInput(t, text)
			in.Src = nil
			assertEntries(t, build(context.Background(), in), c.want)
		})
	}
}

func TestAPIEndpoints(t *testing.T) {
	runCases(t, []caseTest{
		{"nethttp", "added", []string{"added route: GET /healthz", "added route: POST /api/v1/items/{id}", "added route: ANY /metrics"}},
		{"nethttp", "removed", []string{"removed route: GET /healthz"}},
		{"nethttp", "changed", []string{"changed route: GET /v2/items", "changed route: PUT /orders"}},
		{"nethttp", "gorilla", []string{"added route: GET /api/users/{id}"}},
		{"nethttp", "negative", nil},

		{"chi", "added", []string{"added route: POST /api/v1/users", "added route: DELETE /api/v1/users/{id}",
			"added route: PATCH /api/v1/users/{id}", "added mount: /api/v1/billing"}},
		{"chi", "removed", []string{"removed route: GET /api/v1/health"}},
		{"chi", "changed", []string{"changed route: GET /api/v1/accounts", "changed route: PATCH /api/v1/users/{id}", "changed handler: GET /api/v1/me"}},
		{"chi", "negative", nil},

		{"gin", "added", []string{"added route: GET /api/v2/items", "added route: POST /api/v2/items", "added route: ANY /api/ping"}},
		{"gin", "removed", []string{"removed route: DELETE /api/items/:id"}},
		{"gin", "changed", []string{"changed route: GET /api/items/:id"}},
		{"gin", "negative", nil},

		{"echo", "added", []string{"added route: GET /admin/users", "added route: PUT /users/:id", "added route: PATCH /users/:id/role"}},
		{"echo", "removed", []string{"removed route: DELETE /users/:id"}},
		{"echo", "changed", []string{"changed route: GET /users/:id"}},
		{"echo", "negative", nil},

		{"express", "added", []string{"added route: GET /api/users/:id", "added route: POST /api/users",
			"added mount: /api", "added route: DELETE /sessions/:id"}},
		{"express", "removed", []string{"removed route: PUT /api/users/:id"}},
		{"express", "changed", []string{"changed route: GET /api/users/:id"}},
		{"express", "crossfile", []string{"added mount: /api/users", "added route: GET /api/users/:id", "added route: PATCH /api/users/:id"}},
		{"express", "negative", nil},

		{"fastify", "added", []string{"added route: GET /health", "added route: GET /items/:id", "added route: HEAD /items/:id",
			"added mount: /users", "added route: GET /users/:id"}},
		{"fastify", "removed", []string{"removed route: DELETE /items/:id"}},
		{"fastify", "changed", []string{"changed route: GET /items/:id"}},
		{"fastify", "negative", nil},

		{"next", "added", []string{"added route: GET /api/users/[id]", "added route: DELETE /api/users/[id]",
			"added route: ANY /api/health", "added route: GET /api/stats", "added route: POST /api/stats"}},
		{"next", "removed", []string{"removed route: POST /api/users/[id]"}},
		{"next", "changed", []string{"changed route: PATCH /api/users/[id]"}},
		{"next", "negative", nil},

		{"flask", "added", []string{"added route: GET /users/<int:id>", "added route: DELETE /users/<int:id>", "added route: POST /users/"}},
		{"flask", "removed", []string{"removed route: GET /users/<int:id>/avatar"}},
		{"flask", "changed", []string{"changed route: POST /login"}},
		{"flask", "negative", nil},

		{"fastapi", "added", []string{"added route: GET /items/{item_id}", "added route: PUT /items/{item_id}/tags", "added route: PATCH /items/{item_id}/tags"}},
		{"fastapi", "removed", []string{"removed route: DELETE /sessions/{sid}"}},
		{"fastapi", "changed", []string{"changed route: GET /items/{item_id}/owners"}},
		{"fastapi", "negative", nil},

		{"django", "added", []string{"added route: ANY /users/<int:pk>/", "added route: ANY /legacy/(?P<slug>[\\w-]+)/"}},
		{"django", "removed", []string{"removed route: ANY /users/<int:pk>/avatar/"}},
		{"django", "changed", []string{"changed route: ANY /me/"}},
		{"django", "negative", nil},
	})
}
