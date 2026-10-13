package diffview

import (
	"strconv"

	"github.com/fredricw/prt/internal/diff"
)

// rowKind is what a logical row shows.
type rowKind uint8

const (
	kindFile  rowKind = iota // file header: path, markers, counts
	kindHunk                 // "@@ -a,b +c,d @@ section"
	kindLine                 // one diff line with old/new line numbers
	kindNoEOL                // "\ No newline at end of file" after a line
	kindRaw                  // fallback: one raw text line
)

// row is one logical row of the viewer. It holds only indices into the
// parsed files; strings are produced on render.
type row struct {
	kind rowKind
	file int32 // index into Model.files
	hunk int32 // index into File.Hunks
	line int32 // index into Hunk.Lines; raw line index for kindRaw
	pair int32 // row index of the paired del/add line, -1 when unpaired
}

// fileMeta caches per-file totals so headers render in O(1).
type fileMeta struct {
	adds, dels int
}

// index is the result of buildIndex.
type index struct {
	rows   []row
	heads  []int
	meta   []fileMeta
	gutter int
}

// buildIndex lays out files as rows in one pass over every line, and links
// paired deleted/added lines (diff.Pair) for intra-line highlighting.
func buildIndex(files []diff.File) index {
	idx := index{
		rows:  make([]row, 0, countRows(files)),
		heads: make([]int, 0, len(files)),
		meta:  make([]fileMeta, len(files)),
	}
	maxNo := 0
	for fi, f := range files {
		idx.heads = append(idx.heads, len(idx.rows))
		idx.rows = append(idx.rows, row{kind: kindFile, file: int32(fi), pair: -1})
		for hi, h := range f.Hunks {
			idx.rows = append(idx.rows, row{kind: kindHunk, file: int32(fi), hunk: int32(hi), pair: -1})
			lineRow := make([]int32, len(h.Lines))
			for li, l := range h.Lines {
				lineRow[li] = int32(len(idx.rows))
				idx.rows = append(idx.rows, row{kind: kindLine, file: int32(fi), hunk: int32(hi), line: int32(li), pair: -1})
				if l.NoEOL {
					idx.rows = append(idx.rows, row{kind: kindNoEOL, file: int32(fi), hunk: int32(hi), line: int32(li), pair: -1})
				}
				maxNo = max(maxNo, l.OldNo, l.NewNo)
				countOp(&idx.meta[fi], l.Op)
			}
			for _, p := range diff.Pair(h) {
				a, b := lineRow[p[0]], lineRow[p[1]]
				idx.rows[a].pair, idx.rows[b].pair = b, a
			}
		}
	}
	idx.gutter = len(strconv.Itoa(maxNo))
	return idx
}

func countOp(fm *fileMeta, op diff.Op) {
	switch op {
	case diff.Add:
		fm.adds++
	case diff.Del:
		fm.dels++
	}
}

// countRows returns the exact number of rows buildIndex will produce.
func countRows(files []diff.File) int {
	n := len(files)
	for _, f := range files {
		n += len(f.Hunks)
		for _, h := range f.Hunks {
			n += len(h.Lines)
			for _, l := range h.Lines {
				if l.NoEOL {
					n++
				}
			}
		}
	}
	return n
}

// rawRows indexes n fallback raw lines.
func rawRows(n int) []row {
	rows := make([]row, n)
	for i := range rows {
		rows[i] = row{kind: kindRaw, line: int32(i), pair: -1}
	}
	return rows
}
