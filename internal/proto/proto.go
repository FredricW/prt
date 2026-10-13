// Package proto is the daemon ↔ TUI wire protocol: newline-delimited JSON
// over a Unix socket (.git/prt/prtd.sock). Every line is one Message.
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
	MethodDiscover  = "discover"  // {} → {}; refresh picker sources
	MethodItems     = "items"     // → []model.Item
	MethodCheckout  = "checkout"  // CheckoutParams → CheckoutResult
	MethodDetail    = "detail"    // {itemId} → Detail
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
)

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
)

// Event is a state patch pushed to subscribers.
type Event struct {
	Type   EventType     `json:"type"`
	ItemID string        `json:"itemId,omitempty"`
	Stage  model.Stage   `json:"stage,omitempty"`
	Item   *model.Item   `json:"item,omitempty"`
	RunLog *model.RunLog `json:"runLog,omitempty"`
	Note   string        `json:"note,omitempty"`
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

// Detail is everything the item screens render.
type Detail struct {
	Item   model.Item         `json:"item"`
	Diff   string             `json:"diff,omitempty"`
	Files  []model.File       `json:"files,omitempty"`
	Units  []model.ChangeUnit `json:"units,omitempty"`
	Labels []model.Label      `json:"labels,omitempty"`
	Links  []model.Link       `json:"links,omitempty"`
	Groups []model.Group      `json:"groups,omitempty"`
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
