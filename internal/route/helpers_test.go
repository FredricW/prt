package route

import (
	"strconv"

	"github.com/fredricw/prt/internal/model"
)

// unit returns a modified unit with id, order and 10 changed lines.
func unit(id string, order int) model.ChangeUnit {
	return model.ChangeUnit{ID: id, Path: "src/" + id + ".go", Kind: model.UnitModified, Adds: 6, Dels: 4, Order: order}
}

func yes(unitID, q string) model.Label {
	return model.Label{UnitID: unitID, Question: q, Value: model.LabelYes, P: 0.9, Confidence: 0.9}
}

func no(unitID, q string) model.Label {
	return model.Label{UnitID: unitID, Question: q, Value: model.LabelNo, P: 0.1, Confidence: 0.9}
}

func unknownLabel(unitID, q string) model.Label {
	return model.Label{UnitID: unitID, Question: q, Value: model.LabelUnknown}
}

func choice(unitID, q, v string) model.Label {
	return model.Label{UnitID: unitID, Question: q, Value: v, P: 0.8, Confidence: 0.8}
}

func score(unitID, q string, rung int, interp float64) model.Label {
	return model.Label{UnitID: unitID, Question: q, Value: strconv.Itoa(rung), Score: interp, P: 0.7, Confidence: 0.7}
}

// fullLabels returns a complete, quiet label set for a unit: every routing
// question answered, nothing risky, kind as given.
func fullLabels(unitID, kind string) []model.Label {
	ls := []model.Label{
		choice(unitID, model.QKind, kind),
		no(unitID, model.QBreaking),
		no(unitID, model.QBehaviour),
		no(unitID, model.QTestWeakened),
		choice(unitID, model.QContract, model.ContractNone),
		choice(unitID, model.QEffect, "none"),
		score(unitID, model.QRisk, 2, 2.2),
		score(unitID, model.QRelevance, 3, 2.6),
	}
	for _, t := range model.RiskTags {
		ls = append(ls, no(unitID, model.SubQuestion(model.QRiskTags, t)))
	}
	return ls
}

// set replaces (or adds) the label for its subject and question.
func set(ls []model.Label, l model.Label) []model.Label {
	for i := range ls {
		if ls[i].UnitID == l.UnitID && ls[i].Question == l.Question && ls[i].Line == l.Line {
			ls[i] = l
			return ls
		}
	}
	return append(ls, l)
}

// group builds GroupFacts for the units, with ids g-<first unit id>.
func group(units []model.ChangeUnit, labels []model.Label) GroupFacts {
	ids := make([]string, len(units))
	for i, u := range units {
		ids[i] = u.ID
	}
	return GroupFacts{Group: model.Group{ID: "g-" + ids[0], UnitIDs: ids}, Units: units, Labels: labels}
}
