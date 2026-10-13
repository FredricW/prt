package model

import (
	"reflect"
	"testing"
)

func TestDescBullets(t *testing.T) {
	body := "Intro line.\n\n## Changes\n\n- `getUser` takes opts\n* second item \n1. third\n```\n- not a bullet\n```\n    - too deep (code)\nplain\n"
	got := DescBullets(body)
	want := []string{"`getUser` takes opts", "second item", "third"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DescBullets = %q, want %q", got, want)
	}
}

func TestBulletValueRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{{BulletValue(3), 3}, {BulletNone, 0}, {LabelUnknown, 0}, {"b0", 0}, {"bx", 0}} {
		if got := BulletIndex(tc.in); got != tc.want {
			t.Errorf("BulletIndex(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestScoreLevel(t *testing.T) {
	if got := ScoreLevel(Label{Value: "4"}); got != 4 {
		t.Fatalf("ScoreLevel = %d", got)
	}
	if got := ScoreLevel(Label{Value: LabelUnknown}); got != 0 {
		t.Fatalf("ScoreLevel(unknown) = %d", got)
	}
	if SubQuestion(QRiskTags, "auth") != "risk_tags.auth" {
		t.Fatal("SubQuestion")
	}
}
