package daemon

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/route"
)

// ReviewAction implements Client: it stores the group's routing override
// (deep: D/R, skip: s, reset: back to the rules) and queues a re-run of
// the item. The job is a rerun (it follows a run in flight) but not forced:
// fetch, labels and grouping are cache hits, routing applies the override
// and cached reviews are reused.
func (c *InProcess) ReviewAction(ctx context.Context, p proto.ReviewActionParams) error {
	var tier model.Tier
	switch p.Action {
	case proto.ActionDeep:
		tier = model.TierDeep
	case proto.ActionSkip:
		tier = model.TierNone
	case proto.ActionReset:
	default:
		return fmt.Errorf("daemon: review action %q: unknown action", p.Action)
	}
	if p.GroupID == "" {
		return fmt.Errorf("daemon: review action %s: no group", p.Action)
	}
	it, err := c.d.Store.Item(ctx, p.ItemID)
	if err != nil {
		return fmt.Errorf("daemon: review action %s: %w", p.ItemID, err)
	}
	if err := c.p.SetGroupOverride(ctx, it.ID, p.GroupID, tier); err != nil {
		return fmt.Errorf("daemon: review action %s: %w", p.ItemID, err)
	}
	j := job{itemID: it.ID, urgency: it.Urgency, rerun: true}
	if p.Action == proto.ActionSkip && it.AnalysedSHA != "" && it.AnalysedSHA != it.HeadSHA {
		// A skip asks for less spending: on a Stale item it must not
		// request (and pay for) an analysis of the new head. Automatic, so
		// the strategy decides (DECISIONS "M5.1 review fixes").
		c.q.push(j)
		return nil
	}
	// Explicit: the user acted on this item (D, R, s).
	return c.pushExplicit(ctx, it, j)
}

// Routing implements Client. Without flags it describes the active rules.
// Ensure writes the built-in rules to <gitDir>/prt/prt.routing.toml when no
// routing file exists in the config dirs. Reload re-reads the file: on an
// error the previous rules stay active and Err says why; otherwise every
// analysed item with routing rows that are not done is queued again and
// Rerouted counts those rows.
func (c *InProcess) Routing(ctx context.Context, p proto.RoutingParams) (proto.RoutingInfo, error) {
	if p.Ensure {
		if err := c.ensureRoutingFile(); err != nil {
			return c.routingInfo(), err
		}
	}
	if !p.Reload {
		return c.routingInfo(), nil
	}
	r, err := config.LoadRouting(c.d.ConfigDirs)
	if err == nil {
		err = c.p.SetRouting(r)
	}
	if err != nil {
		info := c.routingInfo()
		info.Err = err.Error()
		c.log.Warn("routing reload failed; previous rules kept", "err", err)
		return info, nil
	}
	n, err := c.reroute(ctx)
	info := c.routingInfo()
	info.Rerouted = n
	return info, err
}

// routingPath is the routing file `e` edits: the active file in the config
// dirs, else prt.routing.toml in the first dir (<gitDir>/prt); "" without
// config dirs.
func (c *InProcess) routingPath() string {
	if p := config.Find(c.d.ConfigDirs, config.RoutingFile); p != "" {
		return p
	}
	if len(c.d.ConfigDirs) == 0 {
		return ""
	}
	return filepath.Join(c.d.ConfigDirs[0], config.RoutingFile)
}

// ensureRoutingFile writes the built-in rules when no routing file exists.
func (c *InProcess) ensureRoutingFile() error {
	if config.Find(c.d.ConfigDirs, config.RoutingFile) != "" {
		return nil
	}
	path := c.routingPath()
	if path == "" {
		return errors.New("daemon: routing: no config directory to write prt.routing.toml to")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("daemon: routing: %w", err)
	}
	if err := os.WriteFile(path, []byte(RoutingTOML(config.DefaultRouting())), 0o644); err != nil {
		return fmt.Errorf("daemon: routing: write %s: %w", path, err)
	}
	return nil
}

// RoutingTOML renders a routing configuration as prt.routing.toml (the
// spec's layout: scalars, then one [[rule]] per rule). config.LoadRouting
// reads it back unchanged.
func RoutingTOML(r config.Routing) string {
	var b strings.Builder
	b.WriteString("# prt routing rules: evaluated per group, top to bottom; the first match wins.\n")
	b.WriteString("# `when` uses label ids, tag.<name>, kind, numbers, && || !, in [...].\n\n")
	fmt.Fprintf(&b, "deep_slots = %d\n", r.DeepSlots)
	fmt.Fprintf(&b, "cost_cap_usd = %s\n", tomlFloat(r.CostCapUSD))
	fmt.Fprintf(&b, "escalate_below = %s\n", tomlFloat(r.EscalateBelow))
	fmt.Fprintf(&b, "calibration_sample = %s\n", tomlFloat(r.CalibrationSample))
	for _, rule := range r.Rules {
		b.WriteString("\n[[rule]]\n")
		fmt.Fprintf(&b, "when = %s\n", strconv.Quote(rule.When))
		fmt.Fprintf(&b, "tier = %s\n", strconv.Quote(rule.Tier))
		if len(rule.Prompts) > 0 {
			fmt.Fprintf(&b, "prompts = %s\n", tomlStrings(rule.Prompts))
		}
		if len(rule.Context) > 0 {
			fmt.Fprintf(&b, "context = %s\n", tomlStrings(rule.Context))
		}
	}
	return b.String()
}

// tomlFloat renders a float so TOML reads it back as a float.
func tomlFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(s, ".eE") {
		s += ".0"
	}
	return s
}

func tomlStrings(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Quote(x)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// routingInfo describes the active rules for the rules panel.
func (c *InProcess) routingInfo() proto.RoutingInfo {
	rs := c.p.Routing()
	cfg := rs.Config
	info := proto.RoutingInfo{
		Path: c.routingPath(), Source: cfg.Source,
		DeepSlots: cfg.DeepSlots, CostCapUSD: cfg.CostCapUSD,
		EscalateBelow: cfg.EscalateBelow, CalibrationSample: cfg.CalibrationSample,
		Warnings: append(append([]string(nil), cfg.Warnings...), rs.Warnings...),
	}
	words := map[int]string{}
	for _, r := range rs.Rules {
		words[r.Index] = r.Words
	}
	for i, r := range cfg.Rules {
		rr := proto.RoutingRule{Index: i + 1, When: r.When, Tier: r.Tier, Prompts: r.Prompts, Context: r.Context, Words: words[i+1]}
		if _, err := route.Parse(r.When); err != nil {
			rr.Err = err.Error()
		}
		if rr.Words == "" {
			rr.Words = "if " + r.When + " → " + r.Tier
		}
		info.Rules = append(info.Rules, rr)
	}
	return info
}

// reroute queues every analysed item with an unreviewed routing row
// (queued over the cap, failed, skipped, or routed none by a rule) for a
// re-run that keeps the decisions of its done rows (RunOptions
// KeepReviewed: "saving re-routes unreviewed groups"), and returns how many
// such rows there were. Rows the user decided (D, s) are not counted: an
// override outranks the rules. The jobs are automatic: the strategy gates
// them when they start (an item it does not allow is only re-aggregated).
func (c *InProcess) reroute(ctx context.Context) (int, error) {
	items, err := c.d.Store.Items(ctx)
	if err != nil {
		return 0, fmt.Errorf("daemon: reroute: %w", err)
	}
	total := 0
	var jobs []job
	for _, it := range items {
		if it.AnalysedSHA == "" {
			continue
		}
		rows, err := c.d.Store.ItemReviews(ctx, it.ID)
		if err != nil {
			return total, fmt.Errorf("daemon: reroute %s: %w", it.ID, err)
		}
		n := 0
		for _, r := range rows {
			if r.Status != model.ReviewDone && !r.Forced && !r.UserSkipped {
				n++
			}
		}
		if n > 0 {
			total += n
			jobs = append(jobs, job{itemID: it.ID, urgency: it.Urgency, rerun: true, keepReviewed: true})
		}
	}
	c.q.push(jobs...)
	return total, nil
}

// CallLog implements Client.
func (c *InProcess) CallLog(ctx context.Context, p proto.CallLogParams) ([]model.ReviewCall, error) {
	if p.ItemID == "" {
		return nil, errors.New("daemon: call log: no item")
	}
	calls, err := c.p.CallLog(ctx, p.ItemID, p.GroupID)
	if err != nil {
		return nil, fmt.Errorf("daemon: call log: %w", err)
	}
	return calls, nil
}

// SetCap implements Client: it sets the item's review cost cap (0 restores
// prt.routing.toml's cost_cap_usd), emits the item, and queues a re-run so
// groups queued over the old cap are reviewed (cached reviews are reused).
func (c *InProcess) SetCap(ctx context.Context, p proto.CapParams) error {
	if p.CapUSD < 0 || math.IsNaN(p.CapUSD) || math.IsInf(p.CapUSD, 0) {
		return fmt.Errorf("daemon: set cap %s: invalid cap %v", p.ItemID, p.CapUSD)
	}
	it, ok, err := c.updateItem(ctx, p.ItemID, func(it *model.Item) { it.CapUSD = p.CapUSD })
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("daemon: set cap %s: unknown item", p.ItemID)
	}
	// Explicit: the user acted on this item (:cap).
	return c.pushExplicit(ctx, it, job{itemID: it.ID, urgency: it.Urgency, rerun: true})
}
