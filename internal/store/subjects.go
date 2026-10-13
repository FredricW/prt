package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Label subject prefixes (the model's *Subject constructors); a subject
// with none of them is a unit id.
const (
	prefixItem    = "item:"        // model.ItemSubject
	prefixFile    = "file:"        // model.FileSubject
	prefixGroup   = "group:"       // model.GroupSubject
	prefixComment = "comment:"     // model.CommentSubject
	prefixBranch  = "branchstate:" // model.BranchSubject
	prefixPair    = "pair:"        // model.PairSubject
)

// pruneBatch is how many subjects one DELETE names (well under SQLite's
// bound-parameter limit).
const pruneBatch = 500

// ItemSubjects implements Store. Unit ids and file paths are the suffixes
// of the unit and file docs' ids ("<item>/<unit id>", "<item>/<path>"),
// which docs_kind_parent_id covers, so no document body is read.
func (s *SQLite) ItemSubjects(ctx context.Context, itemID string) ([]string, error) {
	rows, err := s.rd.QueryContext(ctx, `SELECT kind, id FROM docs WHERE kind IN (?,?) AND parent=?`, kUnit, kFile, itemID)
	if err != nil {
		return nil, fmt.Errorf("store: subjects of %s: %w", itemID, err)
	}
	defer func() { _ = rows.Close() }()
	var units, files []string
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			return nil, fmt.Errorf("store: subjects of %s: %w", itemID, err)
		}
		rest, ok := strings.CutPrefix(id, itemID+"/")
		if !ok {
			continue
		}
		if kind == kUnit {
			units = append(units, rest)
		} else {
			files = append(files, model.FileSubject(itemID, rest))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: subjects of %s: %w", itemID, err)
	}
	slices.Sort(units)
	slices.Sort(files)
	return append(append(units, files...), model.ItemSubject(itemID)), nil
}

// PruneLabels implements Store. It reads what is referenced and deletes in
// one write transaction, so no write of this process lands in between.
func (s *SQLite) PruneLabels(ctx context.Context, subjects []string) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: prune labels: %w", err)
	}
	n, err := pruneLabels(ctx, tx, subjects)
	if err != nil {
		_ = tx.Rollback()
		return 0, fmt.Errorf("store: prune labels: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: prune labels: %w", err)
	}
	return n, nil
}

func pruneLabels(ctx context.Context, tx *sql.Tx, subjects []string) (int, error) {
	refs, err := loadRefs(ctx, tx)
	if err != nil {
		return 0, err
	}
	cands, err := pruneCandidates(ctx, tx, subjects)
	if err != nil {
		return 0, err
	}
	var dead []string
	for _, c := range cands {
		if !refs.references(c) {
			dead = append(dead, c)
		}
	}
	return deleteLabels(ctx, tx, dead)
}

// pruneCandidates are the label subjects PruneLabels considers: every
// subject with a stored label (subjects nil), or subjects plus the stored
// pair subjects that name one of them.
func pruneCandidates(ctx context.Context, tx *sql.Tx, subjects []string) ([]string, error) {
	if subjects == nil {
		return queryStrings(ctx, tx, `SELECT DISTINCT parent FROM docs WHERE kind=?`, kLabel)
	}
	pairs, err := queryStrings(ctx, tx, `SELECT DISTINCT parent FROM docs WHERE kind=? AND parent >= ? AND parent < ?`,
		kLabel, prefixPair, "pair;") // ';' follows ':': every "pair:…" subject
	if err != nil {
		return nil, err
	}
	named := make(map[string]bool, len(subjects))
	for _, s := range subjects {
		named[s] = true
	}
	out := slices.Clone(subjects)
	for _, p := range pairs {
		if a, b, ok := strings.Cut(strings.TrimPrefix(p, prefixPair), ":"); ok && (named[a] || named[b]) {
			out = append(out, p)
		}
	}
	return out, nil
}

// labelRefs is every label subject a stored item references: items (item
// and branch-state subjects), unit ids (unit and pair subjects), file and
// group subjects of stored files and groups, and comment subjects of
// stored comments (comments outlive their item, so their labels do too).
type labelRefs struct {
	items, units, files, groups, comments map[string]bool
}

// loadRefs reads labelRefs from document ids only (index-covered).
func loadRefs(ctx context.Context, tx *sql.Tx) (labelRefs, error) {
	r := labelRefs{items: map[string]bool{}, units: map[string]bool{}, files: map[string]bool{},
		groups: map[string]bool{}, comments: map[string]bool{}}
	items, err := queryStrings(ctx, tx, `SELECT id FROM docs WHERE kind=?`, kItem)
	if err != nil {
		return r, err
	}
	for _, id := range items {
		r.items[id] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind, parent, id FROM docs WHERE kind IN (?,?,?,?)`, kUnit, kFile, kGroup, kComment)
	if err != nil {
		return r, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind, parent, id string
		if err := rows.Scan(&kind, &parent, &id); err != nil {
			return r, err
		}
		r.add(kind, parent, id)
	}
	return r, rows.Err()
}

// add records one unit, file, group or comment doc.
func (r labelRefs) add(kind, parent, id string) {
	if kind == kComment {
		r.comments[model.CommentSubject(parent, id)] = true
		return
	}
	rest, ok := strings.CutPrefix(id, parent+"/")
	if !ok {
		return
	}
	switch kind {
	case kUnit:
		r.units[rest] = true
	case kFile:
		r.files[model.FileSubject(parent, rest)] = true
	case kGroup:
		r.groups[rest] = true
	}
}

// references reports whether a stored item still references subject.
func (r labelRefs) references(subject string) bool {
	if rest, ok := strings.CutPrefix(subject, prefixItem); ok {
		return r.items[rest]
	}
	if rest, ok := strings.CutPrefix(subject, prefixBranch); ok {
		return r.items[rest]
	}
	if rest, ok := strings.CutPrefix(subject, prefixGroup); ok {
		return r.groups[rest]
	}
	if rest, ok := strings.CutPrefix(subject, prefixPair); ok {
		a, b, _ := strings.Cut(rest, ":")
		return r.units[a] && r.units[b]
	}
	switch {
	case strings.HasPrefix(subject, prefixFile):
		return r.files[subject]
	case strings.HasPrefix(subject, prefixComment):
		return r.comments[subject]
	}
	return r.units[subject]
}

// deleteLabels deletes every label of subjects, in batches.
func deleteLabels(ctx context.Context, tx *sql.Tx, subjects []string) (int, error) {
	total := 0
	for chunk := range slices.Chunk(subjects, pruneBatch) {
		args := make([]any, 0, len(chunk)+1)
		args = append(args, kLabel)
		for _, s := range chunk {
			args = append(args, s)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM docs WHERE kind=? AND parent IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return total, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return total, err
		}
		total += int(n)
	}
	return total, nil
}

// queryStrings runs a query returning one text column.
func queryStrings(ctx context.Context, tx *sql.Tx, q string, args ...any) ([]string, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
