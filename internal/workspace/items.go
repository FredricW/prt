package workspace

import (
	"context"
	"errors"

	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
)

// Items implements daemon.Client: every repo's items (asked in parallel)
// with workspace ids and Repo set, ranked on one scale: UrgencyQuartile is
// recomputed against the top urgency of every repo together and the list
// is sorted with daemon.SortItems. A repo whose call fails is left out,
// logged and noted in Workspace; only when every repo fails is it an error.
func (c *Client) Items(ctx context.Context) ([]model.Item, error) {
	res := fanOut(ctx, c.members, 0, func(ctx context.Context, m Member) ([]model.Item, error) {
		return m.Client.Items(ctx)
	})
	var all []model.Item
	failed := 0
	for i, m := range c.members {
		key := m.Info.Key
		if err := res.err[i]; err != nil {
			failed++
			c.setItemsErr(key, err)
			c.log.Warn("workspace: items", "repo", key, "err", err)
			continue
		}
		c.setItemsErr(key, nil)
		all = append(all, outItems(key, res.vals[i])...)
	}
	if failed > 0 && failed == len(c.members) {
		return nil, errors.Join(res.errs(c.members)...)
	}
	top := 0.0
	for _, it := range all {
		top = max(top, it.Urgency)
	}
	for i := range all {
		all[i].UrgencyQuartile = daemon.Quartile(all[i].Urgency, top)
	}
	daemon.SortItems(all)
	return all, nil
}

// Discover implements daemon.Client: every repo's discovery, at most
// ClientOptions.DiscoverConcurrency at once. The error joins the failures
// as "<label>: <err>" (nil when every repo succeeded).
func (c *Client) Discover(ctx context.Context) error {
	res := fanOut(ctx, c.members, c.discN, func(ctx context.Context, m Member) (struct{}, error) {
		return struct{}{}, m.Client.Discover(ctx)
	})
	errs := res.errs(c.members)
	for _, err := range errs {
		c.log.Warn("workspace: discover", "err", err)
	}
	return errors.Join(errs...)
}

// AnalysePending queues every repo's pending items (see
// daemon.InProcess.AnalysePending) for the members that support it; the
// error joins the failures as "<label>: <err>".
func (c *Client) AnalysePending(ctx context.Context) error {
	res := fanOut(ctx, c.members, 0, func(ctx context.Context, m Member) (struct{}, error) {
		if a, ok := m.Client.(pendingAnalyser); ok {
			return struct{}{}, a.AnalysePending(ctx)
		}
		return struct{}{}, nil
	})
	return errors.Join(res.errs(c.members)...)
}
