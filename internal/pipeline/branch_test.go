package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/arch"
	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/store"
)

func TestBranchInfoAbandonedDefault(t *testing.T) {
	label := func(state string) model.Label {
		return model.Label{Question: model.QState, Value: state, P: 0.7,
			Probs: map[string]float64{state: 0.7, model.BranchAbandoned: 0.2}}
	}
	type want struct {
		state string
		p     float64
		rule  bool
	}
	states := []string{model.BranchWIP, model.BranchSpike, model.BranchReady, model.BranchSuperseded, ""}
	wants := map[int]map[string]want{
		3: {
			model.BranchWIP: {model.BranchWIP, 0.7, false}, model.BranchSpike: {model.BranchSpike, 0.7, false},
			model.BranchReady: {model.BranchReady, 0.7, false}, model.BranchSuperseded: {model.BranchSuperseded, 0.7, false},
			"": {"", 0, false},
		},
		14: {
			model.BranchWIP: {model.BranchAbandoned, 0.2, true}, model.BranchSpike: {model.BranchAbandoned, 0.2, true},
			model.BranchReady: {model.BranchReady, 0.7, false}, model.BranchSuperseded: {model.BranchSuperseded, 0.7, false},
			"": {model.BranchAbandoned, 1, true},
		},
		30: {
			model.BranchWIP: {model.BranchAbandoned, 0.2, true}, model.BranchSpike: {model.BranchAbandoned, 0.2, true},
			model.BranchReady: {model.BranchReady, 0.7, false}, model.BranchSuperseded: {model.BranchSuperseded, 0.7, false},
			"": {model.BranchAbandoned, 1, true},
		},
	}
	for _, idle := range []int{3, 14, 30} {
		for _, s := range states {
			name := s
			if name == "" {
				name = "unknown"
			}
			t.Run(fmt.Sprintf("%dd/%s", idle, name), func(t *testing.T) {
				known := s != ""
				var l model.Label
				if known {
					l = label(s)
				}
				// 0 means the default (14 days).
				b := branchInfo(l, known, float64(idle)+0.25, 0)
				w := wants[idle][s]
				if b.State != w.state || b.P != w.p || (b.Rule != "") != w.rule || b.Known != known || b.IdleDays != idle {
					t.Errorf("got %+v, want %+v", b, w)
				}
				if w.rule && b.Rule != fmt.Sprintf("no commits for %d days", idle) {
					t.Errorf("rule %q", b.Rule)
				}
			})
		}
	}
	if b := branchInfo(label(model.BranchWIP), true, 8, 7); b.State != model.BranchAbandoned {
		t.Errorf("abandoned_days 7 not applied: %+v", b)
	}
}

func TestSupersededBy(t *testing.T) {
	prs := []model.Item{
		{Number: 7, Title: "Add dark mode toggle to the header"},
		{Number: 8, Title: "Retry failed fetches"},
		{Number: 9, Title: "Dark mode toggle in header"},
	}
	tests := []struct {
		name  string
		texts []string
		want  int
	}{
		{"best overlap", []string{"Add dark mode toggle", "wip: header", "feat/dark-mode"}, 7},
		{"unrelated", []string{"Bump dependencies", "chore/deps"}, 0},
		{"below 0.3", []string{"Retry uploads in background worker with backoff", "feat/upload-retry"}, 0},
		{"empty", nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := supersededBy(prs, tc.texts); got != tc.want {
				t.Errorf("supersededBy = %d, want %d", got, tc.want)
			}
		})
	}
	// Equal overlap: the newest PR wins.
	tie := []model.Item{{Number: 3, Title: "dark mode"}, {Number: 5, Title: "mode dark"}}
	if got := supersededBy(tie, []string{"dark mode"}); got != 5 {
		t.Errorf("tie = %d, want 5", got)
	}
}

func TestTitleGroupNames(t *testing.T) {
	gs := []model.Group{{Name: "Palette"}, {Name: "a.css", Ungrouped: true}, {Name: ""}, {Name: "Toggle"}}
	if got := strings.Join(titleGroupNames(gs), ","); got != "Palette,Toggle" {
		t.Errorf("names = %q", got)
	}
	if got := strings.Join(titleGroupNames([]model.Group{{Name: "a.css", Ungrouped: true}}), ","); got != "a.css" {
		t.Errorf("ungrouped-only names = %q", got)
	}
}

// branchEnv is a pipeline over fakes for one local branch with two
// commits, no upstream, and two stored open PRs.
type branchEnv struct {
	st    store.Store
	git   *gitx.Fake
	item  model.Item
	jev   *jev.Fake
	fast  *review.Fake
	state string // branch.state answer
	idle  time.Duration
}

func newBranchEnv(t *testing.T, state string, idle time.Duration) *branchEnv {
	t.Helper()
	e := &branchEnv{st: newStore(t), state: state, idle: idle}
	last := labelNow.Add(-idle)
	e.git = &gitx.Fake{
		SHAs:       map[string]string{"feat/dark": "h1"},
		MergeBases: map[string]string{"main..h1": "b1"},
		Diffs:      map[string]string{"b1..h1": branchDiff},
		Logs: map[string][]gitx.Commit{"b1..h1": {
			{SHA: "h1", Subject: "Add dark mode toggle", Date: last},
			{SHA: "c1", Subject: "wip: dark palette", Date: last.Add(-time.Hour)},
		}},
		Ahead: map[string]int{"b1..h1": 2},
	}
	e.item = model.Item{ID: model.BranchItemID("feat/dark"), Kind: model.KindBranch, Ref: "feat/dark", BaseRef: "main",
		HeadSHA: "h1", Title: "Add dark mode toggle", LastCommitAt: last, Source: model.SourceRanked}
	ctx := context.Background()
	for _, it := range []model.Item{
		e.item,
		{ID: model.PRItemID(7), Kind: model.KindPR, Number: 7, Title: "Add dark mode toggle to header"},
		{ID: model.PRItemID(8), Kind: model.KindPR, Number: 8, Title: "Retry failed fetches"},
		{ID: model.PRItemID(9), Kind: model.KindPR, Number: 9, Title: "Preview only", Source: model.SourcePreview},
		{ID: model.PRItemID(10), Kind: model.KindPR, Number: 10, Title: "Merged dark mode toggle header work", PRState: "merged", Source: model.SourceAdded},
		{ID: model.PRItemID(11), Kind: model.KindPR, Number: 11, Title: "Closed old thing", PRState: "closed", Source: model.SourceAdded},
	} {
		if err := e.st.UpsertItem(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	e.jev = &jev.Fake{Answer: func(name string, q jev.Question, st any) jev.Answer {
		if name != model.QState {
			return jev.DefaultAnswer(name, q, st)
		}
		return jev.Answer{Type: jev.Choice, Choice: e.state,
			Probabilities: map[string]float64{e.state: 0.8, model.BranchAbandoned: 0.05}}
	}}
	e.fast = &review.Fake{Name: "fake:fast", Respond: review.FakeRespond(0.9)}
	return e
}

func (e *branchEnv) pipeline(mutate ...func(*Deps)) *Pipeline {
	d := Deps{Git: e.git, Jev: e.jev, Store: e.st, Config: config.Defaults(), Questions: questions.Default(),
		Reviewers: review.Tiers{Fast: e.fast}, Now: func() time.Time { return labelNow }}
	for _, m := range mutate {
		m(&d)
	}
	return New(d)
}

func (e *branchEnv) run(t *testing.T, p *Pipeline) model.ItemAgg {
	t.Helper()
	ctx := context.Background()
	if err := p.Run(ctx, e.item); err != nil {
		t.Fatal(err)
	}
	agg, ok, err := e.st.Agg(ctx, e.item.ID)
	if err != nil || !ok {
		t.Fatalf("agg ok=%v err=%v", ok, err)
	}
	return agg
}

// stateCalls counts the branch.state requests Jev received.
func (e *branchEnv) stateCalls() int {
	n := 0
	for _, r := range e.jev.Calls {
		if _, ok := r.Questions[model.QState]; ok {
			n++
		}
	}
	return n
}

func prTitleCalls(f *review.Fake) int {
	n := 0
	for _, r := range f.Calls {
		if r.Task == review.TaskPRTitle {
			n++
		}
	}
	return n
}

// A remote-tracking ref added from search has no unpushed commits: the
// commits ahead of the merge base are already on the remote (M5 review
// finding).
func TestBranchBlockRemoteRefHasNoUnpushed(t *testing.T) {
	e := newBranchEnv(t, model.BranchReady, time.Hour)
	e.item.RemoteRef = true
	if err := e.st.UpsertItem(context.Background(), e.item); err != nil {
		t.Fatal(err)
	}
	agg := e.run(t, e.pipeline())
	if b := agg.Branch; b == nil || b.Unpushed != 0 || len(b.Commits) != 0 {
		t.Fatalf("remote ref block %+v", b)
	}
	if c, ok := contribOf(agg.UrgencyTerms, "w_unpushed"); ok && c != 0 {
		t.Errorf("w_unpushed = %v for a remote ref", c)
	}
	for _, r := range e.jev.Calls {
		if _, ok := r.Questions[model.QState]; ok && !strings.Contains(stateJSON(r.State), `"unpushedCommits":0`) {
			t.Errorf("state sent unpushed commits: %s", stateJSON(r.State))
		}
	}
}

func TestBranchBlockReadyForPR(t *testing.T) {
	e := newBranchEnv(t, model.BranchReady, 3*24*time.Hour+time.Hour)
	p := e.pipeline()
	agg := e.run(t, p)
	b := agg.Branch
	if b == nil {
		t.Fatal("no branch block")
	}
	if !b.Known || b.State != model.BranchReady || b.P != 0.8 || b.Probs[model.BranchReady] != 0.8 || b.Rule != "" {
		t.Errorf("state %+v", b)
	}
	if b.IdleDays != 3 || !b.LastCommitAt.Equal(e.item.LastCommitAt) || b.Unpushed != 2 || len(b.Commits) != 2 || b.Commits[0].SHA != "h1" {
		t.Errorf("git facts %+v", b)
	}
	if b.SuggestedTitle != review.FakePRTitle || b.TitleKey == "" || b.TitleNote != "" || b.SupersededBy != 0 {
		t.Errorf("title %+v", b)
	}
	it, err := e.st.Item(context.Background(), e.item.ID)
	if err != nil || it.BranchState != model.BranchReady || it.BranchStateP != 0.8 || it.Unpushed != 2 {
		t.Errorf("item state %q %v unpushed %d (%v)", it.BranchState, it.BranchStateP, it.Unpushed, err)
	}
	if c, ok := contribOf(agg.UrgencyTerms, "w_ready_stale"); !ok || c <= 0 {
		t.Errorf("w_ready_stale missing: %+v", agg.UrgencyTerms)
	}
	// The first run's urgency counts the unpushed commits it found, though
	// the stored item had none yet (M5 review finding).
	if c, ok := contribOf(agg.UrgencyTerms, "w_unpushed"); !ok || c <= 0 {
		t.Errorf("w_unpushed missing on the first run: %+v", agg.UrgencyTerms)
	}
	if e.stateCalls() != 1 || prTitleCalls(e.fast) != 1 {
		t.Fatalf("state calls %d, title calls %d", e.stateCalls(), prTitleCalls(e.fast))
	}
	// The request carries the log, age, unpushed count and the open PR
	// titles (newest first, previews excluded).
	for _, r := range e.jev.Calls {
		if _, ok := r.Questions[model.QState]; ok {
			s := stateJSON(r.State)
			for _, want := range []string{`"daysSinceLastCommit":3`, `"unpushedCommits":2`, `"Retry failed fetches","Add dark mode toggle to header"`, `"wip: dark palette"`} {
				if !strings.Contains(s, want) {
					t.Errorf("state lacks %s: %s", want, s)
				}
			}
			if strings.Contains(s, "Preview only") {
				t.Errorf("preview PR sent: %s", s)
			}
			if strings.Contains(s, "Merged dark") || strings.Contains(s, "Closed old") {
				t.Errorf("closed or merged PR sent as open: %s", s)
			}
		}
	}
	// A second run reuses the stored answer and the cached title.
	again := e.run(t, p)
	if e.stateCalls() != 1 || prTitleCalls(e.fast) != 1 {
		t.Errorf("second run: state calls %d, title calls %d", e.stateCalls(), prTitleCalls(e.fast))
	}
	if again.Branch == nil || again.Branch.SuggestedTitle != review.FakePRTitle || again.Branch.State != model.BranchReady {
		t.Errorf("second run block %+v", again.Branch)
	}
	// Without a key the cached title is still served.
	p.d.Reviewers.Fast = notReadyReviewer{e.fast}
	if b := e.run(t, p).Branch; b.SuggestedTitle != review.FakePRTitle {
		t.Errorf("cached title without key: %+v", b)
	}
}

func contribOf(terms []model.UrgencyTerm, name string) (float64, bool) {
	for _, t := range terms {
		if t.Name == name {
			return t.Contrib, true
		}
	}
	return 0, false
}

func TestBranchBlockSuperseded(t *testing.T) {
	e := newBranchEnv(t, model.BranchSuperseded, 2*24*time.Hour)
	b := e.run(t, e.pipeline()).Branch
	if b == nil || b.State != model.BranchSuperseded || b.SupersededBy != 7 {
		t.Errorf("block %+v", b)
	}
}

func TestBranchBlockWithoutJevOrModel(t *testing.T) {
	e := newBranchEnv(t, model.BranchWIP, 20*24*time.Hour)
	b := e.run(t, e.pipeline(func(d *Deps) { d.Jev = nil; d.Reviewers = review.Tiers{} })).Branch
	if b == nil || b.Known || b.State != model.BranchAbandoned || b.P != 1 || b.Rule != "no commits for 20 days" {
		t.Errorf("block %+v", b)
	}
	if b.SuggestedTitle != "" || b.TitleNote != titleNoModel {
		t.Errorf("title %+v", b)
	}
	e = newBranchEnv(t, model.BranchWIP, 24*time.Hour)
	b = e.run(t, e.pipeline(func(d *Deps) { d.Jev = &jev.Fake{Err: jev.ErrUnavailable} })).Branch
	if b == nil || b.Known || b.State != "" || b.Rule != "" {
		t.Errorf("fresh branch, Jev down: %+v", b)
	}
}

func TestBranchBlockNoGroups(t *testing.T) {
	var b model.BranchInfo
	e := newBranchEnv(t, model.BranchWIP, time.Hour)
	p := e.pipeline()
	p.suggestTitle(context.Background(), &run{it: e.item, calls: newCallLog()}, nil, &b, &model.RunLog{})
	if b.TitleNote != titleNoGroups || len(e.fast.Calls) != 0 {
		t.Errorf("no groups: %+v, %d calls", b, len(e.fast.Calls))
	}
}

func TestPRItemHasNoBranchBlock(t *testing.T) {
	e := newLabelEnv(t)
	e.run(t, e.pipeline(nil))
	agg := e.agg(t)
	if agg.Branch != nil {
		t.Errorf("PR got a branch block: %+v", agg.Branch)
	}
	if _, ok := contribOf(agg.UrgencyTerms, "w_ready_stale"); ok {
		t.Error("PR got w_ready_stale")
	}
}

// The Architecture graph is built after the record and stored in the
// aggregate; a Build error leaves a graph with a note (the stub returns
// arch.ErrNotImplemented), and cancellation fails the stage.
func TestArchGraphWiring(t *testing.T) {
	e := newLabelEnv(t)
	p := e.pipeline(nil)
	e.run(t, p)
	if g := e.agg(t).Arch; g == nil || (g.Note == "" && len(g.Modules) == 0) {
		t.Errorf("arch graph %+v", g)
	}
	var got arch.Input
	var seen model.ItemAgg
	p.fn.archBuild = func(_ context.Context, in arch.Input) (*model.ArchGraph, error) {
		got = in
		if in.Agg != nil {
			seen = *in.Agg
		}
		return nil, errors.New("boom")
	}
	e.run(t, p)
	if g := e.agg(t).Arch; g == nil || g.Note != "module graph unavailable: boom" {
		t.Errorf("arch graph after error %+v", g)
	}
	if got.ItemID != e.item.ID || got.HeadSHA != "h1" || got.BaseSHA != "b1" || got.Agg == nil || len(got.Units) == 0 ||
		len(got.Files) == 0 || got.MaxHop != arch.DefaultMaxHop || got.Src == nil || got.Cache == nil {
		t.Errorf("arch input %+v", got)
	}
	if seen.ItemID != e.item.ID || seen.Arch != nil || len(seen.Units) == 0 {
		t.Errorf("arch saw record %+v", seen)
	}
	p.fn.archBuild = arch.Build
	if g := e.agg(t).Arch; g == nil {
		t.Fatal("no graph")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.fn.archBuild = func(context.Context, arch.Input) (*model.ArchGraph, error) {
		cancel()
		return nil, context.Canceled
	}
	if err := p.Run(ctx, e.item); err == nil {
		t.Error("cancelled arch build: want error")
	}
}
