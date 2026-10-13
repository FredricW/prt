package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/model"
)

func openFile(t *testing.T) (*SQLite, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "prt.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func openMem(t *testing.T) *SQLite {
	t.Helper()
	s, err := OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// plan is SQLite's query plan for q, one detail per line.
func plan(t *testing.T, db *sql.DB, q string, args ...any) string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "; ")
}

func indexNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='docs' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// A store file written before M5.2 (schema version 2) gains the parent
// index on open and keeps its documents.
func TestMigrationAddsParentIndex(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range append(slices.Clone(migrations[:2]), `PRAGMA user_version = 2`,
		`INSERT INTO docs(kind, id, parent, body, updated_at) VALUES('label', 'u1|risk|0', 'u1', '{"unitId":"u1","question":"risk","value":"3"}', 0)`) {
		if _, err := old.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if got := indexNames(t, old); !slices.Contains(got, "docs_parent") {
		t.Fatalf("v2 indexes = %v", got)
	}
	_ = old.Close()

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("user_version = %d, %v; want %d", v, err, len(migrations))
	}
	got := indexNames(t, s.db)
	if !slices.Contains(got, "docs_kind_parent_id") || slices.Contains(got, "docs_parent") {
		t.Fatalf("indexes after migration = %v, want docs_kind_parent_id instead of docs_parent", got)
	}
	ls, err := s.Labels(ctx, []string{"u1"})
	if err != nil || len(ls) != 1 || ls[0].Value != "3" {
		t.Fatalf("labels after migration = %+v, %v", ls, err)
	}
}

// listDocs with parents searches docs_kind_parent_id for one and for many
// parents (it used to walk the primary key of the whole kind).
func TestListDocsUsesParentIndex(t *testing.T) {
	s, _ := openFile(t)
	for _, n := range []int{1, 63, 2801} {
		parents := make([]string, n)
		for i := range parents {
			parents[i] = "subject" + strconv.Itoa(i)
		}
		q, args := listQuery(kLabel, parents)
		p := plan(t, s.rd, q, args...)
		if !strings.Contains(p, "docs_kind_parent_id") || strings.Contains(p, "sqlite_autoindex_docs_1") {
			t.Errorf("%d parents: plan %q, want the parent index", n, p)
		}
		t.Logf("%d parent(s): %s", n, p)
	}
	q, args := listQuery(kItem, nil)
	if p := plan(t, s.rd, q, args...); !strings.Contains(p, "sqlite_autoindex_docs_1") || strings.Contains(p, "TEMP B-TREE") {
		t.Errorf("whole kind: plan %q, want the primary key in id order", p)
	}
	if p := plan(t, s.rd, `SELECT kind, id FROM docs WHERE kind IN (?,?) AND parent=?`, kUnit, kFile, "pr:1"); !strings.Contains(p, "COVERING INDEX docs_kind_parent_id") {
		t.Errorf("item subjects: plan %q, want a covering index search", p)
	}
}

// Parent lists still come back in id order, across parents.
func TestListDocsSortsByID(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	ls := []model.Label{
		{UnitID: "b", Question: "risk"}, {UnitID: "a", Question: "kind"}, {UnitID: "b", Question: "kind"},
		{UnitID: "a", Question: "risk"}, {UnitID: "c", Question: "risk"},
	}
	if err := s.PutLabels(ctx, ls); err != nil {
		t.Fatal(err)
	}
	got, err := s.Labels(ctx, []string{"b", "a"})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, l := range got {
		keys = append(keys, l.UnitID+"|"+l.Question)
	}
	if want := []string{"a|kind", "a|risk", "b|kind", "b|risk"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("labels = %v, want %v", keys, want)
	}
	if none, err := s.Labels(ctx, []string{"zz"}); err != nil || none != nil {
		t.Fatalf("no labels = %#v, %v; want nil", none, err)
	}
}

func TestItemSubjects(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	id := model.BranchItemID("feat/x") // ids with a slash
	if err := s.PutUnits(ctx, id, []model.ChangeUnit{{ID: "u2", Order: 0}, {ID: "u1", Order: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutFiles(ctx, id, []model.File{{Path: "b/x.go"}, {Path: "a.go"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutUnits(ctx, "other", []model.ChangeUnit{{ID: "u9"}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ItemSubjects(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"u1", "u2", model.FileSubject(id, "a.go"), model.FileSubject(id, "b/x.go"), model.ItemSubject(id)}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subjects = %v, want %v", got, want)
	}
	if got, err := s.ItemSubjects(ctx, "none"); err != nil || !reflect.DeepEqual(got, []string{model.ItemSubject("none")}) {
		t.Fatalf("unknown item subjects = %v, %v", got, err)
	}
}

// pruneFixture stores two items sharing unit "shared": pr:1 (units u1,
// shared; file a.go; group g1; a comment) and branch:b (units shared, u3),
// plus a label on every kind of subject and an override.
func pruneFixture(t *testing.T, s *SQLite) {
	t.Helper()
	ctx := context.Background()
	pr, br := model.PRItemID(1), model.BranchItemID("b")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.UpsertItem(ctx, model.Item{ID: pr}))
	must(s.UpsertItem(ctx, model.Item{ID: br}))
	must(s.PutUnits(ctx, pr, []model.ChangeUnit{{ID: "u1"}, {ID: "shared"}}))
	must(s.PutUnits(ctx, br, []model.ChangeUnit{{ID: "shared"}, {ID: "u3"}}))
	must(s.PutFiles(ctx, pr, []model.File{{Path: "a.go"}}))
	must(s.PutGroups(ctx, pr, []model.Group{{ID: "g1"}}))
	must(s.PutComment(ctx, model.Comment{ID: "c1", ItemID: pr}))
	var ls []model.Label
	for _, subj := range []string{
		"u1", "shared", "u3", "gone",
		model.ItemSubject(pr), model.ItemSubject("pr:9"),
		model.FileSubject(pr, "a.go"), model.FileSubject(pr, "old.go"),
		model.GroupSubject("g1"), model.GroupSubject("g0"),
		model.CommentSubject(pr, "c1"), model.CommentSubject(pr, "c0"),
		model.BranchSubject(br), model.BranchSubject("branch:gone"),
		model.PairSubject("u1", "shared"), model.PairSubject("u1", "gone"), model.PairSubject("u3", "u9"),
	} {
		ls = append(ls, model.Label{UnitID: subj, Question: "q", Value: "v"})
	}
	must(s.PutLabels(ctx, ls))
	must(s.PutOverride(ctx, model.Override{UnitID: "gone", Question: "q", Value: "no"}))
}

// labelSubjectsStored lists the subjects with a stored label.
func labelSubjectsStored(t *testing.T, s *SQLite) []string {
	t.Helper()
	all, err := listDocs[model.Label](context.Background(), s, kLabel, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range all {
		out = append(out, l.UnitID)
	}
	slices.Sort(out)
	return out
}

func TestPruneLabels(t *testing.T) {
	ctx := context.Background()
	pr, br := model.PRItemID(1), model.BranchItemID("b")
	t.Run("sweep keeps referenced subjects and overrides", func(t *testing.T) {
		s := openMem(t)
		pruneFixture(t, s)
		n, err := s.PruneLabels(ctx, nil)
		if err != nil || n != 8 {
			t.Fatalf("pruned %d, %v; want 8", n, err)
		}
		want := []string{
			model.BranchSubject(br), model.CommentSubject(pr, "c1"), model.FileSubject(pr, "a.go"),
			model.GroupSubject("g1"), model.ItemSubject(pr), model.PairSubject("u1", "shared"),
			"shared", "u1", "u3",
		}
		slices.Sort(want)
		if got := labelSubjectsStored(t, s); !reflect.DeepEqual(got, want) {
			t.Fatalf("labels left = %v\nwant %v", got, want)
		}
		if ovs, err := s.AllOverrides(ctx); err != nil || len(ovs) != 1 {
			t.Fatalf("overrides = %+v, %v; user data must stay", ovs, err)
		}
	})
	t.Run("removed item: shared unit survives while the other item holds it", func(t *testing.T) {
		s := openMem(t)
		pruneFixture(t, s)
		cands, err := s.ItemSubjects(ctx, pr)
		if err != nil {
			t.Fatal(err)
		}
		cands = append(cands, model.GroupSubject("g1"))
		if err := s.RemoveItem(ctx, pr); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PruneLabels(ctx, cands); err != nil {
			t.Fatal(err)
		}
		got := labelSubjectsStored(t, s)
		for _, gone := range []string{"u1", model.ItemSubject(pr), model.FileSubject(pr, "a.go"), model.GroupSubject("g1"),
			model.PairSubject("u1", "shared"), model.PairSubject("u1", "gone")} {
			if slices.Contains(got, gone) {
				t.Errorf("%s kept after its item was removed", gone)
			}
		}
		for _, kept := range []string{"shared", "u3", model.CommentSubject(pr, "c1"), "gone", model.GroupSubject("g0")} {
			if !slices.Contains(got, kept) {
				t.Errorf("%s pruned; want it kept (shared, other item's, comment, or not a candidate)", kept)
			}
		}
		if err := s.RemoveItem(ctx, br); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PruneLabels(ctx, []string{"shared", "u3"}); err != nil {
			t.Fatal(err)
		}
		if got := labelSubjectsStored(t, s); slices.Contains(got, "shared") || slices.Contains(got, "u3") {
			t.Errorf("labels %v kept after both items were removed", got)
		}
	})
}

func TestCurrentRunLogsAndPrune(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	id := "pr:1"
	appendRun := func(note string, stages ...model.Stage) {
		t.Helper()
		for _, st := range stages {
			if err := s.AppendRunLog(ctx, model.RunLog{ItemID: id, Stage: st, Status: model.StageDone, Note: note}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got, err := s.CurrentRunLogs(ctx, id); err != nil || got != nil {
		t.Fatalf("empty log = %+v, %v", got, err)
	}
	appendRun("orphan", model.StageAggregate) // before any fetch row
	if got, _ := s.CurrentRunLogs(ctx, id); len(got) != 1 {
		t.Fatalf("no fetch row: current run = %+v, want every row", got)
	}
	full := []model.Stage{model.StageFetch, model.StageParse, model.StageAggregate}
	for i := range 6 {
		appendRun("run"+strconv.Itoa(i), full...)
	}
	appendRun("aggonly", model.StageAggregate)
	if err := s.AppendRunLog(ctx, model.RunLog{ItemID: "other", Stage: model.StageFetch}); err != nil {
		t.Fatal(err)
	}
	cur, err := s.CurrentRunLogs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var notes []string
	for _, r := range cur {
		notes = append(notes, string(r.Stage)+":"+r.Note)
	}
	if want := []string{"fetch:run5", "parse:run5", "aggregate:run5", "aggregate:aggonly"}; !reflect.DeepEqual(notes, want) {
		t.Fatalf("current run = %v, want %v", notes, want)
	}
	n, err := s.PruneRunLogs(ctx, id, 3)
	if err != nil || n != 10 {
		t.Fatalf("pruned %d, %v; want 10 (the orphan and runs 0–2)", n, err)
	}
	all, _ := s.RunLogs(ctx, id)
	if len(all) != 10 || all[0].Note != "run3" || all[len(all)-1].Note != "aggonly" {
		t.Fatalf("after prune: %d rows, first %+v", len(all), all[0])
	}
	if n, err := s.PruneRunLogs(ctx, id, 3); err != nil || n != 0 {
		t.Fatalf("second prune = %d, %v; want nothing", n, err)
	}
	if other, _ := s.RunLogs(ctx, "other"); len(other) != 1 {
		t.Fatalf("another item's rows were pruned: %+v", other)
	}
}

func TestDeleteReviewAndCacheDelete(t *testing.T) {
	ctx := context.Background()
	s := openMem(t)
	if err := s.PutReview(ctx, model.Review{GroupID: "g", ItemID: "pr:1", Tier: model.TierFast}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteReview(ctx, "g", model.TierFast); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Review(ctx, "g", model.TierFast); ok || err != nil {
		t.Fatalf("review after delete: ok %v, %v", ok, err)
	}
	if err := s.DeleteReview(ctx, "missing", model.TierDeep); err != nil {
		t.Fatalf("deleting a missing row: %v", err)
	}
	if err := s.CachePut(ctx, "ns", "k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.CacheDelete(ctx, "ns", "k"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.CacheGet(ctx, "ns", "k"); ok {
		t.Fatal("cache entry kept after delete")
	}
}

// With the read pool a read does not wait for a write transaction the
// single write connection holds; it sees the last committed state.
func TestReadPoolDoesNotWaitForWriter(t *testing.T) {
	ctx := context.Background()
	s, _ := openFile(t)
	if s.rd == s.db {
		t.Fatal("file store in WAL mode has no read pool")
	}
	if err := s.UpsertItem(ctx, model.Item{ID: "pr:1", Title: "committed"}); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := putDoc(ctx, tx, kItem, "pr:1", "", model.Item{ID: "pr:1", Title: "uncommitted"}); err != nil {
		t.Fatal(err)
	}
	rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	it, err := s.Item(rctx, "pr:1")
	if err != nil {
		t.Fatalf("read behind a write transaction: %v (after %s)", err, time.Since(start))
	}
	if it.Title != "committed" {
		t.Fatalf("read saw %q, want the committed row", it.Title)
	}
	if items, err := s.Items(rctx); err != nil || len(items) != 1 {
		t.Fatalf("items behind a write transaction = %+v, %v", items, err)
	}
	// A writer has to wait (one connection): only reads are freed.
	wctx, wcancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer wcancel()
	if err := s.UpsertItem(wctx, model.Item{ID: "pr:2"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second write during a transaction = %v, want it to wait", err)
	}
}

// An in-memory store keeps one connection for reads and writes.
func TestMemoryStoreHasOneConnection(t *testing.T) {
	s := openMem(t)
	if s.rd != s.db {
		t.Fatal("in-memory store opened a separate read handle (it would see another database)")
	}
	if got := s.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max open connections = %d", got)
	}
}
