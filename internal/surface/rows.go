package surface

import (
	"strconv"

	"github.com/fredricw/prt/internal/model"
)

// Row is one display row of a surface entry for the Overview (the TUI
// styles and caps the rows; this package decides their words).
type Row struct {
	// Glyph marks the change: "+" added, "~" changed, "−" removed.
	Glyph string
	// Text is the entry in words: "GET /api/v1/users/{id}/preferences",
	// "table user_preferences", "flag user-preferences-v2", followed by its
	// detail ("users.legacy_theme · one-way").
	Text string
	// Loc is "path:line".
	Loc string
	// Confidence is the entry's confidence (shown as ".86", "?" below .60).
	Confidence float64
	UnitID     string
}

// Rows returns the display rows of entries, in the given (importance)
// order.
func Rows(entries []model.SurfaceEntry) []Row {
	out := make([]Row, 0, len(entries))
	for _, e := range entries {
		out = append(out, Row{Glyph: glyph(e.Change), Text: rowText(e), Loc: loc(e), Confidence: e.Confidence, UnitID: e.UnitID})
	}
	return out
}

// Empty is the explicit empty-state line of an area's block ("no endpoint
// changes"), so absence reads as "checked".
func Empty(area string) string {
	switch area {
	case model.SurfaceAPI:
		return "no endpoint changes"
	case model.SurfaceDB:
		return "no schema or migration changes"
	case model.SurfaceFlag:
		return "no flag, env or config changes"
	}
	return "nothing found"
}

// rowText words an entry: the name, prefixed by its kind where the name
// alone does not say it, then the detail.
func rowText(e model.SurfaceEntry) string {
	t := e.Name
	switch {
	case e.Source == model.SurfaceFromJev:
		t = "contract change: " + e.Name
	case e.What == whatMount:
		t = "mount " + e.Name
	case e.What == whatResponse:
		t = "response field " + e.Name
	case e.What == whatRequest:
		t = "request field " + e.Name
	case e.Area == model.SurfaceFlag && e.What != "":
		t = e.What + " " + e.Name
	}
	if e.Detail != "" {
		t += " · " + e.Detail
	}
	return t
}

func glyph(change string) string {
	switch change {
	case model.SurfaceAdded:
		return "+"
	case model.SurfaceRemoved:
		return "−"
	}
	return "~"
}

func loc(e model.SurfaceEntry) string {
	if e.Path == "" {
		return ""
	}
	if e.Line <= 0 {
		return e.Path
	}
	return e.Path + ":" + strconv.Itoa(e.Line)
}
