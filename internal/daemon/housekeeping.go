package daemon

import (
	"context"

	"github.com/fredricw/prt/internal/model"
)

// keepRuns is how many runs of an item the run log keeps (M5.2): Detail
// serves only the current run, and the older rows are history the Pipeline
// page no longer shows.
const keepRuns = 5

// sweepLabels deletes every stored label no stored item references (M5.2:
// the label cache is bounded by what the store holds). It runs once when
// the client starts, before any job can: a pipeline stage stores some
// labels just before the documents that reference them (group names before
// the groups), which a sweep during a run could take for orphans. Errors
// are logged only.
func (c *InProcess) sweepLabels(ctx context.Context) {
	n, err := c.d.Store.PruneLabels(ctx, nil)
	if err != nil {
		c.log.Warn("label sweep failed", "err", err)
		return
	}
	if n > 0 {
		c.log.Info("pruned labels no item references", "labels", n)
	}
}

// removalSubjects are the label subjects a removed item may leave orphaned:
// its units, files and item subject, its branch-state subject and its
// groups (PruneLabels adds the pairs among its units). Comment subjects
// stay with their comments, which outlive the item. Read before the item
// is removed; errors are logged and give fewer subjects (the start-up
// sweep catches the rest).
func (c *InProcess) removalSubjects(ctx context.Context, itemID string) []string {
	subjects, err := c.d.Store.ItemSubjects(ctx, itemID)
	if err != nil {
		c.log.Warn("remove: label subjects", "item", itemID, "err", err)
		return nil
	}
	subjects = append(subjects, model.BranchSubject(itemID))
	groups, err := c.d.Store.Groups(ctx, itemID)
	if err != nil {
		c.log.Warn("remove: groups", "item", itemID, "err", err)
	}
	for _, g := range groups {
		subjects = append(subjects, model.GroupSubject(g.ID))
	}
	return subjects
}

// pruneLabels deletes the labels of a removed item's subjects that no
// stored item references (a unit its branch or PR still holds keeps them).
func (c *InProcess) pruneLabels(ctx context.Context, itemID string, subjects []string) {
	if len(subjects) == 0 {
		return
	}
	n, err := c.d.Store.PruneLabels(ctx, subjects)
	if err != nil {
		c.log.Warn("prune labels of a removed item", "item", itemID, "err", err)
		return
	}
	c.log.Debug("pruned labels of a removed item", "item", itemID, "labels", n)
}

// pruneRunLog keeps the item's last keepRuns runs in the run log.
func (c *InProcess) pruneRunLog(itemID string) {
	if _, err := c.d.Store.PruneRunLogs(context.Background(), itemID, keepRuns); err != nil {
		c.log.Warn("prune run log", "item", itemID, "err", err)
	}
}
