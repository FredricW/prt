package model

// Change surface areas (SurfaceEntry.Area): the Overview's structured
// change blocks (M5.2, user request): "API endpoints", "Database" and
// "Feature flags & config".
const (
	SurfaceAPI  = "api"
	SurfaceDB   = "db"
	SurfaceFlag = "flag"
)

// Change kinds of a surface entry (SurfaceEntry.Change), in importance
// order: a removal breaks callers, a change may, an addition rarely does.
const (
	SurfaceRemoved = "removed"
	SurfaceChanged = "changed"
	SurfaceAdded   = "added"
)

// Sources of a surface entry (SurfaceEntry.Source).
const (
	// SurfaceFromCode: found by code detection in the diff (a route
	// registration, a DDL statement, a flag lookup with a literal key).
	SurfaceFromCode = "code"
	// SurfaceFromJev: only a unit's Jev `contract` label (api shape,
	// schema/migration, env/flag) says so; Confidence is that label's P.
	SurfaceFromJev = "jev"
	// SurfaceFromBoth: code detection, confirmed by the unit's contract label.
	SurfaceFromBoth = "code+jev"
)

// SurfaceEntry is one structured change of an item: an endpoint added,
// changed or removed, a table or column, a feature flag, an environment
// variable or a config key. Built by internal/surface in stage 8.
type SurfaceEntry struct {
	Area   string `json:"area"`   // SurfaceAPI, SurfaceDB or SurfaceFlag
	Change string `json:"change"` // SurfaceAdded, SurfaceChanged or SurfaceRemoved
	// What names the kind of thing: "route", "handler", "response shape"
	// (api); "migration", "table", "column", "index", "schema" (db);
	// "flag", "env", "config key" (flag).
	What string `json:"what"`
	// Name is what the reviewer recognises: "GET /api/v1/users/{id}",
	// "users.avatar_url", "user-preferences-v2", "PORTAL_OPS_ADDR".
	Name string `json:"name"`
	// Path and Line locate the entry (new side; old side when Side is
	// "old", for a removal that has no new line).
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side,omitempty"`
	// UnitID is the change unit the entry sits in ("" when none matched);
	// `enter` on the row jumps to it.
	UnitID string `json:"unitId,omitempty"`
	// Confidence is 0..1: about 1 for an exact code match, lower for a
	// heuristic, the label's P for a Jev-only entry.
	Confidence float64 `json:"confidence"`
	Source     string  `json:"source"` // SurfaceFromCode, SurfaceFromJev, SurfaceFromBoth
	// Framework is the detector that matched ("chi", "net/http", "express",
	// "next", "fastapi", "django", "sql", "alembic", "launchdarkly", "env",
	// ...); "" for Jev-only entries.
	Framework string `json:"framework,omitempty"`
	// Detail is a short extra fact ("was getFlag(\"x\")", "NOT NULL, no
	// default", "mounted under /api/v1").
	Detail string `json:"detail,omitempty"`
}

// ChangeSurface is the Overview's structured change record (ItemAgg.Surface):
// one list per area, each sorted by importance (removed, changed, added; then
// confidence, path and line), never nil once computed, so an empty list
// means "checked, nothing found" ("no endpoint changes").
type ChangeSurface struct {
	API   []SurfaceEntry `json:"api"`
	DB    []SurfaceEntry `json:"db"`
	Flags []SurfaceEntry `json:"flags"`
	// Note explains a degraded detection ("head not readable: diff only").
	Note string `json:"note,omitempty"`
}
