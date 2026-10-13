package model

import (
	"regexp"
	"strconv"
	"strings"
)

// Label conventions (M1 shared contract; see DECISIONS.md "M1 label
// conventions"). Every package that writes or reads labels follows these:
//
//   - Noul (boolean) question: Value is LabelYes when P >= 0.5, else LabelNo.
//     P is the probability of "yes" (not of Value). Confidence = max(P, 1-P).
//   - Choice question: Value is the chosen option exactly as written in the
//     catalogue (desc_bullet uses BulletValue(i) / BulletNone). P and
//     Confidence are the chosen option's probability; Probs holds every
//     option's probability.
//   - Score question: Value is the most probable rung as a 1-based decimal
//     ("1".."5"); Score is the interpolated score shifted to 1-based (Jev's
//     0-based index + 1); Probs is keyed by 1-based rung ("1".."5"); P and
//     Confidence are the most probable rung's probability. Thresholds such as
//     "risk >= 4" compare ScoreLevel (the integer Value); products such as
//     hot_spot use Score.
//   - Multi-boolean questions (catalogue rows typed "N ×k", e.g. risk_tags)
//     produce one boolean label per option: Question = "<id>.<sub>" (see
//     SubQuestion), e.g. "risk_tags.auth".
//   - Line questions: UnitID is the unit id and Line the 1-based position of
//     the added line among the unit's "+" lines (not its file line number),
//     so labels stay valid when the same unit sits at another offset (a
//     rebase, or another item). AddedLineNumbers maps positions to new-side
//     line numbers; aggregate and the TUI only ever show the latter.
//   - Jev unreachable: Value LabelUnknown, P 0, Confidence 0, Probs nil,
//     Model "". Unknown labels are never treated as cached answers.
//   - Model is the Jev model id ("typesafe-ai/jev") for real answers.

// Boolean label values.
const (
	LabelYes = "yes"
	LabelNo  = "no"
)

// Question ids from the spec's catalogue that code consumes. The catalogue
// (internal/questions/default.toml) is the source of prompts and options;
// these constants only name the ids.
const (
	// Line scope.
	QFmt         = "fmt"
	QValueTweak  = "value_tweak"
	QLeftover    = "leftover"
	QSwallow     = "swallow"
	QPIIOrSecret = "pii_or_secret"
	QSuspicious  = "suspicious"

	// Unit scope.
	QKind             = "kind"
	QBehaviour        = "behaviour"
	QEffect           = "effect"
	QDescBullet       = "desc_bullet"
	QBreaking         = "breaking"
	QContract         = "contract"
	QReversible       = "reversible"
	QRiskTags         = "risk_tags"
	QFailMode         = "fail_mode"
	QRisk             = "risk"
	QCovered          = "covered"
	QTestWeakened     = "test_weakened"
	QFollowsPattern   = "follows_pattern"
	QDuplicatesHelper = "duplicates_helper"
	QInterface        = "interface"
	QRelevance        = "relevance"
	QDepth            = "depth"
	QNeedsContext     = "needs_context"
	QReasoning        = "reasoning"

	// File, group, pair, item, branch scopes.
	QRole               = "role"
	QSeparatePR         = "separate_pr"
	QNameBullet         = "name_bullet"
	QRelation           = "relation"
	QSemanticConflict   = "semantic_conflict"
	QShape              = "shape"
	QDescSufficient     = "desc_sufficient"
	QFlagged            = "flagged"
	QRollback           = "rollback"
	QNeedsOtherReviewer = "needs_other_reviewer"
	QCommentAddressed   = "comment_addressed"
	QState              = "state"
)

// RiskTags are the sub-ids of the risk_tags question, in spec order
// (auth, async/race, error path, data write, money, pii, io-in-loop, date/tz).
// Routing rules refer to them as tag.<sub-id>.
var RiskTags = []string{"auth", "async", "error_path", "data_write", "money", "pii", "io_in_loop", "date_tz"}

// NeedsContextKinds are the sub-ids of the needs_context question.
var NeedsContextKinds = []string{"callers", "types", "tests", "config"}

// Kind label values (question "kind") that count as noise.
const (
	KindFormatting = "formatting"
	KindGenerated  = "generated"
	KindDependency = "dependency"
)

// Depth label values (question "depth").
const (
	DepthSkip = "skip"
	DepthSkim = "skim"
	DepthRead = "read"
	DepthRun  = "run locally"
)

// Contract label value meaning no contract change.
const ContractNone = "none"

// BulletNone is the desc_bullet / name_bullet choice for "no bullet".
const BulletNone = "none"

// SubQuestion returns the label question id of a multi-boolean sub-question,
// e.g. SubQuestion("risk_tags", "auth") == "risk_tags.auth".
func SubQuestion(id, sub string) string { return id + "." + sub }

// BulletValue is the desc_bullet choice value for the i-th (1-based)
// description bullet: "b1", "b2", ...
func BulletValue(i int) string { return "b" + strconv.Itoa(i) }

// BulletIndex parses a BulletValue; 0 for BulletNone, unknown or malformed.
func BulletIndex(v string) int {
	if !strings.HasPrefix(v, "b") {
		return 0
	}
	n, err := strconv.Atoi(v[1:])
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// ScoreLevel returns the integer rung (1..5) of a Score label, 0 if unknown.
func ScoreLevel(l Label) int {
	n, err := strconv.Atoi(l.Value)
	if err != nil {
		return 0
	}
	return n
}

// AddedLineNumbers returns the new-side line numbers of u's added lines in
// diff order, numbering from Range.New.Start: "+" lines and context lines
// advance the new side; "-" lines, hunk headers and "\ No newline" markers
// do not. Element i is the line of the line label with Line i+1.
func AddedLineNumbers(u ChangeUnit) []int {
	if u.Diff == "" {
		return nil
	}
	var out []int
	n := u.Range.New.Start
	for _, ln := range strings.Split(strings.TrimSuffix(u.Diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(ln, "@@"), strings.HasPrefix(ln, `\`), strings.HasPrefix(ln, "-"):
		case strings.HasPrefix(ln, "+"):
			out = append(out, n)
			n++
		default:
			n++
		}
	}
	return out
}

// IsUnknown reports whether a label is the "Jev unreachable" placeholder.
func IsUnknown(l Label) bool { return l.Value == LabelUnknown }

// ItemSubject is the Label.UnitID used for item-scope questions.
func ItemSubject(itemID string) string { return "item:" + itemID }

// FileSubject is the Label.UnitID used for file-scope questions.
func FileSubject(itemID, path string) string { return "file:" + itemID + ":" + path }

var bulletRe = regexp.MustCompile(`^\s{0,3}(?:[-*+]|\d+[.)])\s+(.*\S)\s*$`)

// DescBullets returns the list items of a PR description in order (lines
// starting with "-", "*", "+" or "1." at indent ≤ 3), trimmed. Items inside
// fenced code blocks are skipped. These are the bullets offered to the
// desc_bullet question (as BulletValue(i)) and counted by fidelity.
func DescBullets(body string) []string {
	var out []string
	fenced := false
	for _, ln := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced {
			continue
		}
		if m := bulletRe.FindStringSubmatch(ln); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}
