package route

import (
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// MaxContextBytes caps the total size of a group's context blocks.
const MaxContextBytes = 24 * 1024

// ctxTitles is the title prefix of each context kind.
var ctxTitles = map[string]string{
	model.CtxCallers:       "caller",
	model.CtxTypes:         "type",
	model.CtxTests:         "test",
	model.CtxConfig:        "config",
	model.CtxCodeUnderTest: "code under test",
}

// configExts are file extensions treated as config files.
var configExts = []string{".toml", ".yaml", ".yml", ".json", ".ini", ".conf", ".cfg", ".properties", ".env"}

// Gather renders the context blocks of the given kinds for a group from
// the item's own units and links (no repo reads): callers = units outside
// the group with calls/imports links into it; types = units outside the
// group defining symbols the group's units import (the group's own old and
// new signatures are already in its diff); tests = test units linked to the
// group by tests links; code_under_test = targets of the group's test
// units' tests links; config = units of config files or with an env/flag
// contract. The group's own units are never context, and a unit appears
// once (under the first kind that selects it). Blocks are in canonical
// kind order (callers, types, tests, config, code_under_test), then unit
// Order; the total body size is capped at MaxContextBytes: whole blocks
// first, then the first block that does not fit is cut on a line boundary
// with a "… N lines elided …" marker and later blocks are dropped.
func Gather(kinds []string, g model.Group, units []model.ChangeUnit, links []model.Link, aggs []model.UnitAgg) []model.ContextBlock {
	byID := make(map[string]model.ChangeUnit, len(units))
	for _, u := range units {
		byID[u.ID] = u
	}
	in := make(map[string]bool, len(g.UnitIDs))
	for _, id := range g.UnitIDs {
		in[id] = true
	}
	used := map[string]bool{}
	var blocks []model.ContextBlock
	for _, kind := range ctxOrder {
		if !slices.Contains(kinds, kind) {
			continue
		}
		ids := selectUnits(kind, in, byID, links, aggs)
		picked := make([]model.ChangeUnit, 0, len(ids))
		for id := range ids {
			if u, ok := byID[id]; ok && !in[id] && !used[id] {
				picked = append(picked, u)
			}
		}
		sort.Slice(picked, func(i, j int) bool {
			if picked[i].Order != picked[j].Order {
				return picked[i].Order < picked[j].Order
			}
			return picked[i].ID < picked[j].ID
		})
		for _, u := range picked {
			used[u.ID] = true
			blocks = append(blocks, renderBlock(kind, u))
		}
	}
	return capBlocks(blocks, MaxContextBytes)
}

// selectUnits returns the candidate unit ids of one context kind.
func selectUnits(kind string, in map[string]bool, byID map[string]model.ChangeUnit, links []model.Link, aggs []model.UnitAgg) map[string]bool {
	out := map[string]bool{}
	for _, l := range links {
		switch kind {
		case model.CtxCallers:
			if (l.Kind == model.LinkCalls || l.Kind == model.LinkImports) && in[l.To] && !in[l.From] {
				out[l.From] = true
			}
		case model.CtxTypes:
			if l.Kind == model.LinkImports && in[l.From] && !in[l.To] {
				out[l.To] = true
			}
		case model.CtxTests:
			if l.Kind == model.LinkTests && in[l.To] && !in[l.From] {
				out[l.From] = true
			}
		case model.CtxCodeUnderTest:
			if l.Kind == model.LinkTests && in[l.From] && !in[l.To] {
				out[l.To] = true
			}
		}
	}
	if kind == model.CtxConfig {
		for id, u := range byID {
			if isConfigPath(u.Path) {
				out[id] = true
			}
		}
		for _, a := range aggs {
			if a.Contract == "env/flag" {
				out[a.UnitID] = true
			}
		}
	}
	return out
}

func isConfigPath(p string) bool {
	base := path.Base(p)
	if strings.HasPrefix(base, ".env") {
		return true
	}
	return slices.Contains(configExts, strings.ToLower(path.Ext(base)))
}

// renderBlock renders one unit: path:range, symbol/signature, then its diff.
func renderBlock(kind string, u model.ChangeUnit) model.ContextBlock {
	title := ctxTitles[kind] + " " + u.Path
	if u.Symbol != "" {
		title += " " + u.Symbol
	}
	var b strings.Builder
	b.WriteString(u.Path + ":" + spanText(u.Range) + "\n")
	switch {
	case u.Signature != "":
		b.WriteString(u.Signature + "\n")
	case u.Symbol != "":
		b.WriteString(u.Symbol + "\n")
	}
	b.WriteString(strings.TrimSuffix(u.Diff, "\n"))
	return model.ContextBlock{Kind: kind, Title: title, UnitID: u.ID, Body: strings.TrimSuffix(b.String(), "\n")}
}

// spanText is the new-side line range, or the old side for a deletion.
func spanText(r model.Range) string {
	s := r.New
	if s.Count == 0 && r.Old.Count > 0 {
		s = r.Old
	}
	if s.Count <= 1 {
		return fmt.Sprint(s.Start)
	}
	return fmt.Sprintf("%d-%d", s.Start, s.Start+s.Count-1)
}

// capBlocks keeps whole blocks while they fit in limit bytes, cuts the
// first block that does not fit on a line boundary (with an elision
// marker) and drops the rest.
func capBlocks(blocks []model.ContextBlock, limit int) []model.ContextBlock {
	out := make([]model.ContextBlock, 0, len(blocks))
	left := limit
	for _, b := range blocks {
		if len(b.Body) <= left {
			out = append(out, b)
			left -= len(b.Body)
			continue
		}
		if body, ok := cutBody(b.Body, left); ok {
			b.Body = body
			out = append(out, b)
		}
		break
	}
	return out
}

// cutBody keeps the leading whole lines of body that fit in limit bytes
// together with the elision marker; ok=false when not even one line fits.
func cutBody(body string, limit int) (string, bool) {
	lines := strings.Split(body, "\n")
	// prefix[k] is the byte length of the first k lines plus their newlines.
	prefix := make([]int, len(lines)+1)
	for i, ln := range lines {
		prefix[i+1] = prefix[i] + len(ln) + 1
	}
	for keep := len(lines) - 1; keep >= 1; keep-- {
		marker := elided(len(lines) - keep)
		if prefix[keep]+len(marker) <= limit {
			return strings.Join(lines[:keep], "\n") + "\n" + marker, true
		}
	}
	return "", false
}

func elided(n int) string { return fmt.Sprintf("… %d lines elided …", n) }
