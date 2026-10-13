package surface

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

// bigInput is a synthetic item: files Go, TS and SQL files of lines changed
// lines each, a quarter of them surface-shaped, with a Source for the Go
// files.
func bigInput(files, lines int) Input {
	var in Input
	src := mapSource{revBase: {}, revHead: {}}
	for f := range files {
		var b, full strings.Builder
		var p string
		switch f % 3 {
		case 0:
			p = fmt.Sprintf("server/pkg%d/routes.go", f)
			full.WriteString("func Routes(r chi.Router) {\n")
			b.WriteString(" func Routes(r chi.Router) {\n")
			for i := range lines {
				l := fmt.Sprintf("\tx%d := compute(%d)", i, i)
				if i%4 == 0 {
					l = fmt.Sprintf("\tr.Get(\"/items/%d/%d\", h%d)", f, i, i)
				}
				b.WriteString("+" + l + "\n")
				full.WriteString(l + "\n")
			}
			full.WriteString("}\n")
		case 1:
			p = fmt.Sprintf("web/src/mod%d.ts", f)
			for i := range lines {
				l := fmt.Sprintf("const v%d = compute(%d);", i, i)
				if i%4 == 0 {
					l = fmt.Sprintf("const f%d = isEnabled(\"flag-%d-%d\") && process.env.ENV_%d_%d;", i, f, i, f, i)
				}
				b.WriteString("+" + l + "\n")
				full.WriteString(l + "\n")
			}
		default:
			p = fmt.Sprintf("db/migrations/%04d_x.sql", f)
			for i := range lines {
				l := fmt.Sprintf("INSERT INTO t VALUES (%d);", i)
				if i%4 == 0 {
					l = fmt.Sprintf("ALTER TABLE t%d ADD COLUMN c%d TEXT;", f, i)
				}
				b.WriteString("+" + l + "\n")
				full.WriteString(l + "\n")
			}
		}
		src[revHead][p] = full.String()
		u := testUnit(p, "", b.String(), 0, 1)
		in.Units = append(in.Units, u)
		in.Files = append(in.Files, model.File{Path: p, Added: true})
	}
	in.Src, in.BaseSHA, in.HeadSHA = src, revBase, revHead
	return in
}

func BenchmarkBuild(b *testing.B) {
	in := bigInput(300, 200)
	b.ResetTimer()
	for b.Loop() {
		Build(context.Background(), in)
	}
}

// TestBigItem checks a large item is detected in full (no cap is hit at
// this size).
func TestBigItem(t *testing.T) {
	cs := build(context.Background(), bigInput(30, 40))
	if len(cs.API) != 10*10 || len(cs.DB) != 10*10 || len(cs.Flags) != 10*10*2 {
		t.Errorf("api/db/flags = %d/%d/%d, want 100/100/200", len(cs.API), len(cs.DB), len(cs.Flags))
	}
	if cs.Note != "" {
		t.Errorf("note = %q, want none", cs.Note)
	}
}
