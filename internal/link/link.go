// Package link is pipeline stage 2 (structural links) and the candidate half
// of stage 3 (embedding kNN):
//
//   - Structural: call edges between changed symbols (tree-sitter queries +
//     a symbol index per base SHA), imports, test-to-code by filename and
//     symbol, moved code (identical normalised bodies deleted in one place and
//     added in another), same-symbol edits across files. Source = structural.
//   - Embed: kNN over units of the same item, cosine ≥ embed_min_cosine or
//     top-K, whichever yields more. Units with identical shape hashes link
//     directly as mechanical candidates without embedding.
//
// TODO(M2): implement Structural and Embed.
package link

import (
	"context"

	"github.com/fredricw/prt/internal/model"
)

// Structural returns exact links between units. TODO(M2).
func Structural(_ context.Context, _ []model.ChangeUnit) ([]model.Link, error) {
	return nil, nil
}

// EmbedCandidates returns embed-neighbour and mechanical candidate links from
// unit vectors (keyed by unit id). TODO(M2).
func EmbedCandidates(_ []model.ChangeUnit, _ map[string][]float32, _ float64, _ int) []model.Link {
	return nil
}

// UnitText is the text embedded per unit: path + signature + diff lines.
func UnitText(u model.ChangeUnit) string {
	return u.Path + "\n" + u.Signature + "\n" + u.Diff
}
