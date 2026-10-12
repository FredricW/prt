package tui

import (
	"context"
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/daemon"
)

// Runner runs the pipeline synchronously for snapshots (InProcess has it).
type Runner interface {
	RunSync(ctx context.Context, itemID string) error
}

// Snapshot renders a few screens to w without a TTY, for agents and CI:
// discovery, analysis of every item (synchronously), then the picker with
// each item selected in turn (at most maxItems). Output has no colour.
func Snapshot(ctx context.Context, c daemon.Client, w io.Writer, width, height, maxItems int) error {
	if err := c.Discover(ctx); err != nil {
		return fmt.Errorf("snapshot: discover: %w", err)
	}
	items, err := c.Items(ctx)
	if err != nil {
		return err
	}
	if r, ok := c.(Runner); ok {
		for _, it := range items {
			if err := r.RunSync(ctx, it.ID); err != nil {
				_, _ = fmt.Fprintf(w, "# analysis of %s failed: %v\n", it.ID, err)
			}
		}
		if items, err = c.Items(ctx); err != nil {
			return err
		}
	}
	m := New(c, lipgloss.NewRenderer(io.Discard))
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: width, Height: height})
	tm, _ = tm.Update(itemsMsg(items))
	n := len(tm.(Model).items)
	if n == 0 {
		_, _ = fmt.Fprintf(w, "=== screen: picker (empty) ===\n%s\n", tm.View())
		return nil
	}
	for i := 0; i < n && i < maxItems; i++ {
		root := tm.(Model)
		root.cursor = i
		it, _ := root.selected()
		d, err := c.Detail(ctx, it.ID)
		if err != nil {
			return err
		}
		tm, _ = root.Update(detailMsg(d))
		_, _ = fmt.Fprintf(w, "=== screen: picker + diff · %s ===\n%s\n\n", it.ID, tm.View())
	}
	return nil
}
