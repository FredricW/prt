package link

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fredricw/prt/internal/parse"
)

// Symbol index limits and cache coordinates.
const (
	// maxIndexFiles caps how many source files of the base tree are parsed
	// for the repo symbol index (in path order); the rest are not indexed.
	maxIndexFiles = 5000
	// indexNS is the Cache namespace of the repo symbol index.
	indexNS = "symindex"
	// indexVersion is appended to the base SHA in the cache key; bump it
	// when the index format or extraction rules change.
	indexVersion = "v1"
)

// symIndex is the repo symbol index of one base revision: per source file,
// the names it declares and its import statements.
type symIndex struct {
	Files map[string]indexFile `json:"files"`
	// Truncated is set when the tree had more than maxIndexFiles sources.
	Truncated bool `json:"truncated,omitempty"`
}

// indexFile is one indexed source file.
type indexFile struct {
	Decls   []string     `json:"d,omitempty"` // qualified declared names
	Imports []importInfo `json:"i,omitempty"`
	Exports []string     `json:"e,omitempty"` // names of `export { a }` without a source
}

func indexKey(base string) string { return base + ":" + indexVersion }

// loadIndex returns the symbol index of in.BaseSHA from the cache, or builds
// (and caches) it from the source. Source problems give a partial or empty
// index; only context cancellation is an error.
func loadIndex(ctx context.Context, in Input) (*symIndex, error) {
	if in.Cache != nil && in.BaseSHA != "" {
		if b, ok, err := in.Cache.CacheGet(ctx, indexNS, indexKey(in.BaseSHA)); err == nil && ok {
			var idx symIndex
			if json.Unmarshal(b, &idx) == nil && idx.Files != nil {
				return &idx, nil
			}
		}
	}
	idx, complete, err := buildIndex(ctx, in.Src, in.BaseSHA)
	if err != nil {
		return nil, err
	}
	if complete && in.Cache != nil && in.BaseSHA != "" {
		if b, err := json.Marshal(idx); err == nil {
			_ = in.Cache.CachePut(ctx, indexNS, indexKey(in.BaseSHA), b) // a failed put only costs a rebuild
		}
	}
	return idx, nil
}

// buildIndex parses every supported source file of rev (≤ maxIndexFiles, in
// path order). complete is false when the listing or any file read failed,
// so a transient error is not cached for the base SHA.
func buildIndex(ctx context.Context, src Source, rev string) (*symIndex, bool, error) {
	idx := &symIndex{Files: map[string]indexFile{}}
	if src == nil || rev == "" {
		return idx, false, nil
	}
	all, err := src.ListFiles(ctx, rev)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, false, fmt.Errorf("link: symbol index: %w", cerr)
		}
		return idx, false, nil
	}
	var files []string
	for _, p := range all {
		if parse.Lang(p) != "" {
			files = append(files, p)
		}
	}
	sort.Strings(files)
	if len(files) > maxIndexFiles {
		files, idx.Truncated = files[:maxIndexFiles], true
	}
	complete := true
	for _, p := range files {
		if err := ctx.Err(); err != nil {
			return nil, false, fmt.Errorf("link: symbol index: %w", err)
		}
		b, err := src.Show(ctx, rev, p)
		if err != nil {
			complete = false
			continue
		}
		if syn := parseSyntax(ctx, parse.Lang(p), b); syn != nil {
			idx.Files[p] = indexEntry(syn)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("link: symbol index: %w", err)
	}
	return idx, complete, nil
}

// indexEntry keeps the parts of a file's syntax the index needs.
func indexEntry(syn *fileSyntax) indexFile {
	f := indexFile{Imports: syn.Imports}
	for _, d := range syn.Decls {
		f.Decls = append(f.Decls, d.Name)
	}
	for _, e := range syn.Exports {
		f.Exports = append(f.Exports, e.Name)
	}
	return f
}
