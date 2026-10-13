package surface

import (
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestRows(t *testing.T) {
	es := []model.SurfaceEntry{
		{Area: model.SurfaceAPI, Change: model.SurfaceRemoved, What: whatRoute, Name: "GET /api/v1/health", Path: "main.go", Line: 25, Side: "old", Confidence: 0.97, UnitID: "u1"},
		{Area: model.SurfaceAPI, Change: model.SurfaceAdded, What: whatMount, Name: "/api/v1/billing", Path: "main.go", Line: 30, Confidence: 0.9, Detail: "→ billing.Routes"},
		{Area: model.SurfaceAPI, Change: model.SurfaceChanged, What: whatResponse, Name: "userResponse.avatarUrl", Path: "users.go", Confidence: 0.7},
		{Area: model.SurfaceAPI, Change: model.SurfaceAdded, What: whatRequest, Name: "createUserRequest.email", Confidence: 0.65},
		{Area: model.SurfaceDB, Change: model.SurfaceRemoved, What: whatColumn, Name: "users.legacy_theme", Path: "m.sql", Line: 14, Confidence: 0.97, Detail: "one-way"},
		{Area: model.SurfaceFlag, Change: model.SurfaceAdded, What: whatFlag, Name: "user-preferences-v2", Path: "Form.tsx", Line: 12, Confidence: 0.95},
		{Area: model.SurfaceFlag, Change: model.SurfaceChanged, What: whatEnv, Name: "NEW_NAME", Path: "main.go", Line: 3, Detail: "was OLD_NAME"},
		{Area: model.SurfaceFlag, Change: model.SurfaceChanged, What: whatContract, Name: "Store.Migrate", Path: "s.go", Line: 21, Source: model.SurfaceFromJev, Confidence: 0.7},
	}
	want := []Row{
		{Glyph: "−", Text: "GET /api/v1/health", Loc: "main.go:25", Confidence: 0.97, UnitID: "u1"},
		{Glyph: "+", Text: "mount /api/v1/billing · → billing.Routes", Loc: "main.go:30", Confidence: 0.9},
		{Glyph: "~", Text: "response field userResponse.avatarUrl", Loc: "users.go", Confidence: 0.7},
		{Glyph: "+", Text: "request field createUserRequest.email", Loc: "", Confidence: 0.65},
		{Glyph: "−", Text: "users.legacy_theme · one-way", Loc: "m.sql:14", Confidence: 0.97},
		{Glyph: "+", Text: "flag user-preferences-v2", Loc: "Form.tsx:12", Confidence: 0.95},
		{Glyph: "~", Text: "env NEW_NAME · was OLD_NAME", Loc: "main.go:3"},
		{Glyph: "~", Text: "contract change: Store.Migrate", Loc: "s.go:21", Confidence: 0.7},
	}
	got := Rows(es)
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v\n        want %+v", i, got[i], want[i])
		}
	}
	if rows := Rows(nil); rows == nil || len(rows) != 0 {
		t.Errorf("Rows(nil) = %v, want empty", rows)
	}
}

func TestEmpty(t *testing.T) {
	tests := map[string]string{
		model.SurfaceAPI:  "no endpoint changes",
		model.SurfaceDB:   "no schema or migration changes",
		model.SurfaceFlag: "no flag, env or config changes",
		"other":           "nothing found",
	}
	for area, want := range tests {
		if got := Empty(area); got != want {
			t.Errorf("Empty(%q) = %q, want %q", area, got, want)
		}
	}
}
