package link

import (
	"context"
	"errors"
	"fmt"
	"sort"
)

// ErrUnreadable is wrapped by ImportGraph when the revision's tree cannot be
// listed (for example a PR head that is not fetched locally).
var ErrUnreadable = errors.New("link: revision not readable")

// FileImport is one import statement of a source file that resolves to at
// least one repo file.
type FileImport struct {
	Spec string `json:"spec"` // the specifier as written ("../api/client", "example.com/x/server/auth")
	// Targets are the repo files it resolves to, sorted (every file of the
	// package for Go; one file for TS/JS and Python), never the importing
	// file itself.
	Targets []string `json:"targets"`
}

// FileGraph is the file-level import graph of one revision, built from the
// repo symbol index.
type FileGraph struct {
	// Files are the indexed source files, sorted (supported languages, at
	// most the index cap; files that fail to read or parse are missing).
	Files []string
	// Imports maps a source file to its resolving import statements, in
	// source order. External imports (packages, stdlib) are left out.
	Imports map[string][]FileImport
	// Truncated is set when the tree had more source files than indexed.
	Truncated bool
	// Complete is false when a file could not be read (the graph is then
	// partial and the index was not cached).
	Complete bool
}

// Targets returns the distinct repo files file imports, sorted.
func (g *FileGraph) Targets(file string) []string {
	var out []string
	for _, imp := range g.Imports[file] {
		out = append(out, imp.Targets...)
	}
	if len(out) == 0 {
		return nil
	}
	return dedupe(out)
}

// ImportGraph returns the import graph of every indexed source file at rev:
// for each file, the repo files its imports resolve to (Go by package
// directory, TS/JS relative specifiers with extension and index files,
// Python dotted modules relative to the file or the repo root). The symbol
// index behind it is the one Structural uses, cached per revision under
// the same Cache namespace and key, so a base already indexed by stage 2
// costs no file reads. A tree that cannot be listed gives an error wrapping
// ErrUnreadable; a cancelled ctx gives the context's error. src nil or rev
// "" is unreadable too.
func ImportGraph(ctx context.Context, src Source, cache Cache, rev string) (*FileGraph, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("link: import graph: %w", err)
	}
	res, err := cachedIndex(ctx, src, cache, rev)
	if err != nil {
		return nil, err
	}
	if res.listErr != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrUnreadable, rev, res.listErr)
	}
	return fileGraph(res.idx, res.complete), nil
}

// fileGraph resolves every indexed file's imports against the index.
func fileGraph(idx *symIndex, complete bool) *FileGraph {
	v := newView(idx)
	v.finish()
	g := &FileGraph{Imports: map[string][]FileImport{}, Truncated: idx.Truncated, Complete: complete}
	for p := range idx.Files {
		g.Files = append(g.Files, p)
	}
	sort.Strings(g.Files)
	for _, p := range g.Files {
		for _, imp := range idx.Files[p].Imports {
			targets := withoutPath(v.resolveSpec(p, imp.Spec), p)
			if len(targets) == 0 {
				continue
			}
			g.Imports[p] = append(g.Imports[p], FileImport{Spec: imp.Spec, Targets: dedupe(targets)})
		}
	}
	return g
}

// withoutPath returns ps without p (a copy; ps is not modified).
func withoutPath(ps []string, p string) []string {
	out := make([]string, 0, len(ps))
	for _, x := range ps {
		if x != p {
			out = append(out, x)
		}
	}
	return out
}
