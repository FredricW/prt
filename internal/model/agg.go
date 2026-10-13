package model

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
	// JevCalls and JevCost sum the item's Jev stage rows.
	JevCalls int     `json:"jevCalls"`
	JevCost  float64 `json:"jevCost"`
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
}
