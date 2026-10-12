package proto

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/fredricw/prt/internal/model"
)

func TestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	if err := enc.Encode(Message{Kind: KindReq, ID: 1, Method: MethodItems}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(Message{Kind: KindEvent, Event: &Event{Type: EventStage, ItemID: "pr:1", Stage: model.StageParse}}); err != nil {
		t.Fatal(err)
	}
	if bytes.Count(buf.Bytes(), []byte("\n")) != 2 {
		t.Fatalf("want 2 lines, got %q", buf.String())
	}
	dec := NewDecoder(&buf)
	m, err := dec.Decode()
	if err != nil || m.Method != MethodItems || m.ID != 1 {
		t.Fatalf("%+v %v", m, err)
	}
	m, err = dec.Decode()
	if err != nil || m.Event == nil || m.Event.Stage != model.StageParse {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := dec.Decode(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}
