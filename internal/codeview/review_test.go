package codeview

import (
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/comments"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

const (
	claimHigh = "opts is forwarded unchecked, so a caller passing headers silently replaces the auth header set by fetchJSON"
	claimMed  = "no-store disables the profile cache on every render"
	claimDoc  = "the README does not mention the new opts parameter"
)

// withReviews adds a routing table to the by-change fixture: one row per
// group, covering every tier and status the list shows.
func withReviews(d proto.Detail) proto.Detail {
	fast := model.TierFast
	d.Reviews = []model.Review{
		{GroupID: "g-user", Tier: model.TierFast, Status: model.ReviewDone, RuleIndex: 3,
			RuleWords: "if breaking → fast", Context: []string{model.CtxCallers},
			Findings: []model.Finding{
				{Path: "web/src/api/client.ts", Severity: model.SevLow, Claim: claimDoc},
				{UnitID: "k1", Path: "web/src/components/UserCard.tsx", Line: 9, Severity: model.SevMedium, Claim: claimMed},
				{UnitID: "c1", Path: "web/src/api/client.ts", Line: 11, Severity: model.SevHigh, Claim: claimHigh,
					Fix: "merge opts over the default headers"},
			},
			Calibration: &model.Calibration{Missed: 1}},
		{GroupID: "g-apierror", Tier: model.TierDeep, EscalatedOf: &fast, FastConf: 0.42, Status: model.ReviewDone,
			RuleWords: "if true → fast", Calibration: &model.Calibration{Agree: true}},
		{GroupID: "g-cache", Tier: model.TierDeep, Forced: true, Status: model.ReviewRunning, RuleWords: "forced deep (R)"},
		{GroupID: "g-imports", Tier: model.TierNone, Status: model.ReviewDone,
			RuleWords: "if mechanical || kind in [formatting, moved, generated, dependency] → none"},
		{GroupID: "g-f", Tier: model.TierFast, Status: model.ReviewQueued, RuleWords: "if true → fast",
			QueueNote: "over cost cap: est $0.14, $0.05 of $0.40 left"},
		{GroupID: "g-theme", Tier: model.TierNone, UserSkipped: true, Status: model.ReviewSkipped, RuleWords: "skipped by user (s)"},
		{GroupID: "g-save", Tier: model.TierDeep, Status: model.ReviewFailed, RuleWords: "if risk >= 4 → deep", Err: "timeout"},
	}
	return d
}

func TestGoldenReviews(t *testing.T) {
	cases := map[string]func(t *testing.T) Model{
		"findings_by_change": func(t *testing.T) Model {
			return byChangeModel(t, withReviews(loadGroups(t)), 120, 44, true)
		},
		"findings_by_file": func(t *testing.T) Model {
			m := newModel(t, withReviews(loadGroups(t)), 120, 30)
			allOff(&m)
			return m
		},
		"escalated_row": func(t *testing.T) Model {
			m := byChangeModel(t, withReviews(loadGroups(t)), 120, 30, true)
			press(t, &m, "J")
			return m
		},
		"over_cap_row": func(t *testing.T) Model {
			m := byChangeModel(t, withReviews(loadGroups(t)), 120, 30, true)
			press(t, &m, "J", "J", "J", "J")
			return m
		},
		"no_reviews": func(t *testing.T) Model { return byChangeModel(t, loadGroups(t), 120, 30, true) },
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			m := build(t)
			checkGolden(t, name, m.View()+"\n")
		})
	}
}

func TestReviewMeta(t *testing.T) {
	m := byChangeModel(t, withReviews(loadGroups(t)), 120, 30, true)
	want := map[string]string{
		"g-user":     "3u 3f · b2 .82 · fast · 3 findings",
		"g-apierror": "2u 2f · deep←fast .42 · done",
		"g-cache":    "1u 1f · b1 .64 · deep (forced) · running",
		"g-imports":  "×5 5f · none",
		"g-f":        "1u 1f · fast · queued (over cap)",
		"g-theme":    "1u 1f · none (skipped)",
		"g-save":     "1u 1f · deep · failed",
	}
	for i := range m.c.groups {
		ge := &m.c.groups[i]
		if got := m.groupMeta(ge); got != want[ge.g.ID] {
			t.Errorf("%s meta = %q, want %q", ge.g.ID, got, want[ge.g.ID])
		}
	}
	m = byChangeModel(t, loadGroups(t), 120, 30, true)
	if got := m.groupMeta(&m.c.groups[0]); got != "3u 3f · b2 .82 · not routed" {
		t.Errorf("meta without a routing table = %q", got)
	}
}

func TestFindingsByChange(t *testing.T) {
	m := byChangeModel(t, withReviews(loadGroups(t)), 160, 60, true)
	v := m.View()
	contains(t, v, "routed: fast")
	contains(t, v, "routing: if breaking → fast · context: callers")
	contains(t, v, "findings (3)")
	contains(t, v, "deep check: 1 missed")
	// Anchored findings in unit order (c1 before k1), the unanchored one
	// last; each behind the "!" bar under a "severity · tier" head.
	hi := strings.Index(v, "! high · fast")
	hiClaim := strings.Index(v, "! web/src/api/client.ts:11  opts is forwarded")
	med := strings.Index(v, "! web/src/components/UserCard.tsx:9  "+claimMed)
	low := strings.Index(v, "! web/src/api/client.ts  "+claimDoc)
	if hi < 0 || hiClaim < hi || med < 0 || low < 0 || hiClaim >= med || med >= low {
		t.Errorf("finding order hi %d/%d med %d low %d:\n%s", hi, hiClaim, med, low, v)
	}
	contains(t, v, "! medium · fast")
	contains(t, v, "! low · fast")
	contains(t, v, "! fix: merge opts over the default headers")
	// By change findings are listed once, not inline.
	lacks(t, v, "! [high]")

	press(t, &m, "J")
	v = m.View()
	contains(t, v, "findings: none")
	contains(t, v, "deep check: agree")
	press(t, &m, "J")
	contains(t, m.View(), "findings: not reviewed (running)")
	press(t, &m, "J")
	contains(t, m.View(), "findings: not reviewed (tier none)")
	press(t, &m, "J")
	v = m.View()
	contains(t, v, "findings: not reviewed (queued, over cap)")
	contains(t, v, "over cost cap: est $0.14, $0.05 of $0.40 left")
	press(t, &m, "J")
	contains(t, m.View(), "findings: not reviewed (skipped by you)")
	press(t, &m, "J")
	v = m.View()
	contains(t, v, "findings: not reviewed (failed)")
	contains(t, v, "error: timeout")
}

func TestFindingsInlineByFile(t *testing.T) {
	m := newModel(t, withReviews(loadGroups(t)), 120, 40)
	allOff(&m)
	ls := lines(m)
	row := func(sub string) int {
		t.Helper()
		for i, l := range ls {
			if strings.Contains(paneOf(l), sub) {
				return i
			}
		}
		t.Fatalf("no row with %q:\n%s", sub, m.View())
		return -1
	}
	line := row("11 +   return fetchJSON")
	if got := paneOf(ls[line+1]); !strings.HasPrefix(got, "! [high] opts is forwarded") {
		t.Errorf("row under line 11 = %q", got)
	}
	fix := row("fix: merge opts over the default headers")
	if fix <= line+1 {
		t.Errorf("fix row %d not after the finding at %d", fix, line+1)
	}
	// The unanchored finding of the group is not drawn inline.
	lacks(t, m.View(), claimDoc)

	selectFile(t, &m, "web/src/components/UserCard.tsx")
	ls = lines(m)
	line = row(" 9 +   const user")
	if got := paneOf(ls[line+1]); !strings.HasPrefix(got, "! [medium] "+claimMed) {
		t.Errorf("row under line 9 = %q", got)
	}
}

// A finding shown as a comment (its ai-draft) or marked (dismissed) is not
// drawn again as an inline finding row; the by-change list still has it.
func TestFindingsInlineSkipCommentedAndMarked(t *testing.T) {
	d := withReviews(loadGroups(t))
	fs := d.Reviews[0].Findings
	hi, med := fs[2], fs[1]
	key := func(f model.Finding) string { return comments.FindingKey(d.Item.ID, "g-user", f) }
	d.Comments = []model.Comment{{ID: comments.AIDraftID(key(hi)), ItemID: d.Item.ID, UnitID: hi.UnitID,
		Path: hi.Path, Line: hi.Line, Side: model.SideNew, Body: comments.DraftBody(hi),
		Source: model.CommentAIDraft, State: model.CommentDraft, FindingKey: key(hi), Severity: hi.Severity}}
	d.FindingMarks = []model.FindingMark{{ItemID: d.Item.ID, FindingKey: key(med), State: model.FindingDismissed}}

	m := newModel(t, d, 120, 40)
	allOff(&m)
	v := m.View()
	lacks(t, v, "! [high]")
	contains(t, v, "ai-draft · high")
	selectFile(t, &m, "web/src/components/UserCard.tsx")
	lacks(t, m.View(), "! [medium]")
	if !m.c.hasFindings("k1") {
		t.Error("a dismissed finding still counts for the has-findings filter")
	}

	m = byChangeModel(t, d, 160, 60, true)
	contains(t, m.View(), "findings (3)")
}

func TestFindingUnderHeaderAndNeverCollapsed(t *testing.T) {
	d := withReviews(loadGroups(t))
	d.Reviews[1].Findings = []model.Finding{{UnitID: "e1", Path: "web/src/api/errors.ts", Severity: model.SevLow, Claim: "unanchored note"}}
	m := newModel(t, d, 120, 30)
	allOff(&m)
	selectFile(t, &m, "web/src/api/errors.ts")
	v := m.View()
	lacks(t, v, "(z to expand)") // a moved (noise) unit with a finding stays open
	contains(t, v, "! [low] web/src/api/errors.ts  unanchored note")
	// Under the header: right after the signature and "moved from" rows.
	ls := lines(m)
	sig, note := -1, -1
	for i, l := range ls {
		switch {
		case strings.Contains(l, "│  L1–5"):
			sig = i
		case strings.Contains(l, "unanchored note"):
			note = i
		}
	}
	if sig < 0 || note != sig+2 || !strings.Contains(ls[sig+1], "moved from") {
		t.Errorf("unanchored finding not under the header (sig %d note %d):\n%s", sig, note, v)
	}
}

func TestHasFindingsFilter(t *testing.T) {
	m := newModel(t, withReviews(loadGroups(t)), 120, 30)
	allOff(&m)
	m.SetFilter(FilterFindings, true)
	if !m.FilterOn(FilterFindings) {
		t.Fatal("has findings did not turn on")
	}
	v := m.View()
	contains(t, v, "export async function getUser") // c1 has a finding
	contains(t, v, "1 unit hidden by filters (f)")  // c2 has none
	press(t, &m, "f")
	lacks(t, lines(m)[0], "has findings: no data yet")
	press(t, &m, "9") // toggles it off from the bar, no placeholder note
	if m.FilterOn(FilterFindings) || m.Note() != "" {
		t.Errorf("9: on %v note %q", m.FilterOn(FilterFindings), m.Note())
	}

	// Without a routing table nothing has findings.
	m = newModel(t, loadGroups(t), 120, 30)
	allOff(&m)
	m.SetFilter(FilterFindings, true)
	contains(t, m.View(), "2 units hidden by filters (f)")
}

func TestReReview(t *testing.T) {
	m := byChangeModel(t, withReviews(loadGroups(t)), 120, 30, true)
	press(t, &m, "R")
	if id, ok := m.PendingReview(); !ok || id != "g-user" {
		t.Fatalf("R on g-user: %q %v (note %q)", id, ok, m.Note())
	}
	if _, ok := m.PendingReview(); ok {
		t.Error("PendingReview did not clear")
	}
	press(t, &m, "J", "J", "J", "R") // g-imports: mechanical, routed to none
	if _, ok := m.PendingReview(); ok || !strings.Contains(m.Note(), "mechanical group routed to none") {
		t.Errorf("R on mechanical none: note %q", m.Note())
	}
	press(t, &m, "J", "J", "R") // g-theme: skipped by the user, can be forced
	if id, ok := m.PendingReview(); !ok || id != "g-theme" {
		t.Errorf("R on a skipped group: %q %v", id, ok)
	}

	// By file: the cursor unit's group.
	m = newModel(t, withReviews(loadGroups(t)), 120, 30)
	allOff(&m)
	press(t, &m, "R")
	if id, ok := m.PendingReview(); !ok || id != "g-user" {
		t.Errorf("R by file: %q %v", id, ok)
	}

	// No routing row: refused.
	m = byChangeModel(t, loadGroups(t), 120, 30, true)
	press(t, &m, "R")
	if _, ok := m.PendingReview(); ok || !strings.Contains(m.Note(), "not routed yet") {
		t.Errorf("R without a row: note %q", m.Note())
	}
	// No groups at all: refused.
	d := loadGroups(t)
	d.Groups = nil
	m = newModel(t, d, 120, 30)
	allOff(&m)
	press(t, &m, "R")
	if _, ok := m.PendingReview(); ok || !strings.Contains(m.Note(), "no group here") {
		t.Errorf("R without groups: note %q", m.Note())
	}
}

func TestAITitle(t *testing.T) {
	d := withReviews(loadGroups(t))
	d.Groups[1].Name, d.Groups[1].NameSource = "Move ApiError out", model.NameAI
	m := byChangeModel(t, d, 120, 30, true)
	press(t, &m, "J")
	v := m.View()
	contains(t, v, "ai·Move ApiError out")
	lacks(t, v, "‹Move ApiError out›")
	lacks(t, v, "placeholder name")
	contains(t, v, "ai· title written by the fast model")
}
