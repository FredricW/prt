package pipeline

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/review"
	"github.com/fredricw/prt/internal/route"
)

// Queue notes written by stage 7.
const (
	noteEscalationOverCap = "escalation over cost cap"
	noteEscalationFailed  = "escalation failed: "
)

// IsProgress reports whether a RunLog row is a live progress update of a
// running stage (stage 7 emits one per row change, "k/n reviewed", with
// In = rows to review and Out = rows finished) rather than the stage's own
// start row (In 0). Progress rows are emitted only, never stored in the
// run log; hosts refresh the routing table on them.
func IsProgress(rl model.RunLog) bool {
	return rl.Status == model.StageRunning && rl.In > 0
}

// reviewOut is the outcome of stage 7 as stage 8 consumes it.
type reviewOut struct {
	// rows are the final routing rows (nil when the stage did not run).
	rows []model.Review
	// elapsedMs is the stage's wall time; note explains a degraded stage
	// ("reviews skipped: PRT_ANTHROPIC_API_KEY not set").
	elapsedMs int64
	note      string
}

// reviewJob is one review call to make.
type reviewJob struct {
	groupID string
	tier    model.Tier
	purpose string
	req     review.Request
	est     float64
}

// reviewResult is a finished (or failed) review of one group on one tier.
type reviewResult struct {
	findings      []model.Finding
	verdict       string
	conf          float64
	model         string
	inTok, outTok int
	ms            int64
	cost          float64
	attempts      int
	err           error
}

// fromCache turns a cached review into a result.
func fromCache(c model.Review) reviewResult {
	return reviewResult{
		findings: c.Findings, verdict: c.Verdict, conf: c.Confidence, model: c.Model,
		inTok: c.InputTokens, outTok: c.OutputTokens, ms: c.Ms, cost: c.Cost, attempts: c.Attempts,
	}
}

// reviewRun is the state of one stage-7 run.
type reviewRun struct {
	p           *Pipeline
	r           *run
	ro          *routeOut
	ignoreCache bool
	start       time.Time

	mu        sync.Mutex
	rows      []model.Review
	idx       map[string]int // group id → row
	routed    int            // non-none rows
	calls     int
	cost      float64
	cacheHits int
	disagreed int
	prev      map[string][]model.ReviewCall // previous call logs, by group
}

// reviewStage is stage 7: one LLM call per routed group in plan order (fast
// calls up to pipeline.concurrency, deep calls up to deep_slots at once),
// cached reviews reused, one retry per call; then, sequentially in plan
// order, escalations (fast confidence < escalate_below → deep) and
// calibration samples reserve from the remaining budget and run. A reviewer
// that cannot run (no key) skips its rows with a note. Rows are stored and
// a progress row emitted on every change.
func (p *Pipeline) reviewStage(ctx context.Context, r *run, ro *routeOut, rl *model.RunLog) (reviewOut, error) {
	if ro.failed {
		skipStage(rl, "routing failed")
		return reviewOut{}, nil
	}
	rr := p.newReviewRun(r, ro)
	rl.In = rr.routed
	if rr.routed == 0 {
		rl.Note = "nothing to review"
		return reviewOut{rows: rr.snapshot()}, nil
	}
	fastErr, deepErr := review.Ready(p.d.Reviewers.Fast), review.Ready(p.d.Reviewers.Deep)
	rr.phase1(ctx, fastErr, deepErr)
	if ctx.Err() == nil && fastErr == nil && deepErr == nil {
		rr.phase2(ctx)
	}
	out := reviewOut{elapsedMs: p.d.Now().Sub(rr.start).Milliseconds()}
	if fastErr != nil {
		out.note = skipReason(fastErr)
	}
	rr.finalise()
	out.rows = rr.snapshot()
	if err := ctx.Err(); err != nil {
		rr.persist(context.WithoutCancel(ctx))
		return out, err
	}
	if err := p.d.Store.PutItemReviews(ctx, r.it.ID, out.rows); err != nil {
		return out, fmt.Errorf("pipeline: store routing rows: %w", err)
	}
	if err := r.calls.flush(ctx, p.d.Store, r.it.ID, rowIDs(out.rows)); err != nil {
		p.d.Log.Warn("store call log failed", "item", r.it.ID, "err", err)
	}
	rr.fillRunLog(rl, out)
	if fastErr != nil {
		skipStage(rl, joinNotes(out.note, rl.Note))
	}
	return out, nil
}

func (p *Pipeline) newReviewRun(r *run, ro *routeOut) *reviewRun {
	rr := &reviewRun{
		p: p, r: r, ro: ro, ignoreCache: r.opt.ignoresCache(model.StageReview), start: p.d.Now(),
		rows: append([]model.Review(nil), ro.rows...), idx: map[string]int{}, prev: map[string][]model.ReviewCall{},
	}
	for i, row := range rr.rows {
		rr.idx[row.GroupID] = i
		if row.Tier != model.TierNone {
			rr.routed++
		}
	}
	return rr
}

// rowIDs are the group ids of rows.
func rowIDs(rows []model.Review) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.GroupID
	}
	return out
}

// snapshot copies the rows.
func (rr *reviewRun) snapshot() []model.Review {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return append([]model.Review(nil), rr.rows...)
}

// row returns a copy of a group's row.
func (rr *reviewRun) row(groupID string) (model.Review, int, bool) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	i, ok := rr.idx[groupID]
	if !ok {
		return model.Review{}, 0, false
	}
	return rr.rows[i], i, true
}

// update applies fn to row i, stores the routing table and emits a
// progress row so hosts refresh the table live.
func (rr *reviewRun) update(ctx context.Context, i int, fn func(*model.Review)) {
	rr.mu.Lock()
	fn(&rr.rows[i])
	rows := append([]model.Review(nil), rr.rows...)
	rl := rr.progressLocked()
	if err := rr.p.d.Store.PutItemReviews(ctx, rr.r.it.ID, rows); err != nil && ctx.Err() == nil {
		rr.p.d.Log.Warn("store routing rows failed", "item", rr.r.it.ID, "err", err)
	}
	rr.mu.Unlock()
	rr.p.emit(rl)
}

// persist stores the current rows (used after cancellation).
func (rr *reviewRun) persist(ctx context.Context) {
	if err := rr.p.d.Store.PutItemReviews(ctx, rr.r.it.ID, rr.snapshot()); err != nil {
		rr.p.d.Log.Warn("store routing rows failed", "item", rr.r.it.ID, "err", err)
	}
}

// progressLocked is the progress row "k/n reviewed" (rr.mu held).
func (rr *reviewRun) progressLocked() model.RunLog {
	k := 0
	for _, row := range rr.rows {
		if row.Tier != model.TierNone && finished(row.Status) {
			k++
		}
	}
	return model.RunLog{
		ItemID: rr.r.it.ID, Stage: model.StageReview, Status: model.StageRunning, StartedAt: rr.start,
		In: rr.routed, Out: k, Calls: rr.calls, Cost: rr.cost, Note: fmt.Sprintf("%d/%d reviewed", k, rr.routed),
	}
}

// finished reports whether a row status is final for this run.
func finished(s model.ReviewStatus) bool {
	return s == model.ReviewDone || s == model.ReviewFailed || s == model.ReviewSkipped
}

// count adds calls and their cost to the stage totals.
func (rr *reviewRun) count(calls []model.ReviewCall) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	rr.calls += len(calls)
	for _, c := range calls {
		rr.cost += c.Cost
	}
}

// readiness is the error that keeps a tier from running: the fast
// reviewer's (it gates every review), else the deep one's for deep rows.
func readiness(tier model.Tier, fastErr, deepErr error) error {
	if fastErr != nil {
		return fastErr
	}
	if tier == model.TierDeep {
		return deepErr
	}
	return nil
}

// phase1 serves cached reviews, skips rows whose reviewer cannot run,
// leaves over-cap rows queued and dispatches the rest in plan order: one
// dispatcher per tier, fast up to pipeline.concurrency and deep up to
// deep_slots calls at once.
func (rr *reviewRun) phase1(ctx context.Context, fastErr, deepErr error) {
	byTier := map[model.Tier][]reviewJob{}
	for _, pl := range rr.ro.plan {
		i, ok := rr.idx[pl.GroupID]
		if !ok || pl.Tier == model.TierNone {
			continue
		}
		if c, hit := rr.serveCached(ctx, pl.GroupID, pl.Tier, purposeRouted); hit {
			res := fromCache(c)
			rr.update(ctx, i, func(row *model.Review) { applyResult(row, res); row.QueueNote = "" })
			continue
		}
		if err := readiness(pl.Tier, fastErr, deepErr); err != nil {
			rr.update(ctx, i, func(row *model.Review) { row.Status, row.QueueNote = model.ReviewSkipped, skipReason(err) })
			continue
		}
		if pl.OverCap {
			continue
		}
		byTier[pl.Tier] = append(byTier[pl.Tier], reviewJob{groupID: pl.GroupID, tier: pl.Tier, purpose: purposeRouted, req: rr.ro.reqs[pl.GroupID], est: pl.EstCost})
	}
	var wg sync.WaitGroup
	for tier, jobs := range byTier {
		wg.Add(1)
		go func(slots int, jobs []reviewJob) {
			defer wg.Done()
			forEachLimit(len(jobs), slots, func(k int) {
				if ctx.Err() == nil {
					rr.runRouted(ctx, jobs[k])
				}
			})
		}(rr.slots(tier), jobs)
	}
	wg.Wait()
}

// slots is the concurrency of a tier: deep_slots (≥ 1) for deep,
// pipeline.concurrency for fast.
func (rr *reviewRun) slots(tier model.Tier) int {
	if tier == model.TierDeep {
		return max(1, rr.ro.rules.Config.DeepSlots)
	}
	return rr.p.concurrency()
}

// runRouted runs a group's routed review: running, then done (cached in
// the review cache) or failed with the error and attempts, left for R.
func (rr *reviewRun) runRouted(ctx context.Context, j reviewJob) {
	_, i, ok := rr.row(j.groupID)
	if !ok {
		return
	}
	rr.update(ctx, i, func(row *model.Review) {
		row.Status, row.StartedAt, row.Model = model.ReviewRunning, rr.p.d.Now(), rr.p.tierModel(j.tier)
	})
	res := rr.review(ctx, j)
	if res.err != nil && ctx.Err() != nil {
		rr.update(context.WithoutCancel(ctx), i, func(row *model.Review) { row.Status = model.ReviewQueued })
		return
	}
	rr.update(ctx, i, func(row *model.Review) {
		row.Attempts = res.attempts
		if res.err != nil {
			row.Status, row.Err = model.ReviewFailed, res.err.Error()
			return
		}
		applyResult(row, res)
	})
	if res.err == nil {
		rr.cache(ctx, j, res)
	}
}

// applyResult copies a finished review onto a row (status done).
func applyResult(row *model.Review, res reviewResult) {
	row.Findings, row.Verdict, row.Confidence, row.Model = res.findings, res.verdict, res.conf, res.model
	row.Status, row.Err = model.ReviewDone, ""
	if res.attempts > 0 {
		row.Attempts = res.attempts
	}
}

// review calls the tier's reviewer for a job (one retry; an unparseable
// response counts as an error) and logs the calls.
func (rr *reviewRun) review(ctx context.Context, j reviewJob) reviewResult {
	units := rr.ro.inputs[j.groupID].Units
	var res reviewResult
	out := rr.p.callRetry(ctx, rr.p.d.Reviewers.For(j.tier), j.req,
		callMeta{itemID: rr.r.it.ID, groupID: j.groupID, tier: j.tier, purpose: j.purpose},
		func(r review.Result) error {
			f, v, c, err := rr.p.parseResult(r, units)
			res.findings, res.verdict, res.conf = f, v, c
			return err
		})
	rr.r.calls.add(j.groupID, out.calls...)
	rr.count(out.calls)
	res.model, res.inTok, res.outTok, res.ms = out.res.Model, out.res.InputTokens, out.res.OutputTokens, out.res.Ms
	res.cost, res.attempts, res.err = out.cost(), out.attempts, out.err
	return res
}

// parseResult reads findings, verdict and confidence from a review
// response with review.ParseReview (findings anchored to the group's
// units). When the text does not parse but the reviewer already filled
// the structured fields (fakes, adapters that parse themselves), those are
// used. Confidence is clamped to [0, 1].
func (p *Pipeline) parseResult(res review.Result, units []model.ChangeUnit) ([]model.Finding, string, float64, error) {
	f, v, c, err := p.fn.parseReview(res.Text, units)
	if err != nil {
		if res.Verdict == "" && len(res.Findings) == 0 {
			return nil, "", 0, fmt.Errorf("pipeline: parse review: %w", err)
		}
		f, v, c = res.Findings, res.Verdict, res.Confidence
	}
	return f, v, math.Max(0, math.Min(1, c)), nil
}

// cache stores a finished review in the review cache (group id + tier).
func (rr *reviewRun) cache(ctx context.Context, j reviewJob, res reviewResult) {
	in := rr.ro.inputs[j.groupID]
	c := model.Review{
		GroupID: j.groupID, ItemID: rr.r.it.ID, GroupName: in.Group.Name, Tier: j.tier,
		Prompt: review.PromptReviewV1, Prompts: in.Prompts, Findings: res.findings, Verdict: res.verdict,
		Confidence: res.conf, Model: res.model, Status: model.ReviewDone,
		InputTokens: res.inTok, OutputTokens: res.outTok, Ms: res.ms, Cost: res.cost, Attempts: res.attempts,
	}
	if err := rr.p.d.Store.PutReview(ctx, c); err != nil {
		rr.p.d.Log.Warn("store review cache failed", "group", j.groupID, "tier", j.tier, "err", err)
	}
}

// serveCached returns the cached review of a group on a tier (unless the
// cache is ignored) and logs it as a cached call.
func (rr *reviewRun) serveCached(ctx context.Context, groupID string, tier model.Tier, purpose string) (model.Review, bool) {
	if rr.ignoreCache {
		return model.Review{}, false
	}
	c, ok := rr.p.cachedReview(ctx, groupID, tier)
	if !ok {
		return model.Review{}, false
	}
	rr.r.calls.add(groupID, rr.cachedCall(ctx, groupID, tier, purpose, c))
	rr.mu.Lock()
	rr.cacheHits++
	rr.mu.Unlock()
	return c, true
}

// cachedCall is the call-log entry of a cache hit: the last successful
// review call of that tier in the row's previous log (prompt, response and
// what it cost when it ran), else one rebuilt from the cached review.
func (rr *reviewRun) cachedCall(ctx context.Context, groupID string, tier model.Tier, purpose string, c model.Review) model.ReviewCall {
	prev := rr.previous(ctx, groupID)
	for i := len(prev) - 1; i >= 0; i-- {
		if pc := prev[i]; pc.Tier == tier && pc.Task == string(review.TaskReview) && pc.Err == "" {
			pc.Cached, pc.Purpose = true, purpose
			return pc
		}
	}
	req := rr.ro.reqs[groupID]
	return model.ReviewCall{
		ItemID: rr.r.it.ID, GroupID: groupID, Task: string(review.TaskReview), Tier: tier, Purpose: purpose,
		Model: c.Model, System: req.System, Prompt: req.Prompt, Context: req.Context,
		Response: "(cached review) " + c.Verdict, InputTokens: c.InputTokens, OutputTokens: c.OutputTokens,
		Cost: c.Cost, Ms: c.Ms, Cached: true, At: rr.p.d.Now(),
	}
}

// previous returns the row's call log from the last run (loaded once).
func (rr *reviewRun) previous(ctx context.Context, groupID string) []model.ReviewCall {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if calls, ok := rr.prev[groupID]; ok {
		return calls
	}
	calls, err := storedCalls(ctx, rr.p.d.Store, rr.r.it.ID, groupID)
	if err != nil {
		rr.p.d.Log.Debug("previous call log unreadable", "group", groupID, "err", err)
	}
	rr.prev[groupID] = calls
	return calls
}

// phase2 decides escalations and calibration samples sequentially in plan
// order, reserving their estimates from a route.Budget that starts where
// the plan stopped (deterministic: estimates only), then runs them with
// deep_slots concurrency.
func (rr *reviewRun) phase2(ctx context.Context) {
	budget := rr.p.fn.newBudget(rr.ro.capUSD, rr.ro.reserved)
	if budget == nil {
		rr.p.d.Log.Debug("no review budget; escalation and calibration skipped", "item", rr.r.it.ID)
		return
	}
	cfg := rr.ro.rules.Config
	var jobs []reviewJob
	for _, pl := range rr.ro.plan {
		row, i, ok := rr.row(pl.GroupID)
		if !ok || pl.Tier != model.TierFast || row.Status != model.ReviewDone {
			continue
		}
		escalate := row.Confidence < cfg.EscalateBelow
		if !escalate && !sampled(pl.GroupID, cfg.CalibrationSample) {
			continue
		}
		gi := rr.ro.inputs[pl.GroupID]
		gi.Tier = model.TierDeep
		j := reviewJob{groupID: pl.GroupID, tier: model.TierDeep, purpose: purposeCalibration, req: rr.p.fn.buildReview(gi)}
		if escalate {
			j.purpose = purposeEscalation
		}
		j.est = rr.followUpEstimate(ctx, j)
		if !budget.Reserve(j.est) {
			if escalate {
				rr.update(ctx, i, func(row *model.Review) { row.QueueNote = noteEscalationOverCap })
			}
			continue
		}
		jobs = append(jobs, j)
	}
	forEachLimit(len(jobs), rr.slots(model.TierDeep), func(k int) {
		if ctx.Err() == nil {
			rr.runFollowUp(ctx, jobs[k], budget)
		} else {
			budget.Settle(jobs[k].est, 0)
		}
	})
}

// followUpEstimate is a deep follow-up's estimate (0 when cached).
func (rr *reviewRun) followUpEstimate(ctx context.Context, j reviewJob) float64 {
	if !rr.ignoreCache {
		if _, hit := rr.p.cachedReview(ctx, j.groupID, j.tier); hit {
			return 0
		}
	}
	return review.EstimateCost(rr.p.tierModel(j.tier), j.req)
}

// sampled reports whether a fast-routed group is a calibration sample: the
// first 8 hex digits of its id, as a fraction of 2^32, are below rate.
func sampled(groupID string, rate float64) bool {
	if rate <= 0 || len(groupID) < 8 {
		return false
	}
	n, err := strconv.ParseUint(groupID[:8], 16, 32)
	if err != nil {
		return false
	}
	return float64(n)/(1<<32) < rate
}

// runFollowUp runs an escalation or calibration deep review (cached when
// possible) and applies it to the row.
func (rr *reviewRun) runFollowUp(ctx context.Context, j reviewJob, b route.Budget) {
	_, i, ok := rr.row(j.groupID)
	if !ok {
		b.Settle(j.est, 0)
		return
	}
	var res reviewResult
	c, cached := rr.serveCached(ctx, j.groupID, j.tier, j.purpose)
	switch {
	case cached:
		res = fromCache(c)
		b.Settle(j.est, 0)
	default:
		if j.purpose == purposeEscalation {
			rr.update(ctx, i, func(row *model.Review) { row.Status, row.StartedAt = model.ReviewRunning, rr.p.d.Now() })
		}
		res = rr.review(ctx, j)
		b.Settle(j.est, res.cost)
		if res.err == nil {
			rr.cache(ctx, j, res)
		}
	}
	if j.purpose == purposeEscalation {
		rr.applyEscalation(ctx, i, res)
		return
	}
	if res.err != nil {
		rr.p.d.Log.Warn("calibration review failed", "item", rr.r.it.ID, "group", j.groupID, "err", res.err)
		return
	}
	rr.calibrate(ctx, i, res, cached)
}

// applyEscalation makes the row the deep result, keeping the fast tier
// and confidence that triggered it; a failed escalation keeps the fast
// result with a note.
func (rr *reviewRun) applyEscalation(ctx context.Context, i int, res reviewResult) {
	ctx = context.WithoutCancel(ctx)
	rr.update(ctx, i, func(row *model.Review) {
		if res.err != nil {
			row.Status, row.QueueNote = model.ReviewDone, noteEscalationFailed+reasonText(res.err)
			return
		}
		fast := model.TierFast
		row.EscalatedOf, row.FastConf, row.Tier = &fast, row.Confidence, model.TierDeep
		applyResult(row, res)
		row.Attempts = res.attempts
	})
}

// calibrate records a calibration sample on the row (deep findings,
// Missed = deep findings ≥ medium the fast review lacks), appends it to
// the store's samples when the deep call ran this run, and logs a
// disagreement.
func (rr *reviewRun) calibrate(ctx context.Context, i int, res reviewResult, cached bool) {
	var cal model.Calibration
	var groupID string
	rr.update(ctx, i, func(row *model.Review) {
		missed := missedFindings(row.Findings, res.findings)
		cal = model.Calibration{
			Findings: res.findings, Verdict: res.verdict, Confidence: res.conf,
			Missed: missed, Agree: missed == 0, Cost: res.cost, Ms: res.ms,
		}
		row.Calibration, groupID = &cal, row.GroupID
	})
	if !cal.Agree {
		rr.mu.Lock()
		rr.disagreed++
		rr.mu.Unlock()
		rr.p.d.Log.Warn("calibration: deep review found what fast missed", "item", rr.r.it.ID, "group", groupID, "missed", cal.Missed)
	}
	if cached {
		return
	}
	s := model.CalibrationSample{ItemID: rr.r.it.ID, GroupID: groupID, At: rr.p.d.Now(), Missed: cal.Missed, Agree: cal.Agree}
	if err := rr.p.d.Store.AppendCalibration(ctx, s); err != nil {
		rr.p.d.Log.Warn("store calibration sample failed", "item", rr.r.it.ID, "err", err)
	}
}

// missedFindings counts deep findings of severity ≥ medium with no fast
// finding on the same unit within 3 lines (same path when the deep finding
// is not anchored to a unit).
func missedFindings(fast, deep []model.Finding) int {
	n := 0
	for _, d := range deep {
		if d.Severity != model.SevMedium && d.Severity != model.SevHigh {
			continue
		}
		if !hasMatch(d, fast) {
			n++
		}
	}
	return n
}

func hasMatch(d model.Finding, fast []model.Finding) bool {
	for _, f := range fast {
		switch {
		case d.UnitID != "":
			if f.UnitID == d.UnitID && abs(f.Line-d.Line) <= 3 {
				return true
			}
		case f.Path == d.Path:
			return true
		}
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// finalise sums each row's calls (routed, retries, escalation,
// calibration; cache hits at what they cost when they ran) into its Cost,
// tokens and Ms.
func (rr *reviewRun) finalise() {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	for i := range rr.rows {
		row := &rr.rows[i]
		if row.Tier == model.TierNone && row.Calibration == nil {
			row.Cost, row.InputTokens, row.OutputTokens, row.Ms = 0, 0, 0, 0
			continue
		}
		var cost float64
		var in, out int
		var ms int64
		for _, c := range rr.r.calls.get(row.GroupID) {
			if c.Purpose == purposeTitle {
				continue
			}
			cost, in, out, ms = cost+c.Cost, in+c.InputTokens, out+c.OutputTokens, ms+c.Ms
		}
		row.Cost, row.InputTokens, row.OutputTokens, row.Ms = cost, in, out, ms
	}
}

// fillRunLog sets the stage row's totals and note.
func (rr *reviewRun) fillRunLog(rl *model.RunLog, out reviewOut) {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	done, failed, queued, escalated, calibrated := 0, 0, 0, 0, 0
	for _, row := range out.rows {
		if row.Tier == model.TierNone {
			continue
		}
		switch row.Status {
		case model.ReviewDone:
			done++
		case model.ReviewFailed:
			failed++
		case model.ReviewQueued:
			queued++
		}
		if row.EscalatedOf != nil {
			escalated++
		}
		if row.Calibration != nil {
			calibrated++
		}
	}
	rl.Out, rl.Calls, rl.Cost = done, rr.calls, rr.cost
	rl.CacheHit = rr.calls == 0 && rr.cacheHits > 0 && done == rr.routed
	parts := []string{fmt.Sprintf("%d/%d reviewed", done, rr.routed)}
	for _, c := range []struct {
		n    int
		what string
	}{{rr.cacheHits, "cached"}, {failed, "failed"}, {queued, "queued over cap"}, {escalated, "escalated"}, {calibrated, "calibrated"}} {
		if c.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c.n, c.what))
		}
	}
	if rr.disagreed > 0 {
		parts = append(parts, fmt.Sprintf("calibration: %d disagreed", rr.disagreed))
	}
	rl.Note = strings.Join(parts, " · ")
}
