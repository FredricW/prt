package review

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Review verdicts.
const (
	VerdictApprove        = "approve"
	VerdictComment        = "comment"
	VerdictRequestChanges = "request_changes"
)

// rawReview is the response JSON as the model may write it: lines and
// confidence may arrive as numbers or strings, the path as "file" or
// "path".
type rawReview struct {
	Findings   []rawFinding    `json:"findings"`
	Verdict    *string         `json:"verdict"`
	Confidence json.RawMessage `json:"confidence"`
}

// rawFinding is one finding as written by the model.
type rawFinding struct {
	File     string          `json:"file"`
	Path     string          `json:"path"`
	Line     json.RawMessage `json:"line"`
	Severity string          `json:"severity"`
	Claim    string          `json:"claim"`
	Fix      string          `json:"fix"`
}

// maxJSONAttempts bounds how many '{' positions ParseReview tries.
const maxJSONAttempts = 64

// ParseReview parses a TaskReview response text into findings (anchored to
// a unit of units by path and line, see AnchorFindings), a verdict and a
// confidence in [0,1]. It tolerates code fences and prose around the JSON
// object. Severities other than low/medium/high become low, claims are cut
// to their first sentence, findings without a claim are dropped, and the
// findings come back in a deterministic order (path, line, severity high
// first, claim). A response with no JSON object holding "findings" or
// "verdict" returns an error wrapping ErrBadResponse.
func ParseReview(text string, units []model.ChangeUnit) ([]model.Finding, string, float64, error) {
	raw, err := extractReview(text)
	if err != nil {
		return nil, "", 0, err
	}
	var fs []model.Finding
	for _, rf := range raw.Findings {
		if f, ok := normaliseFinding(rf); ok {
			fs = append(fs, f)
		}
	}
	fs = AnchorFindings(fs, units)
	sortFindings(fs)
	fs = dedupeFindings(fs)
	verdict := ""
	if raw.Verdict != nil {
		verdict = *raw.Verdict
	}
	return fs, normaliseVerdict(verdict, fs), clamp01(number(raw.Confidence)), nil
}

// extractReview finds the first JSON object in text that decodes as a
// review (has "findings" or "verdict").
func extractReview(text string) (rawReview, error) {
	attempts := 0
	for i := strings.IndexByte(text, '{'); i >= 0 && attempts < maxJSONAttempts; attempts++ {
		var r rawReview
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		if err := dec.Decode(&r); err == nil && (r.Findings != nil || r.Verdict != nil) {
			return r, nil
		}
		next := strings.IndexByte(text[i+1:], '{')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return rawReview{}, fmt.Errorf("%w: no JSON object with findings or verdict in %d bytes", ErrBadResponse, len(text))
}

// normaliseFinding cleans one raw finding; ok is false when it has no claim.
func normaliseFinding(rf rawFinding) (model.Finding, bool) {
	claim := firstSentence(rf.Claim)
	if claim == "" {
		return model.Finding{}, false
	}
	path := rf.File
	if path == "" {
		path = rf.Path
	}
	line := int(number(rf.Line))
	if line < 0 {
		line = 0
	}
	return model.Finding{
		Path: normalisePath(path), Line: line, Severity: normaliseSeverity(rf.Severity),
		Claim: claim, Fix: oneLine(rf.Fix),
	}, true
}

// number reads a JSON number or numeric string; 0 when absent or invalid.
func number(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return f
		}
	}
	return 0
}

// clamp01 clamps f to [0,1] (NaN → 0).
func clamp01(f float64) float64 {
	if math.IsNaN(f) || f < 0 {
		return 0
	}
	return min(f, 1)
}

// normaliseSeverity maps a severity to low/medium/high (unknown → low).
func normaliseSeverity(s string) model.Severity {
	switch model.Severity(strings.ToLower(strings.TrimSpace(s))) {
	case model.SevHigh:
		return model.SevHigh
	case model.SevMedium:
		return model.SevMedium
	}
	return model.SevLow
}

// severityRank orders severities high first.
func severityRank(s model.Severity) int {
	switch s {
	case model.SevHigh:
		return 0
	case model.SevMedium:
		return 1
	}
	return 2
}

// normaliseVerdict maps a verdict to approve/comment/request_changes.
// An unknown or missing verdict is request_changes when a finding is high,
// else comment.
func normaliseVerdict(v string, fs []model.Finding) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.NewReplacer("-", "_", " ", "_").Replace(v)
	switch v {
	case VerdictApprove, VerdictComment, VerdictRequestChanges:
		return v
	}
	for _, f := range fs {
		if f.Severity == model.SevHigh {
			return VerdictRequestChanges
		}
	}
	return VerdictComment
}

// normalisePath trims whitespace, "./" prefixes and a leading "/".
func normalisePath(p string) string {
	p = strings.TrimSpace(p)
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	return strings.TrimPrefix(p, "/")
}

// AnchorFindings sets each finding's UnitID: the unit of the same path
// whose span holds the line (the new-side span, or the old-side span of a
// pure deletion), else the nearest unit in that file (ties: the earlier
// unit in units). A finding on a file outside units keeps UnitID "".
func AnchorFindings(fs []model.Finding, units []model.ChangeUnit) []model.Finding {
	byPath := map[string][]model.ChangeUnit{}
	for _, u := range units {
		p := normalisePath(u.Path)
		byPath[p] = append(byPath[p], u)
	}
	for i := range fs {
		fs[i].UnitID = ""
		cands := byPath[normalisePath(fs[i].Path)]
		best, bestDist := "", math.MaxInt
		for _, u := range cands {
			if d := spanDistance(anchorSpan(u), fs[i].Line); d < bestDist {
				best, bestDist = u.ID, d
			}
		}
		fs[i].UnitID = best
	}
	return fs
}

// anchorSpan is the span findings anchor to: the new side, or the old side
// for a pure deletion.
func anchorSpan(u model.ChangeUnit) model.Span {
	if u.Range.New.Count == 0 && u.Range.Old.Count > 0 {
		return u.Range.Old
	}
	return u.Range.New
}

// spanDistance is 0 when line lies inside s, else the distance to its
// nearest end.
func spanDistance(s model.Span, line int) int {
	end := s.Start + max(s.Count, 1) - 1
	switch {
	case line < s.Start:
		return s.Start - line
	case line > end:
		return line - end
	}
	return 0
}

// sortFindings orders findings by path, line, severity (high first) and
// claim.
func sortFindings(fs []model.Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if ra, rb := severityRank(a.Severity), severityRank(b.Severity); ra != rb {
			return ra < rb
		}
		return a.Claim < b.Claim
	})
}

// dedupeFindings drops exact repeats from sorted findings.
func dedupeFindings(fs []model.Finding) []model.Finding {
	var out []model.Finding
	for _, f := range fs {
		if len(out) > 0 && f == out[len(out)-1] {
			continue
		}
		out = append(out, f)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
