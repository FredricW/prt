package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/fredricw/prt/internal/model"
)

// CurrentRunLogs implements Store. It walks the item's rows newest first
// (runlog_item) and stops at the first fetch row, so it reads one run's
// rows however long the log is.
func (s *SQLite) CurrentRunLogs(ctx context.Context, itemID string) ([]model.RunLog, error) {
	rows, err := s.rd.QueryContext(ctx, `SELECT body FROM runlog WHERE item_id=? ORDER BY seq DESC`, itemID)
	if err != nil {
		return nil, fmt.Errorf("store: run log of %s: %w", itemID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []model.RunLog
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, fmt.Errorf("store: run log of %s: %w", itemID, err)
		}
		var r model.RunLog
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("store: decode run log of %s: %w", itemID, err)
		}
		out = append(out, r)
		if r.Stage == model.StageFetch {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: run log of %s: %w", itemID, err)
	}
	slices.Reverse(out)
	return out, nil
}

// PruneRunLogs implements Store.
func (s *SQLite) PruneRunLogs(ctx context.Context, itemID string, keep int) (int, error) {
	cut, err := s.runCutoff(ctx, itemID, max(keep, 1))
	if err != nil || cut == 0 {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM runlog WHERE item_id=? AND seq<?`, itemID, cut)
	if err != nil {
		return 0, fmt.Errorf("store: prune run log of %s: %w", itemID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("store: prune run log of %s: %w", itemID, err)
	}
	return int(n), nil
}

// runCutoff is the seq of the item's keep-th newest fetch row (the first
// row of the oldest run kept), or 0 when the log holds keep runs or fewer.
func (s *SQLite) runCutoff(ctx context.Context, itemID string, keep int) (int64, error) {
	rows, err := s.rd.QueryContext(ctx, `SELECT seq, body FROM runlog WHERE item_id=? ORDER BY seq DESC`, itemID)
	if err != nil {
		return 0, fmt.Errorf("store: run log of %s: %w", itemID, err)
	}
	defer func() { _ = rows.Close() }()
	fetches := 0
	for rows.Next() {
		var seq int64
		var b []byte
		if err := rows.Scan(&seq, &b); err != nil {
			return 0, fmt.Errorf("store: run log of %s: %w", itemID, err)
		}
		var head struct {
			Stage model.Stage `json:"stage"`
		}
		if err := json.Unmarshal(b, &head); err != nil {
			return 0, fmt.Errorf("store: decode run log of %s: %w", itemID, err)
		}
		if head.Stage != model.StageFetch {
			continue
		}
		if fetches++; fetches == keep {
			return seq, nil
		}
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("store: run log of %s: %w", itemID, err)
	}
	return 0, nil
}
