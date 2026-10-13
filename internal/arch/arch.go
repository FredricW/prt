// Package arch builds the Architecture tab's module graph (spec: "module
// graph coloured by attention, as in the v2 design"): the modules an item
// touches, their import neighbours up to Input.MaxHop hops, and the import
// edges between them at base and head, so new and removed modules and
// edges can be shown. Node attention is rolled up from the item's unit
// aggregates. Pure apart from reading files through Source; the pipeline
// calls Build in stage 8 and stores the result in model.ItemAgg.Arch.
//
// A module is the directory of a source file (path.Dir, "." for files at
// the repo root); directories are never collapsed, so web/src/api and
// web/src/components are separate nodes. Only source files (languages
// parse supports) make modules: a diff that touches only package.json or
// docs has no touched module. Imports come from link.ImportGraph at the
// base and head revisions; a module edge From → To exists when a file of
// From has an import statement resolving to a file of To (self edges are
// dropped), and its Imports counts those statements.
//
// The contract below was fixed by the M5 lead (DECISIONS "M5 shared
// contracts"); the arch work package implements it without changing these
// signatures.
package arch

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/link"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/parse"
)

// ErrNotImplemented was returned by the stub Build before the arch work
// package landed; it is kept for callers that still compare against it.
var ErrNotImplemented = errors.New("arch: not implemented yet (M5)")

// DefaultMaxHop is the default Input.MaxHop: the tab opens at depth 1 and
// `+` can show one more hop.
const DefaultMaxHop = 2

// maxModules caps the graph's size: past it the farthest context modules
// (highest hop, then path order) are left out and Note says so.
const maxModules = 150

// maxNetEffect is the most NetEffect sentences kept.
const maxNetEffect = 4

// Source reads trees and files at a revision (gitx.Git satisfies it).
type Source interface {
	ListFiles(ctx context.Context, rev string) ([]string, error)
	Show(ctx context.Context, rev, path string) ([]byte, error)
}

// Cache is a content-hash cache (store.Store satisfies it); the import
// graph of a revision is cached under it so a re-run reads no files.
type Cache interface {
	CacheGet(ctx context.Context, ns, key string) ([]byte, bool, error)
	CachePut(ctx context.Context, ns, key string, val []byte) error
}

// Input is everything Build needs.
type Input struct {
	ItemID string
	// BaseSHA and HeadSHA are the revisions compared (the item's merge base
	// and analysed head). HeadSHA may be unreadable locally (a PR whose
	// head is not fetched): Build then uses the base graph plus the diff's
	// files and says so in ArchGraph.Note.
	BaseSHA, HeadSHA string
	Files            []model.File
	Units            []model.ChangeUnit
	// Agg supplies the unit roll-ups (attention, risk, tests, unsure,
	// undocumented, breaking) and headlines for edge contracts; may be nil
	// (labels unavailable): nodes then carry git facts only.
	Agg *model.ItemAgg
	// MaxHop bounds how far from touched modules neighbours are collected
	// (≤ 0: DefaultMaxHop).
	MaxHop int
	Src    Source
	Cache  Cache // may be nil
}

// ModuleOf returns the module of a repo path: its directory ("." for a
// file at the root).
func ModuleOf(p string) string { return path.Dir(p) }

// Build returns the module graph of in. Source errors degrade (a partial
// graph with ArchGraph.Note); only context cancellation is returned as an
// error.
func Build(ctx context.Context, in Input) (*model.ArchGraph, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("arch: %w", err)
	}
	maxHop := in.MaxHop
	if maxHop <= 0 {
		maxHop = DefaultMaxHop
	}
	t := touchedModules(in.Files)
	base, head, notes, err := revGraphs(ctx, in, t)
	if err != nil {
		return nil, err
	}
	g := &model.ArchGraph{MaxHop: maxHop, Truncated: base.truncated || head.truncated}
	if len(t) == 0 {
		notes = append(notes, "no source modules touched")
	}
	edges := mergeEdges(base.edges, head.edges)
	hops := reach(t, edges, maxHop)
	if cut := capModules(hops, t); cut > 0 {
		notes = append(notes, fmt.Sprintf("graph cut to %d modules (%d far modules left out)", maxModules, cut))
	}
	rolls := rollUps(in)
	for id, hop := range hops {
		g.Modules = append(g.Modules, module(id, hop, t[id], base, head, rolls[id]))
	}
	sortModules(g.Modules)
	g.Edges = graphEdges(edges, hops, contracts(in.Agg))
	g.NetEffect = netEffect(g.Modules, g.Edges)
	g.Note = strings.Join(notes, "; ")
	return g, nil
}

// touch is what the diff says about one module.
type touch struct {
	files   []string // changed file paths in the module, sorted
	added   bool     // a source file was added here
	deleted bool     // a source file was deleted (or renamed away) here
	// atHead and atBase are set when a changed file proves the module has
	// sources on that side (modified files prove both).
	atHead, atBase bool
	owned          bool
	// gain and loss count source files added or renamed in, and deleted or
	// renamed out (to estimate an unreadable side's file counts).
	gain, loss int
}

// isSource reports whether p is a file parse supports (a module member).
func isSource(p string) bool { return p != "" && parse.Lang(p) != "" }

// touchedModules groups the diff's source files by module. A renamed file
// touches its old and its new module (both list its new path).
func touchedModules(files []model.File) map[string]*touch {
	out := map[string]*touch{}
	get := func(m string) *touch {
		t := out[m]
		if t == nil {
			t = &touch{}
			out[m] = t
		}
		return t
	}
	for _, f := range files {
		renamedFrom := ""
		if f.OldPath != "" && f.OldPath != f.Path && !f.Added && !f.Deleted {
			renamedFrom = f.OldPath
		}
		moved := renamedFrom != "" && ModuleOf(renamedFrom) != ModuleOf(f.Path)
		if isSource(f.Path) {
			t := get(ModuleOf(f.Path))
			t.files = append(t.files, f.Path)
			t.owned = t.owned || f.Owned
			switch {
			case f.Added:
				t.added, t.atHead = true, true
				t.gain++
			case f.Deleted:
				t.deleted, t.atBase = true, true
				t.loss++
			case moved:
				t.atHead = true
				t.gain++
			default:
				t.atHead, t.atBase = true, true
			}
		}
		if moved && isSource(renamedFrom) {
			t := get(ModuleOf(renamedFrom))
			t.files = append(t.files, f.Path)
			t.owned = t.owned || f.Owned
			t.deleted, t.atBase = true, true
			t.loss++
		}
	}
	for _, t := range out {
		sort.Strings(t.files)
		t.files = compact(t.files)
	}
	return out
}

// compact drops adjacent duplicates of a sorted slice.
func compact(s []string) []string {
	out := s[:0]
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}

// edgeKey is a module edge (From imports To).
type edgeKey struct{ from, to string }

// revGraph is the module view of one revision's import graph.
type revGraph struct {
	files     map[string]int  // module → source files
	edges     map[edgeKey]int // module edge → import statements
	truncated bool
	ok        bool // read from the revision (not estimated)
}

// moduleGraph aggregates a file graph into modules.
func moduleGraph(fg *link.FileGraph) revGraph {
	g := revGraph{files: map[string]int{}, edges: map[edgeKey]int{}, truncated: fg.Truncated, ok: true}
	for _, f := range fg.Files {
		from := ModuleOf(f)
		g.files[from]++
		for _, imp := range fg.Imports[f] {
			seen := map[string]bool{}
			for _, tf := range imp.Targets {
				to := ModuleOf(tf)
				if to == from || seen[to] {
					continue
				}
				seen[to] = true
				g.edges[edgeKey{from, to}]++
			}
		}
	}
	return g
}

// estimate derives one side's module file counts from the other side and
// the diff (toHead: base → head; else head → base); edges are kept as is.
func estimate(from revGraph, t map[string]*touch, toHead bool) revGraph {
	g := revGraph{files: map[string]int{}, edges: from.edges, truncated: from.truncated}
	for m, n := range from.files {
		g.files[m] = n
	}
	for m, tc := range t {
		gain, loss := tc.gain, tc.loss
		if !toHead {
			gain, loss = loss, gain
		}
		g.files[m] = max(0, g.files[m]+gain-loss)
	}
	return g
}

// revGraphs reads the module graphs at base and head. An unreadable side is
// estimated from the other plus the diff, with a note; only ctx errors are
// returned.
func revGraphs(ctx context.Context, in Input, t map[string]*touch) (base, head revGraph, notes []string, err error) {
	var cache link.Cache
	if in.Cache != nil {
		cache = in.Cache
	}
	var src link.Source
	if in.Src != nil {
		src = in.Src
	}
	read := func(rev string) (revGraph, error) {
		fg, err := link.ImportGraph(ctx, src, cache, rev)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return revGraph{}, fmt.Errorf("arch: %w", cerr)
			}
			return revGraph{}, err
		}
		return moduleGraph(fg), nil
	}
	base, berr := read(in.BaseSHA)
	if berr != nil && ctx.Err() != nil {
		return base, head, nil, berr
	}
	head, herr := base, berr
	if in.HeadSHA != in.BaseSHA {
		head, herr = read(in.HeadSHA)
		if herr != nil && ctx.Err() != nil {
			return base, head, nil, herr
		}
	}
	switch {
	case berr == nil && herr == nil:
	case berr == nil:
		head = estimate(base, t, true)
		notes = append(notes, "head not readable locally: graph from base imports plus the diff's files")
	case herr == nil:
		base = estimate(head, t, false)
		notes = append(notes, "base not readable locally: graph from head imports plus the diff's files")
	default:
		empty := revGraph{files: map[string]int{}, edges: map[edgeKey]int{}}
		base, head = estimate(empty, t, false), estimate(empty, t, true)
		notes = append(notes, "import analysis unavailable: revisions not readable locally")
	}
	return base, head, notes, nil
}

// edgeState is one module edge at base and head.
type edgeState struct {
	base, head int // import statements on each side (0: absent)
}

// mergeEdges joins base and head edges.
func mergeEdges(base, head map[edgeKey]int) map[edgeKey]edgeState {
	out := map[edgeKey]edgeState{}
	for k, n := range base {
		out[k] = edgeState{base: n}
	}
	for k, n := range head {
		e := out[k]
		e.head = n
		out[k] = e
	}
	return out
}

// reach returns every module within maxHop import hops (either direction)
// of a touched module, with its hop.
func reach(t map[string]*touch, edges map[edgeKey]edgeState, maxHop int) map[string]int {
	adj := map[string][]string{}
	for k := range edges {
		adj[k.from] = append(adj[k.from], k.to)
		adj[k.to] = append(adj[k.to], k.from)
	}
	hops := map[string]int{}
	var frontier []string
	for m := range t {
		hops[m] = 0
		frontier = append(frontier, m)
	}
	for hop := 1; hop <= maxHop && len(frontier) > 0; hop++ {
		var next []string
		for _, m := range frontier {
			for _, n := range adj[m] {
				if _, ok := hops[n]; !ok {
					hops[n] = hop
					next = append(next, n)
				}
			}
		}
		frontier = next
	}
	return hops
}

// capModules drops the farthest context modules (highest hop, then path)
// past maxModules and returns how many were dropped.
func capModules(hops map[string]int, t map[string]*touch) int {
	if len(hops) <= maxModules {
		return 0
	}
	var ctxMods []string
	for m := range hops {
		if t[m] == nil {
			ctxMods = append(ctxMods, m)
		}
	}
	sort.Slice(ctxMods, func(i, j int) bool {
		if hops[ctxMods[i]] != hops[ctxMods[j]] {
			return hops[ctxMods[i]] > hops[ctxMods[j]]
		}
		return ctxMods[i] > ctxMods[j]
	})
	cut := 0
	for _, m := range ctxMods {
		if len(hops) <= maxModules {
			break
		}
		delete(hops, m)
		cut++
	}
	return cut
}

// roll is one module's unit roll-up.
type roll struct {
	units, eyes, skim, skip, unsure, untested, undoc, breaking, risk int
	owned                                                            bool
}

// rollUps sums unit aggregates per module (Agg.Units; Input.Units counts
// when there is no aggregate).
func rollUps(in Input) map[string]roll {
	out := map[string]roll{}
	unitPath := map[string]string{}
	for _, u := range in.Units {
		unitPath[u.ID] = u.Path
	}
	if in.Agg == nil {
		for _, u := range in.Units {
			r := out[ModuleOf(u.Path)]
			r.units++
			out[ModuleOf(u.Path)] = r
		}
		return out
	}
	undoc := map[string]bool{}
	for _, id := range in.Agg.Undocumented {
		undoc[id] = true
	}
	for _, ua := range in.Agg.Units {
		p := ua.Path
		if p == "" {
			p = unitPath[ua.UnitID]
		}
		if p == "" {
			continue
		}
		m := ModuleOf(p)
		r := out[m]
		r.units++
		switch ua.Attention {
		case model.AttnEyes:
			r.eyes++
		case model.AttnSkim:
			r.skim++
		case model.AttnSkip:
			r.skip++
		}
		r.unsure += b2i(ua.Unsure)
		r.untested += b2i(ua.Untested)
		r.undoc += b2i(undoc[ua.UnitID])
		r.breaking += b2i(ua.Breaking)
		r.risk = max(r.risk, ua.RiskLevel)
		r.owned = r.owned || ua.Owned
		out[m] = r
	}
	return out
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// module builds one node.
func module(id string, hop int, t *touch, base, head revGraph, r roll) model.ArchModule {
	m := model.ArchModule{
		ID: id, Hop: hop, Status: model.ArchContext, FileCount: head.files[id],
		Units: r.units, Eyes: r.eyes, Skim: r.skim, Skip: r.skip, Unsure: r.unsure,
		Untested: r.untested, Undocumented: r.undoc, Breaking: r.breaking, RiskMax: r.risk,
		Owned: r.owned,
	}
	if t != nil {
		m.Files = append([]string(nil), t.files...)
		m.Owned = m.Owned || t.owned
		m.Status = touchStatus(id, t, base, head)
	}
	if m.Status == model.ArchRemoved || m.FileCount == 0 {
		m.FileCount = base.files[id]
	}
	switch {
	case r.eyes > 0:
		m.Attention = model.AttnEyes
	case r.skim > 0:
		m.Attention = model.AttnSkim
	case r.skip > 0:
		m.Attention = model.AttnSkip
	}
	return m
}

// touchStatus is new (added files, nothing at base), removed (deleted
// files, nothing at head) or changed.
func touchStatus(id string, t *touch, base, head revGraph) string {
	switch {
	case t.added && !t.atBase && base.files[id] == 0:
		return model.ArchNew
	case t.deleted && !t.atHead && head.files[id] == 0:
		return model.ArchRemoved
	}
	return model.ArchChanged
}

// sortModules orders touched modules first (eyes, then skim, then the
// rest; then path), then context modules by hop and path.
func sortModules(ms []model.ArchModule) {
	rank := func(m model.ArchModule) int {
		if m.Hop > 0 {
			return 3
		}
		switch m.Attention {
		case model.AttnEyes:
			return 0
		case model.AttnSkim:
			return 1
		}
		return 2
	}
	sort.Slice(ms, func(i, j int) bool {
		ri, rj := rank(ms[i]), rank(ms[j])
		if ri != rj {
			return ri < rj
		}
		if ms[i].Hop != ms[j].Hop {
			return ms[i].Hop < ms[j].Hop
		}
		return ms[i].ID < ms[j].ID
	})
}

// notContract reports whether a headline kind is not a contract (weakened
// tests and pii lines; every other kind is breaking or a contract value).
func notContract(kind string) bool { return kind == "test weakened" || kind == "pii or secret" }

// contracts maps a module to the contract headline texts of its units
// (breaking or a contract change), deduplicated, in headline order.
func contracts(agg *model.ItemAgg) map[string][]string {
	out := map[string][]string{}
	if agg == nil {
		return out
	}
	seen := map[string]bool{}
	for _, h := range agg.Headlines {
		if notContract(h.Kind) || h.Path == "" {
			continue
		}
		m := ModuleOf(h.Path)
		text := h.Text + " · " + h.Kind
		if seen[m+"\x00"+text] {
			continue
		}
		seen[m+"\x00"+text] = true
		out[m] = append(out[m], text)
	}
	return out
}

// graphEdges lists the edges between shown modules, sorted by From, To.
func graphEdges(edges map[edgeKey]edgeState, hops map[string]int, cs map[string][]string) []model.ArchEdge {
	var out []model.ArchEdge
	for k, e := range edges {
		if _, ok := hops[k.from]; !ok {
			continue
		}
		if _, ok := hops[k.to]; !ok {
			continue
		}
		ae := model.ArchEdge{From: k.from, To: k.to, Status: model.ArchSame, Imports: e.head}
		switch {
		case e.base == 0:
			ae.Status = model.ArchNew
		case e.head == 0:
			ae.Status, ae.Imports = model.ArchRemoved, e.base
		}
		ae.Contracts = append([]string(nil), cs[k.to]...)
		out = append(out, ae)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// netEffect writes the graph diff in at most maxNetEffect sentences: new
// modules, removed modules, new edges ("X now imports Y"), removed edges
// ("X no longer imports Y"), each group sorted; past the cap the last line
// counts the rest.
func netEffect(ms []model.ArchModule, es []model.ArchEdge) []string {
	var newMods, gone, added, dropped []string
	for _, m := range ms {
		switch m.Status {
		case model.ArchNew:
			newMods = append(newMods, "new module "+m.ID)
		case model.ArchRemoved:
			gone = append(gone, m.ID+" removed")
		}
	}
	for _, e := range es {
		switch e.Status {
		case model.ArchNew:
			added = append(added, e.From+" now imports "+e.To)
		case model.ArchRemoved:
			dropped = append(dropped, e.From+" no longer imports "+e.To)
		}
	}
	var all []string
	for _, g := range [][]string{newMods, gone, added, dropped} {
		sort.Strings(g)
		all = append(all, g...)
	}
	if len(all) > maxNetEffect {
		rest := len(all) - (maxNetEffect - 1)
		all = append(all[:maxNetEffect-1], fmt.Sprintf("and %d more import changes", rest))
	}
	return all
}
