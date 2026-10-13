package quality

import (
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Ratio is a metric as a count over a denominator. Den 0 means the metric
// does not apply to the item: it is shown as "—", encoded as JSON null and
// left out of the totals row.
type Ratio struct {
	Num int
	Den int
}

// Applies reports whether the metric applies (Den > 0).
func (r Ratio) Applies() bool { return r.Den > 0 }

// Value is Num/Den (NaN when the metric does not apply).
func (r Ratio) Value() float64 {
	if r.Den <= 0 {
		return math.NaN()
	}
	return float64(r.Num) / float64(r.Den)
}

// add pools two ratios (sums numerators and denominators).
func (r Ratio) add(o Ratio) Ratio { return Ratio{Num: r.Num + o.Num, Den: r.Den + o.Den} }

// MarshalJSON encodes {"num","den","value"} with value rounded to four
// decimals, or null when the metric does not apply.
func (r Ratio) MarshalJSON() ([]byte, error) {
	if !r.Applies() {
		return []byte("null"), nil
	}
	return json.Marshal(struct {
		Num   int     `json:"num"`
		Den   int     `json:"den"`
		Value float64 `json:"value"`
	}{r.Num, r.Den, math.Round(r.Value()*1e4) / 1e4})
}

// Metric keys, in scoreboard column order.
const (
	MEyesRecall        = "eyes_recall"
	MEyesPrecision     = "eyes_precision"
	MHeadlinePrecision = "headline_precision"
	MHeadlineRecall    = "headline_recall"
	MHotSpotsTop3      = "hot_spots_top3"
	MFidelity          = "fidelity"
	MUndocumented      = "undocumented_recall"
	MUnsureLabels      = "unsure_labels"
	MUnsureUnits       = "unsure_units"
	MLinesToRead       = "lines_to_read"
	MMustRead          = "must_read"
	MBranchState       = "branch_state"
	MFileKind          = "file_kind"
)

// MetricInfo describes one ratio metric: its column title, whether higher
// values are better, and whether it is a firm accuracy metric (the
// tuning rule "no item's firm metric worse than at baseline" and the
// regression guard read these).
type MetricInfo struct {
	Key    string
	Title  string
	Higher bool
	Firm   bool
}

// Metrics lists every ratio metric in column order.
var Metrics = []MetricInfo{
	{MEyesRecall, "Eyes recall", true, true},
	{MEyesPrecision, "Eyes precision", true, false},
	{MHeadlinePrecision, "Headline precision", true, false},
	{MHeadlineRecall, "Headline recall (firm)", true, true},
	{MHotSpotsTop3, "Hot spots top 3", true, true},
	{MFidelity, "Fidelity bullets", true, true},
	{MUndocumented, "Undocumented recall (firm)", true, true},
	{MUnsureLabels, "Unsure labels", false, false},
	{MUnsureUnits, "Unsure units", false, false},
	{MLinesToRead, "Lines to read", false, false},
	{MMustRead, "Eyes lines in must_read ± tol", true, false},
	{MBranchState, "Branch state", true, true},
	{MFileKind, "File kind", true, false},
}

// Info returns the MetricInfo of a key (ok false for an unknown key).
func Info(key string) (MetricInfo, bool) {
	for _, m := range Metrics {
		if m.Key == key {
			return m, true
		}
	}
	return MetricInfo{}, false
}

// Outcome is what the pipeline produced for one item: its change units,
// its aggregate record and its stored labels (unit, line and item scope).
type Outcome struct {
	Units  []model.ChangeUnit
	Agg    model.ItemAgg
	Labels []model.Label
}

// Row is one item's scores.
type Row struct {
	Item string `json:"item"`
	// Headlines is the number of headline rows the Overview lists;
	// HeadlinesUnsure the candidates left out below the threshold.
	Headlines       int `json:"headlines"`
	HeadlinesUnsure int `json:"headlinesUnsure"`
	// MustReadRange is the accepted eyes-line range (must_read ±
	// tolerance) the must_read metric compares the lines-to-read
	// numerator with.
	MustReadRange [2]int `json:"mustReadRange"`
	// Ratios holds every metric of Metrics by key.
	Ratios map[string]Ratio `json:"metrics"`
}

// index is an outcome prepared for scoring.
type index struct {
	units map[string]model.ChangeUnit
	spans map[string]unitSpans
	aggs  map[string]model.UnitAgg
	eyes  []string // eyes unit ids, in Agg.Units order
}

func newIndex(o Outcome) index {
	ix := index{units: map[string]model.ChangeUnit{}, spans: map[string]unitSpans{}, aggs: map[string]model.UnitAgg{}}
	for _, u := range o.Units {
		ix.units[u.ID] = u
		ix.spans[u.ID] = spansOf(u)
	}
	for _, a := range o.Agg.Units {
		ix.aggs[a.UnitID] = a
		if a.Attention == model.AttnEyes {
			ix.eyes = append(ix.eyes, a.UnitID)
		}
	}
	return ix
}

// spansFor returns the spans of the given unit ids (unknown ids skipped).
func (ix index) spansFor(ids []string) []unitSpans {
	out := make([]unitSpans, 0, len(ids))
	for _, id := range ids {
		if s, ok := ix.spans[id]; ok {
			out = append(out, s)
		}
	}
	return out
}

// Score computes every metric of one item against its ground truth. It is
// pure and deterministic.
func Score(t Truth, o Outcome) Row {
	ix := newIndex(o)
	r := Row{Item: t.Item, Headlines: len(o.Agg.Headlines), HeadlinesUnsure: o.Agg.HeadlinesUnsure, Ratios: map[string]Ratio{}}
	r.Ratios[MEyesRecall] = eyesRecall(t, ix)
	r.Ratios[MEyesPrecision] = eyesPrecision(t, ix)
	r.Ratios[MHeadlinePrecision] = headlinePrecision(t, o.Agg.Headlines, ix)
	r.Ratios[MHeadlineRecall] = headlineRecall(t, o.Agg.Headlines, ix)
	r.Ratios[MHotSpotsTop3] = hotSpotsTop3(t, o.Agg.HotSpots, ix)
	r.Ratios[MFidelity] = fidelity(t, o.Agg.Bullets)
	r.Ratios[MUndocumented] = undocumentedRecall(t, o.Agg.Undocumented, ix)
	r.Ratios[MUnsureLabels] = unsureLabels(o, ix)
	r.Ratios[MUnsureUnits] = Ratio{Num: len(o.Agg.UnsureUnits), Den: len(o.Agg.Units)}
	lines := linesToRead(o.Agg, ix)
	r.Ratios[MLinesToRead] = lines
	var within Ratio
	r.MustReadRange, within = mustRead(t.LinesThatMatter, lines.Num)
	r.Ratios[MMustRead] = within
	r.Ratios[MBranchState] = branchState(t.Branch, o.Agg.Branch)
	r.Ratios[MFileKind] = fileKind(t.Files, ix)
	return r
}

// eyesRecall is the share of firm needs_eyes regions overlapped by at
// least one pipeline unit with Attention eyes (path equal, the unit's
// changed span on the entry's side intersects the entry's lines or any
// also_at location). Soft entries are never misses.
func eyesRecall(t Truth, ix index) Ratio {
	eyes := ix.spansFor(ix.eyes)
	var r Ratio
	for _, e := range t.NeedsEyes {
		if e.Certainty != Firm {
			continue
		}
		r.Den++
		if anyOverlap(eyes, regionOf(e.Loc)) {
			r.Num++
		}
	}
	return r
}

// eyesPrecision is the share of pipeline eyes units that overlap any
// needs_eyes entry, firm or soft (flagging a soft region is never a false
// positive). Not applicable when the pipeline marks no unit eyes.
func eyesPrecision(t Truth, ix index) Ratio {
	regions := make([]region, len(t.NeedsEyes))
	for i, e := range t.NeedsEyes {
		regions[i] = regionOf(e.Loc)
	}
	var r Ratio
	for _, id := range ix.eyes {
		s, ok := ix.spans[id]
		if !ok {
			continue
		}
		r.Den++
		if overlapsAny(s, regions) {
			r.Num++
		}
	}
	return r
}

// overlapsAny reports whether s overlaps any region.
func overlapsAny(s unitSpans, regions []region) bool {
	for _, rg := range regions {
		if s.overlaps(rg) {
			return true
		}
	}
	return false
}

// headlineSpans returns the spans a pipeline headline is matched with: its
// unit's changed spans, or (unit unknown) its own path and line on both
// sides.
func headlineSpans(h model.Headline, ix index) unitSpans {
	if s, ok := ix.spans[h.UnitID]; ok {
		return s
	}
	pt := span{path: h.Path, first: h.Line, last: h.Line}
	return unitSpans{path: h.Path, new: pt, old: pt, hasNew: true, hasOld: true}
}

// headlinePrecision is the share of pipeline headline rows that match a
// headline entry (firm or soft; its unit overlaps the entry). A row that
// matches no headline entry is a false positive, whether it overlaps a
// not_headlines entry or nothing at all; matching a headline entry wins
// over overlapping a not_headlines entry. Not applicable when the Overview
// lists no headline.
func headlinePrecision(t Truth, hs []model.Headline, ix index) Ratio {
	regions := make([]region, len(t.Headlines))
	for i, e := range t.Headlines {
		regions[i] = regionOf(e.Loc)
	}
	r := Ratio{Den: len(hs)}
	for _, h := range hs {
		if overlapsAny(headlineSpans(h, ix), regions) {
			r.Num++
		}
	}
	return r
}

// headlineRecall is the share of firm headline entries matched by at least
// one pipeline headline row.
func headlineRecall(t Truth, hs []model.Headline, ix index) Ratio {
	spans := make([]unitSpans, len(hs))
	for i, h := range hs {
		spans[i] = headlineSpans(h, ix)
	}
	var r Ratio
	for _, e := range t.Headlines {
		if e.Certainty != Firm {
			continue
		}
		r.Den++
		if anyOverlap(spans, regionOf(e.Loc)) {
			r.Num++
		}
	}
	return r
}

// maxHotSpots is the number of hot spots the Overview shows.
const maxHotSpots = 3

// hotSpotsTop3 counts the pipeline's top-three hot spots (HotSpots[0:3])
// whose unit overlaps a ground-truth hot spot of rank <= 3 (firm or soft),
// out of min(3, number of rank <= 3 entries). A top-three slot spent on
// anything else is a miss; hits are capped at the denominator. Not
// applicable when the truth lists no hot spot.
func hotSpotsTop3(t Truth, hs []model.HotSpot, ix index) Ratio {
	var regions []region
	for _, e := range t.HotSpots {
		if e.Rank <= maxHotSpots {
			regions = append(regions, regionOf(e.Loc))
		}
	}
	r := Ratio{Den: min(maxHotSpots, len(regions))}
	if r.Den == 0 {
		return r
	}
	for _, h := range hs[:min(maxHotSpots, len(hs))] {
		s, ok := ix.spans[h.UnitID]
		if !ok {
			continue
		}
		if overlapsAny(s, regions) {
			r.Num++
		}
	}
	r.Num = min(r.Num, r.Den)
	return r
}

// fidelity is the share of description bullets whose pipeline status
// (implemented / not found) equals the ground truth; "partial" accepts
// either. Bullets are matched by text (whitespace-normalised), else by
// position; a bullet the pipeline does not list counts as wrong. Not
// applicable when fidelity is not applicable or the truth lists no bullet.
func fidelity(t Truth, got []model.BulletAgg) Ratio {
	if !t.Fidelity.Applicable {
		return Ratio{}
	}
	r := Ratio{Den: len(t.Fidelity.Bullets)}
	for i, b := range t.Fidelity.Bullets {
		pb, ok := findBullet(got, b.Text, i)
		if !ok {
			continue
		}
		if b.Status == BulletPartial || pb.Implemented == (b.Status == BulletImplemented) {
			r.Num++
		}
	}
	return r
}

// findBullet finds the pipeline bullet with the same text, else the one at
// position i.
func findBullet(got []model.BulletAgg, text string, i int) (model.BulletAgg, bool) {
	want := normSpace(text)
	for _, b := range got {
		if normSpace(b.Text) == want {
			return b, true
		}
	}
	if i < len(got) {
		return got[i], true
	}
	return model.BulletAgg{}, false
}

// normSpace collapses whitespace runs and trims.
func normSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// undocumentedRecall is the share of firm undocumented changes overlapped
// by a unit the pipeline lists as undocumented (ItemAgg.Undocumented).
func undocumentedRecall(t Truth, undocumented []string, ix index) Ratio {
	if !t.Fidelity.Applicable {
		return Ratio{}
	}
	spans := ix.spansFor(undocumented)
	var r Ratio
	for _, u := range t.Fidelity.Undocumented {
		if u.Certainty != Firm {
			continue
		}
		r.Den++
		if anyOverlap(spans, regionOf(u.Loc)) {
			r.Num++
		}
	}
	return r
}

// unsureLabels is the aggregate's unsure label count (ItemAgg.Unsure, the
// number the Overview's confidence footer shows) over every unit-scope
// and line label of the item's units.
func unsureLabels(o Outcome, ix index) Ratio {
	r := Ratio{Num: o.Agg.Unsure}
	for _, l := range o.Labels {
		if _, ok := ix.units[l.UnitID]; ok {
			r.Den++
		}
	}
	return r
}

// linesToRead is the changed lines (adds + dels) of the units the
// pipeline marks eyes over the item's changed lines (Added + Removed). The
// spec's "Done means" target is below 1/3.
func linesToRead(agg model.ItemAgg, ix index) Ratio {
	r := Ratio{Den: agg.Added + agg.Removed}
	for _, id := range ix.eyes {
		u := ix.units[id]
		r.Num += u.Adds + u.Dels
	}
	return r
}

// mustRead returns the accepted eyes-line range [floor(m·(1−tol)),
// ceil(m·(1+tol))] for must_read m and tolerance tol, and whether eyes
// lies in it (1/1 or 0/1).
func mustRead(l LinesThatMatter, eyes int) ([2]int, Ratio) {
	m := float64(l.MustRead)
	lo := max(0, int(math.Floor(m*(1-l.Tolerance))))
	hi := int(math.Ceil(m * (1 + l.Tolerance)))
	r := Ratio{Den: 1}
	if eyes >= lo && eyes <= hi {
		r.Num = 1
	}
	return [2]int{lo, hi}, r
}

// branchState is 1/1 when the branch block's state is the expected state
// or in state_ok, 0/1 otherwise; not applicable without a [branch] table.
func branchState(want *BranchTruth, got *model.BranchInfo) Ratio {
	if want == nil {
		return Ratio{}
	}
	r := Ratio{Den: 1}
	if got != nil && (got.State == want.State || slices.Contains(want.StateOK, got.State)) {
		r.Num = 1
	}
	return r
}

// fileKind is the share of ground-truth files whose dominant pipeline
// kind (the unit kind label with the most changed lines in the file;
// unknown and unanswered kinds ignored; ties by name) is the expected kind
// or in also. A file without a labelled unit counts as a miss.
func fileKind(files []FileTruth, ix index) Ratio {
	lines := map[string]map[string]int{} // path → kind → lines
	for id, a := range ix.aggs {
		if a.Kind == "" || a.Kind == model.LabelUnknown {
			continue
		}
		u := ix.units[id]
		if lines[u.Path] == nil {
			lines[u.Path] = map[string]int{}
		}
		lines[u.Path][a.Kind] += u.Adds + u.Dels
	}
	r := Ratio{Den: len(files)}
	for _, f := range files {
		k := dominant(lines[f.Path])
		if k != "" && (k == f.Kind || slices.Contains(f.Also, k)) {
			r.Num++
		}
	}
	return r
}

// dominant returns the key with the largest value (ties by name; "" for
// an empty map).
func dominant(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	best := ""
	for _, k := range keys {
		if best == "" || m[k] > m[best] {
			best = k
		}
	}
	return best
}
