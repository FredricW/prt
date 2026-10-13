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
	// Detail returns everything stored for one item.
	Detail(ctx context.Context, itemID string) (proto.Detail, error)
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
	// Events streams patches; closed by Close.
	Events() <-chan proto.Event
	Close() error
}
