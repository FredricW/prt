package settingsview

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// formValues are the form's field values as the fields hold them (text for
// numbers), bound to the Huh fields by pointer.
type formValues struct {
	AutoReview     string
	Scope          proto.SettingsScope
	Role           string
	Owns           string // one glob per line
	CostCap        string
	DeepSlots      string
	Fast           string
	Deep           string
	Embed          string
	Rank           []string // in rankFields order
	UnsureBelow    string
	KeepPairP      string
	EmbedMinCosine string
	MaxGroupUnits  string
	Concurrency    string
	// Action is the final choice (save, edit routing, edit questions,
	// cancel); it is not an edit.
	Action ActionKind
}

// rankField is one urgency weight: its prt.toml key and its field.
type rankField struct {
	key string
	ptr func(*config.Rank) *float64
}

// rankFields lists every config.Rank weight in prt.toml order.
func rankFields() []rankField {
	return []rankField{
		{"w_requested", func(r *config.Rank) *float64 { return &r.WRequested }},
		{"w_breaking", func(r *config.Rank) *float64 { return &r.WBreaking }},
		{"w_risk", func(r *config.Rank) *float64 { return &r.WRisk }},
		{"w_test_gap", func(r *config.Rank) *float64 { return &r.WTestGap }},
		{"w_relevance", func(r *config.Rank) *float64 { return &r.WRelevance }},
		{"w_release", func(r *config.Rank) *float64 { return &r.WRelease }},
		{"w_ci_red", func(r *config.Rank) *float64 { return &r.WCIRed }},
		{"w_age", func(r *config.Rank) *float64 { return &r.WAge }},
		{"w_minutes", func(r *config.Rank) *float64 { return &r.WMinutes }},
		{"w_unpushed", func(r *config.Rank) *float64 { return &r.WUnpushed }},
		{"w_ready_stale", func(r *config.Rank) *float64 { return &r.WReadyStale }},
	}
}

// fromValues renders loaded values as field text. An unknown or empty
// auto_review shows as the strategy it loads as (the default).
func fromValues(v proto.SettingsValues) formValues {
	a, err := model.ParseAutoReview(v.AutoReview)
	if err != nil {
		a = model.DefaultAutoReview
	}
	rank := v.Rank
	fv := formValues{
		AutoReview:     string(a),
		Scope:          proto.ScopeRepo,
		Role:           v.Role,
		Owns:           strings.Join(v.Owns, "\n"),
		CostCap:        fmtFloat(v.CostCapUSD),
		DeepSlots:      strconv.Itoa(v.DeepSlots),
		Fast:           v.Fast,
		Deep:           v.Deep,
		Embed:          v.Embed,
		UnsureBelow:    fmtFloat(v.UnsureBelow),
		KeepPairP:      fmtFloat(v.KeepPairP),
		EmbedMinCosine: fmtFloat(v.EmbedMinCosine),
		MaxGroupUnits:  strconv.Itoa(v.MaxGroupUnits),
		Concurrency:    strconv.Itoa(v.Concurrency),
		Action:         ActSave,
	}
	for _, f := range rankFields() {
		fv.Rank = append(fv.Rank, fmtFloat(*f.ptr(&rank)))
	}
	return fv
}

// clone copies fv (Rank included) so the form can edit it freely.
func (fv formValues) clone() formValues {
	fv.Rank = slices.Clone(fv.Rank)
	return fv
}

// sameEdits reports whether a and b hold the same field values (Action is
// not an edit).
func sameEdits(a, b formValues) bool {
	if a.AutoReview != b.AutoReview || a.Scope != b.Scope || a.Owns != b.Owns || len(a.Rank) != len(b.Rank) {
		return false
	}
	ta, tb := a.textFields(), b.textFields()
	for i := range ta {
		if *ta[i].val != *tb[i].val {
			return false
		}
	}
	return true
}

// apply returns loaded with every field the user changed (cur differs from
// base) parsed from cur. Untouched fields keep the loaded values exactly,
// so float formatting can never alter a value nobody edited. Fields that
// fail to parse (the form's validation prevents that) keep the loaded
// value too.
func apply(loaded proto.SettingsValues, base, cur formValues) proto.SettingsValues {
	v := loaded
	v.Owns = slices.Clone(loaded.Owns)
	setStr := func(dst *string, c, b string) {
		if c != b {
			*dst = strings.TrimSpace(c)
		}
	}
	setStr(&v.AutoReview, cur.AutoReview, base.AutoReview)
	setStr(&v.Role, cur.Role, base.Role)
	setStr(&v.Fast, cur.Fast, base.Fast)
	setStr(&v.Deep, cur.Deep, base.Deep)
	setStr(&v.Embed, cur.Embed, base.Embed)
	if cur.Owns != base.Owns {
		v.Owns = splitLines(cur.Owns)
	}
	setFloat(&v.CostCapUSD, cur.CostCap, base.CostCap)
	setFloat(&v.UnsureBelow, cur.UnsureBelow, base.UnsureBelow)
	setFloat(&v.KeepPairP, cur.KeepPairP, base.KeepPairP)
	setFloat(&v.EmbedMinCosine, cur.EmbedMinCosine, base.EmbedMinCosine)
	setInt(&v.DeepSlots, cur.DeepSlots, base.DeepSlots)
	setInt(&v.MaxGroupUnits, cur.MaxGroupUnits, base.MaxGroupUnits)
	setInt(&v.Concurrency, cur.Concurrency, base.Concurrency)
	for i, f := range rankFields() {
		if i < len(cur.Rank) && i < len(base.Rank) {
			setFloat(f.ptr(&v.Rank), cur.Rank[i], base.Rank[i])
		}
	}
	return v
}

// setFloat stores c in dst when it was edited and parses.
func setFloat(dst *float64, c, b string) {
	if c == b {
		return
	}
	if f, err := parseFloat(c); err == nil {
		*dst = f
	}
}

// setInt stores c in dst when it was edited and parses.
func setInt(dst *int, c, b string) {
	if c == b {
		return
	}
	if n, err := strconv.Atoi(strings.TrimSpace(c)); err == nil {
		*dst = n
	}
}

// splitLines reads the owns text: one glob per line, blank lines dropped.
func splitLines(s string) []string {
	out := make([]string, 0)
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// fmtFloat formats a value the shortest way that reads back exactly.
func fmtFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// errNotNumber is the parse error the validators wrap.
var errNotNumber = errors.New("not a number")

// parseFloat reads a finite number ("$" prefix tolerated for dollars).
func parseFloat(s string) (float64, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "$")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errNotNumber
	}
	return f, nil
}

// The validators below are the page's inline checks (local parse and range
// only); the daemon validates authoritatively on save (settings.Validate).

// unitInterval accepts a number in [0, 1] (probabilities, cosines).
func unitInterval(name string) func(string) error {
	return func(s string) error {
		f, err := parseFloat(s)
		if err != nil || f < 0 || f > 1 {
			return fmt.Errorf("%s must be between 0 and 1", name)
		}
		return nil
	}
}

// atLeastOne accepts a whole number ≥ 1.
func atLeastOne(name string) func(string) error {
	return func(s string) error {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil || n < 1 {
			return fmt.Errorf("%s must be a whole number ≥ 1", name)
		}
		return nil
	}
}

// positiveDollars accepts an amount > 0.
func positiveDollars(name string) func(string) error {
	return func(s string) error {
		f, err := parseFloat(s)
		if err != nil || f <= 0 {
			return fmt.Errorf("%s must be an amount in dollars > 0", name)
		}
		return nil
	}
}

// weight accepts a number ≥ 0.
func weight(name string) func(string) error {
	return func(s string) error {
		f, err := parseFloat(s)
		if err != nil || f < 0 {
			return fmt.Errorf("%s must be a number ≥ 0", name)
		}
		return nil
	}
}

// notEmpty accepts any non-blank text (model specs).
func notEmpty(name string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("%s must not be empty", name)
		}
		return nil
	}
}
