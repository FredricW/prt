// Package proto is the daemon ↔ TUI wire protocol: newline-delimited JSON
// over a Unix socket (.git/prt/prtd.sock). Every line is one Message.
//
// Client → daemon: Kind "req" with ID, Method and Params.
// Daemon → client: Kind "resp" with the same ID and Result or Error, and
// unsolicited Kind "event" messages (patches) after a "subscribe" request.
//
// The protocol is the language-neutral boundary between stages and screens;
// it is inspectable with `nc -U .git/prt/prtd.sock`.
package proto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"sync"

	"github.com/fredricw/prt/internal/model"
)

// Kind is a message kind.
type Kind string

// Message kinds.
const (
	KindReq   Kind = "req"
	KindResp  Kind = "resp"
	KindEvent Kind = "event"
)

// Methods.
const (
	MethodDiscover  = "discover"  // {} → {}; refresh picker sources
	MethodItems     = "items"     // → []model.Item
	MethodCheckout  = "checkout"  // CheckoutParams → CheckoutResult
	MethodDetail    = "detail"    // {itemId} → Detail
	MethodRefresh   = "refresh"   // {itemId, fromStage?, force?} → {}
	MethodAdd       = "add"       // {query} → model.Item
	MethodRemove    = "remove"    // {itemId} → {}
	MethodSubscribe = "subscribe" // {} → {}; then events
	MethodOverride  = "override"  // model.Override → {}
	MethodComment   = "comment"   // model.Comment → model.Comment (draft upsert)
)

// EventType says what changed.
type EventType string

// Event types.
const (
	EventItem   EventType = "item"   // Item changed (state, metadata, rank)
	EventStage  EventType = "stage"  // a RunLog row landed
	EventPatch  EventType = "patch"  // stage output stored; re-read Detail
	EventRemove EventType = "remove" // item removed
	EventLog    EventType = "log"    // human-readable status note
)

// Event is a state patch pushed to subscribers.
type Event struct {
	Type   EventType     `json:"type"`
	ItemID string        `json:"itemId,omitempty"`
	Stage  model.Stage   `json:"stage,omitempty"`
	Item   *model.Item   `json:"item,omitempty"`
	RunLog *model.RunLog `json:"runLog,omitempty"`
	Note   string        `json:"note,omitempty"`
}

// Message is one NDJSON line.
type Message struct {
	Kind   Kind            `json:"kind"`
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Event  *Event          `json:"event,omitempty"`
}

// ItemParams is the params of item-scoped methods.
type ItemParams struct {
	ItemID    string      `json:"itemId"`
	FromStage model.Stage `json:"fromStage,omitempty"`
	Force     bool        `json:"force,omitempty"`
}

// CheckoutParams is the params of MethodCheckout. Without Force, a dirty work
// tree is not touched and CheckoutResult.Dirty is set so the UI can prompt.
type CheckoutParams struct {
	ItemID string `json:"itemId"`
	Force  bool   `json:"force,omitempty"`
}

// CheckoutResult is the result of MethodCheckout.
type CheckoutResult struct {
	Ref        string `json:"ref,omitempty"`        // the ref checked out (or that would be)
	Dirty      bool   `json:"dirty,omitempty"`      // work tree dirty; nothing done
	CheckedOut bool   `json:"checkedOut,omitempty"` // checkout performed
}

// AddParams is the params of MethodAdd.
type AddParams struct {
	Query string `json:"query"`
}

// Detail is everything the item screens render.
type Detail struct {
	Item     model.Item         `json:"item"`
	Diff     string             `json:"diff,omitempty"`
	Files    []model.File       `json:"files,omitempty"`
	Units    []model.ChangeUnit `json:"units,omitempty"`
	Labels   []model.Label      `json:"labels,omitempty"`
	Links    []model.Link       `json:"links,omitempty"`
	Groups   []model.Group      `json:"groups,omitempty"`
	Reviews  []model.Review     `json:"reviews,omitempty"`
	Comments []model.Comment    `json:"comments,omitempty"`
	Agg      *model.ItemAgg     `json:"agg,omitempty"`
	RunLogs  []model.RunLog     `json:"runLogs,omitempty"`
}

// Encoder writes messages, one per line; safe for concurrent use.
type Encoder struct {
	mu sync.Mutex
	w  io.Writer
}

// NewEncoder returns an Encoder writing to w.
func NewEncoder(w io.Writer) *Encoder { return &Encoder{w: w} }

// Encode writes one message followed by '\n'.
func (e *Encoder) Encode(m Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	_, err = e.w.Write(append(b, '\n'))
	return err
}

// Decoder reads messages, one per line. Lines have no size cap: a large
// Detail (diff, files and units) must not tear down the connection.
type Decoder struct {
	r *bufio.Reader
}

// NewDecoder returns a Decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: bufio.NewReaderSize(r, 64*1024)}
}

// Decode reads the next message; io.EOF at end of stream. A final line
// without a trailing newline is still decoded.
func (d *Decoder) Decode() (Message, error) {
	for {
		line, err := d.r.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var m Message
			if uerr := json.Unmarshal(line, &m); uerr != nil {
				return Message{}, uerr
			}
			return m, nil
		}
		if err != nil {
			return Message{}, err
		}
	}
}
