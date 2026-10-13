package review

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Prompt template ids (model.Review.Prompt).
const (
	PromptReviewV1 = "review/v1"
	PromptTLDRV1   = "tldr/v1"
	PromptTitleV1  = "title/v1"
)

// MaxTLDR is the TL;DR length cap in characters (runes; spec Overview).
const MaxTLDR = 160

// Output caps per task. The configured models spend output tokens on
// reasoning before any text (a 100-token TL;DR cap ended at max_tokens with
// no text in the first live recording), so the short tasks get headroom;
// CleanTLDR and CleanTitle still bound what is shown.
const (
	ReviewMaxTokensFast = 1024
	ReviewMaxTokensDeep = 2048
	TLDRMaxTokens       = 1024
	TitleMaxTokens      = 256
)

// Prompt size caps. MaxReviewDiffBytes is the total numbered-diff budget of
// a review prompt, shared evenly by the group's units (at least
// minUnitDiffBytes each); a longer unit diff keeps its head and tail with
// an elision marker. Group-title prompts show at most titleUnits units and
// titleLines diff lines of each.
const (
	MaxReviewDiffBytes = 32 << 10
	minUnitDiffBytes   = 1 << 10
	maxHeadlines       = 8
	titleUnits         = 8
	titleLines         = 12
	titleLineBytes     = 160
)

// ErrBadResponse means a review response could not be parsed.
var ErrBadResponse = errors.New("review: unparseable response")

// GroupInput is everything a group review prompt is built from (spec
// stage 7: the group's units, the attached context and the labels as
// structured hints). Only deterministic inputs: no clocks, no costs, so
// the request bytes (and recording keys) are stable.
type GroupInput struct {
	Item  model.Item  // title and description only are used
	Group model.Group // name, bullet, mechanical facts
	// Units are the group's units in review order; Aggs the matching unit
	// aggregates (matched by UnitID; may be nil) and Labels their
	// unit-scope labels (overrides applied), used for units without an agg.
	Units   []model.ChangeUnit
	Aggs    []model.UnitAgg
	Labels  []model.Label
	Context []model.ContextBlock // from route.Gather
	Prompts []string             // specialist prompts (model.Prompt*)
	Tier    model.Tier
}

// reviewSystem is the base system prompt of TaskReview.
const reviewSystem = `You are a senior code reviewer. You review one group of related change units from a pull request and report the problems a careful reviewer would raise: bugs, broken contracts, missing error handling, security holes, data loss, races, and risky code without tests.

Rules:
- Report findings only. Never restate, summarise or praise the diff.
- Anchor every finding to a file path and a line number printed in the diff: the new-side number, or the old-side number for a deleted line.
- One finding per problem. "claim" is one sentence; "fix" is a short suggested change.
- severity: "high" breaks callers, loses data or opens a security hole; "medium" is a likely bug or missing handling; "low" is a minor risk.
- Never invent numbers, metrics, line numbers or code that is not shown.
- The hints are machine labels (probabilities 0-1, risk 1-5). Use them to decide where to look, not as facts.
- The Context section, when present, is unchanged background (callers, types, tests, config). Report a finding there only when the change breaks it, anchored to the changed line that causes it.
- verdict: "approve" when nothing needs changing, "comment" for low findings or questions only, "request_changes" when a finding must be fixed before merging.
- confidence: a number from 0 to 1, how sure you are that the findings and the verdict are right given what you were shown.

Answer with JSON only, no prose and no code fence, in exactly this shape:
{"findings":[{"file":"path/to/file","line":12,"severity":"low|medium|high","claim":"one sentence","fix":"suggested fix"}],"verdict":"approve|comment|request_changes","confidence":0.8}
Use "findings": [] when there is nothing to report.`

// specialists are the appendices for specialist prompts, in the order
// they are appended.
var specialists = []struct{ id, text string }{
	{model.PromptSecurity, "Security focus: check authentication and authorisation paths, secrets or personal data reaching logs, responses or storage, injection, unsafe deserialisation, and money handling (rounding, double charges)."},
	{model.PromptPerformance, "Performance focus: look for I/O or queries inside loops, unbounded work, missing batching or pagination, blocking calls on hot or async paths, and races between concurrent callers."},
	{model.PromptTests, "Tests focus: check that the risky changed code is exercised by tests, that no assertion was weakened or removed, and that error and edge paths are covered."},
}

// reviewSystemFor returns the system prompt with the requested specialist
// appendices (unknown ids ignored, each at most once, in a fixed order).
func reviewSystemFor(prompts []string) string {
	want := map[string]bool{}
	for _, p := range prompts {
		want[p] = true
	}
	var b strings.Builder
	b.WriteString(reviewSystem)
	for _, s := range specialists {
		if want[s.id] {
			b.WriteString("\n\n")
			b.WriteString(s.text)
		}
	}
	return b.String()
}

// reviewMaxTokens is the output cap of a review on a tier.
func reviewMaxTokens(t model.Tier) int {
	if t == model.TierDeep {
		return ReviewMaxTokensDeep
	}
	return ReviewMaxTokensFast
}

// BuildReview builds the TaskReview request for a group: the PR title and
// description bullets, the group's name, bullet and mechanical note, and
// each unit in review order with its path, line span, symbol, signature,
// label hints and line-numbered diff. Context blocks go in Request.Context
// and specialist appendices in the system prompt. The response is JSON
// findings, a verdict and a confidence (see ParseReview).
func BuildReview(in GroupInput) Request {
	var b strings.Builder
	writeItem(&b, in.Item)
	writeGroup(&b, in.Group)
	hints := unitHints(in)
	budget := diffBudget(len(in.Units))
	b.WriteString("\n# Units in review order\n")
	b.WriteString("Each diff line starts with its old and new line numbers.\n")
	for i, u := range in.Units {
		writeUnit(&b, i+1, len(in.Units), u, hints[u.ID], budget)
	}
	b.WriteString("\nReview these units and answer with the JSON object only.\n")
	return Request{
		Task: TaskReview, Tier: in.Tier, GroupID: in.Group.ID,
		System: reviewSystemFor(in.Prompts), Prompt: b.String(),
		Context: RenderContext(in.Context), MaxTokens: reviewMaxTokens(in.Tier),
		Units: in.Units,
	}
}

// RenderContext renders context blocks as "### <kind>: <title>\n<body>".
func RenderContext(blocks []model.ContextBlock) []string {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]string, 0, len(blocks))
	for _, c := range blocks {
		out = append(out, fmt.Sprintf("### %s: %s\n%s", c.Kind, c.Title, strings.TrimRight(c.Body, "\n")))
	}
	return out
}

// writeItem writes the PR title and description bullets.
func writeItem(b *strings.Builder, it model.Item) {
	b.WriteString("# Pull request\n")
	fmt.Fprintf(b, "Title: %s\n", oneLine(it.Title))
	bullets := model.DescBullets(it.Body)
	if len(bullets) == 0 {
		b.WriteString("Description bullets: none\n")
		return
	}
	b.WriteString("Description bullets:\n")
	for _, bl := range bullets {
		fmt.Fprintf(b, "- %s\n", oneLine(bl))
	}
}

// writeGroup writes the group's name, bullet and mechanical note.
func writeGroup(b *strings.Builder, g model.Group) {
	b.WriteString("\n# Group\n")
	fmt.Fprintf(b, "Name: %s\n", oneLine(g.Name))
	if g.DescBullet != "" {
		fmt.Fprintf(b, "Implements bullet: %s\n", oneLine(g.DescBullet))
	}
	if g.Mechanical {
		fmt.Fprintf(b, "Mechanical: %d edits verified identical in shape; only the example is shown, findings on it apply to all.\n", g.Count)
	}
}

// writeUnit writes one unit: header, symbol, signature, hints and diff.
func writeUnit(b *strings.Builder, i, n int, u model.ChangeUnit, h string, budget int) {
	fmt.Fprintf(b, "\n## Unit %d of %d: %s (%s)\n", i, n, u.Path, spanText(u))
	if u.Symbol != "" {
		fmt.Fprintf(b, "symbol: %s\n", oneLine(u.Symbol))
	}
	if u.Signature != "" {
		fmt.Fprintf(b, "signature: %s\n", oneLine(u.Signature))
	}
	if h != "" {
		fmt.Fprintf(b, "hints: %s\n", h)
	}
	lines := elide(numberDiff(u), budget)
	fence := fenceFor(lines)
	b.WriteString(fence + "diff\n")
	for _, ln := range lines {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	b.WriteString(fence + "\n")
}

// spanText describes a unit's line span: new-side lines, or old-side lines
// for a pure deletion.
func spanText(u model.ChangeUnit) string {
	switch {
	case u.Range.New.Count > 0:
		return fmt.Sprintf("%s, new lines %d-%d", u.Kind, u.Range.New.Start, u.Range.New.Start+u.Range.New.Count-1)
	case u.Range.Old.Count > 0:
		return fmt.Sprintf("%s, old lines %d-%d", u.Kind, u.Range.Old.Start, u.Range.Old.Start+u.Range.Old.Count-1)
	}
	return fmt.Sprintf("%s, at new line %d", u.Kind, u.Range.New.Start)
}

// numberDiff prefixes each diff line with its old and new line numbers
// (blank on the side it does not exist on). Hunk headers and "\ No
// newline" markers are kept unnumbered.
func numberDiff(u model.ChangeUnit) []string {
	if u.Diff == "" {
		return nil
	}
	old, nw := u.Range.Old.Start, u.Range.New.Start
	src := strings.Split(strings.TrimSuffix(u.Diff, "\n"), "\n")
	out := make([]string, 0, len(src))
	for _, ln := range src {
		switch {
		case strings.HasPrefix(ln, "@@"), strings.HasPrefix(ln, `\`):
			out = append(out, fmt.Sprintf("%5s %5s %s", "", "", ln))
		case strings.HasPrefix(ln, "+"):
			out = append(out, fmt.Sprintf("%5s %5d %s", "", nw, ln))
			nw++
		case strings.HasPrefix(ln, "-"):
			out = append(out, fmt.Sprintf("%5d %5s %s", old, "", ln))
			old++
		default:
			out = append(out, fmt.Sprintf("%5d %5d %s", old, nw, ln))
			old++
			nw++
		}
	}
	return out
}

// diffBudget is the per-unit diff byte budget for n units.
func diffBudget(n int) int {
	if n <= 0 {
		return MaxReviewDiffBytes
	}
	return max(MaxReviewDiffBytes/n, minUnitDiffBytes)
}

// elide keeps lines within budget bytes: when they do not fit, the head
// (two thirds of the budget) and the tail (one third) are kept around a
// marker naming how many lines were left out. Deterministic.
func elide(lines []string, budget int) []string {
	total := 0
	for _, ln := range lines {
		total += len(ln) + 1
	}
	if total <= budget {
		return lines
	}
	head, used := 0, 0
	for head < len(lines) && used+len(lines[head])+1 <= budget*2/3 {
		used += len(lines[head]) + 1
		head++
	}
	tail, used := len(lines), 0
	for tail > head && used+len(lines[tail-1])+1 <= budget/3 {
		used += len(lines[tail-1]) + 1
		tail--
	}
	out := make([]string, 0, head+1+len(lines)-tail)
	out = append(out, lines[:head]...)
	out = append(out, fmt.Sprintf("… %d lines elided …", tail-head))
	return append(out, lines[tail:]...)
}

// fenceFor returns a backtick fence longer than any backtick run in lines.
func fenceFor(lines []string) string {
	fence := "```"
	for _, ln := range lines {
		for strings.Contains(ln, fence) {
			fence += "`"
		}
	}
	return fence
}

// oneLine collapses whitespace (newlines included) to single spaces.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// fmtP formats a probability with two decimals.
func fmtP(p float64) string { return fmt.Sprintf("%.2f", p) }

// unitHints renders the compact label hints of every unit, keyed by unit
// id: from its UnitAgg when present, else from its labels.
func unitHints(in GroupInput) map[string]string {
	aggs := make(map[string]model.UnitAgg, len(in.Aggs))
	for _, a := range in.Aggs {
		aggs[a.UnitID] = a
	}
	byUnit := map[string][]model.Label{}
	for _, l := range in.Labels {
		byUnit[l.UnitID] = append(byUnit[l.UnitID], l)
	}
	out := make(map[string]string, len(in.Units))
	for _, u := range in.Units {
		switch a, ok := aggs[u.ID]; {
		case ok:
			out[u.ID] = hintsFromAgg(a)
		case len(byUnit[u.ID]) > 0:
			out[u.ID] = hintsFromAgg(aggFromLabels(byUnit[u.ID]))
		default:
			out[u.ID] = "unknown (no labels)"
		}
	}
	return out
}

// hintsFromAgg renders "kind=… risk=… tags=… breaking=… contract=…
// covered=… untested=…"; unknown values are "?".
func hintsFromAgg(a model.UnitAgg) string {
	kind := a.Kind
	if kind == "" || kind == model.LabelUnknown {
		kind = "?"
	}
	risk := "?"
	if a.RiskLevel > 0 {
		risk = fmt.Sprint(a.RiskLevel)
	}
	tags := "none"
	if len(a.RiskTags) > 0 {
		tags = strings.Join(a.RiskTags, ",")
	}
	breaking := fmtP(a.BreakingP)
	if a.BreakingUnknown {
		breaking = "?"
	}
	contract := a.Contract
	if contract == "" || contract == model.LabelUnknown {
		contract = "?"
	}
	covered := fmtP(a.CoveredP)
	if a.CoveredUnknown {
		covered = "?"
	}
	untested := "no"
	if a.Untested {
		untested = "yes"
	}
	return fmt.Sprintf("kind=%s risk=%s tags=%s breaking=%s contract=%s covered=%s untested=%s",
		kind, risk, tags, breaking, contract, covered, untested)
}

// aggFromLabels derives the hint fields of a unit from its raw labels.
func aggFromLabels(ls []model.Label) model.UnitAgg {
	a := model.UnitAgg{BreakingUnknown: true, CoveredUnknown: true}
	tags := map[string]bool{}
	for _, l := range ls {
		if model.IsUnknown(l) {
			continue
		}
		switch l.Question {
		case model.QKind:
			a.Kind = l.Value
		case model.QRisk:
			a.RiskLevel = model.ScoreLevel(l)
		case model.QBreaking:
			a.BreakingP, a.BreakingUnknown = l.P, false
		case model.QContract:
			a.Contract = l.Value
		case model.QCovered:
			a.CoveredP, a.CoveredUnknown = l.P, false
		default:
			if sub, ok := strings.CutPrefix(l.Question, model.QRiskTags+"."); ok && l.P >= 0.5 {
				tags[sub] = true
			}
		}
	}
	for _, t := range model.RiskTags {
		if tags[t] {
			a.RiskTags = append(a.RiskTags, t)
		}
	}
	a.Untested = a.RiskLevel >= 3 && !a.CoveredUnknown && a.CoveredP < 0.5
	return a
}

// tldrSystem is the system prompt of TaskTLDR.
const tldrSystem = `You write the one-sentence TL;DR shown at the top of a pull request overview, for a reviewer deciding how to spend their time. Use only the facts given; they were computed from machine labels and git. Never invent numbers, names or claims that are not in the facts. At most 160 characters. One plain sentence: no markdown, no quotes, no lead-in such as "TL;DR".`

// BuildTLDR builds the TaskTLDR request from the aggregate record only:
// composition, noise and size, minutes, unit counts, headlines (at most
// eight), hot spots, risk and risk tags, the test gap, description bullets
// and fidelity, undocumented changes, shape and whether labels are
// missing. It never reads fields that drift with the clock or with
// reviews (urgency, Why, Review and Calibration stats, TLDR itself), so
// the request — and its key, the TL;DR cache key — changes only when those
// inputs change. Always TierFast.
func BuildTLDR(agg model.ItemAgg) Request {
	var b strings.Builder
	b.WriteString("Facts about this pull request:\n")
	writeTLDRSize(&b, agg)
	writeTLDRRisk(&b, agg)
	writeTLDRHeadlines(&b, agg.Headlines)
	writeTLDRHotSpots(&b, agg.HotSpots)
	writeTLDRFidelity(&b, agg)
	b.WriteString("\nWrite the TL;DR sentence.\n")
	return Request{Task: TaskTLDR, Tier: model.TierFast, System: tldrSystem, Prompt: b.String(), MaxTokens: TLDRMaxTokens}
}

// writeTLDRSize writes labels availability, size, units and composition.
func writeTLDRSize(b *strings.Builder, agg model.ItemAgg) {
	if agg.LabelsMissing {
		b.WriteString("- labels: unavailable (the labeller could not be reached), so kinds and risk are unknown\n")
	}
	fmt.Fprintf(b, "- size: +%d -%d lines, about %d lines that matter, about %d min to review\n",
		agg.Added, agg.Removed, agg.EffectiveLines, int(math.Round(agg.Minutes)))
	if agg.NoiseLines > 0 {
		fmt.Fprintf(b, "- noise: %d changed lines are formatting, generated or moved code\n", agg.NoiseLines)
	}
	fmt.Fprintf(b, "- units: %d total, %d need careful reading\n", agg.UnitsTotal, agg.UnitsEyes)
	if c := compositionText(agg.Composition); c != "" {
		fmt.Fprintf(b, "- composition: %s\n", c)
	}
	if agg.Shape != "" {
		fmt.Fprintf(b, "- shape: %s\n", agg.Shape)
	}
}

// compositionText renders kind shares as "feature 62%, refactor 30%",
// largest first (ties by name), omitting shares that round to 0%.
func compositionText(c map[string]float64) string {
	type share struct {
		kind string
		pct  int
	}
	var ss []share
	for k, v := range c {
		if p := int(math.Round(v * 100)); p > 0 {
			ss = append(ss, share{k, p})
		}
	}
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].pct != ss[j].pct {
			return ss[i].pct > ss[j].pct
		}
		return ss[i].kind < ss[j].kind
	})
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = fmt.Sprintf("%s %d%%", s.kind, s.pct)
	}
	return strings.Join(parts, ", ")
}

// writeTLDRRisk writes the max risk, risk tags and the test gap.
func writeTLDRRisk(b *strings.Builder, agg model.ItemAgg) {
	if agg.RiskMax > 0 {
		fmt.Fprintf(b, "- highest unit risk: %d of 5\n", int(math.Round(agg.RiskMax)))
	} else {
		b.WriteString("- highest unit risk: unknown\n")
	}
	if len(agg.RiskTags) > 0 {
		tags := make([]string, 0, len(agg.RiskTags))
		for t := range agg.RiskTags {
			tags = append(tags, t)
		}
		sort.Strings(tags)
		for i, t := range tags {
			tags[i] = fmt.Sprintf("%s (%s)", t, plural(agg.RiskTags[t], "unit"))
		}
		fmt.Fprintf(b, "- risk tags: %s\n", strings.Join(tags, ", "))
	}
	if agg.RiskyUnits > 0 {
		fmt.Fprintf(b, "- test gap: %d of %d risky units untested\n", agg.TestGap, agg.RiskyUnits)
	}
}

// writeTLDRHeadlines writes up to maxHeadlines headlines.
func writeTLDRHeadlines(b *strings.Builder, hs []model.Headline) {
	if len(hs) == 0 {
		b.WriteString("- headlines: no contract changes\n")
		return
	}
	b.WriteString("- headlines:\n")
	for i, h := range hs {
		if i == maxHeadlines {
			fmt.Fprintf(b, "  - and %d more\n", len(hs)-maxHeadlines)
			break
		}
		fmt.Fprintf(b, "  - %s at %s:%d: %s (p %s)\n", h.Kind, h.Path, h.Line, oneLine(h.Text), fmtP(h.P))
	}
}

// writeTLDRHotSpots writes the hot spots in rank order.
func writeTLDRHotSpots(b *strings.Builder, hs []model.HotSpot) {
	if len(hs) == 0 {
		return
	}
	b.WriteString("- check before approving:\n")
	for _, h := range hs {
		fmt.Fprintf(b, "  - %s:%d %s\n", h.Path, h.Line, oneLine(h.Reason))
	}
}

// writeTLDRFidelity writes the description bullets, fidelity and the
// undocumented count.
func writeTLDRFidelity(b *strings.Builder, agg model.ItemAgg) {
	if len(agg.Bullets) > 0 {
		done := 0
		for _, bl := range agg.Bullets {
			if bl.Implemented {
				done++
			}
		}
		fmt.Fprintf(b, "- description: %d of %d bullets implemented (fidelity %s)\n", done, len(agg.Bullets), fmtP(agg.Fidelity))
		for _, bl := range agg.Bullets {
			state := "not found"
			if bl.Implemented {
				state = "implemented"
			}
			fmt.Fprintf(b, "  - %s: %s\n", state, oneLine(bl.Text))
		}
	}
	if n := len(agg.Undocumented); n > 0 {
		fmt.Fprintf(b, "- undocumented changes: %s not described\n", plural(n, "unit"))
	}
}

// plural renders "1 unit" / "2 units".
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// titleSystem is the system prompt of TaskGroupTitle.
const titleSystem = `You name a group of related code changes for a reviewer's list. Answer with exactly three words saying what the change does, for example: Validate upload size. No punctuation, no quotes, nothing else.`

// BuildGroupTitle builds the TaskGroupTitle request (three-word title) for
// a group Jev could not name from a bullet: unit paths, symbols,
// signatures and the first diff lines of each unit (capped). Always
// TierFast.
func BuildGroupTitle(g model.Group, units []model.ChangeUnit) Request {
	var b strings.Builder
	b.WriteString("Changes in this group:\n")
	for i, u := range units {
		if i == titleUnits {
			fmt.Fprintf(&b, "\n(and %d more units)\n", len(units)-titleUnits)
			break
		}
		writeTitleUnit(&b, u)
	}
	b.WriteString("\nAnswer with exactly three words.\n")
	return Request{Task: TaskGroupTitle, Tier: model.TierFast, GroupID: g.ID, System: titleSystem, Prompt: b.String(), MaxTokens: TitleMaxTokens}
}

// writeTitleUnit writes one unit of a group-title prompt.
func writeTitleUnit(b *strings.Builder, u model.ChangeUnit) {
	fmt.Fprintf(b, "\n## %s", u.Path)
	if u.Symbol != "" {
		fmt.Fprintf(b, " (%s)", oneLine(u.Symbol))
	}
	b.WriteString("\n")
	if u.Signature != "" {
		fmt.Fprintf(b, "signature: %s\n", oneLine(u.Signature))
	}
	var lines []string
	for _, ln := range strings.Split(strings.TrimSuffix(u.Diff, "\n"), "\n") {
		if ln == "" || strings.HasPrefix(ln, "@@") || strings.HasPrefix(ln, `\`) {
			continue
		}
		if len(lines) == titleLines {
			lines = append(lines, "…")
			break
		}
		lines = append(lines, truncate(ln, titleLineBytes))
	}
	if len(lines) == 0 {
		return
	}
	fence := fenceFor(lines)
	b.WriteString(fence + "diff\n" + strings.Join(lines, "\n") + "\n" + fence + "\n")
}
