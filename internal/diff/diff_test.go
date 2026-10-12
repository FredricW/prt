package diff

import "testing"

const sample = `diff --git a/src/a.go b/src/a.go
index 1111111..2222222 100644
--- a/src/a.go
+++ b/src/a.go
@@ -1,4 +1,5 @@ package a
 package a
-func F(x int) int {
+func F(x int, y int) int {
+	// sum
 	return x
 }
diff --git a/old.txt b/new.txt
similarity index 90%
rename from old.txt
rename to new.txt
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
index 3333333..0000000
--- a/gone.txt
+++ /dev/null
@@ -1,2 +0,0 @@
-one
-two
\ No newline at end of file
diff --git a/img.png b/img.png
new file mode 100644
index 0000000..4444444
Binary files /dev/null and b/img.png differ
`

func TestParse(t *testing.T) {
	files, err := Parse(sample)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 4 {
		t.Fatalf("want 4 files, got %d", len(files))
	}
	a := files[0]
	if a.Path() != "src/a.go" || len(a.Hunks) != 1 {
		t.Fatalf("file a = %+v", a)
	}
	h := a.Hunks[0]
	if h.OldStart != 1 || h.NewLines != 5 || h.Section != "package a" {
		t.Fatalf("hunk = %+v", h)
	}
	if adds, dels := a.Counts(); adds != 2 || dels != 1 {
		t.Fatalf("counts = %d %d", adds, dels)
	}
	if h.Lines[2].NewNo != 2 || h.Lines[1].OldNo != 2 || h.Lines[4].OldNo != 3 || h.Lines[4].NewNo != 4 {
		t.Fatalf("line numbers wrong: %+v", h.Lines)
	}
	if r := files[1]; !r.Renamed || r.OldPath != "old.txt" || r.NewPath != "new.txt" || r.Similarity != 90 {
		t.Fatalf("rename = %+v", r)
	}
	if d := files[2]; !d.Deleted || d.Path() != "gone.txt" || !d.Hunks[0].Lines[1].NoEOL {
		t.Fatalf("delete = %+v", d)
	}
	if b := files[3]; !b.Binary || !b.Added || b.Path() != "img.png" {
		t.Fatalf("binary = %+v", b)
	}
}

func TestIntraLineAndPair(t *testing.T) {
	oldL, newL := "func F(x int) int {", "func F(x int, y int) int {"
	o, n := IntraLine(oldL, newL)
	if len(o) != 0 || len(n) != 1 || n[0].End-n[0].Start != 7 {
		t.Fatalf("spans: %v %v", o, n)
	}
	if newL[:n[0].Start]+newL[n[0].End:] != oldL {
		t.Fatalf("removing the inserted span must yield the old line: %v", n)
	}
	files, _ := Parse(sample)
	pairs := Pair(files[0].Hunks[0])
	if len(pairs) != 1 || pairs[0] != [2]int{1, 2} {
		t.Fatalf("pairs = %v", pairs)
	}
}
