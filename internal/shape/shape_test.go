package shape

import (
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestKey(t *testing.T) {
	ren := func(args string) model.ChangeUnit {
		return model.ChangeUnit{ShapeHash: "s", Diff: "-\tgetUser(" + args + ")\n+\tfetchUser(" + args + ")\n"}
	}
	if Key(ren("a")) != Key(ren("b, \"x\"")) {
		t.Error("rename with different arguments should share a key")
	}
	lit := func(a, b string) model.ChangeUnit {
		return model.ChangeUnit{ShapeHash: "s", Diff: "-  \"k\": \"" + a + "\",\n+  \"k\": \"" + b + "\",\n"}
	}
	if Key(lit("old", "new")) == Key(lit("alt", "neu")) {
		t.Error("different literal edits should not share a key")
	}
	other := lit("old", "new")
	other.ShapeHash = "t"
	if Key(lit("old", "new")) == Key(other) {
		t.Error("different shape hashes should not share a key")
	}
	if Key(model.ChangeUnit{Diff: "-a\n+b\n"}) != "" {
		t.Error("no shape hash should give an empty key")
	}
}
