package route

import (
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/model"
)

func TestCompileDefault(t *testing.T) {
	rs, err := Compile(config.DefaultRouting())
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", rs.Warnings)
	}
	if len(rs.Rules) != 5 {
		t.Fatalf("rules = %d, want 5", len(rs.Rules))
	}
	r := rs.Rules[1]
	if r.Index != 2 || r.Tier != model.TierDeep || r.Words != "if risk >= 4 || tag.auth || tag.money || tag.async → deep" {
		t.Errorf("rule 2 = %+v", r)
	}
}

func TestCompileErrors(t *testing.T) {
	ok := config.Rule{When: "true", Tier: "fast"}
	tests := []struct {
		name string
		bad  config.Rule
		want string
	}{
		{"tier", config.Rule{When: "true", Tier: "slow"}, `rule 2: unknown tier "slow"`},
		{"prompt", config.Rule{When: "true", Tier: "deep", Prompts: []string{"style"}}, `rule 2: unknown prompt "style"`},
		{"context", config.Rule{When: "true", Tier: "deep", Context: []string{"docs"}}, `rule 2: unknown context "docs"`},
		{"parse", config.Rule{When: "risk >=", Tier: "deep"}, `rule 2: when "risk >=": col 8`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(config.Routing{Rules: []config.Rule{ok, tt.bad}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestCompileWarnings(t *testing.T) {
	rs, err := Compile(config.Routing{Rules: []config.Rule{
		{When: "riks >= 4 || tag.secret || risk_tags.auth || needs_context.callers", Tier: "deep"},
		{When: "units > 3 && lines > 100 && !ungrouped && swallow", Tier: "fast"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`rule 1: unknown identifier "riks"`, `rule 1: unknown identifier "tag.secret"`, `rule 2: unknown identifier "swallow"`}
	if len(rs.Warnings) != len(want) {
		t.Fatalf("warnings = %v, want %d", rs.Warnings, len(want))
	}
	for i, w := range want {
		if !strings.HasPrefix(rs.Warnings[i], w) {
			t.Errorf("warning %d = %q, want prefix %q", i, rs.Warnings[i], w)
		}
	}
}
