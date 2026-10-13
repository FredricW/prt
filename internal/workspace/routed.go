package workspace

import (
	"context"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// The methods in this file act on one item: the id's repo prefix picks the
// member, which gets the repo-local id, and the result's ids are prefixed
// again.

// Detail implements daemon.Client.
func (c *Client) Detail(ctx context.Context, itemID string) (proto.Detail, error) {
	return c.DetailOf(ctx, proto.DetailParams{ItemID: itemID})
}

// DetailOf implements daemon.Client.
func (c *Client) DetailOf(ctx context.Context, p proto.DetailParams) (proto.Detail, error) {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return proto.Detail{}, err
	}
	p.ItemID = local
	d, err := m.Client.DetailOf(ctx, p)
	return outDetail(m.Info.Key, d), err
}

// Labels implements daemon.Client.
func (c *Client) Labels(ctx context.Context, p proto.LabelsParams) ([]model.Label, error) {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return nil, err
	}
	p.ItemID = local
	return m.Client.Labels(ctx, p)
}

// Refresh implements daemon.Client.
func (c *Client) Refresh(ctx context.Context, p proto.ItemParams) error {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return err
	}
	p.ItemID = local
	return m.Client.Refresh(ctx, p)
}

// Remove implements daemon.Client.
func (c *Client) Remove(ctx context.Context, itemID string) error {
	m, local, err := c.route(itemID)
	if err != nil {
		return err
	}
	return m.Client.Remove(ctx, local)
}

// RemovePreview implements daemon.Client.
func (c *Client) RemovePreview(ctx context.Context, itemID string) error {
	m, local, err := c.route(itemID)
	if err != nil {
		return err
	}
	return m.Client.RemovePreview(ctx, local)
}

// Checkout implements daemon.Client (in the item's own work tree).
func (c *Client) Checkout(ctx context.Context, p proto.CheckoutParams) (proto.CheckoutResult, error) {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return proto.CheckoutResult{}, err
	}
	p.ItemID = local
	return m.Client.Checkout(ctx, p)
}

// EditGroups implements daemon.Client.
func (c *Client) EditGroups(ctx context.Context, p proto.GroupEditParams) error {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return err
	}
	p.ItemID = local
	p.Edit.ItemID = localOf(m.Info.Key, p.Edit.ItemID)
	return m.Client.EditGroups(ctx, p)
}

// ReviewAction implements daemon.Client.
func (c *Client) ReviewAction(ctx context.Context, p proto.ReviewActionParams) error {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return err
	}
	p.ItemID = local
	return m.Client.ReviewAction(ctx, p)
}

// CallLog implements daemon.Client.
func (c *Client) CallLog(ctx context.Context, p proto.CallLogParams) ([]model.ReviewCall, error) {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return nil, err
	}
	p.ItemID = local
	calls, err := m.Client.CallLog(ctx, p)
	return outCalls(m.Info.Key, calls), err
}

// SetCap implements daemon.Client.
func (c *Client) SetCap(ctx context.Context, p proto.CapParams) error {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return err
	}
	p.ItemID = local
	return m.Client.SetCap(ctx, p)
}

// SaveComment implements daemon.Client.
func (c *Client) SaveComment(ctx context.Context, p proto.CommentParams) (model.Comment, error) {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return model.Comment{}, err
	}
	p.ItemID = local
	p.Comment.ItemID = localOf(m.Info.Key, p.Comment.ItemID)
	cm, err := m.Client.SaveComment(ctx, p)
	return outComment(m.Info.Key, cm), err
}

// CommentAction implements daemon.Client.
func (c *Client) CommentAction(ctx context.Context, p proto.CommentActionParams) (model.Comment, error) {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return model.Comment{}, err
	}
	p.ItemID = local
	cm, err := m.Client.CommentAction(ctx, p)
	return outComment(m.Info.Key, cm), err
}

// SubmitReview implements daemon.Client.
func (c *Client) SubmitReview(ctx context.Context, p proto.SubmitParams) (proto.SubmitResult, error) {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return proto.SubmitResult{}, err
	}
	p.ItemID = local
	r, err := m.Client.SubmitReview(ctx, p)
	return outSubmit(m.Info.Key, r), err
}

// CreatePR implements daemon.Client.
func (c *Client) CreatePR(ctx context.Context, p proto.CreatePRParams) (proto.CreatePRResult, error) {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return proto.CreatePRResult{}, err
	}
	p.ItemID = local
	r, err := m.Client.CreatePR(ctx, p)
	return outCreatePR(m.Info.Key, r), err
}

// MarkViewed implements daemon.Client.
func (c *Client) MarkViewed(ctx context.Context, p proto.ViewedParams) error {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return err
	}
	p.ItemID = local
	return m.Client.MarkViewed(ctx, p)
}

// Override implements daemon.Client.
func (c *Client) Override(ctx context.Context, p proto.OverrideParams) error {
	m, local, err := c.route(p.ItemID)
	if err != nil {
		return err
	}
	p.ItemID = local
	return m.Client.Override(ctx, p)
}
