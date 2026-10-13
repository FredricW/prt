// Package parse is pipeline stage 1: it splits each diff hunk into change
// units at function boundaries using tree-sitter (TS/TSX/JS/Go/Python),
// attaches the enclosing symbol and signature, computes a shape hash
// (whitespace and identifiers normalised to $1, $2, …) for mechanical
// detection, marks pure-formatting units by comparing token streams, and
// pairs identical deleted/added blocks as moved code.
//
// Each file side (base and head) is fetched and parsed at most once per
// Split. When a supported file cannot be used (no Source, a Show error, a
// syntax error, a file too large, or source text that does not match the
// diff) its hunks fall back to one unit per hunk with Symbol = the hunk's
// section heading. Unknown languages give one unit per hunk with no symbol.
package parse

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/fredricw/prt/internal/diff"
	"github.com/fredricw/prt/internal/model"
)

// Source provides file contents at a revision (gitx.Git satisfies it).
type Source interface {
	Show(ctx context.Context, rev, path string) ([]byte, error)
}

// Input is everything stage 1 needs for one item.
type Input struct {
	ItemID  string
	BaseSHA string
	HeadSHA string
	Files   []diff.File
	Src     Source // may be nil: no symbol attachment
}

// Lang maps a path to a tree-sitter language name, or "" if unsupported.
func Lang(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".go":
		return "go"
	case ".ts", ".mts", ".cts":
		return "ts"
	case ".tsx":
		return "tsx"
	case ".js", ".mjs", ".cjs", ".jsx":
		return "js"
	case ".py":
		return "python"
	}
	return ""
}

// Split turns parsed diff files into change units, in file and hunk order.
// Concatenating the Diff of a hunk's units reproduces the hunk body, and
// every unit has at least one changed line. Binary files and files without
// hunks give no units. Source errors never fail Split (those files fall
// back to hunk-level units); only context cancellation does.
func Split(ctx context.Context, in Input) ([]model.ChangeUnit, error) {
	var units []model.ChangeUnit
	for _, f := range in.Files {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("parse: %w", err)
		}
		if f.Binary || len(f.Hunks) == 0 {
			continue
		}
		fc := newFileCtx(ctx, in, f)
		for _, h := range f.Hunks {
			units = fc.appendUnits(units, h, in.ItemID)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	return DetectMoved(units), nil
}

// fileCtx is one diff file with its parsed sides (nil sides = fallback).
type fileCtx struct {
	path string
	lang string
	old  *side
	new  *side
	ok   bool // sides usable for symbol attribution
}

func newFileCtx(ctx context.Context, in Input, f diff.File) *fileCtx {
	fc := &fileCtx{path: f.Path(), lang: Lang(f.Path())}
	if fc.lang == "" || in.Src == nil {
		return fc
	}
	var err error
	if f.OldPath != "" && !f.Added {
		if fc.old, err = loadSide(ctx, in.Src, in.BaseSHA, f.OldPath); err != nil {
			return fc
		}
	}
	if f.NewPath != "" && !f.Deleted {
		if fc.new, err = loadSide(ctx, in.Src, in.HeadSHA, f.NewPath); err != nil {
			fc.old = nil
			return fc
		}
	}
	fc.ok = (fc.old != nil || fc.new != nil) && fc.matches(f)
	if !fc.ok {
		fc.old, fc.new = nil, nil
	}
	return fc
}

// loadSide fetches and parses one file side.
func loadSide(ctx context.Context, src Source, rev, p string) (*side, error) {
	lang := Lang(p)
	if lang == "" {
		return nil, errNoGrammar
	}
	b, err := src.Show(ctx, rev, p)
	if err != nil {
		return nil, fmt.Errorf("show %s:%s: %w", rev, p, err)
	}
	return parseSide(ctx, lang, b)
}

// matches checks every hunk line against the fetched sources, so a source
// that does not belong to this diff never attributes wrong symbols.
func (fc *fileCtx) matches(f diff.File) bool {
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Op != diff.Add && !sideMatches(fc.old, l.OldNo, l.Text) {
				return false
			}
			if l.Op != diff.Del && !sideMatches(fc.new, l.NewNo, l.Text) {
				return false
			}
		}
	}
	return true
}

func sideMatches(s *side, lineNo int, text string) bool {
	if s == nil {
		return false
	}
	return s.lineMatches(lineNo-1, text)
}

// cursor tracks the last old/new line numbers seen before a unit, for the
// start of an empty span.
type cursor struct{ old, new int }

// appendUnits splits one hunk and appends its units.
func (fc *fileCtx) appendUnits(units []model.ChangeUnit, h diff.Hunk, itemID string) []model.ChangeUnit {
	cur := cursor{old: h.OldStart - 1, new: h.NewStart - 1}
	if h.OldLines == 0 {
		cur.old = h.OldStart
	}
	if h.NewLines == 0 {
		cur.new = h.NewStart
	}
	for _, sg := range fc.segments(h) {
		u := fc.unit(h.Lines[sg.start:sg.end], &cur, sg.sym, sg.sig)
		u.ItemID = itemID
		u.Order = len(units)
		units = append(units, u)
	}
	return units
}

// unit builds one change unit from a run of hunk lines.
func (fc *fileCtx) unit(lines []diff.Line, cur *cursor, sym, sig string) model.ChangeUnit {
	var (
		body      strings.Builder
		rng       model.Range
		adds, del int
		changed   []changedLine
		block     int // index of the current run of adjacent +/- lines
	)
	rng.Old.Start, rng.New.Start = cur.old, cur.new
	for i, l := range lines {
		if l.Op == diff.Context && i > 0 && lines[i-1].Op != diff.Context {
			block++
		}
		if i > 0 {
			body.WriteByte('\n')
		}
		body.WriteByte(byte(l.Op))
		body.WriteString(l.Text)
		if l.Op != diff.Add {
			extend(&rng.Old, l.OldNo)
			cur.old = l.OldNo
		}
		if l.Op != diff.Del {
			extend(&rng.New, l.NewNo)
			cur.new = l.NewNo
		}
		switch l.Op {
		case diff.Add:
			adds++
		case diff.Del:
			del++
		}
		if l.Op != diff.Context {
			changed = append(changed, changedLine{line: l, toks: fc.tokens(l), block: block})
		}
	}
	text := body.String()
	kind := model.UnitModified
	switch {
	case del == 0:
		kind = model.UnitAdded
	case adds == 0:
		kind = model.UnitDeleted
	}
	return model.ChangeUnit{
		ID:        model.UnitID(fc.path, text),
		FileID:    fc.path,
		Path:      fc.path,
		Range:     rng,
		Symbol:    sym,
		Signature: sig,
		Diff:      text,
		Kind:      kind,
		ShapeHash: shapeHash(changed),
		Format:    isFormat(changed, fc.lang),
		Adds:      adds,
		Dels:      del,
	}
}

// extend adds line n to span s (the first line sets Start).
func extend(s *model.Span, n int) {
	if s.Count == 0 {
		s.Start = n
	}
	s.Count++
}

// tokens returns a changed line's tokens: from the parse tree of its side
// when available, else from the fallback tokenizer.
func (fc *fileCtx) tokens(l diff.Line) []token {
	if fc.ok {
		s, row := fc.new, l.NewNo-1
		if l.Op == diff.Del {
			s, row = fc.old, l.OldNo-1
		}
		if t, ok := s.tokensAt(row); ok {
			return t
		}
	}
	return scanLine(l.Text, fc.lang)
}
