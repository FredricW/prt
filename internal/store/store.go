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
	"time"

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
	// User merge/split edits of an item's groups, in append order (an
	// EditReset edit clears them). Removed with the item.
	AppendGroupEdit(ctx context.Context, e model.GroupEdit) error
	GroupEdits(ctx context.Context, itemID string) ([]model.GroupEdit, error)
	PutAgg(ctx context.Context, agg model.ItemAgg) error
	Agg(ctx context.Context, itemID string) (model.ItemAgg, bool, error)

	// Labels are keyed by unit id (+ question + line), so they follow the code.
	PutLabels(ctx context.Context, labels []model.Label) error
	Labels(ctx context.Context, unitIDs []string) ([]model.Label, error)
	// ItemSubjects lists the label subjects of the item's stored analysis
	// that Detail serves labels for: its unit ids, its files
	// (model.FileSubject) and the item itself (model.ItemSubject), read from
	// document ids without decoding a body (M5.2).
	ItemSubjects(ctx context.Context, itemID string) ([]string, error)
	// PruneLabels deletes the labels whose subject no stored item references
	// any more and returns how many it deleted (M5.2: the label cache is
	// bounded by what the store holds). With subjects nil it sweeps every
	// label in the store; otherwise it considers only those subjects and the
	// pair subjects naming one of them. A subject stays referenced while any
	// item holds it (a unit shared by a branch and its PR keeps its labels
	// until both are gone); comment subjects stay while their comment is
	// stored. Overrides are user data and are never deleted.
	PruneLabels(ctx context.Context, subjects []string) (int, error)
	// LabelCounts counts every stored label by question id (all subjects,
	// lines and items; unknown answers included). Labels carry no time, so
	// this is the denominator of the calibration panel's override rate.
	LabelCounts(ctx context.Context) (map[string]int, error)

	// Embeddings are keyed by unit id and embedding model id.
	PutEmbedding(ctx context.Context, unitID, embedModel string, vec []float32) error
	Embedding(ctx context.Context, unitID, embedModel string) ([]float32, bool, error)

	// Review cache: one row per (group id, tier) that ran, keyed by the
	// sorted unit ids, so a group whose membership did not change keeps its
	// reviews. Reviews lists the cache rows written with that item id.
	PutReview(ctx context.Context, r model.Review) error
	Review(ctx context.Context, groupID string, tier model.Tier) (model.Review, bool, error)
	Reviews(ctx context.Context, itemID string) ([]model.Review, error)
	// DeleteReview removes one cache row (a resumed run that must review
	// the group again; M5.2). Removing a row that is not stored is no error.
	DeleteReview(ctx context.Context, groupID string, tier model.Tier) error
	// Routing table: exactly one row per current group of the item, in its
	// final state (replaces the item's previous set; removed with the
	// item). proto.Detail.Reviews serves these rows.
	PutItemReviews(ctx context.Context, itemID string, rs []model.Review) error
	ItemReviews(ctx context.Context, itemID string) ([]model.Review, error)

	// Calibration samples (sampled fast+deep double reviews), append-only
	// across items, for the trailing-30-day miss rate. Calibrations returns
	// samples with At >= since, oldest first.
	AppendCalibration(ctx context.Context, c model.CalibrationSample) error
	Calibrations(ctx context.Context, since time.Time) ([]model.CalibrationSample, error)

	// Comments: local drafts, ai-drafts and loaded GitHub comments, keyed
	// by Comment.ID (parent ItemID). They are not removed with the item, so
	// drafts survive a remove/re-add.
	PutComment(ctx context.Context, c model.Comment) error
	DeleteComment(ctx context.Context, id string) error
	Comments(ctx context.Context, itemID string) ([]model.Comment, error)

	// Finding marks: AI findings the user accepted or dismissed, keyed by
	// (ItemID, FindingKey); the pipeline skips them when it syncs ai-drafts.
	PutFindingMark(ctx context.Context, m model.FindingMark) error
	FindingMarks(ctx context.Context, itemID string) ([]model.FindingMark, error)

	// View marks: the head the user last looked at, one per item
	// ("Since you looked"). ViewMark reports false when none is stored.
	PutViewMark(ctx context.Context, v model.ViewMark) error
	ViewMark(ctx context.Context, itemID string) (model.ViewMark, bool, error)

	// Overrides are keyed by unit and question.
	PutOverride(ctx context.Context, o model.Override) error
	Overrides(ctx context.Context, unitIDs []string) ([]model.Override, error)
	AllOverrides(ctx context.Context) ([]model.Override, error)

	// Run log, in insertion order. RunLogs returns every stored row.
	AppendRunLog(ctx context.Context, r model.RunLog) error
	RunLogs(ctx context.Context, itemID string) ([]model.RunLog, error)
	// CurrentRunLogs returns the item's current run: the rows from its last
	// fetch row on (every row when it has none), as the Pipeline page and
	// the picker strip read them. A run starts with its fetch row; the rows
	// of an aggregate-only run belong to the run they follow (M5.2).
	CurrentRunLogs(ctx context.Context, itemID string) ([]model.RunLog, error)
	// PruneRunLogs deletes the item's rows older than its last keep runs
	// (runs delimited as for CurrentRunLogs) and returns how many it
	// deleted (M5.2).
	PruneRunLogs(ctx context.Context, itemID string, keep int) (int, error)

	// Generic content-hash cache for anything else (Jev responses, symbol
	// indexes per base SHA, TL;DR text, ...). ns is a namespace like "jev".
	CacheGet(ctx context.Context, ns, key string) ([]byte, bool, error)
	CachePut(ctx context.Context, ns, key string, val []byte) error
	// CacheDelete removes one entry (no error when it is not stored).
	CacheDelete(ctx context.Context, ns, key string) error

	Close() error
}
