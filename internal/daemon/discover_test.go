package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/pipeline"
	"github.com/fredricw/prt/internal/proto"
)

func discoverGit() *gitx.Fake {
	return &gitx.Fake{
		RefList: []gitx.Ref{
			{Name: "main", SHA: "b1"},
			{Name: "feat/requested", SHA: "s1"},                                  // PR #1 by SHA
			{Name: "renamed-locally", SHA: "s2"},                                 // PR #2 head SHA → hidden
			{Name: "feat/mine", SHA: "other"},                                    // PR #2 head ref → hidden
			{Name: "local", SHA: "l1", Upstream: "origin/local", Subject: "wip"}, // branch item
			{Name: "merged", SHA: "b1"},                                          // not ahead
			{Name: "patch-1", SHA: "mine"},                                       // same name as fork PR #12's head → kept
			{Name: "origin/local", SHA: "l0", Remote: true},
		},
		Ahead: map[string]int{
			"main..feat/requested": 1, "main..renamed-locally": 1, "main..feat/mine": 3,
			"main..local": 2, "origin/local..local": 1, "main..patch-1": 1,
		},
	}
}

func discoverGH() *gh.Fake {
	return &gh.Fake{Login: "me", PRs: map[int]gh.PR{
		1: {Number: 1, Title: "requested", Author: "bob", HeadRef: "feat/requested", HeadSHA: "s1", BaseRef: "main", ReviewRequested: true},
		2: {Number: 2, Title: "mine", Author: "me", HeadRef: "feat/mine", HeadSHA: "s2", BaseRef: "main"},
		3: {Number: 3, Title: "approved red", Author: "bob", HeadRef: "feat/red", HeadSHA: "s3", BaseRef: "main", ViewerApproved: true, CI: model.CIRed},
		// #4 does not involve the viewer; only the owned-path source finds it.
		4:  {Number: 4, Title: "other", Author: "carol", HeadRef: "feat/other", HeadSHA: "s4", BaseRef: "main", Files: []string{"web/app.ts"}},
		5:  {Number: 5, Title: "mine commented", Author: "ME", HeadRef: "feat/mine2", HeadSHA: "s5", BaseRef: "main", ViewerCommented: true},
		6:  {Number: 6, Title: "no sha", Author: "bob", HeadRef: "local-pr", BaseRef: "release/1", ReviewRequested: true},
		7:  {Number: 7, Title: "pushed since review", Author: "bob", HeadRef: "feat/7", HeadSHA: "s7", ViewerReviewedSHA: "s7-old", ViewerCommented: true},
		8:  {Number: 8, Title: "reviewed at head", Author: "bob", HeadRef: "feat/8", HeadSHA: "s8", ViewerReviewedSHA: "s8", ViewerCommented: true},
		9:  {Number: 9, Title: "mentioned", Author: "bob", HeadRef: "feat/9", HeadSHA: "s9", ViewerAddressed: true},
		10: {Number: 10, Title: "commented earlier", Author: "bob", HeadRef: "feat/10", HeadSHA: "s10", ViewerCommented: true},
		11: {Number: 11, Title: "approved green", Author: "bob", HeadRef: "feat/11", HeadSHA: "s11", ViewerApproved: true, ViewerReviewedSHA: "s11", CI: model.CIGreen},
		12: {Number: 12, Title: "fork", Author: "dan", HeadRef: "patch-1", HeadSHA: "f12", CrossRepo: true, ReviewRequested: true},
		13: {Number: 13, Title: "server only", Author: "carol", HeadRef: "feat/13", HeadSHA: "s13", Files: []string{"server/x.go"}},
	}}
}

func TestDiscover(t *testing.T) {
	type want struct {
		sections map[string]model.Section
		absent   []string
		logNote  string
		check    func(t *testing.T, m map[string]model.Item)
	}
	tests := []struct {
		name  string
		pre   []model.Item
		ghErr error
		noGH  bool
		want  want
	}{
		{
			name: "sections and branch matching",
			want: want{
				sections: map[string]model.Section{
					model.PRItemID(1): model.SectionNeedsYou, model.PRItemID(2): model.SectionYours,
					model.PRItemID(3): model.SectionNeedsYou,
					model.PRItemID(5): model.SectionYours, model.PRItemID(6): model.SectionNeedsYou,
					model.PRItemID(7): model.SectionNeedsYou, model.PRItemID(8): model.SectionInvolved,
					model.PRItemID(9): model.SectionNeedsYou, model.PRItemID(10): model.SectionInvolved,
					model.PRItemID(11): model.SectionInvolved, model.PRItemID(12): model.SectionNeedsYou,
					model.BranchItemID("local"):   model.SectionBranches,
					model.BranchItemID("patch-1"): model.SectionBranches,
				},
				absent: []string{
					model.PRItemID(4), model.PRItemID(13), // not involved, no owned globs
					model.BranchItemID("feat/requested"), model.BranchItemID("renamed-locally"),
					model.BranchItemID("feat/mine"), model.BranchItemID("merged"), model.BranchItemID("main"),
				},
				check: func(t *testing.T, m map[string]model.Item) {
					if b := m[model.BranchItemID("local")]; b.Unpushed != 1 || b.Ahead != 2 || b.Title != "wip" || b.HeadSHA != "l1" {
						t.Errorf("branch = %+v", b)
					}
					if p := m[model.PRItemID(6)]; p.HeadSHA != "p6" {
						t.Errorf("PR head not resolved from local ref: %+v", p)
					}
					if p := m[model.PRItemID(1)]; p.Urgency <= 0 || !strings.Contains(p.Why, "asked you") {
						t.Errorf("PR #1 urgency = %v %q", p.Urgency, p.Why)
					}
					if p := m[model.PRItemID(6)]; !strings.Contains(p.Why, "release") {
						t.Errorf("PR #6 why = %q", p.Why)
					}
				},
			},
		},
		{
			name: "added items kept with fresh metadata",
			pre: []model.Item{
				{ID: model.PRItemID(4), Kind: model.KindPR, Number: 4, Title: "old title", Source: model.SourceAdded, Section: model.SectionAdded},
				{ID: model.BranchItemID("gone"), Kind: model.KindBranch, Ref: "gone", Source: model.SourceAdded, Section: model.SectionAdded},
			},
			want: want{
				sections: map[string]model.Section{model.PRItemID(4): model.SectionAdded, model.BranchItemID("gone"): model.SectionAdded},
				check: func(t *testing.T, m map[string]model.Item) {
					if p := m[model.PRItemID(4)]; p.Title != "other" || p.Source != model.SourceAdded {
						t.Errorf("added PR = %+v", p)
					}
				},
			},
		},
		{
			name: "disappeared ranked items removed",
			pre: []model.Item{
				{ID: model.PRItemID(99), Kind: model.KindPR, Number: 99, Source: model.SourceRanked, Section: model.SectionNeedsYou},
				{ID: model.BranchItemID("old"), Kind: model.KindBranch, Ref: "old", Source: model.SourceRanked, Section: model.SectionBranches},
			},
			want: want{absent: []string{model.PRItemID(99), model.BranchItemID("old")}},
		},
		{
			name: "stale after new push",
			pre: []model.Item{
				{ID: model.BranchItemID("local"), Kind: model.KindBranch, Ref: "local", HeadSHA: "l0", State: model.StateReady, Source: model.SourceRanked},
				{ID: model.PRItemID(1), Kind: model.KindPR, Number: 1, HeadSHA: "s1", State: model.StateReady, Source: model.SourceRanked},
			},
			want: want{check: func(t *testing.T, m map[string]model.Item) {
				if st := m[model.BranchItemID("local")].State; st != model.StateStale {
					t.Errorf("moved branch state = %q", st)
				}
				if st := m[model.PRItemID(1)].State; st != model.StateReady {
					t.Errorf("unchanged PR state = %q", st)
				}
			}},
		},
		{
			name:  "GitHub error degrades to branches",
			ghErr: errors.New("rate limited"),
			pre: []model.Item{
				{ID: model.PRItemID(1), Kind: model.KindPR, Number: 1, Ref: "feat/requested", HeadSHA: "s1", Source: model.SourceRanked, Section: model.SectionNeedsYou},
			},
			want: want{
				sections: map[string]model.Section{model.PRItemID(1): model.SectionNeedsYou, model.BranchItemID("local"): model.SectionBranches},
				absent:   []string{model.BranchItemID("feat/requested"), model.PRItemID(2)},
				logNote:  "GitHub unavailable: viewer: rate limited",
			},
		},
		{
			name: "offline lists local branches",
			noGH: true,
			want: want{
				sections: map[string]model.Section{model.BranchItemID("local"): model.SectionBranches, model.BranchItemID("feat/mine"): model.SectionBranches},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := newTestStore(t)
			put(t, st, tc.pre...)
			g := discoverGit()
			g.SHAs = map[string]string{"local-pr": "p6"}
			var ghc gh.Client
			if !tc.noGH {
				f := discoverGH()
				f.Err = tc.ghErr
				ghc = f
			}
			c := newClient(t, st, g, ghc, nil)
			if err := c.Discover(ctx); err != nil {
				t.Fatal(err)
			}
			m := itemMap(t, c)
			for id, sec := range tc.want.sections {
				it, ok := m[id]
				if !ok {
					t.Errorf("%s missing; have %v", id, keys(m))
					continue
				}
				if it.Section != sec {
					t.Errorf("%s section = %q, want %q", id, it.Section, sec)
				}
			}
			for _, id := range tc.want.absent {
				if _, ok := m[id]; ok {
					t.Errorf("%s should be absent", id)
				}
			}
			evs := drain(c.Events())
			if tc.want.logNote != "" && !hasEvent(evs, proto.EventLog, tc.want.logNote) {
				t.Errorf("no log event %q in %+v", tc.want.logNote, evs)
			}
			for _, id := range tc.want.absent {
				if hasPre(tc.pre, id) && !hasEvent(evs, proto.EventRemove, id) {
					t.Errorf("no remove event for %s", id)
				}
			}
			if tc.want.check != nil {
				tc.want.check(t, m)
			}
		})
	}
}

func keys(m map[string]model.Item) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func hasPre(items []model.Item, id string) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}

// hasEvent matches the note (EventLog) or item id (others).
func hasEvent(evs []proto.Event, typ proto.EventType, key string) bool {
	for _, e := range evs {
		if e.Type == typ && (e.Note == key || e.ItemID == key) {
			return true
		}
	}
	return false
}

func TestDiscoverOwnedPaths(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	f := discoverGH()
	p1 := f.PRs[1]
	p1.Files = []string{"web/login.ts"} // involved and owned: listed once
	f.PRs[1] = p1
	c := newClient(t, st, &gitx.Fake{}, f, func(d *pipeline.Deps) {
		d.Config.User.Owns = []string{"web/**"}
	})
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	m := itemMap(t, c)
	if n := len(m); n != 12 {
		t.Fatalf("items = %d (%v), want 12: the 11 involved PRs plus owned #4", n, keys(m))
	}
	if p := m[model.PRItemID(4)]; p.Section != model.SectionInvolved {
		t.Errorf("owned-only PR #4 section = %q, want involved", p.Section)
	}
	if _, ok := m[model.PRItemID(13)]; ok {
		t.Error("PR #13 touches no owned path and does not involve the viewer")
	}
	if p := m[model.PRItemID(1)]; p.Section != model.SectionNeedsYou {
		t.Errorf("PR #1 section = %q", p.Section)
	}
}

func TestDiscoverNoDefaultBranchKeepsPRs(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, model.Item{ID: model.BranchItemID("local"), Kind: model.KindBranch, Ref: "local", Source: model.SourceRanked, Section: model.SectionBranches})
	g := discoverGit()
	g.DefaultErr = gitx.ErrNotFound
	c := newClient(t, st, g, discoverGH(), nil)
	if err := c.Discover(ctx); err != nil {
		t.Fatalf("Discover must degrade, got %v", err)
	}
	m := itemMap(t, c)
	if _, ok := m[model.PRItemID(1)]; !ok {
		t.Errorf("PRs must still be discovered; have %v", keys(m))
	}
	if _, ok := m[model.BranchItemID("local")]; !ok {
		t.Error("stored branch items must be kept when the branch source cannot run")
	}
	if !hasEvent(drain(c.Events()), proto.EventLog, "no default branch; local branches skipped") {
		t.Error("no log note")
	}
}

func TestDiscoverAheadErrorKeepsBranch(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	put(t, st, model.Item{ID: model.BranchItemID("local"), Kind: model.KindBranch, Ref: "local", HeadSHA: "l1", Source: model.SourceRanked, Section: model.SectionBranches})
	g := discoverGit()
	g.AheadErrs = map[string]error{"main..local": errors.New("boom")}
	c := newClient(t, st, g, nil, nil)
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := itemMap(t, c)[model.BranchItemID("local")]; !ok {
		t.Fatal("a CountAhead error means unknown, not absent; branch was pruned")
	}
}

func TestDiscoverUsesRemoteTrackingDefault(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	g := &gitx.Fake{
		RefList: []gitx.Ref{
			{Name: "main", SHA: "old"},
			{Name: "origin/main", FullName: "refs/remotes/origin/main", SHA: "new", Remote: true},
			{Name: "feat", SHA: "f1"},
		},
		// Against stale local main, feat looks 5 ahead (upstream commits).
		Ahead: map[string]int{"main..feat": 5, "origin/main..feat": 1},
	}
	c := newClient(t, st, g, nil, nil)
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	b := itemMap(t, c)[model.BranchItemID("feat")]
	if b.Ahead != 1 || b.BaseRef != "origin/main" {
		t.Fatalf("branch = ahead %d base %q, want 1 against origin/main", b.Ahead, b.BaseRef)
	}
}

// scriptedGH blocks the first SearchPRs call until release is closed and
// answers it with no PRs, simulating a slow discovery that started before a
// newer one.
type scriptedGH struct {
	*gh.Fake
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (s *scriptedGH) SearchPRs(ctx context.Context, q string, limit int) ([]gh.PR, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if n == 1 {
		close(s.entered)
		<-s.release
		return nil, nil
	}
	return s.Fake.SearchPRs(ctx, q, limit)
}

func TestDiscoverSerialised(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	sg := &scriptedGH{
		Fake:    &gh.Fake{Login: "me", PRs: map[int]gh.PR{9: {Number: 9, Title: "new", Author: "bob", HeadSHA: "s9", ReviewRequested: true}}},
		entered: make(chan struct{}), release: make(chan struct{}),
	}
	c := newClient(t, st, &gitx.Fake{}, sg, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(1)
	go func() { defer wg.Done(); errs <- c.Discover(ctx) }() // A: slow, stale snapshot
	<-sg.entered
	wg.Add(1)
	go func() { defer wg.Done(); errs <- c.Discover(ctx) }() // B: sees #9
	time.Sleep(50 * time.Millisecond)                        // without serialisation B finishes here
	close(sg.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := itemMap(t, c)[model.PRItemID(9)]; !ok {
		t.Fatal("an overlapping older discovery pruned PR #9 found by the newer one")
	}
}

func TestPRSection(t *testing.T) {
	tests := []struct {
		name string
		pr   gh.PR
		head string
		want model.Section
	}{
		{"requested", gh.PR{ReviewRequested: true}, "h", model.SectionNeedsYou},
		{"approved red", gh.PR{ViewerApproved: true, CI: model.CIRed}, "h", model.SectionNeedsYou},
		{"approved green", gh.PR{ViewerApproved: true, ViewerReviewedSHA: "h", CI: model.CIGreen}, "h", model.SectionInvolved},
		{"re-review", gh.PR{ViewerReviewedSHA: "old"}, "h", model.SectionNeedsYou},
		{"re-review unknown head", gh.PR{ViewerReviewedSHA: "old"}, "", model.SectionInvolved},
		{"addressed", gh.PR{ViewerAddressed: true}, "h", model.SectionNeedsYou},
		{"commented only", gh.PR{ViewerCommented: true}, "h", model.SectionInvolved},
		{"authored", gh.PR{Author: "Me", ViewerAddressed: true}, "h", model.SectionYours},
		{"owned only", gh.PR{Author: "x"}, "h", model.SectionInvolved},
	}
	for _, tt := range tests {
		if got := prSection(tt.pr, tt.head, "me"); got != tt.want {
			t.Errorf("%s: section = %q, want %q", tt.name, got, tt.want)
		}
	}
}
