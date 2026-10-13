package workspace

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/fredricw/prt/internal/proto"
)

// Stop implements daemon.Client: with p.All every repo stops every
// analysis (in parallel; the stopped ids are merged and sorted, the
// failures joined), otherwise the item's repo stops it.
func (c *Client) Stop(ctx context.Context, p proto.StopParams) (proto.StopResult, error) {
	if !p.All {
		m, local, err := c.route(p.ItemID)
		if err != nil {
			return proto.StopResult{}, err
		}
		r, err := m.Client.Stop(ctx, proto.StopParams{ItemID: local})
		return proto.StopResult{Stopped: outIDs(m.Info.Key, r.Stopped)}, err
	}
	out := fanOut(ctx, c.members, 0, func(ctx context.Context, m Member) ([]string, error) {
		r, err := m.Client.Stop(ctx, proto.StopParams{All: true})
		return outIDs(m.Info.Key, r.Stopped), err
	})
	return proto.StopResult{Stopped: mergeIDs(out.vals)}, errors.Join(out.errs(c.members)...)
}

// Resume implements daemon.Client like Stop.
func (c *Client) Resume(ctx context.Context, p proto.ResumeParams) (proto.ResumeResult, error) {
	if !p.All {
		m, local, err := c.route(p.ItemID)
		if err != nil {
			return proto.ResumeResult{}, err
		}
		r, err := m.Client.Resume(ctx, proto.ResumeParams{ItemID: local})
		return proto.ResumeResult{Resumed: outIDs(m.Info.Key, r.Resumed)}, err
	}
	out := fanOut(ctx, c.members, 0, func(ctx context.Context, m Member) ([]string, error) {
		r, err := m.Client.Resume(ctx, proto.ResumeParams{All: true})
		return outIDs(m.Info.Key, r.Resumed), err
	})
	return proto.ResumeResult{Resumed: mergeIDs(out.vals)}, errors.Join(out.errs(c.members)...)
}

// mergeIDs concatenates and sorts id lists (nil when all are empty).
func mergeIDs(lists [][]string) []string {
	var ids []string
	for _, l := range lists {
		ids = append(ids, l...)
	}
	slices.Sort(ids)
	return ids
}

// RunSync runs an item's pipeline in its repo and waits (tui.Runner for
// --snapshot); the repo's client must support it.
func (c *Client) RunSync(ctx context.Context, itemID string) error {
	m, local, err := c.route(itemID)
	if err != nil {
		return err
	}
	r, ok := m.Client.(syncRunner)
	if !ok {
		return fmt.Errorf("workspace: repo %q cannot run an item synchronously", m.Info.Key)
	}
	return r.RunSync(ctx, local)
}
