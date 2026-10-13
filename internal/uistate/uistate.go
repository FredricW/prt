// Package uistate persists per-user TUI layout state (M5.3): the widths the
// user dragged the picker sidebar and the Code page's file/group list to.
// It lives in $XDG_STATE_HOME/prt/ui.json (config.StateDir), not in a
// prt.toml: it is state the UI writes on its own, never configuration the
// user edits, and it is shared by every repo and workspace.
//
// A missing, empty or unreadable file is the zero Layout (automatic
// widths); the TUI never fails to start over it.
package uistate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/fredricw/prt/internal/config"
)

// FileName is the state file's name inside config.StateDir().
const FileName = "ui.json"

// Layout is the persisted pane geometry. A zero width means "automatic"
// (the built-in width for the terminal size). Widths are outer widths in
// terminal cells as the user left them; the TUI clamps them to the current
// terminal when it applies them, so a narrower terminal never breaks.
type Layout struct {
	// SidebarWidth is the picker sidebar's width (tui: the value
	// sidebarWidth computes automatically, 36 on terminals ≥ 100 columns).
	SidebarWidth int `json:"sidebarWidth,omitempty"`
	// CodeListWidth is the Code page's file/group list width (codeview:
	// automatic is a third of the page, 16–40 columns).
	CodeListWidth int `json:"codeListWidth,omitempty"`
}

// DefaultPath is <config.StateDir()>/ui.json, or "" when no state
// directory is known (then nothing is persisted).
func DefaultPath() string {
	d := config.StateDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, FileName)
}

// Load reads the layout at path. A missing file (or path "") is the zero
// Layout and no error; a file that does not decode returns the zero Layout
// and the error, which the caller only logs. Negative widths read as 0.
func Load(path string) (Layout, error) {
	if path == "" {
		return Layout{}, nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // the per-user state file
	if errors.Is(err, fs.ErrNotExist) {
		return Layout{}, nil
	}
	if err != nil {
		return Layout{}, fmt.Errorf("uistate: %w", err)
	}
	var l Layout
	if len(b) == 0 {
		return l, nil
	}
	if err := json.Unmarshal(b, &l); err != nil {
		return Layout{}, fmt.Errorf("uistate: %s: %w", path, err)
	}
	l.SidebarWidth, l.CodeListWidth = max(0, l.SidebarWidth), max(0, l.CodeListWidth)
	return l, nil
}

// Save writes l to path atomically (a temp file in the same directory, then
// a rename), creating the directory (0700) when missing; the file is 0600.
// path "" is a no-op.
func Save(path string, l Layout) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("uistate: %w", err)
	}
	b, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("uistate: %w", err)
	}
	f, err := os.CreateTemp(dir, ".ui-*.json")
	if err != nil {
		return fmt.Errorf("uistate: %w", err)
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op after a successful rename
	if _, err := f.Write(append(b, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("uistate: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("uistate: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("uistate: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("uistate: %w", err)
	}
	return nil
}
