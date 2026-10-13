package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/gh"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// socketPath returns a short socket path (macOS limits them to ~104 bytes,
// which t.TempDir() can exceed).
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "prt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "sub", "prtd.sock")
}

// startServer serves backing on a fresh socket and returns a dialled client
// plus a stop function that cancels Serve and returns its error.
func startServer(t *testing.T, backing Client) (*Socket, string, func() error) {
	t.Helper()
	path := socketPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, backing, path) }()
	s := dialRetry(t, path)
	var once sync.Once
	var serveErr error
	stop := func() error {
		once.Do(func() {
			cancel()
			select {
			case serveErr = <-errc:
			case <-time.After(5 * time.Second):
				serveErr = errors.New("Serve did not return")
			}
		})
		return serveErr
	}
	t.Cleanup(func() { _ = s.Close(); _ = stop() })
	return s, path, stop
}

func dialRetry(t *testing.T, path string) *Socket {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := Dial(context.Background(), path)
		if err == nil {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func socketBacking(t *testing.T) *InProcess {
	t.Helper()
	st := newTestStore(t)
	g := branchGit("feat/x", "feat/dark")
	ghc := &gh.Fake{Login: "me", PRs: map[int]gh.PR{
		4: {Number: 4, Title: "Dark mode", HeadRef: "feat/dark", HeadSHA: "h-feat/dark", BaseRef: "main", ReviewRequested: true},
	}}
	g.Ahead = map[string]int{"main..feat/x": 1, "main..feat/dark": 1}
	return newClient(t, st, g, ghc, nil)
}

func TestSocketRoundTrip(t *testing.T) {
	ctx := context.Background()
	backing := socketBacking(t)
	s, _, _ := startServer(t, backing)

	if err := s.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, s.Events(), func(e proto.Event) bool { return e.Type == proto.EventItem })

	items, err := s.Items(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != model.PRItemID(4) || items[1].ID != model.BranchItemID("feat/x") {
		t.Fatalf("items = %+v", items)
	}
	if items[0].UrgencyQuartile != 4 {
		t.Errorf("quartile lost on the wire: %+v", items[0])
	}

	if err := s.Refresh(ctx, proto.ItemParams{ItemID: model.PRItemID(4), Force: true}); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, s.Events(), func(e proto.Event) bool {
		return e.Type == proto.EventItem && e.ItemID == model.PRItemID(4) && e.Item.State == model.StateReady
	})
	det, err := s.Detail(ctx, model.PRItemID(4))
	if err != nil {
		t.Fatal(err)
	}
	if det.Diff == "" || len(det.Units) != 1 || det.Agg == nil || len(det.RunLogs) == 0 {
		t.Fatalf("detail = %+v", det)
	}

	added, err := s.Add(ctx, "feat/x")
	if err != nil || added.ID != model.BranchItemID("feat/x") {
		t.Fatalf("add = %+v, %v", added, err)
	}

	res, err := s.Checkout(ctx, proto.CheckoutParams{ItemID: model.BranchItemID("feat/x")})
	if err != nil || !res.CheckedOut || res.Ref != "feat/x" {
		t.Fatalf("checkout = %+v, %v", res, err)
	}

	if err := s.Remove(ctx, model.BranchItemID("feat/x")); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, s.Events(), func(e proto.Event) bool {
		return e.Type == proto.EventRemove && e.ItemID == model.BranchItemID("feat/x")
	})
}

// ⇧M / X edits go Socket → Serve → InProcess with their units, item and
// time intact; a bad edit comes back as a RemoteError.
func TestSocketEditGroups(t *testing.T) {
	ctx := context.Background()
	backing := socketBacking(t)
	id := model.BranchItemID("feat/x")
	put(t, backing.d.Store, branchItem("feat/x", 1))
	s, _, _ := startServer(t, backing)

	at := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	edit := model.GroupEdit{Op: model.EditMerge, Units: []string{"u1", "u2"}, At: at}
	if err := s.EditGroups(ctx, proto.GroupEditParams{ItemID: id, Edit: edit}); err != nil {
		t.Fatal(err)
	}
	edits, err := backing.d.Store.GroupEdits(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(edits) != 1 || edits[0].ItemID != id || edits[0].Op != model.EditMerge ||
		strings.Join(edits[0].Units, ",") != "u1,u2" || !edits[0].At.Equal(at) {
		t.Fatalf("stored edits = %+v", edits)
	}

	err = s.EditGroups(ctx, proto.GroupEditParams{ItemID: id, Edit: model.GroupEdit{Op: model.EditMerge}})
	var re *RemoteError
	if !errors.As(err, &re) || re.Method != proto.MethodEditGroups {
		t.Fatalf("bad edit err = %#v", err)
	}
}

func TestSocketErrorResponse(t *testing.T) {
	s, _, _ := startServer(t, socketBacking(t))
	_, err := s.Detail(context.Background(), "pr:404")
	var re *RemoteError
	if !errors.As(err, &re) || re.Method != proto.MethodDetail || re.Msg == "" {
		t.Fatalf("err = %#v", err)
	}
	if err := s.call(context.Background(), "bogus", nil, nil); err == nil {
		t.Fatal("unknown method should fail")
	}
	// The connection survives errors.
	if _, err := s.Items(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSocketConcurrentRequests(t *testing.T) {
	ctx := context.Background()
	backing := socketBacking(t)
	s, _, _ := startServer(t, backing)
	if err := s.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			items, err := s.Items(ctx)
			if err == nil && len(items) != 2 {
				err = errors.New("wrong item count")
			}
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := s.Detail(ctx, "nope")
			if err == nil {
				err = errors.New("want error")
			} else {
				err = nil
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestServeShutdown(t *testing.T) {
	s, path, stop := startServer(t, socketBacking(t))
	if err := stop(); err != nil {
		t.Fatalf("Serve returned %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket not removed: %v", err)
	}
	// The client sees the drop: Events closes and calls fail.
	waitClosed(t, s.Events())
	if _, err := s.Items(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("call after drop = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Logf("close after drop: %v", err)
	}
}

func waitClosed(t *testing.T, ch <-chan proto.Event) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("events not closed")
		}
	}
}

func TestSocketCloseClosesEvents(t *testing.T) {
	s, _, _ := startServer(t, socketBacking(t))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, s.Events())
	if err := s.Close(); err != nil {
		t.Fatal("second Close should be a no-op")
	}
}

func TestDialMissingSocket(t *testing.T) {
	if _, err := Dial(context.Background(), socketPath(t)); err == nil {
		t.Fatal("dial of a missing socket should fail")
	}
}

func TestSocketPathLength(t *testing.T) {
	short := "/r/.git"
	if got := socketPathIn(short, "/tmp"); got != "/r/.git/prt/prtd.sock" {
		t.Errorf("short git dir: %q", got)
	}
	long := "/" + strings.Repeat("deep/", 30) + ".git"
	a, b := socketPathIn(long, "/tmp"), socketPathIn(long+"/", "/tmp")
	if !strings.HasPrefix(a, "/tmp/prt-") || len(a) > maxSocketPath || a != b {
		t.Errorf("long git dir: %q / %q", a, b)
	}
	if socketPathIn(long+"x", "/tmp") == a {
		t.Error("different repos must get different sockets")
	}
}

// A deeply nested repo must still get a working daemon socket.
func TestServeLongGitDir(t *testing.T) {
	root, err := os.MkdirTemp("", "prt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	gitDir := filepath.Join(root, strings.Repeat("nested-directory/", 8), ".git")
	path := SocketPath(gitDir)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(path)) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, socketBacking(t), path) }()
	s := dialRetry(t, path)
	if _, err := s.Items(ctx); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestServeReplacesStaleSocketFile(t *testing.T) {
	path := socketPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, socketBacking(t), path) }()
	s := dialRetry(t, path)
	defer func() { _ = s.Close() }()
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, %v", fi, err)
	}
	// A second daemon on the same path refuses to start.
	if err := Serve(ctx, socketBacking(t), path); !errors.Is(err, ErrDaemonRunning) {
		t.Fatalf("second Serve = %v", err)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}
