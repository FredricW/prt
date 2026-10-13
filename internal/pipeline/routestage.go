package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// Fresh asks for one deep review that bypasses the review cache (D, R:
	// "re-review this group on the deep tier"); stage 7 clears it once the
	// fresh review is done.
	Fresh bool `json:"fresh,omitempty"`
}

// SetGroupOverride stores a user's routing override for a group of an
// item: TierDeep forces the deep tier (D, R) and asks for one fresh deep
// review (a cached deep review is not served for it), TierNone skips the
// review (s) and "" drops the override (back to the rules). It applies
// from the next run's route stage. Overrides are keyed by group id, which
// is the hash of the group's units: a group whose membership changes is
// routed by the rules again.
func (p *Pipeline) SetGroupOverride(ctx context.Context, itemID, groupID string, tier model.Tier) error {
	switch tier {
	case model.TierDeep, model.TierNone, "":
	default:
		return fmt.Errorf("pipeline: override %s: unsupported tier %q", groupID, tier)
	}
	if itemID == "" || groupID == "" {
		return errors.New("pipeline: override: item and group ids required")
	}
	p.overrideMu.Lock()
	defer p.overrideMu.Unlock()
	return p.putOverride(ctx, itemID, groupID, groupOverride{Tier: tier, At: p.d.Now(), Fresh: tier == model.TierDeep})
}

func (p *Pipeline) putOverride(ctx context.Context, itemID, groupID string, o groupOverride) error {
	b, err := json.Marshal(o)
	if err != nil {
		return fmt.Errorf("pipeline: override %s: %w", groupID, err)
	}
	if err := p.d.Store.CachePut(ctx, nsOverride, rowKey(itemID, groupID), b); err != nil {
		return fmt.Errorf("pipeline: store override %s: %w", groupID, err)
	}
	return nil
}

// clearFresh marks a group's deep override as served: later runs use the
// review cache again. It is a compare-and-set under overrideMu: only a
// deep override that still asks for a fresh review is rewritten, so a skip
// or reset stored meanwhile (s, :tier) survives.
func (p *Pipeline) clearFresh(ctx context.Context, itemID, groupID string) {
	p.overrideMu.Lock()
	defer p.overrideMu.Unlock()
	o, ok := p.loadOverride(ctx, itemID, groupID)
	if !ok || o.Tier != model.TierDeep || !o.Fresh {
		return
	}
	o.Fresh = false
	if err := p.putOverride(ctx, itemID, groupID, o); err != nil {
		p.d.Log.Warn("clear fresh override failed", "item", itemID, "group", groupID, "err", err)
	}
}

func (p *Pipeline) loadOverride(ctx context.Context, itemID, groupID string) (groupOverride, bool) {
	b, ok, err := p.d.Store.CacheGet(ctx, nsOverride, rowKey(itemID, groupID))
	var o groupOverride
	if err != nil || !ok || json.Unmarshal(b, &o) != nil {
		return groupOverride{}, false
	}
	return o, true
}

// groupOverrides loads the overrides of the item's groups (group id →
// tier deep or none; reset or absent groups are left out) and the groups
// whose deep override still asks for a fresh review.
func (p *Pipeline) groupOverrides(ctx context.Context, itemID string, groups []model.Group) (map[string]model.Tier, map[string]bool, error) {
	out, fresh := map[string]model.Tier{}, map[string]bool{}
	for _, g := range groups {
		b, ok, err := p.d.Store.CacheGet(ctx, nsOverride, rowKey(itemID, g.ID))
		if err != nil {
			return nil, nil, fmt.Errorf("pipeline: load override %s: %w", g.ID, err)
		}
		var o groupOverride
		if !ok || json.Unmarshal(b, &o) != nil || o.Tier == "" {
			continue
		}
		out[g.ID] = o.Tier
		if o.Tier == model.TierDeep && o.Fresh {
			fresh[g.ID] = true
		}
	}
	return out, fresh, nil
}

// userFlags says which user override decided a group.
type userFlags struct{ forced, skipped bool }

// applyUserTiers applies the overrides to the decisions: deep → tier deep
// (the rule's prompts and context kept; a group the rules sent to none,
// which carries neither, gets auto's stage-6 context and fired prompts),
// "forced deep (D)"; none → tier none without prompts or context, "skipped
// by user (s)". Either way no rule decided (RuleIndex 0).
func applyUserTiers(decs []route.Decision, ovs map[string]model.Tier, auto func(groupID string) (ctx, prompts []string)) ([]route.Decision, map[string]userFlags) {
	flags := map[string]userFlags{}
	out := make([]route.Decision, len(decs))
	for i, d := range decs {
		switch ovs[d.GroupID] {
		case model.TierDeep:
			if d.Tier == model.TierNone && d.Context == nil && d.Prompts == nil && auto != nil {
				d.Context, d.Prompts = auto(d.GroupID)
			}
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
	// fresh are the groups a user asked to re-review on deep (D, R): their
	// routed deep review skips the review cache once.
	fresh map[string]bool
	// groups and units are the item's current groups and units; prevStale
	// are the groups whose routing row was stale-labels before this run
	// (stage 7 keeps that status on a cached review; see keepStale).
	groups    []model.Group
	units     []model.ChangeUnit
	prevStale map[string]bool
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
	ovs, fresh, err := p.groupOverrides(ctx, r.it.ID, groups)
	if err != nil {
		return nil, err
	}
	rules := p.Routing()
	byGroup := make(map[string]route.GroupFacts, len(facts))
	for _, f := range facts {
		byGroup[f.Group.ID] = f
	}
	decs := p.fn.route(rules, facts)
	if r.opt.KeepReviewed && !r.opt.ignoresCache(model.StageReview) {
		decs = p.keepReviewed(ctx, r.it.ID, decs, ovs)
	}
	decs, flags := applyUserTiers(decs, ovs, func(id string) ([]string, []string) {
		return route.AutoContext(byGroup[id]), route.AutoPrompts(byGroup[id])
	})
	ro := &routeOut{
		rules: rules, capUSD: capFor(in.Item, rules.Config),
		inputs: map[string]review.GroupInput{}, reqs: map[string]review.Request{}, fresh: fresh,
		groups: groups, units: a.parsed.units, prevStale: p.staleGroups(ctx, r.it.ID),
	}
	for _, d := range decs {
		f, ok := byGroup[d.GroupID]
		if !ok || d.Tier == model.TierNone {
			continue
		}
		units, aggs, labels := reviewUnits(f)
		gi := review.GroupInput{
			Item: in.Item, Group: f.Group, Units: units, Aggs: aggs, Labels: labels,
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
	ro.rows = routingRows(r.it.ID, groups, ro.plan, flags, ro.inputs)
	if err := p.d.Store.PutItemReviews(ctx, r.it.ID, ro.rows); err != nil {
		return nil, fmt.Errorf("pipeline: store routing rows: %w", err)
	}
	rl.In, rl.Out = len(groups), len(ro.inputs)
	rl.Note = routeNote(ro.plan, flags)
	return ro, nil
}

// keptWords marks a routing row whose decision a rules reload kept.
const keptWords = " (kept: reviewed before the rules changed)"

// keepReviewed replaces the decision of every group whose stored routing
// row is done (and that the user did not override) with that row's
// decision: its routed tier (fast for an escalated row, so the escalation
// replays from the cache), rule, words, prompts and context. Other groups
// keep the new rules' decision. A store error keeps every new decision.
func (p *Pipeline) keepReviewed(ctx context.Context, itemID string, decs []route.Decision, ovs map[string]model.Tier) []route.Decision {
	prev, err := p.d.Store.ItemReviews(ctx, itemID)
	if err != nil {
		p.d.Log.Warn("load routing rows failed; re-routing every group", "item", itemID, "err", err)
		return decs
	}
	done := make(map[string]model.Review, len(prev))
	for _, row := range prev {
		if row.Status == model.ReviewDone && row.Tier != model.TierNone {
			done[row.GroupID] = row
		}
	}
	out := make([]route.Decision, len(decs))
	for i, d := range decs {
		row, ok := done[d.GroupID]
		if ok && ovs[d.GroupID] == "" {
			tier := row.Tier
			if row.EscalatedOf != nil {
				tier = *row.EscalatedOf
			}
			words := row.RuleWords
			if !strings.HasSuffix(words, keptWords) {
				words += keptWords
			}
			ctxKinds := append(slices.Clone(row.Context), row.ContextNone...)
			d = route.Decision{
				// RuleIndex 0: the index named a rule of the old file.
				GroupID: d.GroupID, Tier: tier, Rule: row.Rule, Words: words,
				Prompts: slices.Clone(row.Prompts), Context: ctxKinds, HotSpot: d.HotSpot,
			}
		}
		out[i] = d
	}
	return out
}

// estimate is the pre-call cost bound of a group's routed review: 0 for
// tier none, else review.PlanCost of its request on the tier's model —
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
	return review.PlanCost(p.tierModel(tier), req)
}

// cachedReview returns the finished cached review of a group on a tier
// when it was produced the way this run would ask: the tier's current
// model, the current review prompt version and the same specialist
// prompts. Anything else is a miss (a new call replaces it).
func (p *Pipeline) cachedReview(ctx context.Context, groupID string, tier model.Tier, prompts []string) (model.Review, bool) {
	rv, ok, err := p.d.Store.Review(ctx, groupID, tier)
	if err != nil || !ok || rv.Status != model.ReviewDone {
		return model.Review{}, false
	}
	if rv.Prompt != review.PromptReviewV1 || !sameModel(rv.Model, p.tierModel(tier)) || !slices.Equal(sortedCopy(rv.Prompts), sortedCopy(prompts)) {
		return model.Review{}, false
	}
	return rv, true
}

// sameModel compares model names with or without the "provider:" prefix;
// an empty name (a review cached without one) matches.
func sameModel(a, b string) bool {
	if a == "" || b == "" {
		return true
	}
	strip := func(s string) string {
		if _, name, ok := strings.Cut(s, ":"); ok {
			return name
		}
		return s
	}
	return strip(a) == strip(b)
}

// sortedCopy is a sorted copy of ss (nil for empty).
func sortedCopy(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	out := slices.Clone(ss)
	slices.Sort(out)
	return out
}

// reviewUnits are the units (with their aggregates and labels) a group's
// review request carries: every member, except for a mechanical group,
// whose prompt says only the example is shown — there it is the example
// unit alone (Group.ExampleUnit, else the first member).
func reviewUnits(f route.GroupFacts) ([]model.ChangeUnit, []model.UnitAgg, []model.Label) {
	if !f.Group.Mechanical || len(f.Units) <= 1 {
		return f.Units, f.Aggs, f.Labels
	}
	k := 0
	for i, u := range f.Units {
		if u.ID == f.Group.ExampleUnit {
			k = i
			break
		}
	}
	ex := f.Units[k]
	var aggs []model.UnitAgg
	if k < len(f.Aggs) {
		aggs = []model.UnitAgg{f.Aggs[k]}
	}
	var labels []model.Label
	for _, l := range f.Labels {
		if l.UnitID == ex.ID {
			labels = append(labels, l)
		}
	}
	return []model.ChangeUnit{ex}, aggs, labels
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

// attachedContext splits the decided context kinds into those whose
// blocks the request carries and those that found nothing to attach.
func attachedContext(decided []string, blocks []model.ContextBlock) (attached, none []string) {
	has := map[string]bool{}
	for _, b := range blocks {
		has[b.Kind] = true
	}
	for _, k := range decided {
		if has[k] {
			attached = append(attached, k)
		} else {
			none = append(none, k)
		}
	}
	return attached, none
}

// routingRows builds the routing table in group order from the plan: tier
// none rows are skipped (never run), the others queued, over-cap ones with
// their queue note. Context lists the kinds whose blocks inputs' request
// carries, ContextNone the decided kinds that found nothing.
func routingRows(itemID string, groups []model.Group, plan []route.Planned, flags map[string]userFlags, inputs map[string]review.GroupInput) []model.Review {
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
		if gi, ok := inputs[g.ID]; ok {
			row.Context, row.ContextNone = attachedContext(pl.Context, gi.Context)
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
