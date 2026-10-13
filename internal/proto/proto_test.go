package proto

import (
	"bytes"
	"errors"
	"io"
	"strings"
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

// A message above the old 64 MiB line cap must decode, not kill the stream.
func TestDecodeHugeMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates ~250 MB")
	}
	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	big := strings.Repeat("a", 65<<20)
	if err := enc.Encode(Message{Kind: KindEvent, Event: &Event{Type: EventLog, Note: big}}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(Message{Kind: KindReq, ID: 2}); err != nil {
		t.Fatal(err)
	}
	dec := NewDecoder(&buf)
	m, err := dec.Decode()
	if err != nil || m.Event == nil || len(m.Event.Note) != len(big) {
		t.Fatalf("huge message: err %v", err)
	}
	if m, err := dec.Decode(); err != nil || m.ID != 2 {
		t.Fatalf("next message: %+v %v", m, err)
	}
}

func TestDecodeLastLineWithoutNewline(t *testing.T) {
	dec := NewDecoder(strings.NewReader("\n{\"kind\":\"req\",\"id\":3}"))
	if m, err := dec.Decode(); err != nil || m.ID != 3 {
		t.Fatalf("%+v %v", m, err)
	}
	if _, err := dec.Decode(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}
