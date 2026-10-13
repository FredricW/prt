package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/fredricw/prt/internal/aggregate"
	"github.com/fredricw/prt/internal/cluster"
	"github.com/fredricw/prt/internal/embed"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/judge"
	"github.com/fredricw/prt/internal/link"
	"github.com/fredricw/prt/internal/model"
)

// Paths of labelDiff's units (one unit per file).
const (
	pClient = "web/src/api/client.ts"
	pUsers  = "server/users.go"
	pReport = "scripts/report.py"
	pTest   = "web/src/__tests__/client.test.ts"
	pNotes  = "docs/notes.md"
)

// fakeM2 stands in for link, cluster and judge (stubs until their agents
// land) so the wiring is tested on its own:
//
//   - structural: one tests link, client.test.ts → client.ts;
//   - embed candidates: client.ts–users.go (.8), client.ts–client.test.ts
//     (.9, structurally linked: never judged), report.py–notes.md (.75);
//   - mechanical: users.go → report.py (classes of one shape each, so
//     below mechanical_min: judged);
//   - Jev relation: same intent .8, same pattern .9 for the mechanical pair,
//     same intent .5 for report.py–notes.md (below keep_pair_p);
//   - cluster: {client.ts, client.test.ts}, {users.go, report.py}, then
//     notes.md ungrouped (oneGroup: everything but notes.md in one group).
type fakeM2 struct {
	mu sync.Mutex

	structErr    error
	oneGroup     bool
	unknownPairs bool
	nameValue    string // default "b1"

	pairCalls  [][]judge.Pair
	keepP      []float64
	clusterIn  []cluster.Input
	nameCalls  [][]judge.GroupText
	splitCalls [][]judge.FileText
	aggIn      []aggregate.Input
	embedCands int
	mechCalls  int
}

func pathOf(units []model.ChangeUnit) map[string]model.ChangeUnit {
	m := map[string]model.ChangeUnit{}
	for _, u := range units {
		m[u.Path] = u
	}
	return m
}

func lnk(units map[string]model.ChangeUnit, from, to string, k model.LinkKind, src model.LinkSource, w float64) model.Link {
	return model.Link{From: units[from].ID, To: units[to].ID, Kind: k, Source: src, Weight: w}
}

func (f *fakeM2) install(p *Pipeline) {
	p.fn.structural = func(_ context.Context, in link.Input) ([]model.Link, error) {
		if f.structErr != nil {
			return nil, f.structErr
		}
		u := pathOf(in.Units)
		return []model.Link{lnk(u, pTest, pClient, model.LinkTests, model.LinkStructural, 1)}, nil
	}
	p.fn.embedCands = func(units []model.ChangeUnit, vecs map[string][]float32, _ link.EmbedOptions) []model.Link {
		f.mu.Lock()
		f.embedCands++
		f.mu.Unlock()
		if len(vecs) != len(units) {
			panic("embed candidates without a vector per unit")
		}
		u := pathOf(units)
		return []model.Link{
			lnk(u, pClient, pUsers, model.LinkEmbedNeighbor, model.LinkEmbed, 0.8),
			lnk(u, pClient, pTest, model.LinkEmbedNeighbor, model.LinkEmbed, 0.9),
			lnk(u, pReport, pNotes, model.LinkEmbedNeighbor, model.LinkEmbed, 0.75),
		}
	}
	p.fn.mechanical = func(units []model.ChangeUnit) []model.Link {
		f.mu.Lock()
		f.mechCalls++
		f.mu.Unlock()
		u := pathOf(units)
		return []model.Link{lnk(u, pUsers, pReport, model.LinkMechanical, model.LinkEmbed, 1)}
	}
	p.fn.pairs = f.pairs
	p.fn.confirm = f.confirm
	p.fn.cluster = f.cluster
	p.fn.names = f.names
	p.fn.nameCtx = func(title string, bullets []string) string { return title + "|" + strings.Join(bullets, "|") }
	p.fn.applyNames = applyB1
	p.fn.separatePR = f.separatePR
	p.fn.splitCtx = func(gs []judge.GroupSummary) string {
		var ids []string
		for _, g := range gs {
			ids = append(ids, g.ID)
		}
		return strings.Join(ids, ",")
	}
	p.fn.suggestion = suggestFiles
	p.fn.aggregate = func(in aggregate.Input) model.ItemAgg {
		f.mu.Lock()
		f.aggIn = append(f.aggIn, in)
		f.mu.Unlock()
		return aggregate.Item(in)
	}
}

func (f *fakeM2) pairs(_ context.Context, _ jev.Client, in judge.PairInput) (judge.Result, error) {
	f.mu.Lock()
	f.pairCalls = append(f.pairCalls, in.Pairs)
	f.mu.Unlock()
	var res judge.Result
	for _, pr := range in.Pairs {
		l := model.Label{UnitID: model.PairSubject(pr.A.ID, pr.B.ID), Question: model.QRelation, Value: model.RelSameIntent, P: 0.8, Confidence: 0.8}
		paths := []string{pr.A.Path, pr.B.Path}
		sort.Strings(paths)
		switch strings.Join(paths, " ") {
		case pNotes + " " + pReport:
			l.P, l.Confidence = 0.5, 0.5
		case pReport + " " + pUsers:
			l.Value, l.P, l.Confidence = model.RelSamePattern, 0.9, 0.9
		}
		if f.unknownPairs {
			l = model.Label{UnitID: l.UnitID, Question: model.QRelation, Value: model.LabelUnknown}
		}
		res.Labels = append(res.Labels, l)
		res.Calls++
	}
	return res, nil
}

// confirm follows judge.Confirm's contract.
func (f *fakeM2) confirm(cands []model.Link, ls []model.Label, keepP float64) ([]model.Link, int) {
	f.mu.Lock()
	f.keepP = append(f.keepP, keepP)
	f.mu.Unlock()
	by := map[string]model.Label{}
	for _, l := range ls {
		by[l.UnitID] = l
	}
	var kept []model.Link
	rejected := 0
	for _, c := range cands {
		l, ok := by[model.PairSubject(c.From, c.To)]
		if !ok || model.IsUnknown(l) {
			continue
		}
		if l.Value == model.RelUnrelated || l.P < keepP {
			rejected++
			continue
		}
		c.Source, c.Relation, c.P = model.LinkJev, l.Value, l.P
		kept = append(kept, c)
	}
	return kept, rejected
}

func (f *fakeM2) cluster(in cluster.Input) cluster.Result {
	f.mu.Lock()
	f.clusterIn = append(f.clusterIn, in)
	f.mu.Unlock()
	u := pathOf(in.Units)
	sets := [][]string{{pClient, pTest}, {pUsers, pReport}}
	if f.oneGroup {
		sets = [][]string{{pClient, pTest, pUsers, pReport}}
	}
	var groups []model.Group
	for _, set := range sets {
		var ids []string
		for _, p := range set {
			ids = append(ids, u[p].ID)
		}
		groups = append(groups, model.Group{ID: model.GroupID(ids), ItemID: in.ItemID, UnitIDs: ids, Name: set[0], NameSource: model.NamePlaceholder, Order: len(groups)})
	}
	ids := []string{u[pNotes].ID}
	groups = append(groups, model.Group{ID: model.GroupID(ids), ItemID: in.ItemID, UnitIDs: ids, Name: pNotes, NameSource: model.NamePlaceholder, Order: len(groups), Ungrouped: true})
	return cluster.Result{Groups: groups, Stats: model.GroupingStats{Groups: len(sets), Ungrouped: 1}}
}

func (f *fakeM2) names(_ context.Context, _ jev.Client, in judge.NameInput) (judge.Result, error) {
	f.mu.Lock()
	f.nameCalls = append(f.nameCalls, in.Groups)
	f.mu.Unlock()
	v := f.nameValue
	if v == "" {
		v = "b1"
	}
	ctx := in.Title + "|" + strings.Join(in.Bullets, "|")
	var res judge.Result
	for _, g := range in.Groups {
		l := model.Label{UnitID: model.GroupSubject(g.ID), Question: model.QNameBullet, Value: v, P: 0.9, Confidence: 0.9, Ctx: ctx}
		if v == model.LabelUnknown {
			l.P, l.Confidence = 0, 0
		}
		res.Labels = append(res.Labels, l)
		res.Calls++
	}
	return res, nil
}

// applyB1 names groups whose current name label is b1 after bullet 1.
func applyB1(groups []model.Group, ls []model.Label, bullets []string, ctx string) []model.Group {
	out := append([]model.Group(nil), groups...)
	for i, g := range out {
		for _, l := range ls {
			if l.UnitID == model.GroupSubject(g.ID) && l.Ctx == ctx && l.Value == "b1" {
				out[i].Name, out[i].NameSource, out[i].DescBullet = bullets[0], model.NameBullet, bullets[0]
			}
		}
	}
	return out
}

func (f *fakeM2) separatePR(_ context.Context, _ jev.Client, in judge.SplitInput) (judge.Result, error) {
	f.mu.Lock()
	f.splitCalls = append(f.splitCalls, in.Files)
	f.mu.Unlock()
	var ids []string
	for _, g := range in.Groups {
		ids = append(ids, g.ID)
	}
	var res judge.Result
	for _, ft := range in.Files {
		l := model.Label{UnitID: model.FileSubject(in.ItemID, ft.Path), Question: model.QSeparatePR, Value: model.LabelNo, P: 0.1, Confidence: 0.9, Ctx: judge.FileCtx(strings.Join(ids, ","), ft.Units)}
		if ft.Path == pClient {
			l.Value, l.P, l.Confidence = model.LabelYes, 0.7, 0.7
		}
		res.Labels = append(res.Labels, l)
		res.Calls++
	}
	return res, nil
}

func suggestFiles(itemID string, units []model.ChangeUnit, _ []model.Group, ls []model.Label, ctx string) *model.SplitSuggestion {
	var s model.SplitSuggestion
	ctxs := judge.FileCtxs(ctx, units)
	for _, l := range ls {
		path := strings.TrimPrefix(l.UnitID, "file:"+itemID+":")
		if l.Ctx == ctxs[path] && l.P >= 0.5 {
			s.Files = append(s.Files, model.SplitFile{Path: path, P: l.P})
		}
	}
	if len(s.Files) == 0 {
		return nil
	}
	return &s
}

// pairPaths renders the judged pairs of one call as sorted "a+b" path pairs.
func pairPaths(ps []judge.Pair) []string {
	var out []string
	for _, p := range ps {
		a, b := p.A.Path, p.B.Path
		if b < a {
			a, b = b, a
		}
		out = append(out, a+"+"+b)
	}
	sort.Strings(out)
	return out
}

// m2Env is a labelEnv pipeline with fakeM2 installed.
func m2Env(t *testing.T, jc jev.Client, emb embed.Embedder, f *fakeM2) (*labelEnv, *Pipeline) {
	t.Helper()
	e := newLabelEnv(t)
	p := e.pipeline(jc, func(d *Deps) {
		if emb != nil {
			d.Embedder = emb
		}
	})
	f.install(p)
	return e, p
}

func TestM2StagesWiredAndCached(t *testing.T) {
	f := &fakeM2{}
	emb := &embed.Fake{}
	e, p := m2Env(t, &jev.Fake{}, emb, f)
	e.run(t, p)

	if rl := e.stage(t, model.StageLink); rl.Status != model.StageDone || rl.Note != "1 tests" || rl.Out != 1 {
		t.Errorf("link row = %+v", rl)
	}
	if rl := e.stage(t, model.StageEmbed); rl.Status != model.StageDone || rl.Calls != 1 || !strings.Contains(rl.Note, "3 candidates · 1 mechanical") {
		t.Errorf("embed row = %+v", rl)
	}
	if emb.Calls != 1 || emb.Texts != 5 {
		t.Errorf("embedder calls %d texts %d, want one call with 5 texts", emb.Calls, emb.Texts)
	}
	// Structural pair never sent; the mechanical pair (class below
	// mechanical_min) is.
	if len(f.pairCalls) != 1 {
		t.Fatalf("pair calls = %d", len(f.pairCalls))
	}
	want := []string{pUsers + "+" + pClient, pNotes + "+" + pReport, pReport + "+" + pUsers}
	sort.Strings(want)
	if got := pairPaths(f.pairCalls[0]); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pairs judged = %v, want %v", got, want)
	}
	if !sort.SliceIsSorted(f.pairCalls[0], func(i, j int) bool {
		a, b := f.pairCalls[0][i], f.pairCalls[0][j]
		return model.PairSubject(a.A.ID, a.B.ID) < model.PairSubject(b.A.ID, b.B.ID)
	}) {
		t.Error("pairs not sorted by subject")
	}
	jrl := e.stage(t, model.StageJev)
	if !strings.Contains(jrl.Note, "pairs: 3 asked, 0 cached") || jrl.Calls < 3 {
		t.Errorf("jev row = %+v", jrl)
	}
	if len(f.keepP) != 1 || f.keepP[0] != 0.6 {
		t.Errorf("confirm keepP = %v, want the config's 0.6", f.keepP)
	}
	// Cluster gets structural (1) + confirmed (2) + mechanical (1) links.
	if in := f.clusterIn[0]; len(in.Links) != 4 || len(in.Labels) == 0 {
		t.Errorf("cluster links = %+v (labels %d)", in.Links, len(in.Labels))
	}
	agg := e.agg(t)
	g := agg.Grouping
	if g == nil {
		t.Fatal("agg.Grouping nil")
	}
	if g.Units != 5 || g.Structural[model.LinkTests] != 1 || g.EmbedCandidates != 3 || g.MechanicalCandidates != 1 ||
		g.PairsAsked != 3 || g.JevKept[model.RelSameIntent] != 1 || g.JevKept[model.RelSamePattern] != 1 || g.JevRejected != 1 ||
		g.Groups != 2 || g.Ungrouped != 1 || g.EmbedNote != "" {
		t.Errorf("grouping = %+v", *g)
	}
	if agg.Split == nil || len(agg.Split.Files) != 1 || agg.Split.Files[0].Path != pClient {
		t.Errorf("split = %+v", agg.Split)
	}
	if len(f.nameCalls) != 1 || len(f.nameCalls[0]) != 2 {
		t.Errorf("name calls = %+v, want one call for the 2 real groups", f.nameCalls)
	}
	if len(f.splitCalls) != 1 || len(f.splitCalls[0]) != 5 {
		t.Errorf("split calls = %+v", f.splitCalls)
	}
	groups, err := e.st.Groups(context.Background(), e.item.ID)
	if err != nil || len(groups) != 3 {
		t.Fatalf("stored groups = %+v, %v", groups, err)
	}
	for _, gr := range groups {
		if gr.Ungrouped != (gr.NameSource == model.NamePlaceholder) || (!gr.Ungrouped && gr.Name != "Build the url once") {
			t.Errorf("group %+v: real groups take the bullet name, ungrouped keep placeholders", gr)
		}
	}
	links, _ := e.st.Links(context.Background(), e.item.ID)
	if len(links) != 4 {
		t.Errorf("stored links = %d, want 4", len(links))
	}
	if in := f.aggIn[0]; in.Links == nil || len(in.Links) != 4 || len(in.Groups) != 3 {
		t.Errorf("aggregate input links %d (nil %v) groups %d", len(in.Links), in.Links == nil, len(in.Groups))
	}

	// Second run: every M2 answer is cached.
	e.run(t, p)
	if emb.Calls != 1 {
		t.Errorf("second run embedded again (%d calls)", emb.Calls)
	}
	if rl := e.stage(t, model.StageEmbed); !rl.CacheHit || rl.Status != model.StageCached || rl.Calls != 0 {
		t.Errorf("second embed row = %+v", rl)
	}
	if len(f.pairCalls) != 1 || len(f.nameCalls) != 1 || len(f.splitCalls) != 1 {
		t.Errorf("second run asked Jev: pairs %d names %d split %d calls", len(f.pairCalls), len(f.nameCalls), len(f.splitCalls))
	}
	if rl := e.stage(t, model.StageJev); !strings.Contains(rl.Note, "pairs: 3 asked, 3 cached") || !rl.CacheHit {
		t.Errorf("second jev row = %+v", rl)
	}
	if agg := e.agg(t); agg.Grouping == nil || agg.Grouping.JevRejected != 1 || agg.Split == nil {
		t.Errorf("second run lost grouping or split: %+v %+v", agg.Grouping, agg.Split)
	}
	groups, _ = e.st.Groups(context.Background(), e.item.ID)
	if groups[0].Name != "Build the url once" {
		t.Errorf("cached names not applied: %+v", groups[0])
	}
}

func TestEmbedderMissingSkipsStage3(t *testing.T) {
	f := &fakeM2{}
	e, p := m2Env(t, &jev.Fake{}, nil, f)
	e.run(t, p)
	rl := e.stage(t, model.StageEmbed)
	if rl.Status != model.StageSkipped || rl.Note != noteNoEmbedder {
		t.Errorf("embed row = %+v", rl)
	}
	if f.embedCands != 0 || f.mechCalls != 1 {
		t.Errorf("embed candidates %d, mechanical %d calls; want 0 and 1", f.embedCands, f.mechCalls)
	}
	if got := pairPaths(f.pairCalls[0]); len(got) != 1 || got[0] != pReport+"+"+pUsers {
		t.Errorf("pairs judged = %v, want only the mechanical pair", got)
	}
	if g := e.agg(t).Grouping; g == nil || g.EmbedNote != noteNoEmbedder || g.EmbedCandidates != 0 || g.MechanicalCandidates != 1 {
		t.Errorf("grouping = %+v", g)
	}
}

func TestEmbedUnavailableSkipsWithReason(t *testing.T) {
	f := &fakeM2{}
	e, p := m2Env(t, &jev.Fake{}, &embed.Fake{Err: fmt.Errorf("%w: ollama: connection refused", embed.ErrUnavailable)}, f)
	e.run(t, p)
	rl := e.stage(t, model.StageEmbed)
	want := "embeddings unavailable: ollama: connection refused; groups from structural links only"
	if rl.Status != model.StageSkipped || rl.Note != want || rl.Err != "" {
		t.Errorf("embed row = %+v, want skipped with %q", rl, want)
	}
	if g := e.agg(t).Grouping; g == nil || g.EmbedNote != want {
		t.Errorf("grouping = %+v", g)
	}
	if f.embedCands != 0 || f.mechCalls != 1 {
		t.Errorf("embed candidates %d, mechanical %d", f.embedCands, f.mechCalls)
	}
}

func TestEmbedErrorIsSoft(t *testing.T) {
	f := &fakeM2{}
	e, p := m2Env(t, &jev.Fake{}, &embed.Fake{Err: errors.New("dimension mismatch")}, f)
	e.run(t, p)
	if rl := e.stage(t, model.StageEmbed); rl.Status != model.StageFailed || !strings.Contains(rl.Err, "dimension mismatch") {
		t.Errorf("embed row = %+v", rl)
	}
	for _, s := range []model.Stage{model.StageCluster, model.StageAggregate} {
		if rl := e.stage(t, s); rl.Status != model.StageDone {
			t.Errorf("%s row = %+v", s, rl)
		}
	}
	if g := e.agg(t).Grouping; g == nil || !strings.HasPrefix(g.EmbedNote, "embeddings failed: ") || g.MechanicalCandidates != 1 {
		t.Errorf("grouping = %+v", g)
	}
}

func TestJevNilLeavesPairsUnknown(t *testing.T) {
	f := &fakeM2{}
	e, p := m2Env(t, nil, &embed.Fake{}, f)
	e.run(t, p)
	if len(f.pairCalls) != 0 || len(f.nameCalls) != 0 || len(f.splitCalls) != 0 {
		t.Errorf("Jev nil but judge called: pairs %d names %d split %d", len(f.pairCalls), len(f.nameCalls), len(f.splitCalls))
	}
	if rl := e.stage(t, model.StageJev); !strings.Contains(rl.Note, "pairs: 3 asked, 0 cached (Jev unavailable)") {
		t.Errorf("jev row = %+v", rl)
	}
	if in := f.clusterIn[0]; len(in.Links) != 2 {
		t.Errorf("cluster links = %+v, want structural + mechanical only", in.Links)
	}
	agg := e.agg(t)
	if agg.Grouping == nil || len(agg.Grouping.JevKept) != 0 || agg.Split != nil {
		t.Errorf("grouping %+v split %+v", agg.Grouping, agg.Split)
	}
	groups, _ := e.st.Groups(context.Background(), e.item.ID)
	for _, g := range groups {
		if g.NameSource != model.NamePlaceholder {
			t.Errorf("group %+v named without Jev", g)
		}
	}
}

// A pair-pass failure is soft and never marks labels missing.
func TestPairFailureIsSoft(t *testing.T) {
	f := &fakeM2{}
	e, p := m2Env(t, &jev.Fake{}, &embed.Fake{}, f)
	p.fn.pairs = func(context.Context, jev.Client, judge.PairInput) (judge.Result, error) {
		return judge.Result{}, errors.New("over-limit question")
	}
	e.run(t, p)
	rl := e.stage(t, model.StageJev)
	if rl.Status != model.StageDone || !strings.Contains(rl.Note, "over-limit question") {
		t.Errorf("jev row = %+v", rl)
	}
	if e.agg(t).LabelsMissing {
		t.Error("a pair failure set LabelsMissing")
	}
}

func TestStructuralErrorIsSoft(t *testing.T) {
	f := &fakeM2{structErr: errors.New("symbol index broke")}
	e, p := m2Env(t, &jev.Fake{}, &embed.Fake{}, f)
	e.run(t, p)
	if rl := e.stage(t, model.StageLink); rl.Status != model.StageFailed || !strings.Contains(rl.Err, "symbol index broke") {
		t.Errorf("link row = %+v", rl)
	}
	if in := f.aggIn[0]; in.Links != nil {
		t.Errorf("aggregate links = %+v, want nil when stage 2 failed", in.Links)
	}
	// Clustering still gets the Jev-confirmed and mechanical links.
	if in := f.clusterIn[0]; len(in.Links) == 0 {
		t.Errorf("cluster links empty after a stage 2 failure; confirmed and mechanical links dropped")
	}
	// Without structural links the client/test pair is judged too.
	if got := pairPaths(f.pairCalls[0]); len(got) != 4 {
		t.Errorf("pairs judged = %v", got)
	}
	if rl := e.stage(t, model.StageCluster); rl.Status != model.StageDone {
		t.Errorf("cluster row = %+v", rl)
	}
}

// With no structural links (and no error), aggregate still gets a non-nil
// link slice.
func TestAggregateLinksNonNil(t *testing.T) {
	f := &fakeM2{}
	e, p := m2Env(t, nil, nil, f)
	p.fn.structural = func(context.Context, link.Input) ([]model.Link, error) { return nil, nil }
	p.fn.mechanical = func([]model.ChangeUnit) []model.Link { return nil }
	e.run(t, p)
	if in := f.aggIn[0]; in.Links == nil || len(in.Links) != 0 {
		t.Errorf("aggregate links = %#v, want empty non-nil", in.Links)
	}
}

func TestGroupEditsReachCluster(t *testing.T) {
	f := &fakeM2{}
	e, p := m2Env(t, &jev.Fake{}, &embed.Fake{}, f)
	e.run(t, p)
	if len(f.clusterIn[0].Edits) != 0 {
		t.Fatalf("edits before any edit: %+v", f.clusterIn[0].Edits)
	}
	us := pathOf(e.units(t))
	edit := model.GroupEdit{ItemID: e.item.ID, Op: model.EditMerge, Units: []string{us[pClient].ID, us[pNotes].ID}, At: labelNow}
	if err := e.st.AppendGroupEdit(context.Background(), edit); err != nil {
		t.Fatal(err)
	}
	e.run(t, p)
	got := f.clusterIn[len(f.clusterIn)-1].Edits
	if len(got) != 1 || got[0].Op != model.EditMerge || len(got[0].Units) != 2 {
		t.Errorf("cluster edits = %+v", got)
	}
}

func TestSplitNeedsTwoGroups(t *testing.T) {
	f := &fakeM2{oneGroup: true}
	e, p := m2Env(t, &jev.Fake{}, &embed.Fake{}, f)
	e.run(t, p)
	if len(f.splitCalls) != 0 || e.agg(t).Split != nil {
		t.Errorf("split asked with one group: calls %d split %+v", len(f.splitCalls), e.agg(t).Split)
	}
	if len(f.nameCalls) != 1 || len(f.nameCalls[0]) != 1 {
		t.Errorf("name calls = %+v", f.nameCalls)
	}
}

// Unknown answers never overwrite known ones, and unknown pairs are asked
// again on the next run.
func TestUnknownNeverOverwritesKnown(t *testing.T) {
	f := &fakeM2{}
	e, p := m2Env(t, &jev.Fake{}, &embed.Fake{}, f)
	e.run(t, p)

	// New title: names are stale, re-asked, and Jev answers unknown.
	e.item.Title = "feat: users v2"
	if err := e.st.UpsertItem(context.Background(), e.item); err != nil {
		t.Fatal(err)
	}
	f.nameValue = model.LabelUnknown
	e.run(t, p)
	if len(f.nameCalls) != 2 {
		t.Fatalf("names not re-asked after the title changed (%d calls)", len(f.nameCalls))
	}
	groups, _ := e.st.Groups(context.Background(), e.item.ID)
	var subjects []string
	for _, g := range groups {
		if !g.Ungrouped {
			subjects = append(subjects, model.GroupSubject(g.ID))
		}
	}
	ls, _ := e.st.Labels(context.Background(), subjects)
	if len(ls) != 2 {
		t.Fatalf("name labels = %+v", ls)
	}
	for _, l := range ls {
		if l.Value != "b1" {
			t.Errorf("known name label overwritten: %+v", l)
		}
	}
	for _, g := range groups {
		if g.NameSource != model.NamePlaceholder {
			t.Errorf("stale name applied: %+v", g)
		}
	}
}

func TestUnknownPairsReasked(t *testing.T) {
	f := &fakeM2{unknownPairs: true}
	e, p := m2Env(t, &jev.Fake{}, &embed.Fake{}, f)
	e.run(t, p)
	if g := e.agg(t).Grouping; g == nil || len(g.JevKept) != 0 || g.JevRejected != 0 {
		t.Errorf("unknown pairs confirmed or rejected: %+v", g)
	}
	f.unknownPairs = false
	e.run(t, p)
	if len(f.pairCalls) != 2 || len(f.pairCalls[1]) != 3 {
		t.Fatalf("unknown pairs not re-asked: %d calls", len(f.pairCalls))
	}
	if g := e.agg(t).Grouping; g.JevKept[model.RelSameIntent] != 1 {
		t.Errorf("grouping after recovery = %+v", g)
	}
}

func TestPairCandidates(t *testing.T) {
	u := func(id, shape string, order int) model.ChangeUnit {
		return model.ChangeUnit{ID: id, ShapeHash: shape, Order: order, Path: id + ".ts"}
	}
	units := []model.ChangeUnit{u("a", "s2", 0), u("b", "s2", 1), u("c", "s3", 2), u("d", "s3", 3), u("e", "s3", 4), u("x", "", 5), u("y", "", 6)}
	l := func(from, to string, k model.LinkKind) model.Link {
		return model.Link{From: from, To: to, Kind: k, Weight: 0.8}
	}
	structural := []model.Link{l("y", "x", model.LinkCalls)}
	embedCands := []model.Link{l("x", "y", model.LinkEmbedNeighbor), l("a", "c", model.LinkEmbedNeighbor)}
	mech := []model.Link{l("a", "b", model.LinkMechanical), l("c", "d", model.LinkMechanical), l("c", "e", model.LinkMechanical)}
	got := pairCandidates(units, structural, embedCands, mech, 3)
	var s []string
	for _, c := range got {
		s = append(s, c.From+c.To)
	}
	if strings.Join(s, ",") != "ac,ab" {
		t.Errorf("candidates = %v, want [ac ab] (structural pair and class-of-3 mechanical links dropped)", s)
	}
	pairs := judgePairs(units, append(got, l("c", "a", model.LinkEmbedNeighbor)))
	if len(pairs) != 2 || pairs[0].A.ID != "a" || pairs[0].B.ID != "b" || pairs[1].B.ID != "c" {
		t.Errorf("judge pairs = %+v", pairs)
	}
}

func TestKeepKnownAndMerge(t *testing.T) {
	stored := []model.Label{
		{UnitID: "s1", Question: model.QRelation, Value: model.RelSameIntent, P: 0.8},
		{UnitID: "s2", Question: model.QRelation, Value: model.LabelUnknown},
	}
	fresh := []model.Label{
		{UnitID: "s1", Question: model.QRelation, Value: model.LabelUnknown},
		{UnitID: "s2", Question: model.QRelation, Value: model.LabelUnknown},
		{UnitID: "s0", Question: model.QRelation, Value: model.RelTestFor, P: 0.9},
	}
	kept := keepKnown(fresh, stored)
	if len(kept) != 2 || kept[0].UnitID != "s2" || kept[1].UnitID != "s0" {
		t.Errorf("kept = %+v", kept)
	}
	m := mergeLabels(stored, kept)
	if len(m) != 3 || m[0].UnitID != "s0" || m[1].Value != model.RelSameIntent || !model.IsUnknown(m[2]) {
		t.Errorf("merged = %+v", m)
	}
}

func TestShortReason(t *testing.T) {
	err := fmt.Errorf("pipeline: embed 3 units with m: %w", fmt.Errorf("%w: ollama HTTP 404: model not found", embed.ErrUnavailable))
	if got := shortReason(err); got != "ollama HTTP 404: model not found" {
		t.Errorf("shortReason = %q", got)
	}
	if got := shortReason(errors.New(strings.Repeat("x", 300))); len([]rune(got)) != 100 {
		t.Errorf("long reason not cut: %d runes", len([]rune(got)))
	}
}
