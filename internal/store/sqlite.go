package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/fredricw/prt/internal/model"
)

// SQLite implements Store. Entities are stored as JSON documents in a single
// "docs" table keyed by (kind, id) with a "parent" column for listing; this
// keeps migrations trivial while the model evolves.
type SQLite struct {
	db *sql.DB
}

var _ Store = (*SQLite)(nil)

// migrations are applied in order; PRAGMA user_version records progress.
var migrations = []string{
	`CREATE TABLE docs (
		kind TEXT NOT NULL,
		id TEXT NOT NULL,
		parent TEXT NOT NULL DEFAULT '',
		body BLOB NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (kind, id)
	);
	CREATE INDEX docs_parent ON docs(kind, parent);
	CREATE TABLE cache (
		ns TEXT NOT NULL,
		key TEXT NOT NULL,
		val BLOB NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (ns, key)
	);
	CREATE TABLE runlog (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		item_id TEXT NOT NULL,
		body BLOB NOT NULL
	);
	CREATE INDEX runlog_item ON runlog(item_id, seq);`,
}

// Open opens (creating if needed) the store at path.
func Open(ctx context.Context, path string) (*SQLite, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return open(ctx, "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
}

// OpenMemory opens a private in-memory store (tests, --no-daemon dry runs).
func OpenMemory(ctx context.Context) (*SQLite, error) { return open(ctx, ":memory:") }

func open(ctx context.Context, dsn string) (*SQLite, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection: SQLite serialises writes anyway, and :memory: databases
	// are per-connection.
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLite) migrate(ctx context.Context) error {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("store: read version: %w", err)
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("store: migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(i+1)); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Close implements Store.
func (s *SQLite) Close() error { return s.db.Close() }

// Document kinds.
const (
	kItem     = "item"
	kFetch    = "fetch"
	kFile     = "file"
	kUnit     = "unit"
	kLink     = "link"
	kGroup    = "group"
	kAgg      = "agg"
	kLabel    = "label"
	kReview   = "review"
	kComment  = "comment"
	kOverride = "override"
)

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func putDoc(ctx context.Context, e execer, kind, id, parent string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = e.ExecContext(ctx,
		`INSERT INTO docs(kind, id, parent, body, updated_at) VALUES(?,?,?,?,?)
		 ON CONFLICT(kind, id) DO UPDATE SET parent=excluded.parent, body=excluded.body, updated_at=excluded.updated_at`,
		kind, id, parent, b, time.Now().UnixMilli())
	return err
}

func getDoc[T any](ctx context.Context, s *SQLite, kind, id string) (T, bool, error) {
	var zero T
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT body FROM docs WHERE kind=? AND id=?`, kind, id).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, false, err
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return zero, false, fmt.Errorf("store: decode %s/%s: %w", kind, id, err)
	}
	return v, true, nil
}

// listDocs returns docs of kind whose parent is one of parents (all docs of
// kind when parents is nil), ordered by id.
func listDocs[T any](ctx context.Context, s *SQLite, kind string, parents []string) ([]T, error) {
	q := `SELECT body FROM docs WHERE kind=?`
	args := []any{kind}
	if parents != nil {
		if len(parents) == 0 {
			return nil, nil
		}
		q += ` AND parent IN (` + strings.TrimSuffix(strings.Repeat("?,", len(parents)), ",") + `)`
		for _, p := range parents {
			args = append(args, p)
		}
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []T
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var v T
		if err := json.Unmarshal(b, &v); err != nil {
			return nil, fmt.Errorf("store: decode %s: %w", kind, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// replaceDocs deletes all docs of kind under parent and inserts the new set.
func replaceDocs[T any](ctx context.Context, s *SQLite, kind, parent string, xs []T, id func(int, T) string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM docs WHERE kind=? AND parent=?`, kind, parent); err != nil {
		_ = tx.Rollback()
		return err
	}
	for i, x := range xs {
		if err := putDoc(ctx, tx, kind, id(i, x), parent, x); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func one[T any](v T, ok bool, err error) (T, error) {
	if err != nil {
		return v, err
	}
	if !ok {
		return v, ErrNotFound
	}
	return v, nil
}

// UpsertItem implements Store.
func (s *SQLite) UpsertItem(ctx context.Context, it model.Item) error {
	return putDoc(ctx, s.db, kItem, it.ID, "", it)
}

// Item implements Store.
func (s *SQLite) Item(ctx context.Context, id string) (model.Item, error) {
	return one(getDoc[model.Item](ctx, s, kItem, id))
}

// Items implements Store.
func (s *SQLite) Items(ctx context.Context) ([]model.Item, error) {
	return listDocs[model.Item](ctx, s, kItem, nil)
}

// RemoveItem implements Store. It removes the item and its per-item outputs;
// unit-keyed data (labels, embeddings, overrides) stays as a cache.
func (s *SQLite) RemoveItem(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM docs WHERE (kind=? AND id=?) OR (kind IN (?,?,?,?,?,?) AND parent=?)`,
		kItem, id, kFetch, kFile, kUnit, kLink, kGroup, kAgg, id); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// PutFetch implements Store.
func (s *SQLite) PutFetch(ctx context.Context, f model.Fetch) error {
	return putDoc(ctx, s.db, kFetch, f.ItemID+"@"+f.HeadSHA, f.ItemID, f)
}

// Fetch implements Store.
func (s *SQLite) Fetch(ctx context.Context, itemID, headSHA string) (model.Fetch, bool, error) {
	return getDoc[model.Fetch](ctx, s, kFetch, itemID+"@"+headSHA)
}

// PutFiles implements Store.
func (s *SQLite) PutFiles(ctx context.Context, itemID string, files []model.File) error {
	return replaceDocs(ctx, s, kFile, itemID, files, func(_ int, f model.File) string { return itemID + "/" + f.Path })
}

// Files implements Store.
func (s *SQLite) Files(ctx context.Context, itemID string) ([]model.File, error) {
	return listDocs[model.File](ctx, s, kFile, []string{itemID})
}

// PutUnits implements Store.
func (s *SQLite) PutUnits(ctx context.Context, itemID string, units []model.ChangeUnit) error {
	return replaceDocs(ctx, s, kUnit, itemID, units, func(_ int, u model.ChangeUnit) string { return itemID + "/" + u.ID })
}

// Units implements Store; returned in Order.
func (s *SQLite) Units(ctx context.Context, itemID string) ([]model.ChangeUnit, error) {
	us, err := listDocs[model.ChangeUnit](ctx, s, kUnit, []string{itemID})
	if err != nil {
		return nil, err
	}
	sortBy(us, func(u model.ChangeUnit) int { return u.Order })
	return us, nil
}

// PutLinks implements Store.
func (s *SQLite) PutLinks(ctx context.Context, itemID string, links []model.Link) error {
	return replaceDocs(ctx, s, kLink, itemID, links, func(i int, _ model.Link) string { return itemID + "/" + pad(i) })
}

// Links implements Store.
func (s *SQLite) Links(ctx context.Context, itemID string) ([]model.Link, error) {
	return listDocs[model.Link](ctx, s, kLink, []string{itemID})
}

// PutGroups implements Store.
func (s *SQLite) PutGroups(ctx context.Context, itemID string, groups []model.Group) error {
	return replaceDocs(ctx, s, kGroup, itemID, groups, func(_ int, g model.Group) string { return itemID + "/" + g.ID })
}

// Groups implements Store; returned in Order.
func (s *SQLite) Groups(ctx context.Context, itemID string) ([]model.Group, error) {
	gs, err := listDocs[model.Group](ctx, s, kGroup, []string{itemID})
	if err != nil {
		return nil, err
	}
	sortBy(gs, func(g model.Group) int { return g.Order })
	return gs, nil
}

// PutAgg implements Store.
func (s *SQLite) PutAgg(ctx context.Context, agg model.ItemAgg) error {
	return putDoc(ctx, s.db, kAgg, agg.ItemID, agg.ItemID, agg)
}

// Agg implements Store.
func (s *SQLite) Agg(ctx context.Context, itemID string) (model.ItemAgg, bool, error) {
	return getDoc[model.ItemAgg](ctx, s, kAgg, itemID)
}

func labelID(l model.Label) string {
	return l.UnitID + "|" + l.Question + "|" + strconv.Itoa(l.Line)
}

// PutLabels implements Store.
func (s *SQLite) PutLabels(ctx context.Context, labels []model.Label) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, l := range labels {
		if err := putDoc(ctx, tx, kLabel, labelID(l), l.UnitID, l); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// Labels implements Store.
func (s *SQLite) Labels(ctx context.Context, unitIDs []string) ([]model.Label, error) {
	if unitIDs == nil {
		unitIDs = []string{}
	}
	return listDocs[model.Label](ctx, s, kLabel, unitIDs)
}

// PutEmbedding implements Store; vectors are stored as little-endian float32.
func (s *SQLite) PutEmbedding(ctx context.Context, unitID, embedModel string, vec []float32) error {
	b := make([]byte, 4*len(vec))
	for i, f := range vec {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return s.CachePut(ctx, "embed:"+embedModel, unitID, b)
}

// Embedding implements Store.
func (s *SQLite) Embedding(ctx context.Context, unitID, embedModel string) ([]float32, bool, error) {
	b, ok, err := s.CacheGet(ctx, "embed:"+embedModel, unitID)
	if err != nil || !ok {
		return nil, ok, err
	}
	vec := make([]float32, len(b)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return vec, true, nil
}

// PutReview implements Store.
func (s *SQLite) PutReview(ctx context.Context, r model.Review) error {
	return putDoc(ctx, s.db, kReview, r.GroupID+"|"+string(r.Tier), r.ItemID, r)
}

// Review implements Store.
func (s *SQLite) Review(ctx context.Context, groupID string, tier model.Tier) (model.Review, bool, error) {
	return getDoc[model.Review](ctx, s, kReview, groupID+"|"+string(tier))
}

// Reviews implements Store.
func (s *SQLite) Reviews(ctx context.Context, itemID string) ([]model.Review, error) {
	return listDocs[model.Review](ctx, s, kReview, []string{itemID})
}

// PutComment implements Store.
func (s *SQLite) PutComment(ctx context.Context, c model.Comment) error {
	return putDoc(ctx, s.db, kComment, c.ID, c.ItemID, c)
}

// DeleteComment implements Store.
func (s *SQLite) DeleteComment(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM docs WHERE kind=? AND id=?`, kComment, id)
	return err
}

// Comments implements Store.
func (s *SQLite) Comments(ctx context.Context, itemID string) ([]model.Comment, error) {
	return listDocs[model.Comment](ctx, s, kComment, []string{itemID})
}

// PutOverride implements Store.
func (s *SQLite) PutOverride(ctx context.Context, o model.Override) error {
	return putDoc(ctx, s.db, kOverride, o.UnitID+"|"+o.Question, o.UnitID, o)
}

// Overrides implements Store.
func (s *SQLite) Overrides(ctx context.Context, unitIDs []string) ([]model.Override, error) {
	if unitIDs == nil {
		unitIDs = []string{}
	}
	return listDocs[model.Override](ctx, s, kOverride, unitIDs)
}

// AllOverrides implements Store.
func (s *SQLite) AllOverrides(ctx context.Context) ([]model.Override, error) {
	return listDocs[model.Override](ctx, s, kOverride, nil)
}

// AppendRunLog implements Store.
func (s *SQLite) AppendRunLog(ctx context.Context, r model.RunLog) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runlog(item_id, body) VALUES(?,?)`, r.ItemID, b)
	return err
}

// RunLogs implements Store.
func (s *SQLite) RunLogs(ctx context.Context, itemID string) ([]model.RunLog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT body FROM runlog WHERE item_id=? ORDER BY seq`, itemID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []model.RunLog
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		var r model.RunLog
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CacheGet implements Store.
func (s *SQLite) CacheGet(ctx context.Context, ns, key string) ([]byte, bool, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, `SELECT val FROM cache WHERE ns=? AND key=?`, ns, key).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

// CachePut implements Store.
func (s *SQLite) CachePut(ctx context.Context, ns, key string, val []byte) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cache(ns, key, val, updated_at) VALUES(?,?,?,?)
		 ON CONFLICT(ns, key) DO UPDATE SET val=excluded.val, updated_at=excluded.updated_at`,
		ns, key, val, time.Now().UnixMilli())
	return err
}

func pad(i int) string { return fmt.Sprintf("%08d", i) }

func sortBy[T any](xs []T, key func(T) int) {
	slices.SortStableFunc(xs, func(a, b T) int { return cmp.Compare(key(a), key(b)) })
}
