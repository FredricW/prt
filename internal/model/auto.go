package model

import (
	"fmt"
	"strings"
)

// AutoReview is the [pipeline] auto_review strategy of prt.toml (M5.1, a
// user override of the spec's "analysis runs in urgency order"; DECISIONS
// "M5.1 auto-review strategy"). It decides which items run their
// model-backed stages (embed, Jev labels and pairs, cluster names and
// titles, routing and reviews, TL;DR, branch state and PR title) without
// being asked. The free stages (fetch, parse, structural links, the plain
// diff) may run for every item.
type AutoReview string

// Auto-review strategies.
const (
	// AutoManual: nothing runs model stages unless the user asks.
	AutoManual AutoReview = "manual"
	// AutoNeedsYou: PRs in the Needs-you section (the default).
	AutoNeedsYou AutoReview = "needs-you"
	// AutoNeedsYouYours: PRs in Needs you or Yours.
	AutoNeedsYouYours AutoReview = "needs-you+yours"
	// AutoAll: every item, branches, added items and previews included.
	AutoAll AutoReview = "all"
)

// DefaultAutoReview is the built-in strategy.
const DefaultAutoReview = AutoNeedsYou

// AutoReviews lists the strategies from the most to the least restrictive
// (settings page and `:auto` completion order).
var AutoReviews = []AutoReview{AutoManual, AutoNeedsYou, AutoNeedsYouYours, AutoAll}

// ParseAutoReview reads a strategy name (case-insensitive, surrounding
// space ignored). "" is the default.
func ParseAutoReview(s string) (AutoReview, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return DefaultAutoReview, nil
	}
	for _, a := range AutoReviews {
		if string(a) == s {
			return a, nil
		}
	}
	return DefaultAutoReview, fmt.Errorf("unknown auto_review %q (want manual, needs-you, needs-you+yours or all)", s)
}

// Covers reports whether the strategy alone lets it run model stages.
// Branches are covered only by AutoAll; so are Added items and previews
// (they run when the user adds them: that is an explicit request, see
// Allows). An unknown strategy covers like the default.
func (a AutoReview) Covers(it Item) bool {
	switch a {
	case AutoAll:
		return true
	case AutoManual:
		return false
	}
	if it.Kind != KindPR || it.Source == SourcePreview {
		return false
	}
	switch it.Section {
	case SectionNeedsYou:
		return true
	case SectionYours:
		return a == AutoNeedsYouYours
	}
	return false
}

// Requested reports whether the user explicitly asked to analyse the
// item's current head (Item.RequestedSHA).
func (it Item) Requested() bool {
	return it.RequestedSHA != "" && it.RequestedSHA == it.HeadSHA
}

// Allows reports whether an automatic run of it (discovery, daemon start,
// the TUI's first load, a settings change) may run model stages: the
// strategy covers it or the user asked for its current head. A run the user
// starts by hand (r, :resume, :add, enter on a search result) is always
// allowed and is not decided here.
func (a AutoReview) Allows(it Item) bool { return a.Covers(it) || it.Requested() }
