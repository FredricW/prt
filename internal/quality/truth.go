package quality

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// Certainty values of a ground-truth entry (testdata/groundtruth/README.md):
// a firm entry must be found by a correct pipeline; a soft one is a
// judgment call that never counts as a miss or a false positive.
const (
	Firm = "firm"
	Soft = "soft"
)

// Sides of a located entry: lines are new-side unless the entry says old
// (deleted code only).
const (
	SideNew = "new"
	SideOld = "old"
)

// Truth is one parsed ground-truth file: the hand-written reference answer
// for one fixture item.
type Truth struct {
	Item         string `toml:"item"` // "pr:<n>" or "branch:<name>"
	Title        string `toml:"title"`
	Patch        string `toml:"patch"`
	WrittenBlind bool   `toml:"written_blind"`

	Summary         Summary         `toml:"summary"`
	LinesThatMatter LinesThatMatter `toml:"lines_that_matter"`
	NeedsEyes       []NeedsEyes     `toml:"needs_eyes"`
	Headlines       []TruthHeadline `toml:"headlines"`
	NotHeadlines    []NotHeadline   `toml:"not_headlines"`
	HotSpots        []TruthHotSpot  `toml:"hot_spots"`
	Coverage        []Coverage      `toml:"coverage"`
	Fidelity        Fidelity        `toml:"fidelity"`
	Comments        []CommentTruth  `toml:"comments"`
	Files           []FileTruth     `toml:"files"`
	Branch          *BranchTruth    `toml:"branch"`

	// path is the file the truth was read from (for error messages).
	path string
}

// Summary is the [summary] table.
type Summary struct {
	Shape        string   `toml:"shape"`
	ShapeOK      []string `toml:"shape_ok"`
	RiskMax      int      `toml:"risk_max"`
	RiskBand     string   `toml:"risk_band"`
	RiskBandOK   []string `toml:"risk_band_ok"`
	Breaking     int      `toml:"breaking"`
	BreakingOK   []int    `toml:"breaking_ok"`
	NeedsEyesMax *int     `toml:"needs_eyes_max"`
	OneLine      string   `toml:"one_line"`
}

// LinesThatMatter is the [lines_that_matter] table.
type LinesThatMatter struct {
	TotalChanged int     `toml:"total_changed"`
	MustRead     int     `toml:"must_read"`
	Skim         int     `toml:"skim"`
	Skip         int     `toml:"skip"`
	Tolerance    float64 `toml:"tolerance"`
	EstMinutes   float64 `toml:"est_minutes"`
	Note         string  `toml:"note"`
}

// Loc is a located finding: path, inclusive line range on one side, and
// the other locations that count as the same finding (also_at).
type Loc struct {
	Path   string   `toml:"path"`
	Lines  []int    `toml:"lines"`
	Side   string   `toml:"side"`
	AlsoAt []string `toml:"also_at"`
	Symbol string   `toml:"symbol"`
}

// NeedsEyes is one [[needs_eyes]] region.
type NeedsEyes struct {
	Loc
	Depth     string `toml:"depth"`
	Certainty string `toml:"certainty"`
	Reason    string `toml:"reason"`
}

// TruthHeadline is one [[headlines]] entry.
type TruthHeadline struct {
	Loc
	Contract  string `toml:"contract"`
	Breaking  bool   `toml:"breaking"`
	Certainty string `toml:"certainty"`
	Summary   string `toml:"summary"`
}

// NotHeadline is one [[not_headlines]] entry: flagging it is wrong.
type NotHeadline struct {
	Loc
	Why string `toml:"why"`
}

// TruthHotSpot is one [[hot_spots]] entry, most important first by Rank.
type TruthHotSpot struct {
	Loc
	Rank       int      `toml:"rank"`
	Risk       int      `toml:"risk"`
	FailMode   string   `toml:"fail_mode"`
	FailModeOK []string `toml:"fail_mode_ok"`
	RiskTags   []string `toml:"risk_tags"`
	Certainty  string   `toml:"certainty"`
	Reason     string   `toml:"reason"`
	Detail     string   `toml:"detail"`
}

// Coverage is one [[coverage]] entry: the expected covered answer.
type Coverage struct {
	Loc
	Covered bool   `toml:"covered"`
	By      string `toml:"by"`
}

// Fidelity is the [fidelity] table.
type Fidelity struct {
	Applicable       bool                `toml:"applicable"`
	BulletsTotal     int                 `toml:"bullets_total"`
	Implemented      int                 `toml:"implemented"`
	UndocumentedFirm *int                `toml:"undocumented_firm"`
	Notes            string              `toml:"notes"`
	Bullets          []BulletTruth       `toml:"bullets"`
	Undocumented     []UndocumentedTruth `toml:"undocumented"`
}

// Bullet statuses.
const (
	BulletImplemented    = "implemented"
	BulletPartial        = "partial"
	BulletNotImplemented = "not implemented"
)

// BulletTruth is one [[fidelity.bullets]] entry.
type BulletTruth struct {
	Text     string `toml:"text"`
	Status   string `toml:"status"`
	Evidence string `toml:"evidence"`
}

// UndocumentedTruth is one [[fidelity.undocumented]] entry.
type UndocumentedTruth struct {
	Loc
	Certainty string `toml:"certainty"`
	What      string `toml:"what"`
}

// CommentTruth is one [[comments]] entry: the expected comment_addressed
// answer for a review thread of prs.json.
type CommentTruth struct {
	Thread    string `toml:"thread"`
	Path      string `toml:"path"`
	Line      int    `toml:"line"`
	Addressed bool   `toml:"addressed"`
	Certainty string `toml:"certainty"`
	Why       string `toml:"why"`
}

// FileTruth is one [[files]] entry: the file's dominant kind (also: other
// acceptable kinds) and role.
type FileTruth struct {
	Path string   `toml:"path"`
	Kind string   `toml:"kind"`
	Also []string `toml:"also"`
	Role string   `toml:"role"`
}

// BranchTruth is the [branch] table of a branch item.
type BranchTruth struct {
	State              string   `toml:"state"`
	StateOK            []string `toml:"state_ok"`
	Evidence           string   `toml:"evidence"`
	SuggestedTitleLike string   `toml:"suggested_title_like"`
}

// Vocabularies (docs/spec.md "Jev question catalogue", as listed in the
// ground-truth README).
var (
	vocabKind     = []string{"feature", "bugfix", "refactor", "perf", "test", "config", "docs", "dependency", "generated", "formatting"}
	vocabContract = []string{"none", "signature", "api shape", "schema/migration", "env/flag", "event format"}
	vocabDepth    = []string{"skip", "skim", "read", "run locally"}
	vocabFailMode = []string{"crash", "wrong data", "security", "slowdown", "cosmetic"}
	vocabRiskTags = []string{"auth", "async/race", "error path", "data write", "money", "pii", "io-in-loop", "date/tz"}
	vocabShape    = []string{"single-purpose", "mixed", "sweeping refactor", "WIP"}
	vocabState    = []string{"WIP", "ready for PR", "spike", "abandoned", "superseded"}
	vocabRole     = []string{"core", "glue", "ui", "test", "config", "generated"}
	vocabBand     = []string{"HIGH", "MED", "LOW"}
	vocabBullet   = []string{BulletImplemented, BulletPartial, BulletNotImplemented}
)

var itemRe = regexp.MustCompile(`^(pr:[1-9][0-9]*|branch:[^\s]+)$`)

// LoadDir parses every *.toml file in dir strictly (see Parse), sorted by
// item (PRs by number, then branches by name).
func LoadDir(dir string) ([]Truth, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, fmt.Errorf("quality: list %s: %w", dir, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("quality: no ground-truth files in %s", dir)
	}
	out := make([]Truth, 0, len(paths))
	seen := map[string]string{}
	for _, p := range paths {
		t, err := ParseFile(p)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[t.Item]; dup {
			return nil, fmt.Errorf("quality: item %s in both %s and %s", t.Item, prev, p)
		}
		seen[t.Item] = p
		out = append(out, t)
	}
	SortTruths(out)
	return out, nil
}

// SortTruths orders truths by item: PRs by number, then branches by name.
func SortTruths(ts []Truth) {
	sort.SliceStable(ts, func(i, j int) bool { return itemLess(ts[i].Item, ts[j].Item) })
}

// itemLess orders item ids: "pr:<n>" numerically first, then the rest by
// name.
func itemLess(a, b string) bool {
	na, aPR := prNumber(a)
	nb, bPR := prNumber(b)
	switch {
	case aPR && bPR:
		return na < nb
	case aPR != bPR:
		return aPR
	}
	return a < b
}

// prNumber parses "pr:<n>".
func prNumber(item string) (int, bool) {
	s, ok := strings.CutPrefix(item, "pr:")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// ParseFile reads and parses one ground-truth file (see Parse).
func ParseFile(path string) (Truth, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Truth{}, fmt.Errorf("quality: read %s: %w", path, err)
	}
	t, err := Parse(b)
	if err != nil {
		return Truth{}, fmt.Errorf("quality: %s: %w", filepath.Base(path), err)
	}
	t.path = path
	return t, nil
}

// Parse decodes a ground-truth file strictly: any key the schema of
// testdata/groundtruth/README.md does not define is an error (toml
// MetaData.Undecoded), and every entry is validated (vocabularies, line
// ranges, certainty, also_at syntax).
func Parse(b []byte) (Truth, error) {
	var t Truth
	md, err := toml.Decode(string(b), &t)
	if err != nil {
		return Truth{}, fmt.Errorf("decode: %w", err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		keys := make([]string, len(und))
		for i, k := range und {
			keys[i] = k.String()
		}
		return Truth{}, fmt.Errorf("unknown keys: %s", strings.Join(keys, ", "))
	}
	if err := t.validate(); err != nil {
		return Truth{}, err
	}
	return t, nil
}

// validate checks every field against the schema.
func (t *Truth) validate() error {
	var errs []error
	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	if !itemRe.MatchString(t.Item) {
		add(fmt.Errorf("item %q: want pr:<n> or branch:<name>", t.Item))
	}
	add(t.validateSummary())
	add(t.validateLocated())
	add(t.validateFidelity())
	add(t.validateRest())
	return errors.Join(errs...)
}

func (t *Truth) validateSummary() error {
	s := t.Summary
	var errs []error
	errs = append(errs, inVocab("summary.shape", s.Shape, vocabShape, true))
	for _, v := range s.ShapeOK {
		errs = append(errs, inVocab("summary.shape_ok", v, vocabShape, false))
	}
	errs = append(errs, inVocab("summary.risk_band", s.RiskBand, vocabBand, true))
	for _, v := range s.RiskBandOK {
		errs = append(errs, inVocab("summary.risk_band_ok", v, vocabBand, false))
	}
	if s.RiskMax < 0 || s.RiskMax > 5 {
		errs = append(errs, fmt.Errorf("summary.risk_max %d: want 0..5", s.RiskMax))
	}
	if s.NeedsEyesMax != nil && *s.NeedsEyesMax < 0 {
		errs = append(errs, fmt.Errorf("summary.needs_eyes_max %d: want >= 0", *s.NeedsEyesMax))
	}
	l := t.LinesThatMatter
	if l.MustRead < 0 || l.Skim < 0 || l.Skip < 0 || l.TotalChanged < 0 || l.Tolerance < 0 {
		errs = append(errs, errors.New("lines_that_matter: negative value"))
	}
	return errors.Join(errs...)
}

func (t *Truth) validateLocated() error {
	var errs []error
	for i, e := range t.NeedsEyes {
		w := fmt.Sprintf("needs_eyes[%d]", i)
		errs = append(errs, e.validate(w), certainty(w, e.Certainty), inVocab(w+".depth", e.Depth, vocabDepth, false))
	}
	for i, e := range t.Headlines {
		w := fmt.Sprintf("headlines[%d]", i)
		errs = append(errs, e.validate(w), certainty(w, e.Certainty), inVocab(w+".contract", e.Contract, vocabContract, false))
	}
	for i, e := range t.NotHeadlines {
		errs = append(errs, e.validate(fmt.Sprintf("not_headlines[%d]", i)))
	}
	for i, e := range t.HotSpots {
		w := fmt.Sprintf("hot_spots[%d]", i)
		errs = append(errs, e.validate(w), certainty(w, e.Certainty), inVocab(w+".fail_mode", e.FailMode, vocabFailMode, false))
		for _, v := range e.FailModeOK {
			errs = append(errs, inVocab(w+".fail_mode_ok", v, vocabFailMode, false))
		}
		for _, v := range e.RiskTags {
			errs = append(errs, inVocab(w+".risk_tags", v, vocabRiskTags, false))
		}
		if e.Rank < 1 {
			errs = append(errs, fmt.Errorf("%s.rank %d: want >= 1", w, e.Rank))
		}
		if e.Risk < 0 || e.Risk > 5 {
			errs = append(errs, fmt.Errorf("%s.risk %d: want 0..5", w, e.Risk))
		}
	}
	for i, e := range t.Coverage {
		errs = append(errs, e.validate(fmt.Sprintf("coverage[%d]", i)))
	}
	return errors.Join(errs...)
}

func (t *Truth) validateFidelity() error {
	var errs []error
	for i, b := range t.Fidelity.Bullets {
		w := fmt.Sprintf("fidelity.bullets[%d]", i)
		if strings.TrimSpace(b.Text) == "" {
			errs = append(errs, fmt.Errorf("%s: empty text", w))
		}
		errs = append(errs, inVocab(w+".status", b.Status, vocabBullet, true))
	}
	for i, u := range t.Fidelity.Undocumented {
		w := fmt.Sprintf("fidelity.undocumented[%d]", i)
		errs = append(errs, u.validate(w), certainty(w, u.Certainty))
	}
	if n := len(t.Fidelity.Bullets); n > 0 && t.Fidelity.BulletsTotal != n {
		errs = append(errs, fmt.Errorf("fidelity.bullets_total %d, but %d bullets listed", t.Fidelity.BulletsTotal, n))
	}
	return errors.Join(errs...)
}

func (t *Truth) validateRest() error {
	var errs []error
	for i, c := range t.Comments {
		w := fmt.Sprintf("comments[%d]", i)
		if c.Thread == "" {
			errs = append(errs, fmt.Errorf("%s: empty thread", w))
		}
		errs = append(errs, certainty(w, c.Certainty))
	}
	for i, f := range t.Files {
		w := fmt.Sprintf("files[%d]", i)
		if f.Path == "" {
			errs = append(errs, fmt.Errorf("%s: empty path", w))
		}
		errs = append(errs, inVocab(w+".kind", f.Kind, vocabKind, true), inVocab(w+".role", f.Role, vocabRole, false))
		for _, k := range f.Also {
			errs = append(errs, inVocab(w+".also", k, vocabKind, false))
		}
	}
	if b := t.Branch; b != nil {
		if !strings.HasPrefix(t.Item, "branch:") {
			errs = append(errs, fmt.Errorf("[branch] on non-branch item %s", t.Item))
		}
		errs = append(errs, inVocab("branch.state", b.State, vocabState, true))
		for _, v := range b.StateOK {
			errs = append(errs, inVocab("branch.state_ok", v, vocabState, false))
		}
	}
	return errors.Join(errs...)
}

// validate checks a location: a path, lines [first, last] with
// 1 <= first <= last, side new/old, and well-formed also_at entries.
func (l Loc) validate(what string) error {
	if l.Path == "" {
		return fmt.Errorf("%s: empty path", what)
	}
	if len(l.Lines) != 2 || l.Lines[0] < 1 || l.Lines[0] > l.Lines[1] {
		return fmt.Errorf("%s: lines %v: want [first, last] with 1 <= first <= last", what, l.Lines)
	}
	if l.Side != "" && l.Side != SideNew && l.Side != SideOld {
		return fmt.Errorf("%s: side %q: want new or old", what, l.Side)
	}
	for _, a := range l.AlsoAt {
		if _, err := parseAlsoAt(a); err != nil {
			return fmt.Errorf("%s: also_at %q: %w", what, a, err)
		}
	}
	return nil
}

// certainty checks a certainty value (required).
func certainty(what, v string) error {
	if v != Firm && v != Soft {
		return fmt.Errorf("%s: certainty %q: want firm or soft", what, v)
	}
	return nil
}

// inVocab checks v against a vocabulary; an empty v is accepted unless
// required.
func inVocab(what, v string, vocab []string, required bool) error {
	if v == "" && !required {
		return nil
	}
	if !slices.Contains(vocab, v) {
		return fmt.Errorf("%s %q: want one of %s", what, v, strings.Join(vocab, ", "))
	}
	return nil
}
