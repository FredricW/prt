package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// ErrClosed is returned by Socket calls after the connection closed.
var ErrClosed = errors.New("daemon: connection closed")

// RemoteError is an error reported by the daemon for one request.
type RemoteError struct {
	Method string
	Msg    string
}

// Error implements error.
func (e *RemoteError) Error() string { return "daemon " + e.Method + ": " + e.Msg }

// eventBuffer is the client-side event buffer; overflow drops events (the UI
// re-reads state on the next one).
const eventBuffer = 256

// Socket is a Client talking to `prt daemon` over its Unix socket. It is
// safe for concurrent use: requests are correlated with responses by ID.
type Socket struct {
	conn net.Conn
	enc  *proto.Encoder

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan proto.Message
	err     error // set once the read loop ends

	events   chan proto.Event
	readDone chan struct{}
	once     sync.Once
}

var _ Client = (*Socket)(nil)

// Dial connects to a running daemon and subscribes to its events, so Events
// works immediately. It fails if nothing listens on path.
func Dial(ctx context.Context, path string) (*Socket, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("daemon: dial %s: %w", path, err)
	}
	s := &Socket{
		conn: conn, enc: proto.NewEncoder(conn),
		pending: map[int64]chan proto.Message{},
		events:  make(chan proto.Event, eventBuffer), readDone: make(chan struct{}),
	}
	go s.readLoop()
	if err := s.call(ctx, proto.MethodSubscribe, nil, nil); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// readLoop routes responses to waiting calls and events to Events. When the
// connection drops it fails pending calls and closes Events.
func (s *Socket) readLoop() {
	defer close(s.readDone)
	defer close(s.events)
	dec := proto.NewDecoder(s.conn)
	for {
		m, err := dec.Decode()
		if err != nil {
			s.fail(err)
			return
		}
		switch m.Kind {
		case proto.KindResp:
			s.deliver(m)
		case proto.KindEvent:
			if m.Event != nil {
				select {
				case s.events <- *m.Event:
				default:
				}
			}
		}
	}
}

func (s *Socket) deliver(m proto.Message) {
	s.mu.Lock()
	ch, ok := s.pending[m.ID]
	delete(s.pending, m.ID)
	s.mu.Unlock()
	if ok {
		ch <- m // buffered 1
	}
}

func (s *Socket) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = fmt.Errorf("%w: %w", ErrClosed, err)
	for id, ch := range s.pending {
		close(ch)
		delete(s.pending, id)
	}
}

// call sends one request and waits for its response, decoding the result
// into out (when non-nil).
func (s *Socket) call(ctx context.Context, method string, params, out any) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("daemon: %s params: %w", method, err)
		}
		raw = b
	}
	s.mu.Lock()
	if s.err != nil {
		err := s.err
		s.mu.Unlock()
		return err
	}
	s.nextID++
	id := s.nextID
	ch := make(chan proto.Message, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	if err := s.enc.Encode(proto.Message{Kind: proto.KindReq, ID: id, Method: method, Params: raw}); err != nil {
		s.forget(id)
		return fmt.Errorf("daemon: send %s: %w", method, err)
	}
	select {
	case <-ctx.Done():
		s.forget(id)
		return ctx.Err()
	case m, ok := <-ch:
		if !ok {
			return s.closedErr()
		}
		return decodeResp(method, m, out)
	}
}

func decodeResp(method string, m proto.Message, out any) error {
	if m.Error != "" {
		return &RemoteError{Method: method, Msg: m.Error}
	}
	if out == nil || len(m.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(m.Result, out); err != nil {
		return fmt.Errorf("daemon: %s result: %w", method, err)
	}
	return nil
}

func (s *Socket) forget(id int64) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

func (s *Socket) closedErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	return ErrClosed
}

// Discover implements Client.
func (s *Socket) Discover(ctx context.Context) error {
	return s.call(ctx, proto.MethodDiscover, nil, nil)
}

// Items implements Client.
func (s *Socket) Items(ctx context.Context) ([]model.Item, error) {
	var items []model.Item
	err := s.call(ctx, proto.MethodItems, nil, &items)
	return items, err
}

// Detail implements Client.
func (s *Socket) Detail(ctx context.Context, itemID string) (proto.Detail, error) {
	var d proto.Detail
	err := s.call(ctx, proto.MethodDetail, proto.ItemParams{ItemID: itemID}, &d)
	return d, err
}

// Refresh implements Client.
func (s *Socket) Refresh(ctx context.Context, p proto.ItemParams) error {
	return s.call(ctx, proto.MethodRefresh, p, nil)
}

// Add implements Client.
func (s *Socket) Add(ctx context.Context, query string) (model.Item, error) {
	var it model.Item
	err := s.call(ctx, proto.MethodAdd, proto.AddParams{Query: query}, &it)
	return it, err
}

// Remove implements Client.
func (s *Socket) Remove(ctx context.Context, itemID string) error {
	return s.call(ctx, proto.MethodRemove, proto.ItemParams{ItemID: itemID}, nil)
}

// Checkout implements Client.
func (s *Socket) Checkout(ctx context.Context, p proto.CheckoutParams) (proto.CheckoutResult, error) {
	var r proto.CheckoutResult
	err := s.call(ctx, proto.MethodCheckout, p, &r)
	return r, err
}

// EditGroups implements Client.
func (s *Socket) EditGroups(ctx context.Context, p proto.GroupEditParams) error {
	return s.call(ctx, proto.MethodEditGroups, p, nil)
}

// ReviewAction implements Client.
func (s *Socket) ReviewAction(ctx context.Context, p proto.ReviewActionParams) error {
	return s.call(ctx, proto.MethodReviewAction, p, nil)
}

// Routing implements Client.
func (s *Socket) Routing(ctx context.Context, p proto.RoutingParams) (proto.RoutingInfo, error) {
	var r proto.RoutingInfo
	err := s.call(ctx, proto.MethodRouting, p, &r)
	return r, err
}

// CallLog implements Client.
func (s *Socket) CallLog(ctx context.Context, p proto.CallLogParams) ([]model.ReviewCall, error) {
	var calls []model.ReviewCall
	err := s.call(ctx, proto.MethodCallLog, p, &calls)
	return calls, err
}

// SetCap implements Client.
func (s *Socket) SetCap(ctx context.Context, p proto.CapParams) error {
	return s.call(ctx, proto.MethodSetCap, p, nil)
}

// SaveComment implements Client.
func (s *Socket) SaveComment(ctx context.Context, p proto.CommentParams) (model.Comment, error) {
	var c model.Comment
	err := s.call(ctx, proto.MethodComment, p, &c)
	return c, err
}

// CommentAction implements Client.
func (s *Socket) CommentAction(ctx context.Context, p proto.CommentActionParams) (model.Comment, error) {
	var c model.Comment
	err := s.call(ctx, proto.MethodCommentAction, p, &c)
	return c, err
}

// SubmitReview implements Client.
func (s *Socket) SubmitReview(ctx context.Context, p proto.SubmitParams) (proto.SubmitResult, error) {
	var r proto.SubmitResult
	err := s.call(ctx, proto.MethodSubmitReview, p, &r)
	return r, err
}

// CreatePR implements Client.
func (s *Socket) CreatePR(ctx context.Context, p proto.CreatePRParams) (proto.CreatePRResult, error) {
	var r proto.CreatePRResult
	err := s.call(ctx, proto.MethodCreatePR, p, &r)
	return r, err
}

// MarkViewed implements Client.
func (s *Socket) MarkViewed(ctx context.Context, p proto.ViewedParams) error {
	return s.call(ctx, proto.MethodViewed, p, nil)
}

// Override implements Client.
func (s *Socket) Override(ctx context.Context, p proto.OverrideParams) error {
	return s.call(ctx, proto.MethodOverride, p, nil)
}

// Events implements Client. The channel closes when the connection drops or
// Close is called.
func (s *Socket) Events() <-chan proto.Event { return s.events }

// Close implements Client: closes the connection and waits until Events is
// closed. Safe to call more than once.
func (s *Socket) Close() error {
	var err error
	s.once.Do(func() {
		err = s.conn.Close()
		<-s.readDone
	})
	return err
}
