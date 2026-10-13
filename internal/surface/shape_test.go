package surface

import (
	"context"
	"testing"
)

func TestResponseShapes(t *testing.T) {
	runCases(t, []caseTest{
		{"goshape", "added", []string{"added response shape: userResponse.avatarUrl", "added request shape: createUserRequest.email"}},
		{"goshape", "removed", []string{"removed response shape: userResponse.legacy"}},
		{"goshape", "changed", []string{"changed response shape: userResponse.count", "changed response shape: userResponse.displayName"}},
		{"goshape", "negative", nil},

		{"tsshape", "added", []string{"added response shape: UserResponse.avatarUrl"}},
		{"tsshape", "removed", []string{"removed response shape: UserResponse.legacy"}},
		{"tsshape", "changed", []string{"changed response shape: ListUsersResponse.count"}},
		{"tsshape", "negative", nil},
	})
}

// TestShapeConfidence checks shape entries stay below exact registrations
// and say what changed.
func TestShapeConfidence(t *testing.T) {
	cs := build(context.Background(), patchInput(t, loadCases(t, "goshape")["changed"]))
	tests := []struct{ words, detail string }{
		{"changed response shape: userResponse.count", "was int `json:\"count\"`"},
		{"changed response shape: userResponse.displayName", `was "name"`},
	}
	for _, tc := range tests {
		e := find(t, cs, tc.words)
		if e.Detail != tc.detail || e.Confidence >= confRouteName {
			t.Errorf("%s = %+v, want detail %q and a heuristic confidence", tc.words, e, tc.detail)
		}
	}
}
