// Package parse is pipeline stage 1: it splits each diff hunk into change
// units at function boundaries using tree-sitter (TS/TSX/JS/Go/Python),
// attaches the enclosing symbol and signature, computes a shape hash
// (whitespace and identifiers normalised to $1, $2, …) for mechanical
// detection, and marks pure-formatting units by comparing token streams.
//
// Unknown languages fall back to one unit per hunk with no symbol.
//
// TODO(M1): implement Split with tree-sitter. Units currently come from
// Split's fallback (one unit per hunk).
package parse

import (
	"context"
	"path"
	"strings"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/model"
)

// Source provides file contents at a revision (gitx.Git satisfies it).
type Source interface {
	Show(ctx context.Context, rev, path string) ([]byte, error)
}

// Input is everything stage 1 needs for one item.
type Input struct {
	ItemID  string
	BaseSHA string
	HeadSHA string
	Files   []diff.File
	Src     Source // may be nil: no symbol attachment
}

// Lang maps a path to a tree-sitter language name, or "" if unsupported.
func Lang(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".go":
		return "go"
	case ".ts", ".mts", ".cts":
		return "ts"
	case ".tsx":
		return "tsx"
	case ".js", ".mjs", ".cjs", ".jsx":
		return "js"
	case ".py":
		return "python"
	}
	return ""
}

// Split turns parsed diff files into change units. The fallback emits one
// unit per hunk with content-based ids.
func Split(_ context.Context, in Input) ([]model.ChangeUnit, error) {
	var units []model.ChangeUnit
	for _, f := range in.Files {
		for _, h := range f.Hunks {
			body := h.Body()
			adds, dels := h.Counts()
			kind := model.UnitModified
			switch {
			case dels == 0:
				kind = model.UnitAdded
			case adds == 0:
				kind = model.UnitDeleted
			}
			units = append(units, model.ChangeUnit{
				ID:     model.UnitID(f.Path(), body),
				ItemID: in.ItemID,
				FileID: f.Path(),
				Path:   f.Path(),
				Range: model.Range{
					Old: model.Span{Start: h.OldStart, Count: h.OldLines},
					New: model.Span{Start: h.NewStart, Count: h.NewLines},
				},
				Symbol: h.Section,
				Diff:   body,
				Kind:   kind,
				Adds:   adds,
				Dels:   dels,
				Order:  len(units),
			})
		}
	}
	return units, nil
}
