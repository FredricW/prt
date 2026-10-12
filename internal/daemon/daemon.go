// Package daemon hosts the analysis side of prt. The TUI talks to it only
// through the Client interface, which has two implementations:
//
//   - InProcess: runs discovery and the pipeline in the TUI's process
//     (`prt --no-daemon`, tests, CI);
//   - Socket: talks NDJSON (package proto) to `prt daemon` over the Unix
//     socket at .git/prt/prtd.sock. TODO(M0): Serve and Dial.
package daemon

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// ErrNotImplemented marks the socket transport placeholder.
var ErrNotImplemented = errors.New("daemon: not implemented")

// SocketPath returns the daemon socket path for a git dir.
func SocketPath(gitDir string) string { return filepath.Join(gitDir, "prt", "prtd.sock") }

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
	// Events streams patches; closed by Close.
	Events() <-chan proto.Event
	Close() error
}

// Serve runs the daemon on the socket until ctx is done. TODO(M0).
func Serve(_ context.Context, _ Client, _ string) error { return ErrNotImplemented }

// Dial connects to a running daemon. TODO(M0).
func Dial(_ context.Context, _ string) (Client, error) { return nil, ErrNotImplemented }
