package surface

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// fixtureDir is the lead's fixture tree (base files and PR patches).
var fixtureDir = filepath.Join("..", "..", "testdata", "fixtures")

// fixtureInput builds the Input of a fixture PR patch with a Source over
// the fixture base tree (head = base with the patch applied).
func fixtureInput(t *testing.T, patch string) Input {
	t.Helper()
	text := readFile(t, filepath.Join(fixtureDir, "prs", patch))
	files := parsePatch(t, text)
	in := patchInput(t, text)
	in.Src = treeSource(t, filepath.Join(fixtureDir, "base"), files)
	return in
}

// TestFixturePR106 checks the lead's fixture PR #106 (user preferences API):
// the removed and added endpoints, the migration's schema changes and the
// new flag and environment variables.
func TestFixturePR106(t *testing.T) {
	cs := build(context.Background(), fixtureInput(t, "06-user-preferences-api.patch"))
	t.Logf("surface:\n%s", strings.Join(allEntries(cs), "\n"))

	health := find(t, cs, "removed route: GET /api/v1/health")
	if health.Path != "server/cmd/api/main.go" || health.Side != "old" || health.Line != 25 || health.Framework != fwChi {
		t.Errorf("health = %+v, want server/cmd/api/main.go:25 old side, chi", health)
	}
	for _, m := range []string{"GET", "PUT"} {
		e := find(t, cs, "added route: "+m+" /api/v1/users/{id}/preferences")
		if e.Path != "server/internal/users/users.go" || e.Side != "" || e.Detail != "" {
			t.Errorf("%s preferences = %+v, want users.go new side, no prefix note", m, e)
		}
	}
	if e := find(t, cs, "added route: GET /healthz"); e.Framework != fwNetHTTP || e.Path != "server/cmd/api/main.go" {
		t.Errorf("healthz = %+v, want net/http in main.go", e)
	}
	mig := "server/migrations/20261010_user_preferences.sql"
	for _, w := range []string{
		"added table: table user_preferences",
		"added index: index user_preferences_theme_idx",
		"added column: users.avatar_url",
		"removed column: users.legacy_theme",
	} {
		if e := find(t, cs, w); e.Path != mig {
			t.Errorf("%s at %s, want %s", w, e.Path, mig)
		}
	}
	if e := find(t, cs, "removed column: users.legacy_theme"); !strings.Contains(e.Detail, detailOneWay) {
		t.Errorf("legacy_theme detail = %q, want one-way", e.Detail)
	}
	if e := find(t, cs, "added flag: user-preferences-v2"); e.Path != "web/src/components/SettingsForm.tsx" || e.Framework != fwCustom {
		t.Errorf("flag = %+v, want SettingsForm.tsx, custom", e)
	}
	for _, env := range []string{"PORTAL_PREFERENCES_API", "PORTAL_OPS_ADDR"} {
		if e := find(t, cs, "added env: "+env); e.Path != "server/cmd/api/main.go" {
			t.Errorf("%s at %s, want main.go", env, e.Path)
		}
	}
	// Nothing in the test file, and no client calls as endpoints.
	for _, es := range [][]model.SurfaceEntry{cs.API, cs.DB, cs.Flags} {
		for _, e := range es {
			if strings.HasSuffix(e.Path, "_test.go") || strings.HasPrefix(e.Path, "web/src/api/") {
				t.Errorf("unexpected entry %s at %s", fmtEntry(e), e.Path)
			}
		}
	}
	if cs.Note != "" {
		t.Errorf("note = %q, want none (every file readable)", cs.Note)
	}
}

// TestFixturePR101 checks fixture PR #101: the avatarUrl field of the user
// response is a response-shape change, and the web client's fetch calls
// are not endpoints.
func TestFixturePR101(t *testing.T) {
	cs := build(context.Background(), fixtureInput(t, "01-user-profile-refactor.patch"))
	t.Logf("surface:\n%s", strings.Join(allEntries(cs), "\n"))
	e := find(t, cs, "added response shape: userResponse.avatarUrl")
	if e.Path != "server/internal/users/users.go" || e.Area != model.SurfaceAPI || e.Confidence >= confRouteExact {
		t.Errorf("avatarUrl = %+v, want an API entry in users.go with heuristic confidence", e)
	}
	for _, x := range cs.API {
		if x.What == whatRoute || x.What == whatMount || x.What == whatHandler {
			t.Errorf("unexpected endpoint %s at %s", fmtEntry(x), x.Path)
		}
	}
	if len(cs.DB) != 0 || len(cs.Flags) != 0 {
		t.Errorf("db/flags = %v / %v, want none", cs.DB, cs.Flags)
	}
}
