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
	// SourcePreview marks an item opened with `tab` on a search result
	// (spec Picker: "previews its overview without adding"). It is analysed
	// and served by Detail like any item but never listed by Items(); `enter`
	// on the same result turns it into SourceAdded, and the daemon removes
	// leftover previews (another preview, Remove, or start-up). M5.
	SourcePreview ItemSource = "preview"
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
	// StateNotAnalysed (M5.1): only the free stages (fetch, parse, link) ran
	// because the item is outside the [pipeline] auto_review strategy and
	// nobody asked for it; the picker and Overview show "not analysed · r to
	// run". StateNote is "".
	StateNotAnalysed ItemState = "not-analysed"
	// StateStopped (M5.1): the user stopped the item's analysis (stop / stop
	// all). StateNote is the stage it stopped at ("jev"); the picker shows
	// "stopped at <stage> · r to resume". Nothing restarts it automatically.
	StateStopped ItemState = "stopped"
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
	// PRState is the PR's GitHub state ("open", "closed", "merged"; "" when
	// unknown, read as open). Closed and merged PRs reach the picker only
	// when added from search.
	PRState string `json:"prState,omitempty"`
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
	// Relevance is the urgency relevance term's value (max interpolated
	// relevance over non-noise units, 0 unknown), copied by the pipeline for
	// the picker's relevance sort.
	Relevance float64 `json:"relevance,omitempty"`

	// CapUSD is this item's review cost cap set with `$` on the Pipeline
	// page (or `:cap`); 0 means prt.routing.toml cost_cap_usd.
	CapUSD float64 `json:"capUsd,omitempty"`

	// ViewerReviewedSHA is the commit the viewer's latest submitted GitHub
	// review was made on (gh.PR.ViewerReviewedSHA; "" for branches or when
	// the viewer never reviewed). "Since you looked" falls back to it when
	// prt has no ViewMark for the item (M4).
	ViewerReviewedSHA string `json:"viewerReviewedSha,omitempty"`
	// ViewerReviewedAt is when that review was submitted (zero when
	// unknown); comments.SinceBase prefers the review over an older view
	// mark.
	ViewerReviewedAt time.Time `json:"viewerReviewedAt,omitzero"`
	// UnitsSHA is the head the stored units, and so the line numbers of the
	// item's local drafts, describe. Stage 1 sets it when it stores units
	// and re-anchors the comments, which is before AnalysedSHA moves (at the
	// end of the run); a review is posted on this commit.
	UnitsSHA string `json:"unitsSha,omitempty"`

	// Branch facts (M5; branches only). Upstream is the branch's upstream
	// ref ("origin/feat/x"; "" when it has none) and LastCommitAt the
	// commit date of its head, both set by discovery from gitx.Ref.
	// BranchState and BranchStateP are copied from ItemAgg.Branch by the
	// pipeline for the picker's Branches line ("ready for PR .81"); the
	// state is one of the Branch* constants, "" before analysis.
	Upstream string `json:"upstream,omitempty"`
	// RemoteRef marks a remote-tracking ref added from search
	// ("origin/feat/y"): its commits are on the remote, so none is unpushed.
	RemoteRef    bool      `json:"remoteRef,omitempty"`
	LastCommitAt time.Time `json:"lastCommitAt,omitzero"`
	BranchState  string    `json:"branchState,omitempty"`
	BranchStateP float64   `json:"branchStateP,omitempty"`

	// RequestedSHA (M5.1) is the head the user explicitly asked to analyse
	// (r, :resume, enter on a search result, :add). While it equals HeadSHA
	// the item may run its model-backed stages whatever the auto_review
	// strategy (see AutoReview.Allows), so an explicit run interrupted by a
	// restart resumes. A new push makes it stale again: the next run of the
	// new head needs a new request (or strategy coverage). Set by the daemon.
	RequestedSHA string `json:"requestedSha,omitempty"`

	// Repo is the workspace repo key of the item (M5.3, multi-repo; the
	// prefix of its ID, see RepoItemID). Only the workspace client sets it,
	// on the way out: per-repo stores never hold it, and it is "" in
	// single-repo mode.
	Repo string `json:"repo,omitempty"`
}

// Branch states: the options of the catalogue's branch-scope question
// "state" (spec Jev catalogue: WIP, ready for PR, spike, abandoned,
// superseded). The UI shows BranchAbandoned as "abandoned?" (a guess).
const (
	BranchWIP        = "WIP"
	BranchReady      = "ready for PR"
	BranchSpike      = "spike"
	BranchAbandoned  = "abandoned"
	BranchSuperseded = "superseded"
)

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
	// Overridden is set (to the override) when the user's override replaced
	// the answer; Value then holds the override and Original Jev's answer
	// ("" when no answer was stored).
	Overridden *string `json:"overridden,omitempty"`
	Original   string  `json:"original,omitempty"`
	Score      float64 `json:"score,omitempty"` // interpolated 1-based score for Score questions
	// Probs is the full distribution: choice option → p, or 1-based score
	// rung ("1".."5") → p. Empty for booleans and unknown labels.
	Probs map[string]float64 `json:"probs,omitempty"`
	// Ctx is the hash of the item-level inputs the question was asked with
	// (PR title and bullets, reviewer role and globs, changed tests; see
	// labels.InputHash), "" when the answer depends on the unit alone. A
	// stored label whose Ctx differs from the current item's is stale.
	Ctx string `json:"ctx,omitempty"`
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

// Link connects two units. Direction conventions (M2, see DECISIONS "M2
// shared contracts"):
//   - calls: From = the calling unit, To = the unit that defines/changes
//     the called symbol;
//   - tests: From = the test unit, To = the code-under-test unit;
//   - imports: From = the importing unit, To = the unit defining the
//     imported symbol;
//   - moved: From = the origin (deleted) unit, To = the destination unit;
//   - same-symbol, embed-neighbour: From = the unit with the lower Order;
//   - mechanical: From = the example unit (lowest Order of its shape class).
//
// Weight is 1 for structural links and the cosine similarity for embed
// candidates; a Jev-confirmed link keeps the candidate's Kind and Weight and
// adds Source jev, Relation and P.
type Link struct {
	From     string     `json:"from"`
	To       string     `json:"to"`
	Kind     LinkKind   `json:"kind"`
	Weight   float64    `json:"weight"`
	Source   LinkSource `json:"source"`
	Relation string     `json:"relation,omitempty"` // Jev pair relation, when Source == jev
	P        float64    `json:"p,omitempty"`        // Jev probability of Relation, when Source == jev
}

// Jev pair relations (catalogue question "relation"; the spec's stage-4
// wording "same mechanical pattern" is the catalogue's "same pattern").
const (
	RelSameIntent   = "same intent"
	RelCallerUpdate = "caller update"
	RelTestFor      = "test for"
	RelSamePattern  = "same pattern"
	RelUnrelated    = "unrelated"
)

// Group name sources (Group.NameSource).
const (
	// NameBullet: Jev name_bullet chose a description bullet; Name and
	// DescBullet hold the bullet text.
	NameBullet = "bullet"
	// NamePlaceholder: no bullet (Jev chose none, Jev unavailable, the PR
	// has no bullets, or an ungrouped unit); Name is a code-derived
	// placeholder until the fast-LLM title lands (M3).
	NamePlaceholder = "placeholder"
	// NameAI: the fast LLM wrote a three-word title for a group Jev could
	// not name from a bullet (M3); shown marked as model-written.
	NameAI = "ai"
)

// Group is a set of related units reviewed together.
type Group struct {
	ID      string   `json:"id"` // hash of sorted unit ids, see GroupID
	ItemID  string   `json:"itemId"`
	UnitIDs []string `json:"unitIds"` // in review order (interface, callers, tests)
	Name    string   `json:"name"`
	// NameSource is NameBullet or NamePlaceholder; NameP is the Jev
	// probability of the chosen bullet (0 for placeholders).
	NameSource  string  `json:"nameSource,omitempty"`
	NameP       float64 `json:"nameP,omitempty"`
	DescBullet  string  `json:"descBullet,omitempty"` // bullet text when NameSource == NameBullet
	Mechanical  bool    `json:"mechanical,omitempty"`
	ExampleUnit string  `json:"exampleUnit,omitempty"`
	Count       int     `json:"count,omitempty"` // mechanical: verified members incl. the example
	Order       int     `json:"order"`
	// Ungrouped marks a single unit that no kept link binds to anything;
	// such groups are listed last, under "ungrouped".
	Ungrouped bool `json:"ungrouped,omitempty"`
	// Links are the links that bind the group ("why these belong
	// together"): structural, Jev-confirmed and, for mechanical groups,
	// the verified mechanical links. Cut and rejected links are not listed.
	Links []Link `json:"links,omitempty"`
	// Ejected lists units whose shape hash did not match the mechanical
	// example; they were promoted out of this group.
	Ejected []string `json:"ejected,omitempty"`
	// UserEdited is set when a user merge or split (GroupEdit) shaped it.
	UserEdited bool `json:"userEdited,omitempty"`
}

// GroupEditOp is a user edit of the grouping (Code page by change).
type GroupEditOp string

// Group edit operations.
const (
	// EditMerge puts Units (the union of the merged groups' units) into one
	// group, taking them out of whatever groups they were in.
	EditMerge GroupEditOp = "merge"
	// EditSplit takes Units out of their group into a new group of their own.
	EditSplit GroupEditOp = "split"
	// EditReset drops every earlier edit of the item.
	EditReset GroupEditOp = "reset"
)

// GroupEdit is one persisted user edit. Edits are keyed by unit ids, not
// group ids, so they survive re-clustering; cluster applies them in order
// after computing groups, ignoring unit ids no longer in the item.
type GroupEdit struct {
	ItemID string      `json:"itemId"`
	Op     GroupEditOp `json:"op"`
	Units  []string    `json:"units,omitempty"`
	At     time.Time   `json:"at"`
}

// GroupingStats is the Pipeline page's grouping panel: units → links
// (structural by kind, embed candidates, Jev kept by relation) → groups, and
// the mechanical collapse count. Filled by the pipeline's cluster stage.
type GroupingStats struct {
	Units                int              `json:"units"`
	Structural           map[LinkKind]int `json:"structural"` // structural links by kind
	EmbedCandidates      int              `json:"embedCandidates"`
	MechanicalCandidates int              `json:"mechanicalCandidates"`
	PairsAsked           int              `json:"pairsAsked"` // candidates judged by Jev (cached or not)
	JevKept              map[string]int   `json:"jevKept"`    // relation → confirmed links
	JevRejected          int              `json:"jevRejected"`
	CutLinks             int              `json:"cutLinks"` // embed links cut to respect max_group_units
	Groups               int              `json:"groups"`   // excluding ungrouped singletons
	Mechanical           int              `json:"mechanical"`
	// MechanicalCollapsed counts units hidden behind "×N verified
	// identical" (sum of Count−1 over mechanical groups).
	MechanicalCollapsed int `json:"mechanicalCollapsed"`
	Ejected             int `json:"ejected"`
	Ungrouped           int `json:"ungrouped"`
	UserEdits           int `json:"userEdits"`
	// EmbedNote says why stage 3 was skipped ("" when it ran).
	EmbedNote string `json:"embedNote,omitempty"`
}

// SplitFile is one file the separate_pr question says should be its own PR.
type SplitFile struct {
	Path     string   `json:"path"`
	P        float64  `json:"p"`        // P(yes) of separate_pr
	GroupIDs []string `json:"groupIds"` // groups with units in this file
}

// SplitSuggestion is the Code page's split suggestion (by change mode):
// files with separate_pr P >= 0.5, highest P first. Nil when none.
type SplitSuggestion struct {
	Files []SplitFile `json:"files"`
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
	// ReviewStopped (M5.1): the call was in flight (or queued in this run)
	// when the user stopped the item; not a failure, not retried, re-run on
	// resume. Never cached as a finished review.
	ReviewStopped ReviewStatus = "stopped"
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
	UnitID string `json:"unitId,omitempty"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	// Side is the diff side Line counts on: SideOld for a deleted line,
	// SideNew (or "", which means new) otherwise (M4; DECISIONS "M4 shared
	// contracts").
	Side     Side     `json:"side,omitempty"`
	Severity Severity `json:"severity"`
	Claim    string   `json:"claim"`
	Fix      string   `json:"fix,omitempty"`
}

// Review is the result of reviewing one group.
//
// Two uses (DECISIONS "M3 shared contracts"):
//   - the review cache: Store.PutReview/Review keyed by (GroupID, Tier),
//     one row per tier that actually ran, reused while group membership is
//     unchanged ("cached by sorted unit-id list");
//   - the routing table: Store.PutItemReviews/ItemReviews, exactly one row
//     per current group of the item in its final state (Tier = final tier,
//     so an escalated group has Tier deep, EscalatedOf fast and FastConf).
//     proto.Detail.Reviews carries these rows.
type Review struct {
	GroupID string `json:"groupId"`
	ItemID  string `json:"itemId"`
	// GroupName is the group's name when the row was written (display only).
	GroupName string `json:"groupName,omitempty"`
	Tier      Tier   `json:"tier"`
	Rule      string `json:"rule,omitempty"` // the routing rule's `when` text that fired, verbatim
	// RuleIndex is the 1-based index of the rule that fired in
	// prt.routing.toml; 0 when no rule decided (labels unknown → deep,
	// forced with D/R, skipped with s).
	RuleIndex int `json:"ruleIndex,omitempty"`
	// RuleWords is the decision in words for the routing table
	// ("if risk >= 4 || tag.auth → deep", "labels unknown → deep",
	// "forced deep (D)", "skipped by user (s)").
	RuleWords string `json:"ruleWords,omitempty"`
	// Prompt names the base prompt template ("review/v1"); the full prompt
	// text lives in the call log (ReviewCall), not here.
	Prompt string `json:"prompt,omitempty"`
	// Context lists the context kinds (callers, types, tests, config,
	// code_under_test) whose blocks were attached to the review request;
	// ContextNone the kinds routing decided on that found nothing to attach
	// (e.g. callers of a breaking unit when no caller changed in this PR).
	Context     []string  `json:"context,omitempty"`
	ContextNone []string  `json:"contextNone,omitempty"`
	Prompts     []string  `json:"prompts,omitempty"` // specialist prompts appended
	Findings    []Finding `json:"findings,omitempty"`
	// UnitStarts maps each unit id of the group to the first line of its
	// anchor span (new side; old side for a pure deletion) when this review
	// ran. A cached review served after the unit moved (push, rebase,
	// stacked PRs) shifts its findings' lines by the difference. A key
	// "<unitID>@old" holds the start of that unit's old span, used to shift
	// old-side findings (Finding.Side old); reviews cached before M4 lack
	// it and fall back to the anchor start.
	UnitStarts  map[string]int `json:"unitStarts,omitempty"`
	Verdict     string         `json:"verdict,omitempty"`
	Confidence  float64        `json:"confidence"`
	Model       string         `json:"model,omitempty"` // "provider:model" that answered
	Status      ReviewStatus   `json:"status"`
	EscalatedOf *Tier          `json:"escalatedOf,omitempty"` // set when fast → deep
	FastConf    float64        `json:"fastConf,omitempty"`
	// Calibration is set when this fast-routed group was sampled for a
	// double review (calibration_sample) and the deep run finished.
	Calibration *Calibration `json:"calibration,omitempty"`
	// Forced is set by D (Pipeline page) or R (Code page): deep regardless
	// of rules. UserSkipped is set by s: tier none, logged.
	Forced      bool `json:"forced,omitempty"`
	UserSkipped bool `json:"userSkipped,omitempty"`
	// QueueNote says why a row is still queued ("over cost cap: est $0.14,
	// $0.05 of $0.40 left") or skipped ("no PRT_ANTHROPIC_API_KEY").
	QueueNote string `json:"queueNote,omitempty"`
	// HotSpot is the group's queue key (max unit hot-spot score).
	HotSpot      float64   `json:"hotSpot,omitempty"`
	EstCost      float64   `json:"estCost,omitempty"` // pre-call upper-bound estimate (USD)
	StartedAt    time.Time `json:"startedAt,omitempty"`
	InputTokens  int       `json:"inputTokens,omitempty"`
	OutputTokens int       `json:"outputTokens,omitempty"`
	Ms           int64     `json:"ms"`
	Cost         float64   `json:"cost"` // all calls for this row (routed + escalation + calibration)
	Err          string    `json:"err,omitempty"`
	Attempts     int       `json:"attempts,omitempty"`
	// StaleNote says which label overrides made a finished review
	// ReviewStaleLabels (spec: "An override invalidates aggregate only, and
	// marks the affected review as stale-labels without re-running it"),
	// e.g. "label overridden: risk 4→2 (web/src/a.ts:12)". Set together
	// with Status ReviewStaleLabels; findings and verdict are kept. M5.
	StaleNote string `json:"staleNote,omitempty"`
}

// Calibration is the sampled deep double review of a fast-routed group
// (spec stage 7; Ranking "Calibration metrics").
type Calibration struct {
	Findings   []Finding `json:"findings,omitempty"` // deep findings
	Verdict    string    `json:"verdict,omitempty"`
	Confidence float64   `json:"confidence"`
	// Missed counts deep findings of severity ≥ medium that the fast review
	// did not report (same unit and line ±3, or same path when unanchored);
	// Agree is Missed == 0.
	Missed int     `json:"missed"`
	Agree  bool    `json:"agree"`
	Cost   float64 `json:"cost"`
	Ms     int64   `json:"ms"`
}

// CalibrationSample is one stored double review, kept across items for the
// trailing-30-day fast-tier miss rate (Store.AppendCalibration).
type CalibrationSample struct {
	ItemID  string    `json:"itemId"`
	GroupID string    `json:"groupId"`
	At      time.Time `json:"at"`
	Missed  int       `json:"missed"`
	Agree   bool      `json:"agree"`
}

// Context kinds a routing rule may attach (`context = [...]`).
const (
	CtxCallers       = "callers"
	CtxTypes         = "types"
	CtxTests         = "tests"
	CtxConfig        = "config"
	CtxCodeUnderTest = "code_under_test"
)

// Specialist prompts a routing rule may append (`prompts = [...]`).
const (
	PromptSecurity    = "security"
	PromptPerformance = "performance"
	PromptTests       = "tests"
)

// ContextBlock is one rendered piece of review context (route.Gather):
// a caller, a type/signature, a test, a config unit or the code under test.
type ContextBlock struct {
	Kind   string `json:"kind"`             // one of the Ctx* kinds
	Title  string `json:"title"`            // e.g. "caller web/src/ProfilePage.tsx ProfilePage"
	UnitID string `json:"unitId,omitempty"` // the unit it was taken from, if any
	Body   string `json:"body"`
}

// ReviewCall is one raw LLM call, for the Pipeline page's call log (`c`):
// prompt, context, response and tokens.
type ReviewCall struct {
	ItemID  string `json:"itemId"`
	GroupID string `json:"groupId,omitempty"` // "" for the TL;DR
	Task    string `json:"task"`              // review | tldr | group-title
	Tier    Tier   `json:"tier"`
	// Purpose is routed | escalation | calibration | retry | tldr | title.
	Purpose      string    `json:"purpose"`
	Model        string    `json:"model"`
	System       string    `json:"system"`
	Prompt       string    `json:"prompt"`
	Context      []string  `json:"context,omitempty"`
	Response     string    `json:"response,omitempty"`
	InputTokens  int       `json:"inputTokens"`
	OutputTokens int       `json:"outputTokens"`
	Cost         float64   `json:"cost"`
	Ms           int64     `json:"ms"`
	Cached       bool      `json:"cached,omitempty"`
	Err          string    `json:"err,omitempty"`
	At           time.Time `json:"at"`
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
//
// Anchoring (spec "Identity rules"; DECISIONS "M4 shared contracts"): a
// comment anchors to Path + Line + Side as GitHub requires (a range adds
// StartLine/StartSide), and additionally to UnitID with UnitOffset = Line −
// the unit's first line on Side, so comments.Reanchor can move it when a
// push or rebase shifts the unit. Outdated is set when neither the unit nor
// the path/line can be found in the current analysis.
//
// Lifecycle by Source:
//   - user: State draft (local, editable) → pending (being submitted) →
//     posted (GHID set) ; drafts survive restarts (Store.PutComment).
//   - ai-draft: one per review finding not yet accepted or dismissed
//     (FindingKey set, Severity copied); `a` turns it into a user draft
//     with the same FindingKey, `d`/`!` dismiss it (Store.PutFindingMark).
//   - github: loaded from review threads (ThreadID, GHID, CommitSHA);
//     State posted, or resolved when the thread is resolved.
type Comment struct {
	ID        string        `json:"id"`
	ItemID    string        `json:"itemId"`
	UnitID    string        `json:"unitId,omitempty"`
	Path      string        `json:"path"`
	Line      int           `json:"line"`
	StartLine int           `json:"startLine,omitempty"` // multi-line selection
	Side      Side          `json:"side"`
	StartSide Side          `json:"startSide,omitempty"` // side of StartLine ("" = Side)
	Body      string        `json:"body"`
	Author    string        `json:"author,omitempty"`
	Source    CommentSource `json:"source"`
	State     CommentState  `json:"state"`
	GHID      int64         `json:"ghId,omitempty"`
	ThreadID  string        `json:"threadId,omitempty"` // GraphQL thread node id
	InReplyTo int64         `json:"inReplyTo,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt,omitempty"`

	// UnitOffset is Line minus the first line of UnitID's span on Side
	// (valid when UnitID is set).
	UnitOffset int `json:"unitOffset,omitempty"`
	// StartOffset is StartLine minus the first line of UnitID's span on the
	// start side (StartSide, "" = Side); set for ranges (StartLine > 0) when
	// UnitID is set. Re-anchoring uses it for ranges whose ends lie on
	// different sides (the two spans move independently).
	StartOffset int `json:"startOffset,omitempty"`
	// Outdated is set when the anchor no longer exists in the analysed diff.
	Outdated bool `json:"outdated,omitempty"`
	// CommitSHA is the commit the comment was made on (GitHub
	// originalCommit; for local drafts the head their Line describes:
	// Item.UnitsSHA when saved, moved with them by re-anchoring).
	CommitSHA string `json:"commitSha,omitempty"`
	// OriginalLine is a GitHub comment's line on CommitSHA (the thread's
	// originalLine; Line is on the PR's current head). 0 when unknown or
	// for local comments, whose Line is already on CommitSHA.
	OriginalLine int `json:"originalLine,omitempty"`
	// FindingKey ties an ai-draft (and the user draft accepted from it) to
	// its review finding (comments.FindingKey).
	FindingKey string `json:"findingKey,omitempty"`
	// Severity is the finding's severity (ai-drafts only).
	Severity Severity `json:"severity,omitempty"`
	// ReviewID is the GitHub review the comment was posted in (posted only).
	ReviewID int64 `json:"reviewId,omitempty"`
}

// FindingMarkState is what the user did with an AI finding.
type FindingMarkState string

// Finding mark states.
const (
	FindingAccepted  FindingMarkState = "accepted"
	FindingDismissed FindingMarkState = "dismissed"
)

// FindingMark records that an ai-draft was accepted or dismissed, keyed by
// item and FindingKey, so the pipeline never re-creates it on a re-run
// (Store.PutFindingMark/FindingMarks).
type FindingMark struct {
	ItemID     string           `json:"itemId"`
	FindingKey string           `json:"findingKey"`
	State      FindingMarkState `json:"state"`
	// CommentID is the user draft an accept created ("" for dismissals).
	CommentID string `json:"commentId,omitempty"`
	// Override is set when `!` dismissed the finding and filed a label
	// override.
	Override *Override `json:"override,omitempty"`
	At       time.Time `json:"at"`
}

// ViewMark records which head of an item the user last looked at
// (Store.PutViewMark/ViewMark). PrevHeadSHA is the head looked at before
// HeadSHA (the last distinct one), so "Since you looked" stays stable while
// the user reads the current head: its base is PrevHeadSHA when HeadSHA is
// the item's current head, else HeadSHA (comments.SinceBase).
type ViewMark struct {
	ItemID      string    `json:"itemId"`
	HeadSHA     string    `json:"headSha"`
	PrevHeadSHA string    `json:"prevHeadSha,omitempty"`
	At          time.Time `json:"at"` // when HeadSHA was first looked at
	// PrevAt is when PrevHeadSHA was first looked at (zero when unknown).
	PrevAt time.Time `json:"prevAt,omitzero"`
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
	// StageStopped (M5.1): the user stopped the run while this stage was
	// running; its output was not stored. Resume re-runs it (earlier stages
	// hit their caches).
	StageStopped StageStatus = "stopped"
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
