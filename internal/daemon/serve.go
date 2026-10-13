package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/fredricw/prt/internal/proto"
)

// ErrDaemonRunning is returned by Serve when another daemon answers on the socket.
var ErrDaemonRunning = errors.New("daemon: another daemon is already listening")

// subscriberBuffer is how many events a slow subscriber may lag before
// events are dropped for it (the UI re-reads state on the next event).
const subscriberBuffer = 256

// Serve runs the daemon on the Unix socket at path until ctx is done, serving
// every Client method of c to any number of connections. Serve owns c's
// event stream and fans it out to subscribed connections. It returns nil
// after ctx is cancelled, having closed all connections and removed the
// socket file. It does not close c.
func Serve(ctx context.Context, c Client, path string) error {
	ln, err := listen(ctx, path)
	if err != nil {
		return err
	}
	srv := &server{client: c, conns: map[*serverConn]struct{}{}}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var bg sync.WaitGroup
	bg.Add(2)
	go func() { defer bg.Done(); srv.broadcast(ctx) }()
	go func() { defer bg.Done(); <-ctx.Done(); _ = ln.Close() }()

	acceptErr := srv.accept(ctx, ln)
	cancel()
	srv.closeAll()
	srv.wg.Wait()
	bg.Wait()
	if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && acceptErr == nil {
		acceptErr = fmt.Errorf("daemon: remove socket: %w", rerr)
	}
	return acceptErr
}

// listen prepares the socket directory, clears a stale socket file and
// listens with owner-only permissions.
func listen(ctx context.Context, path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: socket dir: %w", err)
	}
	// The fallback directory lives in a temp dir that other users may share:
	// refuse one somebody else created.
	if fi, err := os.Lstat(filepath.Dir(path)); err != nil || !fi.IsDir() || !ownedByMe(fi) {
		return nil, fmt.Errorf("daemon: socket dir %s is not a directory owned by this user", filepath.Dir(path))
	}
	if _, err := os.Stat(path); err == nil {
		if socketAlive(ctx, path) {
			return nil, fmt.Errorf("%w on %s", ErrDaemonRunning, path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("daemon: remove stale socket: %w", err)
		}
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "unix", path)
	if err != nil {
		return nil, fmt.Errorf("daemon: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("daemon: chmod socket: %w", err)
	}
	return ln, nil
}

// ownedByMe reports whether fi belongs to the current user (true where the
// platform does not report owners).
func ownedByMe(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return !ok || int(st.Uid) == os.Getuid()
}

// socketAlive reports whether something answers on the socket.
func socketAlive(ctx context.Context, path string) bool {
	d := net.Dialer{Timeout: 200 * time.Millisecond}
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// server is the state shared by all connections of one Serve call.
type server struct {
	client Client
	wg     sync.WaitGroup // connection handlers

	mu    sync.Mutex
	conns map[*serverConn]struct{}
}

// serverConn is one client connection.
type serverConn struct {
	conn net.Conn
	enc  *proto.Encoder

	writer sync.WaitGroup // event writer

	mu         sync.Mutex
	subscribed bool
	events     chan proto.Event // nil until subscribed
	closed     bool
}

func (s *server) accept(ctx context.Context, ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("daemon: accept: %w", err)
		}
		sc := &serverConn{conn: conn, enc: proto.NewEncoder(conn)}
		s.mu.Lock()
		s.conns[sc] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(ctx, sc)
		}()
	}
}

func (s *server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sc := range s.conns {
		_ = sc.conn.Close()
	}
}

// broadcast reads the backing client's events and fans them out without
// blocking: a subscriber whose buffer is full misses the event.
func (s *server) broadcast(ctx context.Context) {
	events := s.client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			s.fanOut(e)
		}
	}
}

func (s *server) fanOut(e proto.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sc := range s.conns {
		sc.deliver(e)
	}
}

func (sc *serverConn) deliver(e proto.Event) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if !sc.subscribed || sc.closed {
		return
	}
	select {
	case sc.events <- e:
	default:
	}
}

// subscribe starts the connection's event writer (idempotent). The writer
// exits when shutdown closes the events channel.
func (sc *serverConn) subscribe() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.subscribed || sc.closed {
		return
	}
	sc.subscribed = true
	sc.events = make(chan proto.Event, subscriberBuffer)
	sc.writer.Add(1)
	go sc.writeEvents(sc.events)
}

func (sc *serverConn) writeEvents(ch <-chan proto.Event) {
	defer sc.writer.Done()
	failed := false
	for e := range ch {
		if failed {
			continue // keep draining until shutdown closes ch
		}
		ev := e
		if err := sc.enc.Encode(proto.Message{Kind: proto.KindEvent, Event: &ev}); err != nil {
			failed = true
			_ = sc.conn.Close()
		}
	}
}

func (sc *serverConn) shutdown() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.closed {
		return
	}
	sc.closed = true
	if sc.events != nil {
		close(sc.events)
	}
	_ = sc.conn.Close()
}

// handle reads requests until the connection drops; each request runs in its
// own goroutine so a slow call never blocks the others.
func (s *server) handle(ctx context.Context, sc *serverConn) {
	var reqs sync.WaitGroup
	defer func() {
		reqs.Wait()
		sc.shutdown()
		sc.writer.Wait()
		s.mu.Lock()
		delete(s.conns, sc)
		s.mu.Unlock()
	}()
	dec := proto.NewDecoder(sc.conn)
	for {
		m, err := dec.Decode()
		if err != nil {
			return
		}
		if m.Kind != proto.KindReq {
			continue
		}
		reqs.Add(1)
		go func() {
			defer reqs.Done()
			resp := s.dispatch(ctx, sc, m)
			_ = sc.enc.Encode(resp)
		}()
	}
}

// dispatch runs one request and builds its response.
func (s *server) dispatch(ctx context.Context, sc *serverConn, m proto.Message) proto.Message {
	resp := proto.Message{Kind: proto.KindResp, ID: m.ID}
	result, err := s.call(ctx, sc, m)
	if err == nil && result != nil {
		resp.Result, err = json.Marshal(result)
	}
	if err != nil {
		resp.Error = err.Error()
		resp.Result = nil
	}
	return resp
}

// empty is the result of methods with no payload.
type empty struct{}

func (s *server) call(ctx context.Context, sc *serverConn, m proto.Message) (any, error) {
	c := s.client
	switch m.Method {
	case proto.MethodDiscover:
		return empty{}, c.Discover(ctx)
	case proto.MethodItems:
		return c.Items(ctx)
	case proto.MethodDetail:
		p, err := params[proto.DetailParams](m)
		if err != nil {
			return nil, err
		}
		return c.DetailOf(ctx, p)
	case proto.MethodLabels:
		p, err := params[proto.LabelsParams](m)
		if err != nil {
			return nil, err
		}
		return c.Labels(ctx, p)
	case proto.MethodRefresh:
		p, err := params[proto.ItemParams](m)
		if err != nil {
			return nil, err
		}
		return empty{}, c.Refresh(ctx, p)
	case proto.MethodAdd:
		p, err := params[proto.AddParams](m)
		if err != nil {
			return nil, err
		}
		return c.Add(ctx, p.Query)
	case proto.MethodRemove:
		p, err := params[proto.ItemParams](m)
		if err != nil {
			return nil, err
		}
		return empty{}, c.Remove(ctx, p.ItemID)
	case proto.MethodCheckout:
		p, err := params[proto.CheckoutParams](m)
		if err != nil {
			return nil, err
		}
		return c.Checkout(ctx, p)
	case proto.MethodEditGroups:
		p, err := params[proto.GroupEditParams](m)
		if err != nil {
			return nil, err
		}
		return empty{}, c.EditGroups(ctx, p)
	case proto.MethodReviewAction:
		p, err := params[proto.ReviewActionParams](m)
		if err != nil {
			return nil, err
		}
		return empty{}, c.ReviewAction(ctx, p)
	case proto.MethodRouting:
		p, err := params[proto.RoutingParams](m)
		if err != nil {
			return nil, err
		}
		return c.Routing(ctx, p)
	case proto.MethodCallLog:
		p, err := params[proto.CallLogParams](m)
		if err != nil {
			return nil, err
		}
		return c.CallLog(ctx, p)
	case proto.MethodSetCap:
		p, err := params[proto.CapParams](m)
		if err != nil {
			return nil, err
		}
		return empty{}, c.SetCap(ctx, p)
	case proto.MethodComment:
		p, err := params[proto.CommentParams](m)
		if err != nil {
			return nil, err
		}
		return c.SaveComment(ctx, p)
	case proto.MethodCommentAction:
		p, err := params[proto.CommentActionParams](m)
		if err != nil {
			return nil, err
		}
		return c.CommentAction(ctx, p)
	case proto.MethodSubmitReview:
		p, err := params[proto.SubmitParams](m)
		if err != nil {
			return nil, err
		}
		return c.SubmitReview(ctx, p)
	case proto.MethodCreatePR:
		p, err := params[proto.CreatePRParams](m)
		if err != nil {
			return nil, err
		}
		return c.CreatePR(ctx, p)
	case proto.MethodViewed:
		p, err := params[proto.ViewedParams](m)
		if err != nil {
			return nil, err
		}
		return empty{}, c.MarkViewed(ctx, p)
	case proto.MethodOverride:
		p, err := params[proto.OverrideParams](m)
		if err != nil {
			return nil, err
		}
		return empty{}, c.Override(ctx, p)
	case proto.MethodSearch:
		p, err := params[proto.SearchParams](m)
		if err != nil {
			return nil, err
		}
		return c.Search(ctx, p)
	case proto.MethodPreview:
		p, err := params[proto.AddParams](m)
		if err != nil {
			return nil, err
		}
		return c.Preview(ctx, p.Query)
	case proto.MethodRemovePreview:
		p, err := params[proto.ItemParams](m)
		if err != nil {
			return nil, err
		}
		return empty{}, c.RemovePreview(ctx, p.ItemID)
	case proto.MethodStop:
		p, err := params[proto.StopParams](m)
		if err != nil {
			return nil, err
		}
		return c.Stop(ctx, p)
	case proto.MethodResume:
		p, err := params[proto.ResumeParams](m)
		if err != nil {
			return nil, err
		}
		return c.Resume(ctx, p)
	case proto.MethodSettings:
		p, err := params[proto.SettingsParams](m)
		if err != nil {
			return nil, err
		}
		return c.Settings(ctx, p)
	case proto.MethodSaveSettings:
		p, err := params[proto.SaveSettingsParams](m)
		if err != nil {
			return nil, err
		}
		return c.SaveSettings(ctx, p)
	case proto.MethodSubscribe:
		sc.subscribe()
		return empty{}, nil
	default:
		return nil, fmt.Errorf("daemon: unknown method %q", m.Method)
	}
}

// params decodes a request's params (absent params decode as the zero value).
func params[T any](m proto.Message) (T, error) {
	var v T
	if len(m.Params) == 0 {
		return v, nil
	}
	if err := json.Unmarshal(m.Params, &v); err != nil {
		return v, fmt.Errorf("daemon: %s params: %w", m.Method, err)
	}
	return v, nil
}
