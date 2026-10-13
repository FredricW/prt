// Package model holds the shared domain types of prt (spec: "Data model").
//
// Everything hangs off an Item (a PR or a branch) and its ChangeUnits. Labels,
// groups, reviews and comments reference units by a content hash, so a rebase
// keeps everything that did not change. This package has no dependencies on
// other prt packages and contains no I/O; every other package may import it.
package model

import "time"

// ItemKind distinguishes PRs from local branches.
type ItemKind string

// Item kinds.
const (
	KindPR     ItemKind = "pr"
	KindBranch ItemKind = "branch"
)

// ItemSource says how an item entered the picker.
type ItemSource string

// Item sources.
const (
	SourceRanked ItemSource = "ranked"
	SourceAdded  ItemSource = "added"
)

// CIState is the combined CI status of an item's head commit.
type CIState string

// CI states.
const (
	CIUnknown CIState = ""
	CIPending CIState = "pending"
	CIGreen   CIState = "green"
	CIRed     CIState = "red"
)

// Section is the picker section an item is listed in.
type Section string

// Picker sections, in display order.
const (
	SectionNeedsYou Section = "needs-you"
	SectionYours    Section = "yours"
	SectionBranches Section = "branches"
	// SectionInvolved holds PRs a source returned (involves:@me, owned
	// paths) that trigger none of the Needs-you rules; listed after
	// Branches and hidden when empty (see DECISIONS.md).
	SectionInvolved Section = "involved"
	SectionAdded    Section = "added"
)

// ItemState is the analysis state shown in the picker.
type ItemState string

// Item states.
const (
	StateFetching  ItemState = "fetching"
	StateAnalysing ItemState = "analysing"
	StateReady     ItemState = "ready"
	StateStale     ItemState = "stale"
	StateError     ItemState = "error"
)

// Item is a PR or a local branch under review.
type Item struct {
	ID      string   `json:"id"` // "pr:<number>" or "branch:<ref>"; see ItemID
	Kind    ItemKind `json:"kind"`
	Number  int      `json:"number,omitempty"` // PR number, 0 for branches
	Ref     string   `json:"ref"`              // head branch name
	BaseRef string   `json:"baseRef,omitempty"`
	HeadSHA string   `json:"headSha"`
	BaseSHA string   `json:"baseSha"` // merge base with the base ref
	// AnalysedSHA is the head the stored files, units and overview describe
	// (set when a run finishes). It differs from HeadSHA while Stale.
	AnalysedSHA string     `json:"analysedSha,omitempty"`
	Title       string     `json:"title"`
	Body        string     `json:"body,omitempty"`
	Author      string     `json:"author,omitempty"`
	Reviewers   []string   `json:"reviewers,omitempty"`
	CI          CIState    `json:"ci,omitempty"`
	Labels      []string   `json:"labels,omitempty"` // GitHub labels, not Jev labels
	Pinned      bool       `json:"pinned,omitempty"`
	Source      ItemSource `json:"source"`
	Section     Section    `json:"section,omitempty"`
	URL         string     `json:"url,omitempty"`
	State       ItemState  `json:"state,omitempty"`
	StateNote   string     `json:"stateNote,omitempty"` // stage name or error reason
	CreatedAt   time.Time  `json:"createdAt,omitempty"`
	UpdatedAt   time.Time  `json:"updatedAt,omitempty"`

	// ReviewRequested is true when the current user is a requested reviewer.
	ReviewRequested bool `json:"reviewRequested,omitempty"`
	// Unpushed is the number of local commits not on the upstream (branches).
	Unpushed int `json:"unpushed,omitempty"`
	// Ahead is the number of commits not on the default branch.
	Ahead int `json:"ahead,omitempty"`

	// Draft is true for draft PRs.
	Draft bool `json:"draft,omitempty"`
	// ViewerApproved is true when the current user has approved this PR
	// (feeds "CI red on something you approved").
	ViewerApproved bool `json:"viewerApproved,omitempty"`
	// Additions and Deletions are line counts (from GitHub, or from the
	// aggregate stage once the diff is parsed).
	Additions int `json:"additions,omitempty"`
	Deletions int `json:"deletions,omitempty"`
	// Urgency is the picker sort key, copied from ItemAgg.Urgency by the
	// pipeline (M0: placeholder terms only; full formula in M3).
	Urgency float64 `json:"urgency,omitempty"`
	// UrgencyQuartile is 0–4: filled cells of the picker's urgency bar,
	// relative to the other items (computed by the daemon on Items()).
	UrgencyQuartile int `json:"urgencyQuartile,omitempty"`
	// Why is the top three urgency terms in words ("asked you · release · age").
	Why string `json:"why,omitempty"`
	// Minutes is the estimated review time (M0: the line-count stand-in;
	// ItemAgg.Minutes once the diff is aggregated).
	Minutes float64 `json:"minutes,omitempty"`
	// OpenThreads counts unresolved GitHub review threads (PRs).
	OpenThreads int `json:"openThreads,omitempty"`
	// CrossRepo is true for a PR whose head lives in a fork: Ref is then a
	// branch name in another repository, never a local branch.
	CrossRepo bool `json:"crossRepo,omitempty"`

	// LabelsUnavailable is copied from ItemAgg.LabelsMissing by the pipeline:
	// Jev was unreachable, so the picker shows "labels unavailable".
	LabelsUnavailable bool `json:"labelsUnavailable,omitempty"`
	// Breaking and NeedsEyes are copied from the aggregate (count of
	// headline units labelled breaking; units with attention eyes) for the
	// picker's Needs-you secondary line.
	Breaking  int `json:"breaking,omitempty"`
	NeedsEyes int `json:"needsEyes,omitempty"`
}

// FileRole is the Jev-assigned role of a changed file.
type FileRole string

// File roles (question "role").
const (
	RoleCore      FileRole = "core"
	RoleGlue      FileRole = "glue"
	RoleUI        FileRole = "ui"
	RoleTest      FileRole = "test"
	RoleConfig    FileRole = "config"
	RoleGenerated FileRole = "generated"
)

// File is a file changed by an item.
type File struct {
	ItemID  string   `json:"itemId"`
	Path    string   `json:"path"`
	OldPath string   `json:"oldPath,omitempty"` // set when renamed
	Role    FileRole `json:"role,omitempty"`
	Owned   bool     `json:"owned"`
	Added   bool     `json:"added,omitempty"`
	Deleted bool     `json:"deleted,omitempty"`
	Renamed bool     `json:"renamed,omitempty"`
	Binary  bool     `json:"binary,omitempty"`
	Lang    string   `json:"lang,omitempty"` // "go", "ts", "tsx", "js", "python" or ""
	Adds    int      `json:"adds"`
	Dels    int      `json:"dels"`
}

// UnitKind is the git-level kind of a change unit (not the Jev "kind" label).
type UnitKind string

// Unit kinds.
const (
	UnitAdded    UnitKind = "added"
	UnitDeleted  UnitKind = "deleted"
	UnitModified UnitKind = "modified"
	UnitMoved    UnitKind = "moved"
)

// Span is a 1-based inclusive line range; Count 0 means empty.
type Span struct {
	Start int `json:"start"`
	Count int `json:"count"`
}

// Range locates a unit on both sides of the diff.
type Range struct {
	Old Span `json:"old"`
	New Span `json:"new"`
}

// ChangeUnit is a hunk (or a function-sized slice of one) and the unit of
// labelling. Its ID is content-based (see UnitID).
type ChangeUnit struct {
	ID        string   `json:"id"`
	ItemID    string   `json:"itemId"`
	FileID    string   `json:"fileId"` // file path within the item
	Path      string   `json:"path"`
	Range     Range    `json:"range"`
	Symbol    string   `json:"symbol,omitempty"`    // enclosing function, if any
	Signature string   `json:"signature,omitempty"` // enclosing function signature
	Diff      string   `json:"diff"`                // unified diff lines with +/-/space prefixes
	Kind      UnitKind `json:"kind"`
	MovedFrom string   `json:"movedFrom,omitempty"` // unit id of the origin
	ShapeHash string   `json:"shapeHash,omitempty"` // normalised shape, for mechanical detection
	Format    bool     `json:"format,omitempty"`    // pure formatting change (token streams equal)
	Adds      int      `json:"adds"`
	Dels      int      `json:"dels"`
	Order     int      `json:"order"` // position within the item, for stable ordering
}

// Label is one Jev answer for a unit (or a line of it; or a file or item:
// UnitID then holds FileSubject / ItemSubject). Value, P, Score and Probs
// follow the conventions documented in labels.go.
type Label struct {
	UnitID     string  `json:"unitId"`
	Question   string  `json:"question"` // question id, e.g. "risk" or "risk_tags.auth"
	Line       int     `json:"line,omitempty"`
	Value      string  `json:"value"`           // choice, "yes"/"no", 1-based score rung, or "unknown"
	P          float64 `json:"p"`               // booleans: P(yes); choice/score: P(Value)
	Confidence float64 `json:"confidence"`      // 0 when unknown
	Model      string  `json:"model,omitempty"` // e.g. "typesafe-ai/jev"
	Overridden *string `json:"overridden,omitempty"`
	Score      float64 `json:"score,omitempty"` // interpolated 1-based score for Score questions
	// Probs is the full distribution: choice option → p, or 1-based score
	// rung ("1".."5") → p. Empty for booleans and unknown labels.
	Probs map[string]float64 `json:"probs,omitempty"`
}

// LabelUnknown is the value used when Jev is unreachable (spec: failure handling).
const LabelUnknown = "unknown"

// LinkKind is the relation between two units.
type LinkKind string

// Link kinds.
const (
	LinkCalls         LinkKind = "calls"
	LinkTests         LinkKind = "tests"
	LinkImports       LinkKind = "imports"
	LinkMoved         LinkKind = "moved"
	LinkSameSymbol    LinkKind = "same-symbol"
	LinkEmbedNeighbor LinkKind = "embed-neighbour"
	LinkMechanical    LinkKind = "mechanical"
)

// LinkSource says which stage produced a link.
type LinkSource string

// Link sources.
const (
	LinkStructural LinkSource = "structural"
	LinkEmbed      LinkSource = "embed"
	LinkJev        LinkSource = "jev"
)

// Link connects two units.
type Link struct {
	From     string     `json:"from"`
	To       string     `json:"to"`
	Kind     LinkKind   `json:"kind"`
	Weight   float64    `json:"weight"`
	Source   LinkSource `json:"source"`
	Relation string     `json:"relation,omitempty"` // Jev pair relation, when Source == jev
}

// Group is a set of related units reviewed together.
type Group struct {
	ID          string   `json:"id"` // hash of sorted unit ids, see GroupID
	ItemID      string   `json:"itemId"`
	UnitIDs     []string `json:"unitIds"`
	Name        string   `json:"name"`
	DescBullet  string   `json:"descBullet,omitempty"`
	Mechanical  bool     `json:"mechanical,omitempty"`
	ExampleUnit string   `json:"exampleUnit,omitempty"`
	Count       int      `json:"count,omitempty"`
	Order       int      `json:"order"`
	Ungrouped   bool     `json:"ungrouped,omitempty"`
}

// Tier is a review tier.
type Tier string

// Review tiers.
const (
	TierNone Tier = "none"
	TierFast Tier = "fast"
	TierDeep Tier = "deep"
)

// ReviewStatus is the lifecycle of a review row.
type ReviewStatus string

// Review statuses.
const (
	ReviewQueued      ReviewStatus = "queued"
	ReviewRunning     ReviewStatus = "running"
	ReviewDone        ReviewStatus = "done"
	ReviewFailed      ReviewStatus = "failed"
	ReviewSkipped     ReviewStatus = "skipped"
	ReviewStaleLabels ReviewStatus = "stale-labels"
)

// Severity of a review finding.
type Severity string

// Finding severities.
const (
	SevLow    Severity = "low"
	SevMedium Severity = "medium"
	SevHigh   Severity = "high"
)

// Finding is one review claim anchored to a unit and line.
type Finding struct {
	UnitID   string   `json:"unitId,omitempty"`
	Path     string   `json:"path"`
	Line     int      `json:"line"`
	Severity Severity `json:"severity"`
	Claim    string   `json:"claim"`
	Fix      string   `json:"fix,omitempty"`
}

// Review is the result of reviewing one group.
type Review struct {
	GroupID     string       `json:"groupId"`
	ItemID      string       `json:"itemId"`
	Tier        Tier         `json:"tier"`
	Rule        string       `json:"rule,omitempty"` // the routing rule that fired, verbatim
	Prompt      string       `json:"prompt,omitempty"`
	Context     []string     `json:"context,omitempty"` // callers, types, tests, config, code_under_test
	Prompts     []string     `json:"prompts,omitempty"` // specialist prompts appended
	Findings    []Finding    `json:"findings,omitempty"`
	Verdict     string       `json:"verdict,omitempty"`
	Confidence  float64      `json:"confidence"`
	Status      ReviewStatus `json:"status"`
	EscalatedOf *Tier        `json:"escalatedOf,omitempty"` // set when fast → deep
	FastConf    float64      `json:"fastConf,omitempty"`
	Calibration bool         `json:"calibration,omitempty"` // sampled double review
	Ms          int64        `json:"ms"`
	Cost        float64      `json:"cost"`
	Err         string       `json:"err,omitempty"`
	Attempts    int          `json:"attempts,omitempty"`
}

// Side is the side of a diff a comment is on.
type Side string

// Diff sides (GitHub: LEFT/RIGHT).
const (
	SideOld Side = "old"
	SideNew Side = "new"
)

// CommentSource says where a comment came from.
type CommentSource string

// Comment sources.
const (
	CommentUser    CommentSource = "user"
	CommentAIDraft CommentSource = "ai-draft"
	CommentGitHub  CommentSource = "github"
)

// CommentState is a comment's lifecycle.
type CommentState string

// Comment states.
const (
	CommentDraft    CommentState = "draft"
	CommentPending  CommentState = "pending"
	CommentPosted   CommentState = "posted"
	CommentResolved CommentState = "resolved"
)

// Comment is a review comment, local draft or GitHub thread entry.
type Comment struct {
	ID        string        `json:"id"`
	ItemID    string        `json:"itemId"`
	UnitID    string        `json:"unitId,omitempty"`
	Path      string        `json:"path"`
	Line      int           `json:"line"`
	StartLine int           `json:"startLine,omitempty"` // multi-line selection
	Side      Side          `json:"side"`
	Body      string        `json:"body"`
	Author    string        `json:"author,omitempty"`
	Source    CommentSource `json:"source"`
	State     CommentState  `json:"state"`
	GHID      int64         `json:"ghId,omitempty"`
	ThreadID  string        `json:"threadId,omitempty"` // GraphQL thread node id
	InReplyTo int64         `json:"inReplyTo,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
}

// Override is a user correction of a label.
type Override struct {
	UnitID   string    `json:"unitId"`
	Question string    `json:"question"`
	Value    string    `json:"value"`
	At       time.Time `json:"at"`
}

// Stage identifies a pipeline stage.
type Stage string

// Pipeline stages, in order (fetch is stage 0).
const (
	StageFetch     Stage = "fetch"
	StageParse     Stage = "parse"
	StageLink      Stage = "link"
	StageEmbed     Stage = "embed"
	StageJev       Stage = "jev"
	StageCluster   Stage = "cluster"
	StageRoute     Stage = "route"
	StageReview    Stage = "review"
	StageAggregate Stage = "aggregate"
)

// Stages lists all stages in execution order.
var Stages = []Stage{
	StageFetch, StageParse, StageLink, StageEmbed, StageJev,
	StageCluster, StageRoute, StageReview, StageAggregate,
}

// StageStatus is the state of a stage for one item.
type StageStatus string

// Stage statuses.
const (
	StageQueued  StageStatus = "queued"
	StageRunning StageStatus = "running"
	StageDone    StageStatus = "done"
	StageFailed  StageStatus = "failed"
	StageCached  StageStatus = "skipped-cache"
	StageSkipped StageStatus = "skipped"
)

// RunLog records one stage execution (what the Pipeline page shows).
type RunLog struct {
	ItemID    string      `json:"itemId"`
	Stage     Stage       `json:"stage"`
	Status    StageStatus `json:"status"`
	StartedAt time.Time   `json:"startedAt"`
	Ms        int64       `json:"ms"`
	Calls     int         `json:"calls"`
	Cost      float64     `json:"cost"`
	CacheHit  bool        `json:"cacheHit"`
	In        int         `json:"in,omitempty"`  // units in
	Out       int         `json:"out,omitempty"` // units/links/groups out
	Note      string      `json:"note,omitempty"`
	Err       string      `json:"err,omitempty"`
}

// Fetch is the cached result of stage 0 for one head SHA.
type Fetch struct {
	ItemID   string    `json:"itemId"`
	HeadSHA  string    `json:"headSha"`
	BaseSHA  string    `json:"baseSha"`
	Diff     string    `json:"diff"` // unified diff against the merge base
	Item     Item      `json:"item"`
	Comments []Comment `json:"comments,omitempty"`
	At       time.Time `json:"at"`
}
