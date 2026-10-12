package questions

import (
	"testing"

	"github.com/fredricw/prt/internal/jev"
)

func TestDefaultValid(t *testing.T) {
	c := Default()
	if len(c.ByScope(ScopeUnit)) == 0 {
		t.Fatal("built-in catalogue has no unit questions")
	}
	for _, q := range c.Questions {
		w := q.Wire()
		if q.Type == Score && w.Type != jev.Score {
			t.Fatalf("%s: wire type %s", q.ID, w.Type)
		}
	}
}

func TestRejectsFeedsNothing(t *testing.T) {
	_, err := Parse([]byte("[[question]]\nscope=\"unit\"\nid=\"x\"\ntype=\"N\"\nprompt=\"?\"\nfeeds=[]\n"), "t")
	if err == nil {
		t.Fatal("want error")
	}
}
