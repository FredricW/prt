package tui

import (
	"context"
	"fmt"
	"io"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fredricw/prt/internal/codeview"
	"github.com/fredricw/prt/internal/daemon"
)

// Runner runs the pipeline synchronously for snapshots (InProcess has it).
type Runner interface {
	RunSync(ctx context.Context, itemID string) error
}

// Snapshot renders screens to w without a TTY, for agents and CI. See
// SnapshotWithOptions; ages are relative to the real clock.
func Snapshot(ctx context.Context, c daemon.Client, w io.Writer, width, height, maxItems int) error {
	return SnapshotWithOptions(ctx, c, w, width, height, maxItems, Options{})
}

// SnapshotWithOptions discovers items, analyses every item synchronously
// (when c is a Runner), then writes, for each of the first maxItems items,
// the picker with the Overview tab, the picker with the Code tab by file,
// the picker with the Code tab by change ("code-by-change"; by file with
// the note "no groups yet" when the item has no groups) and the picker with
// the Pipeline page ("pipeline"), plus one help-overlay screen. Screens are separated by "=== screen: … ==="
// headers. Output has no colour; pass o.Now for deterministic ages.
func SnapshotWithOptions(ctx context.Context, c daemon.Client, w io.Writer, width, height, maxItems int, o Options) error {
	if err := c.Discover(ctx); err != nil {
		_, _ = fmt.Fprintf(w, "# discover failed: %v\n", err)
	}
	items, err := c.Items(ctx)
	if err != nil {
		return fmt.Errorf("snapshot: items: %w", err)
	}
	if r, ok := c.(Runner); ok {
		for _, it := range items {
			if err := r.RunSync(ctx, it.ID); err != nil {
				_, _ = fmt.Fprintf(w, "# analysis of %s failed: %v\n", it.ID, err)
			}
		}
		if items, err = c.Items(ctx); err != nil {
			return fmt.Errorf("snapshot: items: %w", err)
		}
	}
	o.RefreshEvery = 0
	m := NewWithOptions(c, lipgloss.NewRenderer(io.Discard), o)
	var tm tea.Model = m
	tm, _ = tm.Update(tea.WindowSizeMsg{Width: width, Height: height})
	tm, _ = tm.Update(itemsMsg{items: items})
	m = tm.(Model)
	n := len(m.picker.items)
	if n == 0 {
		_, err := fmt.Fprintf(w, "=== screen: picker (empty) ===\n%s\n", m.View())
		return err
	}
	for i := 0; i < n && i < maxItems; i++ {
		m.picker.moveTo(i)
		it, _ := m.picker.selected()
		d, err := c.Detail(ctx, it.ID)
		if err != nil {
			return fmt.Errorf("snapshot: detail %s: %w", it.ID, err)
		}
		m.applyDetail(d)
		for _, tab := range []struct {
			t    Tab
			name string
		}{{TabOverview, "overview"}, {TabCode, "code"}} {
			m.tab = tab.t
			if _, err := fmt.Fprintf(w, "=== screen: picker + %s · %s ===\n%s\n\n", tab.name, it.ID, m.View()); err != nil {
				return err
			}
		}
		// By change; without groups the page stays by file and says so.
		m.code.SetMode(codeview.ModeChange)
		_, err = fmt.Fprintf(w, "=== screen: picker + code-by-change · %s ===\n%s\n\n", it.ID, m.View())
		m.code.SetMode(codeview.ModeFile)
		if err != nil {
			return err
		}
		// The Pipeline page, clocked like the rest of the snapshot.
		m.tab = TabPipeline
		m.pipe.SetNow(m.now())
		_, err = fmt.Fprintf(w, "=== screen: picker + pipeline · %s ===\n%s\n\n", it.ID, m.View())
		m.tab = TabOverview
		if err != nil {
			return err
		}
	}
	m.tab, m.focus, m.showHelp = TabOverview, FocusPicker, true
	_, err = fmt.Fprintf(w, "=== screen: help · picker ===\n%s\n", m.View())
	return err
}
