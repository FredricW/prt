package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
)

// proseOut is the outcome of one prose request (group title, TL;DR).
type proseOut struct {
	text   string // cleaned text; "" when the call failed or said nothing
	key    string // review.RequestKey of the request
	calls  []model.ReviewCall
	cached bool
	err    error
}

// spent sums the cost of the calls actually made (0 for a cache hit).
func (o proseOut) spent() float64 {
	if o.cached {
		return 0
	}
	total := 0.0
	for _, c := range o.calls {
		total += c.Cost
	}
	return total
}

// prose answers a fast-tier prose request from the cache namespace ns, or
// (bypass, or a miss) with one call retried once, then caches the cleaned
// text. A cache hit is logged as a Cached call with what it cost when it
// ran. The caller has checked that the fast reviewer is ready.
func (p *Pipeline) prose(ctx context.Context, ns string, req review.Request, m callMeta, bypass bool, clean func(string) string) proseOut {
	rev := p.d.Reviewers.Fast
	key, err := review.RequestKey(rev.Model(), req)
	if err != nil {
		return proseOut{err: fmt.Errorf("pipeline: %s request key: %w", ns, err)}
	}
	if !bypass {
		if pc, ok := p.loadProse(ctx, ns, key); ok {
			c := pc.Call
			c.Cached, c.Purpose, c.ItemID, c.GroupID = true, m.purpose, m.itemID, m.groupID
			return proseOut{text: pc.Text, key: key, calls: []model.ReviewCall{c}, cached: true}
		}
	}
	out := p.callRetry(ctx, rev, req, m, nil)
	po := proseOut{key: key, calls: out.calls, err: out.err}
	if out.err != nil {
		return po
	}
	po.text = clean(out.res.Text)
	if po.text != "" {
		p.putProse(ctx, ns, key, proseCache{Text: po.text, Call: out.calls[len(out.calls)-1]})
	}
	return po
}

// needsTitle reports whether a group gets a fast-LLM title: its name is
// still a placeholder and it is neither an ungrouped singleton nor a
// mechanical group (DECISIONS "M3 shared contracts": group titles).
func needsTitle(g model.Group) bool {
	return g.NameSource == model.NamePlaceholder && !g.Ungrouped && !g.Mechanical
}

// titleGroups gives every group that needsTitle a three-word fast-tier
// title (review.BuildGroupTitle → CleanTitle), NameSource NameAI. Titles are
// cached by request key (nsTitle); FromStage ≤ cluster re-asks them.
// Without a ready fast reviewer the placeholders stay. Calls and cost go on
// the cluster row; failures are only noted. Only cancellation is returned.
func (p *Pipeline) titleGroups(ctx context.Context, r *run, units []model.ChangeUnit, groups []model.Group, rl *model.RunLog) ([]model.Group, string, error) {
	var idx []int
	for i, g := range groups {
		if needsTitle(g) {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return groups, "", nil
	}
	if err := review.Ready(p.d.Reviewers.Fast); err != nil {
		return groups, fmt.Sprintf("titles: %d placeholders kept (%s)", len(idx), reasonText(err)), nil
	}
	out := append([]model.Group(nil), groups...)
	byID := unitsByID(units)
	bypass := r.opt.ignoresCache(model.StageCluster)
	results := make([]proseOut, len(idx))
	forEachLimit(len(idx), p.concurrency(), func(k int) {
		g := out[idx[k]]
		req := p.fn.buildGroupTitle(g, lookupUnits(byID, g.UnitIDs))
		results[k] = p.prose(ctx, nsTitle, req, callMeta{itemID: r.it.ID, groupID: g.ID, tier: model.TierFast, purpose: purposeTitle}, bypass, p.fn.cleanTitle)
	})
	if err := ctx.Err(); err != nil {
		return groups, "", err
	}
	cached, failed := 0, 0
	for k, i := range idx {
		res := results[k]
		r.calls.add(out[i].ID, res.calls...)
		if !res.cached {
			rl.Calls += len(res.calls)
		}
		rl.Cost += res.spent()
		r.titleCost += res.spent()
		switch {
		case res.cached:
			cached++
		case res.err != nil:
			failed++
			p.d.Log.Warn("group title failed; placeholder kept", "item", r.it.ID, "group", out[i].ID, "err", res.err)
		}
		if res.err == nil && res.text != "" {
			out[i].Name, out[i].NameSource = res.text, model.NameAI
		}
	}
	note := fmt.Sprintf("titles: %d asked, %d cached", len(idx), cached)
	if failed > 0 {
		note += fmt.Sprintf(" (%d failed)", failed)
	}
	return out, note, nil
}

// concurrency is pipeline.concurrency (default 4).
func (p *Pipeline) concurrency() int {
	if n := p.d.Config.Pipeline.Concurrency; n > 0 {
		return n
	}
	return 4
}

// forEachLimit calls fn(0..n-1) with at most limit calls in flight and
// waits for all of them.
func forEachLimit(n, limit int, fn func(int)) {
	if limit <= 0 {
		limit = 1
	}
	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(i)
		}(i)
	}
	wg.Wait()
}

// capRunes cuts s to at most n runes, at a word boundary when one is
// close, ending in "…" when cut. A safety net under review.CleanTLDR so the
// Overview never shows more than the spec's 160 characters.
func capRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	cut := n - 1
	for i := cut; i > n/2; i-- {
		if unicode.IsSpace(r[i]) {
			cut = i
			break
		}
	}
	return strings.TrimRightFunc(string(r[:cut]), func(c rune) bool { return unicode.IsSpace(c) || unicode.IsPunct(c) }) + "…"
}
