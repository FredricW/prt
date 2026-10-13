// Package proto is the daemon ↔ TUI wire protocol: newline-delimited JSON
// over a Unix socket (.git/prt/prtd.sock for one repo; for a workspace,
// M5.3, $XDG_STATE_HOME/prt/ws-<hash>/prtd.sock, see
// daemon.WorkspaceSocketPath). Every line is one Message.
//
// Client → daemon: Kind "req" with ID, Method and Params.
// Daemon → client: Kind "resp" with the same ID and Result or Error, and
// unsolicited Kind "event" messages (patches) after a "subscribe" request.
//
// The protocol is the language-neutral boundary between stages and screens;
// it is inspectable with `nc -U .git/prt/prtd.sock`.
package proto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

// Kind is a message kind.
type Kind string

// Message kinds.
const (
	KindReq   Kind = "req"
	KindResp  Kind = "resp"
	KindEvent Kind = "event"
)

// Methods.
const (
	MethodDiscover = "discover" // {} → {}; refresh picker sources
	MethodItems    = "items"    // → []model.Item
	MethodCheckout = "checkout" // CheckoutParams → CheckoutResult
	MethodDetail   = "detail"   // DetailParams ({itemId} = everything) → Detail
	// MethodLabels returns an item's labels, overrides applied, without the
	// rest of its Detail (M5.2): LabelsParams → []model.Label. The TUI loads
	// Details without labels and fetches them when `L` or `!` need them.
	MethodLabels    = "labels"
	MethodRefresh   = "refresh"   // {itemId, fromStage?, force?} → {}
	MethodAdd       = "add"       // {query} → model.Item
	MethodRemove    = "remove"    // {itemId} → {}
	MethodSubscribe = "subscribe" // {} → {}; then events
	// MethodOverride stores a label override (Code page `!`) and re-runs
	// the item's aggregate: OverrideParams → {}.
	MethodOverride = "override"
	// MethodComment saves (upserts) or deletes a local draft comment:
	// CommentParams → model.Comment (the stored draft; zero after a delete).
	MethodComment = "comment"
	// MethodCommentAction accepts or dismisses an ai-draft, or replies to or
	// resolves a GitHub thread: CommentActionParams → model.Comment (the
	// accepted draft or the posted reply; zero otherwise).
	MethodCommentAction = "comment_action"
	// MethodSubmitReview submits the item's drafts as one GitHub review:
	// SubmitParams → SubmitResult.
	MethodSubmitReview = "submit_review"
	// MethodCreatePR creates a PR for a branch item and attaches its local
	// drafts: CreatePRParams → CreatePRResult.
	MethodCreatePR = "create_pr"
	// MethodViewed records that the user looked at an item's head
	// ("Since you looked"): ViewedParams → {}.
	MethodViewed = "viewed"
	// MethodEditGroups persists a user merge/split/reset of an item's
	// groups and re-runs the item (cached stages are skipped, so only
	// clustering re-runs): GroupEditParams → {}.
	MethodEditGroups = "edit_groups"
	// MethodReviewAction forces a group to the deep tier (Pipeline page D,
	// Code page R) or skips its review (Pipeline page s), then re-runs the
	// item from routing: ReviewActionParams → {}.
	MethodReviewAction = "review_action"
	// MethodRouting returns the active routing rules (and, with Ensure,
	// writes the built-in rules to .git/prt/prt.routing.toml when no file
	// exists; with Reload, re-reads the file and re-routes every item's
	// unreviewed groups): RoutingParams → RoutingInfo.
	MethodRouting = "routing"
	// MethodCallLog returns the raw LLM calls behind a routing-table row
	// (prompt, context, response, tokens): CallLogParams → []model.ReviewCall.
	MethodCallLog = "call_log"
	// MethodSetCap sets an item's review cost cap (Pipeline page $, `:cap`)
	// and re-runs groups queued over the old cap: CapParams → {}.
	MethodSetCap = "set_cap"
	// MethodSearch runs one source of the picker's search-and-add (`/`):
	// SearchParams → SearchResult. The TUI calls it once per source in
	// parallel and merges the results as they land (M5).
	MethodSearch = "search"
	// MethodPreview stores and analyses an item without listing it (`tab`
	// on a search result): AddParams → model.Item (Source preview). M5.
	MethodPreview = "preview"
	// MethodRemovePreview deletes an item only while it is still a preview
	// (the search prompt closed): ItemParams → {}. An item that is listed
	// (ranked, discovered or added) is left alone. M5.
	MethodRemovePreview = "remove_preview"
	// MethodStop stops analysis (M5.1): StopParams → StopResult. In-flight
	// stages are cancelled (RunLog rows and routing rows "stopped"), queued
	// jobs dropped, items left StateStopped ("stopped at <stage>").
	MethodStop = "stop"
	// MethodResume resumes stopped (or not-analysed) items from their last
	// completed stage; content-hash caches mean nothing finished is paid
	// again (M5.1): ResumeParams → ResumeResult. It is an explicit request,
	// so the auto_review strategy does not gate it.
	MethodResume = "resume"
	// MethodSettings returns the editable settings, where they are saved and
	// the environment status (M5.1): SettingsParams → SettingsInfo.
	MethodSettings = "settings"
	// MethodSaveSettings writes the settings to the chosen scope's prt.toml
	// (and prt.routing.toml for cost cap and deep slots), reloads the
	// configuration in the daemon and applies it live (M5.1):
	// SaveSettingsParams → SettingsInfo (after the reload).
	MethodSaveSettings = "save_settings"
	// MethodWorkspace describes what the daemon serves (M5.3): {} →
	// WorkspaceInfo. A single-repo daemon answers with one repo and Multi
	// false; a workspace daemon (`prt daemon --root DIR`) with every repo
	// it discovered under DIR.
	MethodWorkspace = "workspace"
)

// RepoStatus is a workspace repo's GitHub status (the picker's repo header,
// M5.3).
type RepoStatus string

// Repo statuses.
const (
	// RepoGitHub: the GitHub repo is known (RepoInfo.GHRepo, from
	// [github] repo or the origin remote) and a client is wired.
	RepoGitHub RepoStatus = "github"
	// RepoGitHubDisabled: the repo is on GitHub but no client is wired (no
	// token from `gh auth token`, --offline); RepoInfo.Note says why. Local
	// branches only.
	RepoGitHubDisabled RepoStatus = "github-disabled"
	// RepoNotGitHub: origin is not a GitHub remote. Local branches only.
	RepoNotGitHub RepoStatus = "not-github"
	// RepoNoRemote: the repo has no origin remote. Local branches only.
	RepoNoRemote RepoStatus = "no-remote"
	// RepoError: the repo could not be opened (store, config); Note says
	// why. It has no items.
	RepoError RepoStatus = "error"
)

// RepoInfo is one repository of a workspace (M5.3).
type RepoInfo struct {
	// Key is the repo's workspace key: its path relative to the workspace
	// root (model.CleanRepoKey), the prefix of its items' ids
	// (model.RepoItemID) and the "repo" of Item.Repo, SearchParams.Repo and
	// SettingsParams.Repo. "" in single-repo mode.
	Key string `json:"key"`
	// Label is what the UI shows: GHRepo when known, else Key (else the
	// work tree's directory name).
	Label string `json:"label"`
	// Root is the work tree; GitDir its git dir (store, log and repo config
	// live under GitDir/prt).
	Root   string `json:"root"`
	GitDir string `json:"gitDir,omitempty"`
	// GHRepo is "owner/name" when the repo is on GitHub.
	GHRepo string     `json:"ghRepo,omitempty"`
	Status RepoStatus `json:"status"`
	// Note explains a status that is not RepoGitHub ("no token from `gh
	// auth token`", an open error); "" otherwise.
	Note string `json:"note,omitempty"`
	// Role is the repo's [user] role (its own prt.toml, else the global
	// one), for the header while one of its items is selected.
	Role string `json:"role,omitempty"`
	// SpentUSD is the model spend of this repo's runs since the daemon
	// started (the shared ledger's per-repo total).
	SpentUSD float64 `json:"spentUsd,omitempty"`
}

// WorkspaceInfo is the result of MethodWorkspace (M5.3).
type WorkspaceInfo struct {
	// Multi is set for a workspace (`prt --root DIR`, or prt started
	// outside a git repo), even one with a single repo; the TUI then groups
	// the picker by repo. False in single-repo mode, where the TUI behaves
	// exactly as before M5.3.
	Multi bool `json:"multi,omitempty"`
	// Root is the workspace root (Multi) or the repo's work tree.
	Root string `json:"root"`
	// Repos are the repos in workspace order (Key order); one in
	// single-repo mode.
	Repos []RepoInfo `json:"repos"`
	// Concurrency is the shared worker pool's size (slots for analyses of
	// every repo together).
	Concurrency int `json:"concurrency,omitempty"`
	// SpentUSD is the model spend of every repo's runs since the daemon
	// started.
	SpentUSD float64 `json:"spentUsd,omitempty"`
	// Notes are discovery notes ("skipped 2 nested worktrees", "depth 2").
	Notes []string `json:"notes,omitempty"`
}

// StopParams is the params of MethodStop: ItemID's analysis, or with All
// every running and queued analysis (ItemID ignored).
type StopParams struct {
	ItemID string `json:"itemId,omitempty"`
	All    bool   `json:"all,omitempty"`
}

// StopResult lists the items that were running or queued and are now
// stopped (empty when there was nothing to stop: not an error).
type StopResult struct {
	Stopped []string `json:"stopped,omitempty"`
}

// ResumeParams is the params of MethodResume: ItemID, or with All every
// item in StateStopped.
type ResumeParams struct {
	ItemID string `json:"itemId,omitempty"`
	All    bool   `json:"all,omitempty"`
}

// ResumeResult lists the items queued again.
type ResumeResult struct {
	Resumed []string `json:"resumed,omitempty"`
}

// SettingsScope is where the settings page saves.
type SettingsScope string

// Settings scopes.
const (
	// ScopeRepo is <gitDir>/prt (the default).
	ScopeRepo SettingsScope = "repo"
	// ScopeGlobal is $XDG_CONFIG_HOME/prt (config.GlobalDir).
	ScopeGlobal SettingsScope = "global"
)

// SettingsValues are the settings page's fields (M5.1). Every field maps
// to one TOML key: prt.toml unless noted.
type SettingsValues struct {
	AutoReview string   `json:"autoReview"` // [pipeline] auto_review
	Role       string   `json:"role"`       // [user] role
	Owns       []string `json:"owns"`       // [user] owns
	CostCapUSD float64  `json:"costCapUsd"` // prt.routing.toml cost_cap_usd
	DeepSlots  int      `json:"deepSlots"`  // prt.routing.toml deep_slots
	Fast       string   `json:"fast"`       // [models] fast
	Deep       string   `json:"deep"`       // [models] deep
	Embed      string   `json:"embed"`      // [models] embed
	// Rank holds the urgency weights ([rank], every w_* key).
	Rank           config.Rank `json:"rank"`
	UnsureBelow    float64     `json:"unsureBelow"`    // [pipeline] unsure_below
	KeepPairP      float64     `json:"keepPairP"`      // [pipeline] keep_pair_p
	EmbedMinCosine float64     `json:"embedMinCosine"` // [pipeline] embed_min_cosine
	MaxGroupUnits  int         `json:"maxGroupUnits"`  // [pipeline] max_group_units
	Concurrency    int         `json:"concurrency"`    // [pipeline] concurrency
}

// Probe is the outcome of one environment check. Note is a short reason
// ("unreachable at 127.0.0.1:11434", "not set"); it never holds a secret.
type Probe struct {
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

// EnvStatus is what the settings page shows about the daemon's
// environment: presence only, never values.
type EnvStatus struct {
	JevKey       Probe `json:"jevKey"`       // JEV_API_KEY set
	AnthropicKey Probe `json:"anthropicKey"` // PRT_ANTHROPIC_API_KEY set
	Ollama       Probe `json:"ollama"`       // OLLAMA_HOST answers
	GitHub       Probe `json:"github"`       // gh auth token / client wired
}

// SettingsParams is the params of MethodSettings. Ensure names a file to
// create with built-in content when missing, so the page's "edit" actions
// always open a file: "routing" (prt.routing.toml) or "questions"
// (prt.questions.toml, a commented starter); "" for none.
type SettingsParams struct {
	Ensure string `json:"ensure,omitempty"`
	// Repo is the workspace repo key whose settings to read (M5.3; the
	// selected item's repo). "" in single-repo mode; in a workspace "" means
	// the first repo.
	Repo string `json:"repo,omitempty"`
}

// SettingsInfo is the result of MethodSettings and MethodSaveSettings.
type SettingsInfo struct {
	Values SettingsValues `json:"values"`
	// Source is the prt.toml the values were read from ("builtin" when
	// none); RoutingSource likewise for prt.routing.toml.
	Source        string `json:"source"`
	RoutingSource string `json:"routingSource"`
	// RepoDir and GlobalDir are the two scopes' directories ("" when the
	// global one is unknown).
	RepoDir   string `json:"repoDir"`
	GlobalDir string `json:"globalDir,omitempty"`
	// RoutingPath and QuestionsPath are the files the page's edit actions
	// open in $EDITOR (the active file, else the repo scope's path).
	RoutingPath   string    `json:"routingPath"`
	QuestionsPath string    `json:"questionsPath"`
	Env           EnvStatus `json:"env"`
	// Warnings are config warnings and save notes ("global prt.toml is
	// shadowed by .git/prt/prt.toml").
	Warnings []string `json:"warnings,omitempty"`
	// Applied says what a save changed live ("re-aggregated 4 items,
	// re-routed 2"); "" for MethodSettings.
	Applied string `json:"applied,omitempty"`
	// Repo and RepoLabel name the workspace repo these settings belong to
	// (M5.3; RepoInfo.Key and Label); Workspace is the workspace root. All
	// three are "" in single-repo mode. A repo-scope save writes that repo's
	// RepoDir; a global save writes GlobalDir and applies to every repo.
	Repo      string `json:"repo,omitempty"`
	RepoLabel string `json:"repoLabel,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// SaveSettingsParams is the params of MethodSaveSettings ("" Scope means
// ScopeRepo).
type SaveSettingsParams struct {
	Scope  SettingsScope  `json:"scope,omitempty"`
	Values SettingsValues `json:"values"`
	// Repo is the workspace repo key a repo-scope save writes to (M5.3; ""
	// in single-repo mode, the first repo in a workspace).
	Repo string `json:"repo,omitempty"`
}

// SearchSource is one of the search-and-add sources (spec Picker "Search
// and add").
type SearchSource string

// Search sources.
const (
	// SearchLocal: local refs whose name (or tip subject) contains the
	// query, from git for-each-ref (instant).
	SearchLocal SearchSource = "local"
	// SearchOpen: open PRs from GitHub search `repo:o/n is:pr is:open <q>`;
	// a bare number ("482" or "#482") returns exactly that PR, whatever its
	// state.
	SearchOpen SearchSource = "open"
	// SearchClosed: closed and merged PRs (`is:pr is:closed <q>`); the TUI
	// asks it when the query starts with "!" or the open source matched
	// nothing.
	SearchClosed SearchSource = "closed"
)

// SearchParams is the params of MethodSearch. Query is what the user typed
// (a leading "!" is stripped by the daemon); Limit 0 means 20.
type SearchParams struct {
	Query  string       `json:"query"`
	Source SearchSource `json:"source"`
	Limit  int          `json:"limit,omitempty"`
	// Repo limits a workspace search to one repo key (M5.3: the picker's
	// repo filter). "" searches every repo. A repo-qualified Query
	// ("acme/billing-svc#12", "billing-svc:feat/x") searches only the repo it
	// names, whatever Repo says. Ignored in single-repo mode.
	Repo string `json:"repo,omitempty"`
}

// SearchHit is one search result: kind, number or ref, title, author and
// age (spec: "Each result shows kind, number or ref, title, author and
// age"). Query is the exact string MethodAdd/MethodPreview accept for it
// ("#482" or the ref name); ItemID the id the item has or would get; Listed
// is set when that item is already in the picker.
type SearchHit struct {
	Source    SearchSource   `json:"source"`
	Kind      model.ItemKind `json:"kind"`
	Number    int            `json:"number,omitempty"`
	Ref       string         `json:"ref,omitempty"`
	Title     string         `json:"title"`
	Author    string         `json:"author,omitempty"`
	State     string         `json:"state,omitempty"` // open, closed, merged; "" for local refs
	UpdatedAt time.Time      `json:"updatedAt,omitzero"`
	Query     string         `json:"query"`
	ItemID    string         `json:"itemId"`
	Listed    bool           `json:"listed,omitempty"`
	// Repo is the hit's workspace repo key (M5.3; "" in single-repo mode).
	// In a workspace, Query is repo-qualified and ItemID carries the repo
	// prefix, so MethodAdd/MethodPreview are never ambiguous.
	Repo string `json:"repo,omitempty"`
}

// SearchResult is the result of MethodSearch. Note explains a source that
// could not run ("GitHub unavailable"); it is not an error.
type SearchResult struct {
	Source SearchSource `json:"source"`
	Query  string       `json:"query"`
	Hits   []SearchHit  `json:"hits,omitempty"`
	Note   string       `json:"note,omitempty"`
}

// ReviewAction is what MethodReviewAction does to a group.
type ReviewAction string

// Review actions.
const (
	// ActionDeep forces the group to the deep tier and re-reviews it there
	// once with a fresh call (a cached deep review is not served for it).
	ActionDeep ReviewAction = "deep"
	// ActionSkip skips the group's review (tier none, logged).
	ActionSkip ReviewAction = "skip"
	// ActionReset drops a previous deep/skip override (back to the rules).
	ActionReset ReviewAction = "reset"
)

// ReviewActionParams is the params of MethodReviewAction.
type ReviewActionParams struct {
	ItemID  string       `json:"itemId"`
	GroupID string       `json:"groupId"`
	Action  ReviewAction `json:"action"`
}

// RoutingParams is the params of MethodRouting.
type RoutingParams struct {
	// Ensure writes the built-in rules to Path when no routing file exists
	// yet, so `e` always has a file to open.
	Ensure bool `json:"ensure,omitempty"`
	// Reload re-reads the routing file and re-routes every analysed item's
	// groups that have no finished review.
	Reload bool `json:"reload,omitempty"`
	// Repo is the workspace repo key whose routing file this is (M5.3; the
	// shown item's repo). "" in single-repo mode; in a workspace "" means
	// the first repo.
	Repo string `json:"repo,omitempty"`
}

// RoutingRule is one rule of the active routing file, for the rules panel.
type RoutingRule struct {
	Index   int      `json:"index"` // 1-based
	When    string   `json:"when"`
	Tier    string   `json:"tier"`
	Prompts []string `json:"prompts,omitempty"`
	Context []string `json:"context,omitempty"`
	// Words is the rule rendered as "if … → tier" (route.Rule.Words).
	Words string `json:"words"`
	// Err is the parse error of When ("" when it compiled).
	Err string `json:"err,omitempty"`
}

// RoutingInfo is the result of MethodRouting and Detail.Routing.
type RoutingInfo struct {
	// Path is the file `e` edits: the active routing file, or
	// <gitDir>/prt/prt.routing.toml when the built-in rules are active.
	Path string `json:"path"`
	// Source is where the active rules came from (a path or "builtin").
	Source            string        `json:"source"`
	DeepSlots         int           `json:"deepSlots"`
	CostCapUSD        float64       `json:"costCapUsd"`
	EscalateBelow     float64       `json:"escalateBelow"`
	CalibrationSample float64       `json:"calibrationSample"`
	Rules             []RoutingRule `json:"rules"`
	Warnings          []string      `json:"warnings,omitempty"`
	// Err is set when a reload failed (syntax error, bad rule); the
	// previous rules stay active.
	Err string `json:"err,omitempty"`
	// Rerouted counts groups queued for re-routing by a Reload.
	Rerouted int `json:"rerouted,omitempty"`
}

// CallLogParams is the params of MethodCallLog. An empty GroupID returns
// the item's TL;DR calls.
type CallLogParams struct {
	ItemID  string `json:"itemId"`
	GroupID string `json:"groupId,omitempty"`
}

// CapParams is the params of MethodSetCap; CapUSD 0 restores the default.
type CapParams struct {
	ItemID string  `json:"itemId"`
	CapUSD float64 `json:"capUsd"`
}

// CommentParams is the params of MethodComment. Comment.ItemID must equal
// ItemID (the daemon fills it when empty). Save with Comment.ID "" creates
// a draft (the daemon assigns ID, Author, Source user, State draft,
// CommitSHA = the analysed head, CreatedAt, and anchors UnitID/UnitOffset);
// with an ID it updates that draft's body and anchor (only user drafts can
// be edited). Delete removes the draft Comment.ID.
type CommentParams struct {
	ItemID  string        `json:"itemId"`
	Comment model.Comment `json:"comment"`
	Delete  bool          `json:"delete,omitempty"`
}

// CommentAction is what MethodCommentAction does.
type CommentAction string

// Comment actions.
const (
	// CommentAccept turns the ai-draft CommentID into an editable user
	// draft (same FindingKey) and marks the finding accepted.
	CommentAccept CommentAction = "accept"
	// CommentDismiss removes the ai-draft CommentID and marks the finding
	// dismissed; with Override set (`!`) it also stores that label override.
	CommentDismiss CommentAction = "dismiss"
	// CommentReply posts Body as a reply to thread ThreadID.
	CommentReply CommentAction = "reply"
	// CommentResolve resolves thread ThreadID.
	CommentResolve CommentAction = "resolve"
)

// CommentActionParams is the params of MethodCommentAction.
type CommentActionParams struct {
	ItemID    string          `json:"itemId"`
	Action    CommentAction   `json:"action"`
	CommentID string          `json:"commentId,omitempty"` // accept, dismiss
	ThreadID  string          `json:"threadId,omitempty"`  // reply, resolve
	Body      string          `json:"body,omitempty"`      // reply
	Override  *model.Override `json:"override,omitempty"`  // dismiss with `!`
}

// ReviewEvent is the verdict of a submitted review (GitHub REST values).
type ReviewEvent string

// Review events.
const (
	EventApprove        ReviewEvent = "APPROVE"
	EventRequestChanges ReviewEvent = "REQUEST_CHANGES"
	EventComment        ReviewEvent = "COMMENT"
)

// SubmitParams is the params of MethodSubmitReview: one GitHub review with
// Event, the optional summary Body and, as line comments, every user draft
// of the item (CommentIDs, when set, limits it to those drafts). ai-drafts
// are never sent.
type SubmitParams struct {
	ItemID     string      `json:"itemId"`
	Event      ReviewEvent `json:"event"`
	Body       string      `json:"body,omitempty"`
	CommentIDs []string    `json:"commentIds,omitempty"`
}

// SubmitResult is the result of MethodSubmitReview: the posted drafts
// (State posted, GHID set) and the review's URL. Skipped lists drafts that
// could not be sent (outdated anchor), which stay drafts.
type SubmitResult struct {
	ReviewID int64           `json:"reviewId"`
	URL      string          `json:"url,omitempty"`
	Posted   []model.Comment `json:"posted,omitempty"`
	Skipped  []model.Comment `json:"skipped,omitempty"`
}

// CreatePRParams is the params of MethodCreatePR: open a PR from the branch
// item ItemID (head = its ref, Base default the item's base ref) and attach
// the branch's local drafts as one COMMENT review on it.
type CreatePRParams struct {
	ItemID string `json:"itemId"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Base   string `json:"base,omitempty"`
	Draft  bool   `json:"draft,omitempty"`
}

// CreatePRResult is the result of MethodCreatePR: the new PR item and how
// many drafts were attached to it.
type CreatePRResult struct {
	Item     model.Item `json:"item"`
	Attached int        `json:"attached"`
	URL      string     `json:"url,omitempty"`
}

// ViewedParams is the params of MethodViewed: the user looked at HeadSHA
// of ItemID (the analysed head shown).
type ViewedParams struct {
	ItemID  string `json:"itemId"`
	HeadSHA string `json:"headSha"`
}

// OverrideParams is the params of MethodOverride (Override.At is filled by
// the daemon when zero).
type OverrideParams struct {
	ItemID   string         `json:"itemId"`
	Override model.Override `json:"override"`
}

// GroupEditParams is the params of MethodEditGroups. Edit.ItemID must equal
// ItemID; the daemon fills Edit.At when zero.
type GroupEditParams struct {
	ItemID string          `json:"itemId"`
	Edit   model.GroupEdit `json:"edit"`
}

// EventType says what changed.
type EventType string

// Event types.
const (
	EventItem   EventType = "item"   // Item changed (state, metadata, rank)
	EventStage  EventType = "stage"  // a RunLog row landed
	EventPatch  EventType = "patch"  // stage output stored; re-read Detail
	EventRemove EventType = "remove" // item removed
	EventLog    EventType = "log"    // human-readable status note
	// EventSettings: the daemon reloaded its configuration (M5.1); screens
	// that show settings or the strategy re-read MethodSettings.
	EventSettings EventType = "settings"
)

// Event is a state patch pushed to subscribers.
type Event struct {
	Type   EventType     `json:"type"`
	ItemID string        `json:"itemId,omitempty"`
	Stage  model.Stage   `json:"stage,omitempty"`
	Item   *model.Item   `json:"item,omitempty"`
	RunLog *model.RunLog `json:"runLog,omitempty"`
	Note   string        `json:"note,omitempty"`
	// Repo is the workspace repo key the event came from (M5.3; "" in
	// single-repo mode). ItemID, Item.ID and RunLog.ItemID then carry the
	// repo prefix; an EventLog note or EventSettings is about that repo.
	Repo string `json:"repo,omitempty"`
}

// Message is one NDJSON line.
type Message struct {
	Kind   Kind            `json:"kind"`
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Event  *Event          `json:"event,omitempty"`
}

// ItemParams is the params of item-scoped methods.
type ItemParams struct {
	ItemID    string      `json:"itemId"`
	FromStage model.Stage `json:"fromStage,omitempty"`
	Force     bool        `json:"force,omitempty"`
	// User marks a refresh the user asked for by hand (`r`, Pipeline page
	// `r`): it runs the model stages whatever the auto_review strategy and
	// records Item.RequestedSHA. Without it (the TUI's automatic first
	// load) the strategy decides (M5.1). The safe default is false.
	User bool `json:"user,omitempty"`
}

// CheckoutParams is the params of MethodCheckout. Without Force, a dirty work
// tree is not touched and CheckoutResult.Dirty is set so the UI can prompt.
type CheckoutParams struct {
	ItemID string `json:"itemId"`
	Force  bool   `json:"force,omitempty"`
}

// CheckoutResult is the result of MethodCheckout.
type CheckoutResult struct {
	Ref        string `json:"ref,omitempty"`        // the ref checked out (or that would be)
	Dirty      bool   `json:"dirty,omitempty"`      // work tree dirty; nothing done
	CheckedOut bool   `json:"checkedOut,omitempty"` // checkout performed
}

// AddParams is the params of MethodAdd.
type AddParams struct {
	Query string `json:"query"`
}

// DetailParams is the params of MethodDetail (M5.2). A plain {itemId}
// (the pre-M5.2 ItemParams shape) is the full Detail.
type DetailParams struct {
	ItemID string `json:"itemId"`
	// NoLabels leaves Detail.Labels out and sets Detail.LabelsOmitted.
	// Labels are most of a Detail's store time and payload (docs/
	// m52-open-latency.md) and only the `L` and `!` overlays read them;
	// fetch them with MethodLabels.
	NoLabels bool `json:"noLabels,omitempty"`
}

// LabelsParams is the params of MethodLabels. UnitIDs limits the result
// to those label subjects (unit ids, model.FileSubject or
// model.ItemSubject ids); empty means every subject of the analysis the
// item's Detail serves (the snapshot during a re-run, see RunStatus).
type LabelsParams struct {
	ItemID  string   `json:"itemId"`
	UnitIDs []string `json:"unitIds,omitempty"`
}

// RunStatus is Detail.Run: an analysis of the item is in flight (M5.2,
// stale-while-revalidate). The TUI shows "↻ updating review · <stage>"
// over the analysis it already has instead of blanking the page.
type RunStatus struct {
	// Stage is the stage the run is in ("" before its first stage row);
	// the same value as the item's StateNote while analysing.
	Stage model.Stage `json:"stage,omitempty"`
	// Snapshot is set when Detail's analysis parts (Diff, Files, Units,
	// Labels, Links, Groups, Reviews, Agg) are the item's last complete
	// analysis, served unchanged until the run's aggregate lands; the
	// item, comments, finding marks and run log are live. SnapshotSHA is
	// the head that analysis is of.
	Snapshot    bool   `json:"snapshot,omitempty"`
	SnapshotSHA string `json:"snapshotSha,omitempty"`
	// Reviews is the run's live routing table while Snapshot is set (the
	// Pipeline page's rows during the review stage); nil otherwise, when
	// Detail.Reviews is live.
	Reviews []model.Review `json:"reviews,omitempty"`
}

// Detail is everything the item screens render.
type Detail struct {
	Item   model.Item         `json:"item"`
	Diff   string             `json:"diff,omitempty"`
	Files  []model.File       `json:"files,omitempty"`
	Units  []model.ChangeUnit `json:"units,omitempty"`
	Labels []model.Label      `json:"labels,omitempty"`
	// LabelsOmitted is set when the Detail was loaded with
	// DetailParams.NoLabels: Labels is empty because it was not asked for,
	// not because the item has none (M5.2).
	LabelsOmitted bool          `json:"labelsOmitted,omitempty"`
	Links         []model.Link  `json:"links,omitempty"`
	Groups        []model.Group `json:"groups,omitempty"`
	// Reviews is the routing table: one row per current group, final state
	// (Store.ItemReviews).
	Reviews  []model.Review  `json:"reviews,omitempty"`
	Comments []model.Comment `json:"comments,omitempty"`
	// FindingMarks are the item's accepted and dismissed review findings
	// (Store.FindingMarks); the Code page stops drawing those inline.
	FindingMarks []model.FindingMark `json:"findingMarks,omitempty"`
	Agg          *model.ItemAgg      `json:"agg,omitempty"`
	RunLogs      []model.RunLog      `json:"runLogs,omitempty"`
	// Routing is the active routing configuration (rules panel).
	Routing *RoutingInfo `json:"routing,omitempty"`
	// Run is set while an analysis of the item is in flight (M5.2); nil
	// when none is.
	Run *RunStatus `json:"run,omitempty"`
}

// Encoder writes messages, one per line; safe for concurrent use.
type Encoder struct {
	mu sync.Mutex
	w  io.Writer
}

// NewEncoder returns an Encoder writing to w.
func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

// Encode writes one message followed by '\n'.
func (e *Encoder) Encode(m Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err = e.w.Write(append(b, '\n'))
	return err
}

// Decoder reads messages, one per line. Lines have no size cap: a large
// Detail (diff, files and units) must not tear down the connection.
type Decoder struct {
	r *bufio.Reader
}

// NewDecoder returns a Decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: bufio.NewReaderSize(r, 64*1024)}
}

// Decode reads the next message; io.EOF at end of stream. A final line
// without a trailing newline is still decoded.
func (d *Decoder) Decode() (Message, error) {
	for {
		line, err := d.r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var m Message
			if uerr := json.Unmarshal(line, &m); uerr != nil {
				return Message{}, uerr
			}
			return m, nil
		}
		if err != nil {
			return Message{}, err
		}
	}
}
