package judge

import (
	"math"
	"slices"
	"strconv"

	"github.com/fredricw/prt/internal/jev"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/questions"
)

// ModelNoBullets is Label.Model for a name_bullet label decided without Jev
// because the PR description has no bullets (same as internal/labels).
const ModelNoBullets = "prt:no-bullets"

// unknownLabel is the placeholder for an unanswered question.
func unknownLabel(subject, ctx string, wq questions.WireQuestion) model.Label {
	return model.Label{UnitID: subject, Question: wq.Label, Value: model.LabelUnknown, Ctx: ctx}
}

// decidedLabel answers a single-option choice without asking Jev.
func decidedLabel(subject, ctx string, wq questions.WireQuestion) model.Label {
	v := model.BulletNone
	if len(wq.Options) == 1 {
		v = wq.Options[0]
	}
	return model.Label{
		UnitID: subject, Question: wq.Label, Value: v, P: 1, Confidence: 1,
		Probs: map[string]float64{v: 1}, Model: ModelNoBullets, Ctx: ctx,
	}
}

// decode maps one answer to a label (internal/model/labels.go conventions);
// a missing, mistyped or out-of-catalogue answer yields an unknown label.
func decode(subject, ctx string, wq questions.WireQuestion, a jev.Answer, ok bool, modelID string) model.Label {
	l := model.Label{UnitID: subject, Question: wq.Label, Model: modelID, Ctx: ctx}
	good := false
	if ok {
		switch wq.Type {
		case questions.Noul:
			good = decodeBoolean(&l, a)
		case questions.Choice:
			good = decodeChoice(&l, a, wq.Options)
		case questions.Score:
			good = decodeScore(&l, a, len(wq.Options))
		}
	}
	if !good {
		return unknownLabel(subject, ctx, wq)
	}
	return l
}

// typeOK accepts an answer of the expected type, or one without a type.
func typeOK(a jev.Answer, want jev.QuestionType) bool { return a.Type == "" || a.Type == want }

func validProb(p float64) bool { return !math.IsNaN(p) && p >= 0 && p <= 1 }

func decodeBoolean(l *model.Label, a jev.Answer) bool {
	if !typeOK(a, jev.Boolean) || a.Probability == nil || !validProb(*a.Probability) {
		return false
	}
	p := *a.Probability
	l.Value = model.LabelNo
	if p >= 0.5 {
		l.Value = model.LabelYes
	}
	l.P, l.Confidence = p, max(p, 1-p)
	return true
}

func decodeChoice(l *model.Label, a jev.Answer, options []string) bool {
	if !typeOK(a, jev.Choice) || !slices.Contains(options, a.Choice) {
		return false
	}
	probs := map[string]float64{}
	for _, o := range options {
		if p, ok := a.Probabilities[o]; ok && validProb(p) {
			probs[o] = p
		}
	}
	l.Value = a.Choice
	l.P = probs[a.Choice]
	l.Confidence = l.P
	l.Probs = probs
	return true
}

func decodeScore(l *model.Label, a jev.Answer, rungs int) bool {
	if !typeOK(a, jev.Score) {
		return false
	}
	probs := map[string]float64{}
	best, bestP := -1, -1.0
	for i := range rungs {
		p, ok := a.Probabilities[strconv.Itoa(i)]
		if !ok || !validProb(p) {
			continue
		}
		probs[strconv.Itoa(i+1)] = p
		if p > bestP {
			best, bestP = i, p
		}
	}
	if best < 0 {
		return false
	}
	l.Value = strconv.Itoa(best + 1)
	l.P, l.Confidence = bestP, bestP
	l.Probs = probs
	l.Score = float64(best + 1)
	if a.Score != nil && !math.IsNaN(*a.Score) && *a.Score >= 0 && *a.Score <= float64(rungs-1) {
		l.Score = *a.Score + 1
	}
	return true
}
