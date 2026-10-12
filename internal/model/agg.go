package model

// Attention is the reading depth a unit deserves.
type Attention string

// Attention levels (from the "depth" label; see spec "Ranking and aggregation").
const (
	AttnEyes Attention = "eyes"
	AttnSkim Attention = "skim"
	AttnSkip Attention = "skip"
)

// Headline is a unit labelled breaking, contract change, migration, env or flag.
type Headline struct {
	UnitID     string  `json:"unitId"`
	Path       string  `json:"path"`
	Line       int     `json:"line"`
	Kind       string  `json:"kind"` // "breaking", "signature", "schema/migration", ...
	Text       string  `json:"text"`
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

// UnitAgg is the per-unit arithmetic over labels.
type UnitAgg struct {
	UnitID         string    `json:"unitId"`
	Noise          bool      `json:"noise"`
	EffectiveLines int       `json:"effectiveLines"`
	Attention      Attention `json:"attention"`
	Minutes        float64   `json:"minutes"`
	Unsure         bool      `json:"unsure"`
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
	ItemID         string             `json:"itemId"`
	Composition    map[string]float64 `json:"composition"`
	Added          int                `json:"added"`
	Removed        int                `json:"removed"`
	EffectiveLines int                `json:"effectiveLines"`
	Minutes        float64            `json:"minutes"`
	UnitsTotal     int                `json:"unitsTotal"`
	UnitsEyes      int                `json:"unitsEyes"`
	Headlines      []Headline         `json:"headlines"`
	HotSpots       []HotSpot          `json:"hotSpots"`
	RiskMax        float64            `json:"riskMax"`
	RiskAny        float64            `json:"riskAny"`
	RiskTags       map[string]int     `json:"riskTags"`
	TestGap        int                `json:"testGap"`
	RiskyUnits     int                `json:"riskyUnits"`
	Fidelity       float64            `json:"fidelity"`
	Undocumented   []string           `json:"undocumented"` // unit ids
	Shape          string             `json:"shape,omitempty"`
	Unsure         int                `json:"unsure"`
	TLDR           string             `json:"tldr,omitempty"`
	Urgency        float64            `json:"urgency"`
	UrgencyTerms   []UrgencyTerm      `json:"urgencyTerms"`
	Why            string             `json:"why"` // top three terms, in words
	Units          []UnitAgg          `json:"units"`
	Groups         []GroupAgg         `json:"groups"`
	LabelsMissing  bool               `json:"labelsMissing,omitempty"` // Jev unavailable
}
