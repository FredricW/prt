// Package store persists every intermediate result in SQLite (modernc.org/sqlite,
// pure Go), one file per repo at .git/prt/prt.db.
//
// Caching keys follow the spec: units, labels, embeddings and overrides by
// unit id (content hash, shared across items); groups and reviews by the
// sorted unit-id list (model.GroupID); fetches by head SHA. Aggregates are
// stored only as a cache of the last computation, never as truth.
package store

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/fredricw/prt/internal/model"
)

// ErrNotFound is returned by single-record getters when nothing is stored.
var ErrNotFound = errors.New("store: not found")

// DefaultPath returns the store path for a repo's git dir.
func DefaultPath(gitDir string) string { return filepath.Join(gitDir, "prt", "prt.db") }

// Store is the persistence boundary. Implementations must be safe for
// concurrent use. "Put…(itemID, xs)" methods replace that item's previous set.
type Store interface {
	// Items (picker). Added items persist until removed.
	UpsertItem(ctx context.Context, it model.Item) error
	Item(ctx context.Context, id string) (model.Item, error)
	Items(ctx context.Context) ([]model.Item, error)
	RemoveItem(ctx context.Context, id string) error

	// Fetch cache, keyed by item and head SHA.
	PutFetch(ctx context.Context, f model.Fetch) error
	Fetch(ctx context.Context, itemID, headSHA string) (model.Fetch, bool, error)

	// Stage outputs per item.
	PutFiles(ctx context.Context, itemID string, files []model.File) error
	Files(ctx context.Context, itemID string) ([]model.File, error)
	PutUnits(ctx context.Context, itemID string, units []model.ChangeUnit) error
	Units(ctx context.Context, itemID string) ([]model.ChangeUnit, error)
	PutLinks(ctx context.Context, itemID string, links []model.Link) error
	Links(ctx context.Context, itemID string) ([]model.Link, error)
	PutGroups(ctx context.Context, itemID string, groups []model.Group) error
	Groups(ctx context.Context, itemID string) ([]model.Group, error)
	PutAgg(ctx context.Context, agg model.ItemAgg) error
	Agg(ctx context.Context, itemID string) (model.ItemAgg, bool, error)

	// Labels are keyed by unit id (+ question + line), so they follow the code.
	PutLabels(ctx context.Context, labels []model.Label) error
	Labels(ctx context.Context, unitIDs []string) ([]model.Label, error)

	// Embeddings are keyed by unit id and embedding model id.
	PutEmbedding(ctx context.Context, unitID, embedModel string, vec []float32) error
	Embedding(ctx context.Context, unitID, embedModel string) ([]float32, bool, error)

	// Reviews are keyed by group id (sorted unit ids) and tier.
	PutReview(ctx context.Context, r model.Review) error
	Review(ctx context.Context, groupID string, tier model.Tier) (model.Review, bool, error)
	Reviews(ctx context.Context, itemID string) ([]model.Review, error)

	// Comments: local drafts and loaded GitHub comments.
	PutComment(ctx context.Context, c model.Comment) error
	DeleteComment(ctx context.Context, id string) error
	Comments(ctx context.Context, itemID string) ([]model.Comment, error)

	// Overrides are keyed by unit and question.
	PutOverride(ctx context.Context, o model.Override) error
	Overrides(ctx context.Context, unitIDs []string) ([]model.Override, error)
	AllOverrides(ctx context.Context) ([]model.Override, error)

	// Run log, append-only, in insertion order.
	AppendRunLog(ctx context.Context, r model.RunLog) error
	RunLogs(ctx context.Context, itemID string) ([]model.RunLog, error)

	// Generic content-hash cache for anything else (Jev responses, symbol
	// indexes per base SHA, TL;DR text, ...). ns is a namespace like "jev".
	CacheGet(ctx context.Context, ns, key string) ([]byte, bool, error)
	CachePut(ctx context.Context, ns, key string, val []byte) error

	Close() error
}
