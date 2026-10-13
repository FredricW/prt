package workspace

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/fredricw/prt/internal/proto"
)

// noteNoReload is a repo's Applied part after a global save when its client
// cannot reload settings live.
const noteNoReload = "applies after restart"

// Routing implements daemon.Client: the routing of p.Repo's repo (Resolve),
// or of the first repo when p.Repo is "".
func (c *Client) Routing(ctx context.Context, p proto.RoutingParams) (proto.RoutingInfo, error) {
	m, err := c.memberFor(p.Repo)
	if err != nil {
		return proto.RoutingInfo{}, err
	}
	p.Repo = ""
	return m.Client.Routing(ctx, p)
}

// Settings implements daemon.Client: the settings of p.Repo's repo
// (Resolve), or of the first repo when p.Repo is "", with Repo, RepoLabel
// and Workspace set.
func (c *Client) Settings(ctx context.Context, p proto.SettingsParams) (proto.SettingsInfo, error) {
	m, err := c.memberFor(p.Repo)
	if err != nil {
		return proto.SettingsInfo{}, err
	}
	p.Repo = ""
	info, err := m.Client.Settings(ctx, p)
	return c.withRepo(m, info), err
}

// SaveSettings implements daemon.Client. A repo-scope save writes p.Repo's
// repo (the first when ""). A global save is made through that repo too
// and then reloaded by every other repo that can (ReloadSettings), so the
// global file applies to every repo that reads it (a repo with its own
// prt.toml does not, and its reload warns that the file shadows the global
// one); its Applied reads "<label>: <applied>" for every repo, joined by
// "; ", and its warnings are merged (the other repos' prefixed with their
// label). The result is the saving repo's info;
// the error joins the save's and the reloads' failures.
func (c *Client) SaveSettings(ctx context.Context, p proto.SaveSettingsParams) (proto.SettingsInfo, error) {
	m, err := c.memberFor(p.Repo)
	if err != nil {
		return proto.SettingsInfo{}, err
	}
	p.Repo = ""
	info, err := m.Client.SaveSettings(ctx, p)
	info = c.withRepo(m, info)
	if p.Scope != proto.ScopeGlobal || err != nil {
		return info, err
	}
	return c.reloadOthers(ctx, m, info)
}

// reloadOthers applies a global save made through saved to every other
// member and merges their outcome into info.
func (c *Client) reloadOthers(ctx context.Context, saved Member, info proto.SettingsInfo) (proto.SettingsInfo, error) {
	others := make([]Member, 0, len(c.members))
	for _, m := range c.members {
		if m.Info.Key != saved.Info.Key {
			others = append(others, m)
		}
	}
	type reload struct {
		info proto.SettingsInfo
		ok   bool // the member can reload
	}
	out := fanOut(ctx, others, 0, func(ctx context.Context, m Member) (reload, error) {
		r, ok := m.Client.(settingsReloader)
		if !ok {
			return reload{}, nil
		}
		ri, err := r.ReloadSettings(ctx)
		return reload{info: ri, ok: true}, err
	})
	applied := []string{label(saved) + ": " + info.Applied}
	warnings := slices.Clone(info.Warnings)
	var errs []error
	for i, m := range others {
		lbl := label(m)
		if err := out.err[i]; err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", lbl, err))
			applied = append(applied, lbl+": reload failed")
			continue
		}
		r := out.vals[i]
		if !r.ok {
			applied = append(applied, lbl+": "+noteNoReload)
			continue
		}
		applied = append(applied, lbl+": "+r.info.Applied)
		for _, w := range r.info.Warnings {
			warnings = append(warnings, lbl+": "+w)
		}
	}
	info.Applied = strings.Join(applied, "; ")
	info.Warnings = warnings
	return info, errors.Join(errs...)
}

// withRepo names m's repo and the workspace in a settings result.
func (c *Client) withRepo(m Member, info proto.SettingsInfo) proto.SettingsInfo {
	info.Repo, info.RepoLabel, info.Workspace = m.Info.Key, label(m), c.root
	return info
}
