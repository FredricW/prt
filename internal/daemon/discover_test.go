package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/gitx"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/store"
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
			{Name: "origin/local", SHA: "l0", Remote: true},
		},
		Ahead: map[string]int{
			"main..feat/requested": 1, "main..renamed-locally": 1, "main..feat/mine": 3,
			"main..local": 2, "origin/local..local": 1,
		},
	}
}

func discoverGH() *gh.Fake {
	return &gh.Fake{Login: "me", PRs: map[int]gh.PR{
		1: {Number: 1, Title: "requested", Author: "bob", HeadRef: "feat/requested", HeadSHA: "s1", BaseRef: "main", ReviewRequested: true},
		2: {Number: 2, Title: "mine", Author: "me", HeadRef: "feat/mine", HeadSHA: "s2", BaseRef: "main"},
		3: {Number: 3, Title: "approved red", Author: "bob", HeadRef: "feat/red", HeadSHA: "s3", BaseRef: "main", ViewerApproved: true, CI: model.CIRed},
		4: {Number: 4, Title: "other", Author: "carol", HeadRef: "feat/other", HeadSHA: "s4", BaseRef: "main"},
		5: {Number: 5, Title: "mine commented", Author: "ME", HeadRef: "feat/mine2", HeadSHA: "s5", BaseRef: "main", ViewerCommented: true},
		6: {Number: 6, Title: "no sha", Author: "bob", HeadRef: "local-pr", BaseRef: "release/1", ReviewRequested: true},
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
					model.PRItemID(3): model.SectionNeedsYou, model.PRItemID(4): model.SectionNeedsYou,
					model.PRItemID(5): model.SectionYours, model.PRItemID(6): model.SectionNeedsYou,
					model.BranchItemID("local"): model.SectionBranches,
				},
				absent: []string{
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
				{ID: model.PRItemID(4), Kind: model.KindPR, Number: 4, HeadSHA: "s4", State: model.StateReady, Source: model.SourceRanked},
			},
			want: want{check: func(t *testing.T, m map[string]model.Item) {
				if st := m[model.BranchItemID("local")].State; st != model.StateStale {
					t.Errorf("moved branch state = %q", st)
				}
				if st := m[model.PRItemID(4)].State; st != model.StateReady {
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

func TestDiscoverOwnedPathSearch(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := newClient(t, st, &gitx.Fake{}, discoverGH(), nil)
	c.d.Config.User.Owns = []string{"web/**"}
	if err := c.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(itemMap(t, c)); n != 6 {
		t.Fatalf("items = %d, want 6 (owned search must not duplicate)", n)
	}
	if _, err := st.Item(ctx, model.PRItemID(1)); errors.Is(err, store.ErrNotFound) {
		t.Fatal("PR #1 missing")
	}
}
