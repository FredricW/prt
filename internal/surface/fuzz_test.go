package surface

import (
	"context"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// FuzzBuild feeds arbitrary paths, diffs and file contents through Build:
// it must never panic and must return a well-formed surface. Plain
// `go test` runs the seeds; `go test -fuzz FuzzBuild` explores.
func FuzzBuild(f *testing.F) {
	seeds := []struct{ path, diff, file string }{
		{"main.go", " func main() {\n+\tr.Get(\"/x\", h)\n }", "package main\nfunc main() {\n\tr.Route(\"/a\", func(r chi.Router) {\n"},
		{"app.js", "+app.use(\"/api\", require(\"./r\"));\n+router.get(\"/x\", h);", "const app = express();"},
		{"m/migrations/0001.sql", "+ALTER TABLE t\n+  ADD COLUMN c INT,\n+  DROP COLUMN d;", "-- +goose Down\n"},
		{"u/urls.py", "+    path(\"a/\", include(\"b.urls\")),", "from django.urls import path"},
		{"m/migrations/0002.py", "+        migrations.AddField(\n+            model_name=\"u\",\n", ""},
		{"web/a.tsx", "+  const x = isEnabled(\"f\") && flags.y && process.env.Z;", ""},
		{"x", "\x00\xff@@\n\\\n+\"`'/*", "\"\"\"\n"},
	}
	for _, s := range seeds {
		f.Add(s.path, s.diff, s.file, 1, 1)
	}
	f.Fuzz(func(t *testing.T, p, diffText, file string, oldStart, newStart int) {
		u := testUnit(p, "", diffText, oldStart, newStart)
		src := mapSource{revBase: {p: file}, revHead: {p: file}}
		in := Input{Units: []model.ChangeUnit{u}, Files: []model.File{{Path: p}}, Src: src, BaseSHA: revBase, HeadSHA: revHead,
			Labels: []model.Label{contract(u.ID, "env/flag", 0.7)}}
		checkWellFormed(t, build(context.Background(), in))
	})
}
