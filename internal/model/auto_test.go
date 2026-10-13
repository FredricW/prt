package model

import "testing"

func TestParseAutoReview(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want AutoReview
		err  bool
	}{
		{"", AutoNeedsYou, false},
		{"manual", AutoManual, false},
		{" Needs-You ", AutoNeedsYou, false},
		{"needs-you+yours", AutoNeedsYouYours, false},
		{"all", AutoAll, false},
		{"everything", AutoNeedsYou, true},
	} {
		got, err := ParseAutoReview(tc.in)
		if (err != nil) != tc.err || got != tc.want {
			t.Errorf("ParseAutoReview(%q) = %q, %v; want %q, err %v", tc.in, got, err, tc.want, tc.err)
		}
	}
}

func TestAutoReviewCovers(t *testing.T) {
	needs := Item{Kind: KindPR, Section: SectionNeedsYou, Source: SourceRanked}
	yours := Item{Kind: KindPR, Section: SectionYours, Source: SourceRanked}
	involved := Item{Kind: KindPR, Section: SectionInvolved, Source: SourceRanked}
	added := Item{Kind: KindPR, Section: SectionAdded, Source: SourceAdded}
	branch := Item{Kind: KindBranch, Section: SectionBranches, Source: SourceRanked}
	ownBranch := Item{Kind: KindBranch, Section: SectionYours, Source: SourceRanked}
	preview := Item{Kind: KindPR, Source: SourcePreview}
	for _, tc := range []struct {
		a    AutoReview
		it   Item
		name string
		want bool
	}{
		{AutoManual, needs, "manual needs", false},
		{AutoNeedsYou, needs, "needs needs", true},
		{AutoNeedsYou, yours, "needs yours", false},
		{AutoNeedsYouYours, yours, "nyy yours", true},
		{AutoNeedsYouYours, involved, "nyy involved", false},
		{AutoNeedsYouYours, added, "nyy added", false},
		{AutoNeedsYouYours, branch, "nyy branch", false},
		{AutoNeedsYouYours, ownBranch, "nyy own branch", false},
		{AutoNeedsYou, preview, "needs preview", false},
		{AutoAll, branch, "all branch", true},
		{AutoAll, preview, "all preview", true},
		{AutoReview("bogus"), needs, "bogus needs", true},
	} {
		if got := tc.a.Covers(tc.it); got != tc.want {
			t.Errorf("%s: Covers = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAutoReviewAllowsRequestedHead(t *testing.T) {
	b := Item{Kind: KindBranch, Section: SectionBranches, HeadSHA: "abc", RequestedSHA: "abc"}
	if !AutoManual.Allows(b) {
		t.Error("a requested head must be allowed under manual")
	}
	b.HeadSHA = "def"
	if AutoManual.Allows(b) {
		t.Error("a new push must need a new request")
	}
	if (Item{}).Requested() {
		t.Error("empty RequestedSHA is not a request")
	}
}
