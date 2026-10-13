// Package surface finds an item's structured changes for the Overview's
// "API endpoints", "Database" and "Feature flags & config" blocks (M5.2,
// user request; DECISIONS "M5.2 shared contracts"): endpoints added,
// changed or removed, migrations and schema changes, feature flag lookups,
// environment variables and config keys.
//
// Detection is pure code over the item's change units (their unified diff
// hunks), with per-framework detectors:
//
//   - API endpoints: Go net/http (incl. 1.22 method patterns), chi, gin,
//     echo (and fiber, gorilla/mux); TS/JS express, fastify, Next.js route
//     files; Python flask, fastapi, django. Router prefixes (chi Route and
//     Mount, gin/echo Group, express use, fastify register, flask and
//     fastapi routers, django include) are composed when they can be
//     resolved from the item's files; a Detail says when one cannot.
//     Response- and request-shape changes of Go structs and server-side TS
//     types are reported with lower confidence.
//   - Database: migration files, SQL DDL, Django/Alembic/Rails/knex/
//     sequelize migrations, Prisma schemas and ORM models.
//   - Feature flags & config: LaunchDarkly, Unleash, GrowthBook,
//     OpenFeature and custom flag helpers with a literal key; environment
//     variables (Go, Node, Python, Ruby, .env files); config keys (viper,
//     node-config, Django settings, CLI flags).
//
// Comments, string-only matches and test files are ignored. Jev's unit
// `contract` label (api shape, schema/migration, env/flag) confirms code
// entries and adds entries code did not find. The only I/O is the optional
// Source (file contents at the base and head revisions, to resolve router
// prefixes and enclosing types), passed in by the pipeline.
//
// Build is called by the pipeline in stage 8 (normal and aggregate-only
// runs); its result is ItemAgg.Surface.
package surface

import (
	"context"

	"github.com/fredricw/prt/internal/model"
)

// Source reads a file at a revision (gitx.Git satisfies it).
type Source interface {
	Show(ctx context.Context, rev, path string) ([]byte, error)
}

// Input is everything Build reads.
type Input struct {
	// Units are the item's change units (diffs with line numbers, paths,
	// symbols); Files its changed files.
	Units []model.ChangeUnit
	Files []model.File
	// Aggs are the unit aggregates (UnitAgg.Contract is the Jev contract
	// label's value; a Noise unit gets no Jev-only entry) and Labels the
	// item's unit-scope labels with overrides applied (the contract label's
	// value and P decide); both may be empty when labels are unavailable:
	// code detection still runs.
	Aggs   []model.UnitAgg
	Labels []model.Label
	// BaseSHA and HeadSHA are the compared revisions; Src may be nil
	// (diff-only detection).
	BaseSHA, HeadSHA string
	Src              Source
}

// Build returns the item's change surface: never nil, every list non-nil
// (empty when nothing was found), each sorted by importance (removed,
// changed, added; then confidence descending, path, line).
//
// Build never panics on malformed input: a detector failure yields empty
// lists and a Note.
func Build(ctx context.Context, in Input) (out *model.ChangeSurface) {
	defer func() {
		if r := recover(); r != nil {
			out = emptySurface()
			out.Note = "change surface detection failed"
		}
	}()
	return build(ctx, in)
}

// build is Build without the panic guard (tests and fuzzing call it, so a
// detector bug fails loudly there).
func build(ctx context.Context, in Input) *model.ChangeSurface {
	if ctx == nil {
		ctx = context.Background()
	}
	b := newBuilder(ctx, in)
	api := b.detectAPI()
	db := b.detectDB()
	flags := b.detectFlags()
	api, db, flags = b.mergeJev(api, db, flags)
	if ctx.Err() != nil {
		b.notes.add(noteStopped)
	}
	return &model.ChangeSurface{API: finish(api), DB: finish(db), Flags: finish(flags), Note: b.notes.String()}
}

func emptySurface() *model.ChangeSurface {
	return &model.ChangeSurface{API: []model.SurfaceEntry{}, DB: []model.SurfaceEntry{}, Flags: []model.SurfaceEntry{}}
}

// builder holds one Build's state.
type builder struct {
	ctx    context.Context
	in     Input
	rd     *reader
	notes  notes
	files  []*fileInfo // in first-seen order (Files, then units)
	byPath map[string]*fileInfo
	views  []*view // the item's units, in input order
	// djangoRoots caches, per side, whether a directory holds the root
	// URLconf.
	djangoRoots [2]map[string]bool
}

// newBuilder indexes the item's files and scans its units (up to maxUnits,
// while ctx is live).
func newBuilder(ctx context.Context, in Input) *builder {
	b := &builder{ctx: ctx, in: in, byPath: map[string]*fileInfo{}}
	b.rd = &reader{ctx: ctx, src: in.Src, revs: [2]string{in.BaseSHA, in.HeadSHA}, notes: &b.notes}
	for _, f := range in.Files {
		fi := b.file(f.Path)
		fi.file = f
		if f.OldPath != "" {
			fi.oldPath = f.OldPath
		}
		fi.test = fi.test || f.Role == model.RoleTest
	}
	for i := range in.Units {
		if i >= maxUnits {
			b.notes.add(noteTruncated)
			break
		}
		if ctx.Err() != nil {
			break
		}
		u := &in.Units[i]
		f := b.file(u.Path)
		v := newView(u, f)
		if v.truncated {
			b.notes.add(noteTruncated)
		}
		for _, s := range sides {
			for _, l := range v.changedLines(s) {
				f.changed[s][l.no] = true
			}
		}
		f.views = append(f.views, v)
		b.views = append(b.views, v)
	}
	return b
}

// file returns the fileInfo of p, creating it.
func (b *builder) file(p string) *fileInfo {
	if f, ok := b.byPath[p]; ok {
		return f
	}
	f := &fileInfo{path: p, lang: langOf(p), test: isTestPath(p), db: dbFileOf(p)}
	f.changed = [2]map[int]bool{{}, {}}
	b.byPath[p] = f
	b.files = append(b.files, f)
	return f
}

// live returns the views detectors scan: units outside test files, while
// the context is live.
func (b *builder) live() []*view {
	var out []*view
	for _, v := range b.views {
		if b.ctx.Err() != nil {
			break
		}
		if !v.f.test {
			out = append(out, v)
		}
	}
	return out
}
