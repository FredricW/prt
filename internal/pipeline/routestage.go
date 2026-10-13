package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/route"
)

// Routing-table words for user overrides (model.Review.RuleWords).
const (
	wordsForcedDeep = "forced deep (D)"
	wordsSkipped    = "skipped by user (s)"
)

// initialRules compiles r; when it does not compile, the built-in rules
// are used (and, should even those fail, an empty rule set carrying r's
// scalars).
func (p *Pipeline) initialRules(r config.Routing) route.Rules {
	rs, err := p.fn.compile(r)
	if err == nil {
		return rs
	}
	p.d.Log.Warn("routing rules do not compile; using the built-in rules", "source", r.Source, "err", err)
	if def, derr := p.fn.compile(config.DefaultRouting()); derr == nil {
		return def
	}
	return route.Rules{Config: r}
}

// SetRouting compiles r and makes it the active routing configuration for
// every run that starts routing afterwards. On a compile error the active
// rules are kept and the error is returned.
func (p *Pipeline) SetRouting(r config.Routing) error {
	if len(r.Rules) == 0 {
		return errors.New("pipeline: routing: no rules")
	}
	rs, err := p.fn.compile(r)
	if err != nil {
		return fmt.Errorf("pipeline: routing %s: %w", r.Source, err)
	}
	p.rulesMu.Lock()
	p.rules = rs
	p.rulesMu.Unlock()
	return nil
}

// Routing returns the active compiled rules; Config is the routing file
// they came from (Source a path or "builtin").
func (p *Pipeline) Routing() route.Rules {
	p.rulesMu.RLock()
	defer p.rulesMu.RUnlock()
	return p.rules
}

// groupOverride is a user's tier override of one group (nsOverride).
type groupOverride struct {
	// Tier is deep (Pipeline page D, Code page R), none (s) or "" (reset).
	Tier model.Tier `json:"tier"`
	At   time.Time  `json:"at"`
}

// SetGroupOverride stores a user's routing override for a group of an
// item: TierDeep forces the deep tier (D, R), TierNone skips the review
// (s) and "" drops the override (back to the rules). It applies from the
// next run's route stage. Overrides are keyed by group id, which is the
// hash of the group's units: a group whose membership changes is routed by
// the rules again.
func (p *Pipeline) SetGroupOverride(ctx context.Context, itemID, groupID string, tier model.Tier) error {
	switch tier {
	case model.TierDeep, model.TierNone, "":
	default:
		return fmt.Errorf("pipeline: override %s: unsupported tier %q", groupID, tier)
	}
	if itemID == "" || groupID == "" {
		return errors.New("pipeline: override: item and group ids required")
	}
	b, err := json.Marshal(groupOverride{Tier: tier, At: p.d.Now()})
	if err != nil {
		return fmt.Errorf("pipeline: override %s: %w", groupID, err)
	}
	if err := p.d.Store.CachePut(ctx, nsOverride, rowKey(itemID, groupID), b); err != nil {
		return fmt.Errorf("pipeline: store override %s: %w", groupID, err)
	}
	return nil
}

// groupOverrides loads the overrides of the item's groups (group id →
// tier deep or none; reset or absent groups are left out).
func (p *Pipeline) groupOverrides(ctx context.Context, itemID string, groups []model.Group) (map[string]model.Tier, error) {
	out := map[string]model.Tier{}
	for _, g := range groups {
		b, ok, err := p.d.Store.CacheGet(ctx, nsOverride, rowKey(itemID, g.ID))
		if err != nil {
			return nil, fmt.Errorf("pipeline: load override %s: %w", g.ID, err)
		}
		var o groupOverride
		if !ok || json.Unmarshal(b, &o) != nil || o.Tier == "" {
			continue
		}
		out[g.ID] = o.Tier
	}
	return out, nil
}

// userFlags says which user override decided a group.
type userFlags struct{ forced, skipped bool }

// applyUserTiers applies the overrides to the decisions: deep → tier deep
// (the rule's prompts and context kept), "forced deep (D)"; none → tier
// none without prompts or context, "skipped by user (s)". Either way no
// rule decided (RuleIndex 0).
func applyUserTiers(decs []route.Decision, ovs map[string]model.Tier) ([]route.Decision, map[string]userFlags) {
	flags := map[string]userFlags{}
	out := make([]route.Decision, len(decs))
	for i, d := range decs {
		switch ovs[d.GroupID] {
		case model.TierDeep:
			d.Tier, d.RuleIndex, d.Rule, d.Words, d.Unknown = model.TierDeep, 0, "", wordsForcedDeep, false
			flags[d.GroupID] = userFlags{forced: true}
		case model.TierNone:
			d.Tier, d.RuleIndex, d.Rule, d.Words, d.Unknown = model.TierNone, 0, "", wordsSkipped, false
			d.Prompts, d.Context = nil, nil
			flags[d.GroupID] = userFlags{skipped: true}
		}
		out[i] = d
	}
	return out, flags
}

// routeOut is the outcome of stage 6 as stage 7 consumes it.
type routeOut struct {
	// failed is set when stage 6 failed: nothing is reviewed.
	failed bool
	rules  route.Rules
	// rows is the routing table in group order (Status queued; skipped for
	// tier none); plan is the dispatch order.
	rows []model.Review
	plan []route.Planned
	// inputs are the review inputs of the non-none groups at their routed
	// tier and reqs their routed requests, by group id.
	inputs map[string]review.GroupInput
	reqs   map[string]review.Request
	// capUSD is the effective per-item cap; reserved is what the plan
	// counted against it (Spent plus the estimates of planned calls), where
	// escalations and calibration samples start.
	capUSD   float64
	reserved float64
}

// capFor is the effective per-item cost cap: Item.CapUSD, else the
// routing file's cost_cap_usd.
func capFor(it model.Item, r config.Routing) float64 {
	if it.CapUSD > 0 {
		return it.CapUSD
	}
	return r.CostCapUSD
}

// routeStage is stage 6: route.Route over the groups' aggregate labels
// (from a pre-aggregate pass without reviews), the user's overrides,
// context blocks (route.Gather), review requests, and route.Plan under the
// cost cap with estimates (cached reviews included, see estimate). It
// stores the routing table (queued rows) and returns what stage 7 needs.
func (p *Pipeline) routeStage(ctx context.Context, r *run, a analysis, rl *model.RunLog) (*routeOut, error) {
	in, err := p.aggInput(ctx, r, a)
	if err != nil {
		return nil, err
	}
	pre := p.fn.aggregate(in)
	groups := a.gro.groups
	facts := groupFacts(groups, a.parsed.units, pre, in.Labels, a.jo.missing)
	ovs, err := p.groupOverrides(ctx, r.it.ID, groups)
	if err != nil {
		return nil, err
	}
	rules := p.Routing()
	decs, flags := applyUserTiers(p.fn.route(rules, facts), ovs)
	ro := &routeOut{
		rules: rules, capUSD: capFor(in.Item, rules.Config),
		inputs: map[string]review.GroupInput{}, reqs: map[string]review.Request{},
	}
	byGroup := make(map[string]route.GroupFacts, len(facts))
	for _, f := range facts {
		byGroup[f.Group.ID] = f
	}
	for _, d := range decs {
		f, ok := byGroup[d.GroupID]
		if !ok || d.Tier == model.TierNone {
			continue
		}
		gi := review.GroupInput{
			Item: in.Item, Group: f.Group, Units: f.Units, Aggs: f.Aggs, Labels: f.Labels,
			Context: p.fn.gather(d.Context, f.Group, a.parsed.units, a.gro.links, pre.Units),
			Prompts: d.Prompts, Tier: d.Tier,
		}
		ro.inputs[d.GroupID] = gi
		ro.reqs[d.GroupID] = p.fn.buildReview(gi)
	}
	ro.plan = p.fn.plan(route.PlanInput{
		Decisions: decs, CapUSD: ro.capUSD, Spent: r.titleCost,
		Estimate: func(d route.Decision) float64 { return p.estimate(ro, d.GroupID, d.Tier) },
	})
	ro.reserved = r.titleCost
	for _, pl := range ro.plan {
		if !pl.OverCap {
			ro.reserved += pl.EstCost
		}
	}
	ro.rows = routingRows(r.it.ID, groups, ro.plan, flags)
	if err := p.d.Store.PutItemReviews(ctx, r.it.ID, ro.rows); err != nil {
		return nil, fmt.Errorf("pipeline: store routing rows: %w", err)
	}
	rl.In, rl.Out = len(groups), len(ro.inputs)
	rl.Note = routeNote(ro.plan, flags)
	return ro, nil
}

// estimate is the pre-call cost bound of a group's routed review: 0 for
// tier none, else review.EstimateCost of its request on the tier's model —
// also when the review is cached. The cap bounds the item's estimated
// review cost, not this run's spend, so a re-run plans exactly like the run
// that filled the cache: a refresh does not quietly review the over-cap
// groups (only `$` does), and replays do not depend on the store's history
// (DECISIONS "M3 integration").
func (p *Pipeline) estimate(ro *routeOut, groupID string, tier model.Tier) float64 {
	req, ok := ro.reqs[groupID]
	if !ok || tier == model.TierNone {
		return 0
	}
	return review.EstimateCost(p.tierModel(tier), req)
}

// cachedReview returns the finished cached review of a group on a tier.
func (p *Pipeline) cachedReview(ctx context.Context, groupID string, tier model.Tier) (model.Review, bool) {
	rv, ok, err := p.d.Store.Review(ctx, groupID, tier)
	if err != nil || !ok || rv.Status != model.ReviewDone {
		return model.Review{}, false
	}
	return rv, true
}

// groupFacts builds route.GroupFacts per group: units in group order, the
// matching unit aggregates of the pre-aggregate (nil when it has none),
// the units' unit-scope labels and the group aggregate.
func groupFacts(groups []model.Group, units []model.ChangeUnit, pre model.ItemAgg, labels []model.Label, missing bool) []route.GroupFacts {
	byID := unitsByID(units)
	uaggs := make(map[string]model.UnitAgg, len(pre.Units))
	for _, a := range pre.Units {
		uaggs[a.UnitID] = a
	}
	gaggs := make(map[string]model.GroupAgg, len(pre.Groups))
	for _, g := range pre.Groups {
		gaggs[g.GroupID] = g
	}
	byUnit := map[string][]model.Label{}
	for _, l := range labels {
		if l.Line == 0 {
			byUnit[l.UnitID] = append(byUnit[l.UnitID], l)
		}
	}
	out := make([]route.GroupFacts, 0, len(groups))
	for _, g := range groups {
		f := route.GroupFacts{Group: g, Units: lookupUnits(byID, g.UnitIDs), GroupAgg: gaggs[g.ID], LabelsMissing: missing}
		if f.GroupAgg.GroupID == "" {
			f.GroupAgg.GroupID = g.ID
		}
		for _, u := range f.Units {
			if len(uaggs) > 0 {
				a, ok := uaggs[u.ID]
				if !ok {
					a = model.UnitAgg{UnitID: u.ID, Path: u.Path}
				}
				f.Aggs = append(f.Aggs, a)
			}
			f.Labels = append(f.Labels, byUnit[u.ID]...)
		}
		out = append(out, f)
	}
	return out
}

// routingRows builds the routing table in group order from the plan: tier
// none rows are skipped (never run), the others queued, over-cap ones with
// their queue note.
func routingRows(itemID string, groups []model.Group, plan []route.Planned, flags map[string]userFlags) []model.Review {
	byGroup := make(map[string]route.Planned, len(plan))
	for _, pl := range plan {
		byGroup[pl.GroupID] = pl
	}
	rows := make([]model.Review, 0, len(groups))
	for _, g := range groups {
		pl, ok := byGroup[g.ID]
		if !ok {
			pl = route.Planned{Decision: route.Decision{GroupID: g.ID, Tier: model.TierNone, Words: "not routed"}}
		}
		f := flags[g.ID]
		row := model.Review{
			GroupID: g.ID, ItemID: itemID, GroupName: g.Name, Tier: pl.Tier,
			Rule: pl.Rule, RuleIndex: pl.RuleIndex, RuleWords: pl.Words,
			Context: pl.Context, Prompts: pl.Prompts, HotSpot: pl.HotSpot, EstCost: pl.EstCost,
			Forced: f.forced, UserSkipped: f.skipped, Status: model.ReviewQueued,
		}
		switch {
		case pl.Tier == model.TierNone:
			row.Status = model.ReviewSkipped
		case pl.OverCap:
			row.Prompt = review.PromptReviewV1
			row.QueueNote = pl.QueueNote
			if row.QueueNote == "" {
				row.QueueNote = fmt.Sprintf("over cost cap: est $%.2f", pl.EstCost)
			}
		default:
			row.Prompt = review.PromptReviewV1
		}
		rows = append(rows, row)
	}
	return rows
}

// routeNote is the route row's note: "N deep · N fast · N none" plus
// over-cap, labels-unknown, user-override and rule-error counts.
func routeNote(plan []route.Planned, flags map[string]userFlags) string {
	tiers := map[model.Tier]int{}
	overCap, unknown, forced, skipped := 0, 0, 0, 0
	errs := map[string]bool{}
	for _, pl := range plan {
		tiers[pl.Tier]++
		if pl.OverCap {
			overCap++
		}
		if pl.Unknown {
			unknown++
		}
		if pl.Err != "" {
			errs[pl.Err] = true
		}
	}
	for _, f := range flags {
		if f.forced {
			forced++
		}
		if f.skipped {
			skipped++
		}
	}
	parts := []string{fmt.Sprintf("%d deep · %d fast · %d none", tiers[model.TierDeep], tiers[model.TierFast], tiers[model.TierNone])}
	for _, c := range []struct {
		n    int
		what string
	}{{overCap, "over cap"}, {unknown, "labels unknown → deep"}, {forced, "forced deep"}, {skipped, "skipped by user"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	if len(errs) > 0 {
		es := make([]string, 0, len(errs))
		for e := range errs {
			es = append(es, e)
		}
		sort.Strings(es)
		parts = append(parts, "rule errors: "+strings.Join(es, "; "))
	}
	return strings.Join(parts, " · ")
}
