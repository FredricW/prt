package model

import "time"

// Attention is the reading depth a unit deserves.
type Attention string

// Attention levels (from the "depth" label; see spec "Ranking and aggregation").
const (
	AttnEyes Attention = "eyes"
	AttnSkim Attention = "skim"
	AttnSkip Attention = "skip"
)

// Headline is a unit labelled breaking, contract change, migration, env or flag
// (also pii_or_secret lines and test_weakened, per the catalogue's feeds).
type Headline struct {
	UnitID     string  `json:"unitId"`
	Path       string  `json:"path"`
	Line       int     `json:"line"`
	Kind       string  `json:"kind"` // "breaking", "signature", "schema/migration", "env/flag", "test weakened", "pii or secret", ...
	Text       string  `json:"text"` // e.g. "getUser(id, opts) — breaks existing callers"
	P          float64 `json:"p"`
	Confidence float64 `json:"confidence"`
}

// HotSpot is a unit ranked by risk × relevance × (1 − covered) × min(confidence).
type HotSpot struct {
	UnitID string  `json:"unitId"`
	Path   string  `json:"path"`
	Line   int     `json:"line"`
	Score  float64 `json:"score"`
	Reason string  `json:"reason"` // five words, from a finding or the label that fired
}

// Mark is a per-line gutter mark (spec Code page: ◆ ~ ⋯ ·).
type Mark string

// Gutter marks, highest precedence first. The TUI maps them to glyphs:
// suspicious ◆ (shaded by Score), value ~, leftover ⋯, none ·.
const (
	MarkSuspicious Mark = "suspicious"
	MarkValue      Mark = "value"
	MarkLeftover   Mark = "leftover"
	MarkNone       Mark = "none"
)

// LineMark is the gutter mark of one added line (new-side line number).
type LineMark struct {
	Line  int     `json:"line"`
	Mark  Mark    `json:"mark"`
	Score float64 `json:"score,omitempty"` // suspicious score (1..5) for shading
	P     float64 `json:"p,omitempty"`     // probability behind the mark
	Fmt   bool    `json:"fmt,omitempty"`   // formatting/whitespace only (fmt label)
}

// Noise reasons (UnitAgg.NoiseReason); "" when the unit is signal.
const (
	NoiseFormatting = "formatting" // kind = formatting, or ChangeUnit.Format
	NoiseGenerated  = "generated"  // kind = generated
	NoiseMoved      = "moved"      // moved, verified identical
	NoiseFmtLines   = "fmt-lines"  // every added line labelled fmt
)

// UnitAgg is the per-unit arithmetic over labels, plus the label facts the
// Code page needs for chips and filters, so the TUI never reads raw labels
// except for the `L` overlay.
type UnitAgg struct {
	UnitID         string    `json:"unitId"`
	Path           string    `json:"path,omitempty"`
	Owned          bool      `json:"owned,omitempty"`
	Kind           string    `json:"kind,omitempty"` // Jev kind label value, or "unknown"
	Noise          bool      `json:"noise"`
	NoiseReason    string    `json:"noiseReason,omitempty"`
	EffectiveLines int       `json:"effectiveLines"`
	Attention      Attention `json:"attention"`
	Minutes        float64   `json:"minutes"`
	// Unsure is true when any of the unit's labels has confidence below
	// pipeline.unsure_below; such a unit is never skip and stays expanded.
	Unsure        bool    `json:"unsure"`
	MinConfidence float64 `json:"minConfidence"`

	Behaviour    bool       `json:"behaviour,omitempty"` // behaviour P >= 0.5
	BehaviourP   float64    `json:"behaviourP,omitempty"`
	Breaking     bool       `json:"breaking,omitempty"` // breaking P >= 0.5
	BreakingP    float64    `json:"breakingP,omitempty"`
	Contract     string     `json:"contract,omitempty"` // contract label value ("none", "signature", ...)
	Covered      bool       `json:"covered,omitempty"`  // covered P >= 0.5
	CoveredP     float64    `json:"coveredP,omitempty"`
	Risk         float64    `json:"risk,omitempty"`      // interpolated 1..5, 0 unknown
	RiskLevel    int        `json:"riskLevel,omitempty"` // integer rung 1..5, 0 unknown
	Relevance    float64    `json:"relevance,omitempty"` // interpolated 1..5, 0 unknown
	RiskTags     []string   `json:"riskTags,omitempty"`  // sub-ids with P >= 0.5, in RiskTags order
	FailMode     string     `json:"failMode,omitempty"`
	DescBullet   int        `json:"descBullet,omitempty"` // 1-based bullet index, 0 none/unknown
	Undocumented bool       `json:"undocumented,omitempty"`
	Untested     bool       `json:"untested,omitempty"` // risk >= 3 and covered < 0.5 (covered known)
	Lines        []LineMark `json:"lines,omitempty"`
	// BehaviourUnknown, BreakingUnknown and CoveredUnknown are set when the
	// label has no real answer (Jev unreachable, a missing or malformed
	// answer, or never asked). The matching bool and P are then false and 0
	// but claim nothing: filters let the unit through and chips show "?".
	BehaviourUnknown bool `json:"behaviourUnknown,omitempty"`
	BreakingUnknown  bool `json:"breakingUnknown,omitempty"`
	CoveredUnknown   bool `json:"coveredUnknown,omitempty"`
}

// FileAgg is one row of the Code page's file list (mode A).
type FileAgg struct {
	Path      string   `json:"path"`
	Owned     bool     `json:"owned"`
	Attention int      `json:"attention"` // number of needs-eyes units (sort key)
	Eyes      int      `json:"eyes"`
	Skim      int      `json:"skim"`
	Skip      int      `json:"skip"`
	UnitIDs   []string `json:"unitIds"` // in line order (minimap cells)
}

// BulletAgg is one description bullet and the units that implement it.
type BulletAgg struct {
	Index       int      `json:"index"` // 1-based, matches BulletValue
	Text        string   `json:"text"`
	Implemented bool     `json:"implemented"`
	UnitIDs     []string `json:"unitIds,omitempty"`
	P           float64  `json:"p,omitempty"` // best desc_bullet probability among its units
}

// SliceRow is one line of the Overview's "Your slice" block.
type SliceRow struct {
	Module string `json:"module"` // owned glob ("web/**") or top-level dir ("server/*")
	Owned  bool   `json:"owned"`
	Units  int    `json:"units"`
	Eyes   int    `json:"eyes"`
}

// UnitHotSpot is the spec's hot-spot score of one unit:
// risk × relevance × (1 − covered) × min(confidence), using the
// interpolated Risk/Relevance, CoveredP and MinConfidence. Aggregate (hot
// spots) and route (queue order) share it.
func UnitHotSpot(a UnitAgg) float64 {
	return a.Risk * a.Relevance * (1 - a.CoveredP) * a.MinConfidence
}

// GroupAgg is computed from labels for one group; never stored as truth.
type GroupAgg struct {
	GroupID        string             `json:"groupId"`
	Composition    map[string]float64 `json:"composition"`
	EffectiveLines int                `json:"effectiveLines"`
	Minutes        float64            `json:"minutes"`
	RiskMax        float64            `json:"riskMax"`
	RiskTags       map[string]int     `json:"riskTags"`
	TestGap        int                `json:"testGap"`
	Breaking       []string           `json:"breaking"` // unit ids
	Unsure         int                `json:"unsure"`
	// Kind is the group's kind for routing and display: the Jev kind
	// shared by every unit ("moved" when every unit is git-moved,
	// "formatting" for ChangeUnit.Format units), "unknown" when any unit's
	// kind is unknown, else "mixed" (M3; see route package doc).
	Kind string `json:"kind,omitempty"`
	// HotSpot is the max UnitHotSpot over the group's units (queue order
	// for deep slots and the cost cap).
	HotSpot float64 `json:"hotSpot,omitempty"`
	// Findings counts review findings on the group (from the routing row).
	Findings int `json:"findings,omitempty"`
}

// ReviewStats is the Pipeline page's totals block (spec Pipeline page 3),
// computed by the pipeline after stage 7; nil before M3 analysis.
type ReviewStats struct {
	Groups int `json:"groups"`
	// Tiers counts routing rows by final tier ("none", "fast", "deep").
	Tiers map[Tier]int `json:"tiers"`
	// Status counts rows by status.
	Status       map[ReviewStatus]int `json:"status"`
	Escalations  int                  `json:"escalations"`
	Calibrations int                  `json:"calibrations"`
	Findings     int                  `json:"findings"`
	// ElapsedMs is the review stage's wall time; AllDeepEstMs estimates
	// running every routed (non-none) group on the deep tier one after
	// another.
	ElapsedMs    int64 `json:"elapsedMs"`
	AllDeepEstMs int64 `json:"allDeepEstMs"`
	// Cost is the LLM cost of the item's current reviews, TL;DR and group
	// titles (cache hits count what they cost when they ran); CapUSD is
	// the effective cap (Item.CapUSD or cost_cap_usd).
	Cost    float64 `json:"cost"`
	CapUSD  float64 `json:"capUsd"`
	OverCap int     `json:"overCap"` // rows queued over the cap
	// JevCalls and JevCost are the Jev calls behind the item's current
	// labels: this run's stage-4 totals, or, when this run's labels all came
	// from the cache, the totals of the run that asked them (JevCached),
	// the way cached review costs count what they cost when they ran.
	JevCalls  int     `json:"jevCalls"`
	JevCost   float64 `json:"jevCost"`
	JevCached bool    `json:"jevCached,omitempty"`
	// Note explains a degraded review stage ("reviews skipped:
	// PRT_ANTHROPIC_API_KEY not set").
	Note string `json:"note,omitempty"`
}

// CalibrationStats is the Pipeline page's calibration panel.
type CalibrationStats struct {
	// Samples and Misses are sampled double reviews in the trailing 30
	// days, and those where deep found a severity ≥ medium finding fast
	// did not; MissRate = Misses ÷ Samples (0 when no samples).
	Samples  int     `json:"samples"`
	Misses   int     `json:"misses"`
	MissRate float64 `json:"missRate"`
	// Overrides counts label overrides by question id (all items).
	Overrides map[string]int `json:"overrides,omitempty"`
	// LastAgree is the last sample's agreement (nil when none).
	LastAgree *bool  `json:"lastAgree,omitempty"`
	LastAt    string `json:"lastAt,omitempty"` // RFC3339
	// LastGroup is the name of the group the last sample double-reviewed
	// ("" when unknown), for "sampled <group> to deep too: agrees". M5.
	LastGroup string `json:"lastGroup,omitempty"`
	// Questions is the override rate per question over the trailing 30 days
	// (spec Ranking "Calibration metrics": overrides ÷ labels), one row per
	// question with at least one override, highest rate first. FlagAbove is
	// the rate above which a question is flagged as a rewording candidate
	// (0.15). M5.
	Questions []QuestionRate `json:"questions,omitempty"`
	FlagAbove float64        `json:"flagAbove,omitempty"`
}

// QuestionRate is one question's label override rate (CalibrationStats).
type QuestionRate struct {
	Question  string  `json:"question"`
	Overrides int     `json:"overrides"` // overrides in the trailing 30 days
	Labels    int     `json:"labels"`    // labels of the question (denominator)
	Rate      float64 `json:"rate"`      // Overrides ÷ Labels (0 when Labels is 0)
	Flagged   bool    `json:"flagged"`   // Rate > FlagAbove
}

// BranchCommit is one commit of a branch's overview block.
type BranchCommit struct {
	SHA     string    `json:"sha"`
	Subject string    `json:"subject"`
	Date    time.Time `json:"date"`
}

// BranchInfo is the Overview's branch state block (spec Overview,
// "Branches"): the Jev Choice branch.state with its probability, the
// unpushed commits and the suggested PR title. Set by the pipeline for
// branch items in stage 8 (nil for PRs). M5.
type BranchInfo struct {
	// State is one of the Branch* constants ("" when unknown: Jev
	// unavailable and no rule applied); P is its probability (1 for a rule
	// decision) and Probs the full Jev distribution (option → p).
	State string             `json:"state"`
	P     float64            `json:"p"`
	Probs map[string]float64 `json:"probs,omitempty"`
	// Known is false when Jev gave no answer (labels unavailable).
	Known bool `json:"known"`
	// Rule explains a state set by code rather than Jev, e.g. "no commits
	// for 16 days" (the abandoned? default, DECISIONS "Branch states (M5)").
	Rule string `json:"rule,omitempty"`
	// SupersededBy is the open PR that supersedes the branch (0 when none
	// or unknown).
	SupersededBy int `json:"supersededBy,omitempty"`
	// LastCommitAt is the head commit's date; IdleDays the whole days since
	// then at analysis time.
	LastCommitAt time.Time `json:"lastCommitAt,omitzero"`
	IdleDays     int       `json:"idleDays"`
	// Unpushed counts commits not on the upstream (all commits ahead for a
	// branch without one); Commits lists them, newest first (at most 20).
	Unpushed int            `json:"unpushed"`
	Commits  []BranchCommit `json:"commits,omitempty"`
	// SuggestedTitle is the fast LLM's PR title written from the group
	// names (shown marked as model-written); TitleKey the request key it
	// came from; TitleNote why there is none ("no review model", "no
	// groups").
	SuggestedTitle string `json:"suggestedTitle,omitempty"`
	TitleKey       string `json:"titleKey,omitempty"`
	TitleNote      string `json:"titleNote,omitempty"`
}

// Architecture module and edge statuses (ArchModule.Status, ArchEdge.Status).
const (
	ArchChanged = "changed" // module exists at base and head and has changed files
	ArchNew     = "new"     // module exists only at head
	ArchRemoved = "removed" // module exists only at base
	ArchContext = "context" // unchanged module reached through imports (Hop ≥ 1)
	ArchSame    = "same"    // edge present at base and head
)

// ArchModule is one node of the Architecture tab's module graph. A module
// is a directory of source files (see internal/arch for the exact rule).
type ArchModule struct {
	ID     string `json:"id"` // module path, e.g. "web/src/api"; "." for the repo root
	Status string `json:"status"`
	// Hop is 0 for modules the diff touches, n for modules n import hops
	// away from a touched one.
	Hop   int  `json:"hop"`
	Owned bool `json:"owned,omitempty"`
	// Files are the item's changed files in this module (diff paths, sorted);
	// FileCount the module's source files at head (at base when removed).
	Files     []string `json:"files,omitempty"`
	FileCount int      `json:"fileCount"`
	// Unit roll-ups over the module's change units (from ItemAgg.Units).
	Units        int `json:"units"`
	Eyes         int `json:"eyes"`
	Skim         int `json:"skim"`
	Skip         int `json:"skip"`
	Unsure       int `json:"unsure"`
	Untested     int `json:"untested"`
	Undocumented int `json:"undocumented"`
	Breaking     int `json:"breaking"`
	RiskMax      int `json:"riskMax"` // max unit risk level, 0 unknown
	// Attention colours the node: eyes if any unit needs eyes, else skim,
	// else skip; "" for context modules without units.
	Attention Attention `json:"attention,omitempty"`
}

// ArchEdge is an import dependency between two modules (From imports To).
type ArchEdge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Status string `json:"status"` // ArchSame, ArchNew or ArchRemoved
	// Imports counts the file-level import statements behind the edge (at
	// head; at base for a removed edge).
	Imports int `json:"imports"`
	// Contracts are headline texts of breaking or contract-changing units
	// in To that From's files import ("contracts crossing edges").
	Contracts []string `json:"contracts,omitempty"`
}

// ArchGraph is the Architecture tab's record (spec: "module graph coloured
// by attention, as in the v2 design"): modules touched by the diff plus
// their import neighbours up to MaxHop, and the edges between them. Built
// from import analysis by internal/arch and set by the pipeline in stage 8.
type ArchGraph struct {
	Modules []ArchModule `json:"modules"`
	Edges   []ArchEdge   `json:"edges"`
	// MaxHop is the largest Hop computed (the tab's +/- depth range).
	MaxHop int `json:"maxHop"`
	// NetEffect are short sentences written by code from the graph diff
	// ("new module server/middleware", "web/api no longer imports
	// server/session"); no model call.
	NetEffect []string `json:"netEffect,omitempty"`
	// Note explains a degraded graph ("import analysis unavailable: …");
	// Truncated is set when the repo had more source files than indexed.
	Note      string `json:"note,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// UrgencyTerm is one weighted term of the urgency score.
type UrgencyTerm struct {
	Name    string  `json:"name"`   // e.g. "w_requested"
	Words   string  `json:"words"`  // e.g. "asked you"
	Value   float64 `json:"value"`  // raw factor value
	Weight  float64 `json:"weight"` // configured weight
	Contrib float64 `json:"contrib"`
}

// ItemAgg is the overview record for an item; computed by the aggregate stage.
type ItemAgg struct {
	ItemID string `json:"itemId"`
	// Composition is effective lines of each Jev kind ÷ total effective
	// lines (spec formula; noise excluded). NoiseLines are the changed lines
	// of noise units, so the bar can show a noise segment.
	Composition    map[string]float64 `json:"composition"`
	NoiseLines     int                `json:"noiseLines"`
	Added          int                `json:"added"`
	Removed        int                `json:"removed"`
	EffectiveLines int                `json:"effectiveLines"`
	Minutes        float64            `json:"minutes"`
	UnitsTotal     int                `json:"unitsTotal"`
	UnitsEyes      int                `json:"unitsEyes"`
	UnitsSkim      int                `json:"unitsSkim"`
	UnitsSkip      int                `json:"unitsSkip"`
	Headlines      []Headline         `json:"headlines"`
	HotSpots       []HotSpot          `json:"hotSpots"` // top 3
	RiskMax        float64            `json:"riskMax"`  // max unit risk level (1..5), 0 unknown
	RiskAny        float64            `json:"riskAny"`
	RiskTags       map[string]int     `json:"riskTags"` // sub-id → unit count
	TestGap        int                `json:"testGap"`
	RiskyUnits     int                `json:"riskyUnits"` // units with risk >= 3 (denominator of test gap)
	Bullets        []BulletAgg        `json:"bullets,omitempty"`
	Fidelity       float64            `json:"fidelity"`
	Undocumented   []string           `json:"undocumented"`    // unit ids
	Shape          string             `json:"shape,omitempty"` // item "shape" label, when answered
	ShapeP         float64            `json:"shapeP,omitempty"`
	Unsure         int                `json:"unsure"`                // labels with confidence < unsure_below
	UnsureUnits    []string           `json:"unsureUnits,omitempty"` // unit ids kept expanded
	Files          []FileAgg          `json:"files,omitempty"`       // owned first, then by attention
	Slice          []SliceRow         `json:"slice,omitempty"`       // owned modules first, then the rest
	// TLDR is the fast tier's one sentence (≤160 chars) written from this
	// record only (shown "ai·from labels"); TLDRKey is the key of the
	// request it came from, so it is regenerated only when the record's
	// TL;DR inputs change. Both empty when no reviewer ran.
	TLDR          string        `json:"tldr,omitempty"`
	TLDRKey       string        `json:"tldrKey,omitempty"`
	Urgency       float64       `json:"urgency"`
	UrgencyTerms  []UrgencyTerm `json:"urgencyTerms"`
	Why           string        `json:"why"` // top three terms, in words
	Units         []UnitAgg     `json:"units"`
	Groups        []GroupAgg    `json:"groups"`
	LabelsMissing bool          `json:"labelsMissing,omitempty"` // Jev unavailable
	// Grouping is the grouping panel record and Split the split suggestion;
	// both are set by the pipeline from the cluster stage (not by
	// aggregate.Item). Nil before M2 analysis.
	Grouping *GroupingStats   `json:"grouping,omitempty"`
	Split    *SplitSuggestion `json:"split,omitempty"`
	// Review and Calibration are the Pipeline page's totals and
	// calibration panel, set by the pipeline after stage 7 (M3).
	Review      *ReviewStats      `json:"review,omitempty"`
	Calibration *CalibrationStats `json:"calibration,omitempty"`
	// Since is the Overview's "Since you looked" block, set by the pipeline
	// (M4); nil when there is no earlier head to compare with.
	Since *SinceLooked `json:"since,omitempty"`
	// Branch is the branch state block (branch items only) and Arch the
	// Architecture tab's module graph; both set by the pipeline in stage 8
	// (M5), nil before.
	Branch *BranchInfo `json:"branch,omitempty"`
	Arch   *ArchGraph  `json:"arch,omitempty"`
}

// Since-you-looked base sources (SinceLooked.Source).
const (
	// SinceViewed: the base is a head the user looked at in prt (ViewMark).
	SinceViewed = "viewed"
	// SinceReviewed: no ViewMark; the base is the commit of the viewer's
	// latest GitHub review (Item.ViewerReviewedSHA).
	SinceReviewed = "reviewed"
)

// SinceLooked is the Overview's "Since you looked" block (spec Overview 9):
// the units changed since the base head, and for each of the user's earlier
// comments whether the latest push addressed it (Jev comment_addressed).
type SinceLooked struct {
	// BaseSHA is the head the user last looked at; HeadSHA the analysed head
	// it is compared with. Source is SinceViewed or SinceReviewed.
	BaseSHA string `json:"baseSha"`
	HeadSHA string `json:"headSha"`
	Source  string `json:"source"`
	// ChangedUnits are the current unit ids that did not exist at BaseSHA
	// (unit ids are content hashes), in unit order.
	ChangedUnits []string `json:"changedUnits"`
	// Comments are the user's earlier top-level comments with their
	// comment_addressed answers, in path/line order.
	Comments []AddressedComment `json:"comments,omitempty"`
	// Note explains a degraded block ("base head not available locally",
	// "labels unavailable").
	Note string `json:"note,omitempty"`
}

// AddressedComment is one of the user's earlier comments and Jev's answer
// to comment_addressed for it. Known is false when Jev was unavailable (the
// row then shows "?"); Addressed is P >= 0.5.
type AddressedComment struct {
	CommentID string  `json:"commentId"`
	UnitID    string  `json:"unitId,omitempty"`
	Path      string  `json:"path"`
	Line      int     `json:"line"`
	Excerpt   string  `json:"excerpt"` // first line of the body, ≤ 80 runes
	Known     bool    `json:"known"`
	Addressed bool    `json:"addressed"`
	P         float64 `json:"p"`
	Resolved  bool    `json:"resolved,omitempty"` // the thread is resolved on GitHub
}
