// Package daemon hosts the analysis side of prt. The TUI talks to it only
// through the Client interface, which has two implementations:
//
//   - InProcess: runs discovery and the pipeline in the TUI's process
//     (`prt --no-daemon`, tests, CI) and is what `prt daemon` serves;
//   - Socket: talks NDJSON (package proto) to `prt daemon` over the Unix
//     socket at .git/prt/prtd.sock (see Serve and Dial).
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// maxSocketPath is the longest Unix socket path used as is. sun_path holds
// 104 bytes on macOS and 108 on Linux, including the terminating NUL.
const maxSocketPath = 100

// SocketPath returns the daemon socket path for a git dir:
// <gitDir>/prt/prtd.sock, or, when that is too long for a Unix socket (deep
// repos, worktrees), $TMPDIR/prt-<hash of gitDir>/prtd.sock. Daemon and TUI
// derive it the same way, so both agree.
func SocketPath(gitDir string) string {
	return socketPathIn(gitDir, os.TempDir())
}

func socketPathIn(gitDir, tmp string) string {
	p := filepath.Join(gitDir, "prt", "prtd.sock")
	if len(p) <= maxSocketPath {
		return p
	}
	sum := sha256.Sum256([]byte(filepath.Clean(gitDir)))
	return filepath.Join(tmp, "prt-"+hex.EncodeToString(sum[:8]), "prtd.sock")
}

// Client is what screens use to read state and send commands.
type Client interface {
	// Discover refreshes the picker sources (GitHub searches, local branches).
	Discover(ctx context.Context) error
	// Items returns all picker items.
	Items(ctx context.Context) ([]model.Item, error)
	// Detail returns everything stored for one item (DetailOf with every
	// part).
	Detail(ctx context.Context, itemID string) (proto.Detail, error)
	// DetailOf returns an item's Detail with the parts p asks for: with
	// p.NoLabels the labels are left out (Detail.LabelsOmitted), which is
	// what screens load on open (M5.2). While a run of the item is in
	// flight, Detail.Run says so and the analysis parts may be the last
	// complete analysis (RunStatus.Snapshot).
	DetailOf(ctx context.Context, p proto.DetailParams) (proto.Detail, error)
	// Labels returns an item's labels with the user's overrides applied
	// (M5.2), for screens that loaded the Detail without them.
	Labels(ctx context.Context, p proto.LabelsParams) ([]model.Label, error)
	// Refresh (re-)runs the pipeline for an item in the background.
	Refresh(ctx context.Context, p proto.ItemParams) error
	// Add resolves a query ("#482", "482", a ref) and adds it as an Added item.
	Add(ctx context.Context, query string) (model.Item, error)
	// Remove deletes an item from the picker.
	Remove(ctx context.Context, itemID string) error
	// Checkout runs `git checkout` of the item's head ref. With p.Force false
	// and a dirty tree it does nothing and returns Dirty = true.
	Checkout(ctx context.Context, p proto.CheckoutParams) (proto.CheckoutResult, error)
	// EditGroups stores a user merge/split/reset of the item's groups and
	// queues the item for re-analysis (cached stages are skipped, so only
	// clustering re-runs); a patch event follows when the new groups land.
	EditGroups(ctx context.Context, p proto.GroupEditParams) error
	// ReviewAction forces a group deep (D, R), skips it (s) or resets that
	// override, then re-runs the item from routing (cached reviews hit).
	ReviewAction(ctx context.Context, p proto.ReviewActionParams) error
	// Routing returns the active routing rules; Ensure writes the built-in
	// file when missing, Reload re-reads it and re-routes unreviewed groups.
	Routing(ctx context.Context, p proto.RoutingParams) (proto.RoutingInfo, error)
	// CallLog returns the raw LLM calls of a routing-table row.
	CallLog(ctx context.Context, p proto.CallLogParams) ([]model.ReviewCall, error)
	// SetCap sets an item's review cost cap and re-runs groups queued over it.
	SetCap(ctx context.Context, p proto.CapParams) error
	// SaveComment upserts or deletes a local draft comment (M4).
	SaveComment(ctx context.Context, p proto.CommentParams) (model.Comment, error)
	// CommentAction accepts/dismisses an ai-draft or replies to/resolves a
	// GitHub thread (M4).
	CommentAction(ctx context.Context, p proto.CommentActionParams) (model.Comment, error)
	// SubmitReview posts the item's drafts as one GitHub review (M4).
	SubmitReview(ctx context.Context, p proto.SubmitParams) (proto.SubmitResult, error)
	// CreatePR opens a PR for a branch item and attaches its drafts (M4).
	CreatePR(ctx context.Context, p proto.CreatePRParams) (proto.CreatePRResult, error)
	// MarkViewed records the head the user looked at (M4, Since you looked).
	MarkViewed(ctx context.Context, p proto.ViewedParams) error
	// Override stores a label override and re-aggregates the item (M4: `!`;
	// M5: aggregate only, affected reviews marked stale-labels).
	Override(ctx context.Context, p proto.OverrideParams) error
	// Search runs one search-and-add source (M5, picker `/`).
	Search(ctx context.Context, p proto.SearchParams) (proto.SearchResult, error)
	// Preview resolves a query like Add but stores the item as
	// model.SourcePreview (never listed by Items) and starts its analysis,
	// so `tab` can show its overview without adding it (M5).
	Preview(ctx context.Context, query string) (model.Item, error)
	// RemovePreview deletes itemID only if it is still a preview; a listed
	// item (ranked, discovered, added or promoted meanwhile) is kept. The
	// TUI uses it to discard a preview when the search prompt closes (M5).
	RemovePreview(ctx context.Context, itemID string) error
	// Stop cancels an item's analysis, or every running and queued one with
	// p.All (M5.1): in-flight stages end "stopped", the item is left
	// StateStopped ("stopped at <stage>") and nothing restarts it
	// automatically.
	Stop(ctx context.Context, p proto.StopParams) (proto.StopResult, error)
	// Resume queues stopped (or not-analysed) items again as an explicit
	// request; finished stages are served from their caches (M5.1).
	Resume(ctx context.Context, p proto.ResumeParams) (proto.ResumeResult, error)
	// Settings returns the settings page's values and environment status;
	// p.Ensure creates the routing or questions file first (M5.1).
	Settings(ctx context.Context, p proto.SettingsParams) (proto.SettingsInfo, error)
	// SaveSettings writes the settings to the chosen scope, reloads the
	// configuration and applies it live (M5.1); an EventSettings follows.
	SaveSettings(ctx context.Context, p proto.SaveSettingsParams) (proto.SettingsInfo, error)
	// Workspace describes what the client serves (M5.3): one repo with
	// Multi false for a single-repo client, every discovered repo for a
	// workspace client (internal/workspace).
	Workspace(ctx context.Context) (proto.WorkspaceInfo, error)
	// Events streams patches; closed by Close.
	Events() <-chan proto.Event
	Close() error
}
