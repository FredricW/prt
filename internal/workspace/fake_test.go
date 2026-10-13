package workspace

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fredricw/prt/internal/daemon"
	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// call is one recorded fake call.
type call struct {
	method string
	arg    any
}

// fakeMember is a member client with canned results that records every
// call. Besides daemon.Client it has every optional capability (RunSync,
// AnalysePending, ReloadSettings, RepoInfo); wrap it in plainMember to
// hide them.
type fakeMember struct {
	info proto.RepoInfo

	mu    sync.Mutex
	calls []call

	items      []model.Item
	itemsErr   error
	hits       map[proto.SearchSource][]proto.SearchHit
	notes      map[proto.SearchSource]string
	searchErr  error
	discover   func(context.Context) error
	stopped    []string
	resumed    []string
	stopErr    error
	applied    string
	warnings   []string
	saveErr    error
	reloadErr  error
	pendingErr error
	closeErr   error
	live       proto.RepoInfo

	events     chan proto.Event
	keepEvents bool // Close leaves the events channel open
	closeOnce  sync.Once
	closes     atomic.Int32
}

var _ daemon.Client = (*fakeMember)(nil)

func newFake(info proto.RepoInfo) *fakeMember {
	return &fakeMember{info: info, events: make(chan proto.Event, 16)}
}

// plainMember exposes only daemon.Client of its fake.
type plainMember struct{ daemon.Client }

func (f *fakeMember) member() Member      { return Member{Info: f.info, Client: f} }
func (f *fakeMember) plainMember() Member { return Member{Info: f.info, Client: plainMember{f}} }

func (f *fakeMember) record(method string, arg any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{method, arg})
}

// argsOf returns the recorded arguments of method, in call order.
func (f *fakeMember) argsOf(method string) []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []any
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c.arg)
		}
	}
	return out
}

// last returns the last argument of method, failing when it was not called.
func (f *fakeMember) last(t *testing.T, method string) any {
	t.Helper()
	args := f.argsOf(method)
	if len(args) == 0 {
		t.Fatalf("%s: %s not called", f.info.Key, method)
	}
	return args[len(args)-1]
}

// itemIDFor is the local id Add gives a query.
func itemIDFor(q string) string {
	if n, err := strconv.Atoi(strings.TrimPrefix(q, "#")); err == nil {
		return model.PRItemID(n)
	}
	return model.BranchItemID(q)
}

func (f *fakeMember) Discover(ctx context.Context) error {
	f.record("Discover", nil)
	if f.discover != nil {
		return f.discover(ctx)
	}
	return nil
}

func (f *fakeMember) Items(context.Context) ([]model.Item, error) {
	f.record("Items", nil)
	return f.items, f.itemsErr
}

func (f *fakeMember) Detail(_ context.Context, id string) (proto.Detail, error) {
	f.record("Detail", id)
	return filled[proto.Detail](id), nil
}

func (f *fakeMember) DetailOf(_ context.Context, p proto.DetailParams) (proto.Detail, error) {
	f.record("DetailOf", p)
	return filled[proto.Detail](p.ItemID), nil
}

func (f *fakeMember) Labels(_ context.Context, p proto.LabelsParams) ([]model.Label, error) {
	f.record("Labels", p)
	return []model.Label{{UnitID: "u1", Question: "risk", Value: "2"}}, nil
}

func (f *fakeMember) Refresh(_ context.Context, p proto.ItemParams) error {
	f.record("Refresh", p)
	return nil
}

func (f *fakeMember) Add(_ context.Context, q string) (model.Item, error) {
	f.record("Add", q)
	return model.Item{ID: itemIDFor(q), Title: q}, nil
}

func (f *fakeMember) Remove(_ context.Context, id string) error {
	f.record("Remove", id)
	return nil
}

func (f *fakeMember) Checkout(_ context.Context, p proto.CheckoutParams) (proto.CheckoutResult, error) {
	f.record("Checkout", p)
	return proto.CheckoutResult{Ref: "feat/x", CheckedOut: true}, nil
}

func (f *fakeMember) EditGroups(_ context.Context, p proto.GroupEditParams) error {
	f.record("EditGroups", p)
	return nil
}

func (f *fakeMember) ReviewAction(_ context.Context, p proto.ReviewActionParams) error {
	f.record("ReviewAction", p)
	return nil
}

func (f *fakeMember) Routing(_ context.Context, p proto.RoutingParams) (proto.RoutingInfo, error) {
	f.record("Routing", p)
	return proto.RoutingInfo{Path: f.info.Key + "/prt.routing.toml"}, nil
}

func (f *fakeMember) CallLog(_ context.Context, p proto.CallLogParams) ([]model.ReviewCall, error) {
	f.record("CallLog", p)
	return filled[[]model.ReviewCall](p.ItemID), nil
}

func (f *fakeMember) SetCap(_ context.Context, p proto.CapParams) error {
	f.record("SetCap", p)
	return nil
}

func (f *fakeMember) SaveComment(_ context.Context, p proto.CommentParams) (model.Comment, error) {
	f.record("SaveComment", p)
	return filled[model.Comment](p.ItemID), nil
}

func (f *fakeMember) CommentAction(_ context.Context, p proto.CommentActionParams) (model.Comment, error) {
	f.record("CommentAction", p)
	return filled[model.Comment](p.ItemID), nil
}

func (f *fakeMember) SubmitReview(_ context.Context, p proto.SubmitParams) (proto.SubmitResult, error) {
	f.record("SubmitReview", p)
	return filled[proto.SubmitResult](p.ItemID), nil
}

func (f *fakeMember) CreatePR(_ context.Context, p proto.CreatePRParams) (proto.CreatePRResult, error) {
	f.record("CreatePR", p)
	return filled[proto.CreatePRResult](p.ItemID), nil
}

func (f *fakeMember) MarkViewed(_ context.Context, p proto.ViewedParams) error {
	f.record("MarkViewed", p)
	return nil
}

func (f *fakeMember) Override(_ context.Context, p proto.OverrideParams) error {
	f.record("Override", p)
	return nil
}

func (f *fakeMember) Search(_ context.Context, p proto.SearchParams) (proto.SearchResult, error) {
	f.record("Search", p)
	if f.searchErr != nil {
		return proto.SearchResult{}, f.searchErr
	}
	return proto.SearchResult{Source: p.Source, Query: p.Query, Hits: f.hits[p.Source], Note: f.notes[p.Source]}, nil
}

func (f *fakeMember) Preview(_ context.Context, q string) (model.Item, error) {
	f.record("Preview", q)
	return model.Item{ID: itemIDFor(q), Title: q, Source: model.SourcePreview}, nil
}

func (f *fakeMember) RemovePreview(_ context.Context, id string) error {
	f.record("RemovePreview", id)
	return nil
}

func (f *fakeMember) Stop(_ context.Context, p proto.StopParams) (proto.StopResult, error) {
	f.record("Stop", p)
	if !p.All {
		return proto.StopResult{Stopped: []string{p.ItemID}}, f.stopErr
	}
	return proto.StopResult{Stopped: f.stopped}, f.stopErr
}

func (f *fakeMember) Resume(_ context.Context, p proto.ResumeParams) (proto.ResumeResult, error) {
	f.record("Resume", p)
	if !p.All {
		return proto.ResumeResult{Resumed: []string{p.ItemID}}, nil
	}
	return proto.ResumeResult{Resumed: f.resumed}, nil
}

func (f *fakeMember) Settings(_ context.Context, p proto.SettingsParams) (proto.SettingsInfo, error) {
	f.record("Settings", p)
	return proto.SettingsInfo{Source: f.info.Key + "/prt.toml", Warnings: f.warnings}, nil
}

func (f *fakeMember) SaveSettings(_ context.Context, p proto.SaveSettingsParams) (proto.SettingsInfo, error) {
	f.record("SaveSettings", p)
	return proto.SettingsInfo{Source: f.info.Key + "/prt.toml", Applied: f.applied, Warnings: f.warnings}, f.saveErr
}

func (f *fakeMember) Workspace(context.Context) (proto.WorkspaceInfo, error) {
	f.record("Workspace", nil)
	return proto.WorkspaceInfo{Repos: []proto.RepoInfo{f.info}}, nil
}

func (f *fakeMember) Events() <-chan proto.Event { return f.events }

func (f *fakeMember) Close() error {
	f.closes.Add(1)
	if !f.keepEvents {
		f.closeEvents()
	}
	return f.closeErr
}

// closeEvents ends the fake's event stream (once).
func (f *fakeMember) closeEvents() {
	f.closeOnce.Do(func() { close(f.events) })
}

func (f *fakeMember) RunSync(_ context.Context, id string) error {
	f.record("RunSync", id)
	return nil
}

func (f *fakeMember) AnalysePending(context.Context) error {
	f.record("AnalysePending", nil)
	return f.pendingErr
}

func (f *fakeMember) ReloadSettings(context.Context) (proto.SettingsInfo, error) {
	f.record("ReloadSettings", nil)
	return proto.SettingsInfo{Applied: f.applied, Warnings: f.warnings}, f.reloadErr
}

func (f *fakeMember) RepoInfo() proto.RepoInfo { return f.live }

// newTestClient builds a workspace client over fakes (closed at cleanup).
func newTestClient(t *testing.T, fakes ...*fakeMember) *Client {
	t.Helper()
	ms := make([]Member, 0, len(fakes))
	for _, f := range fakes {
		ms = append(ms, f.member())
	}
	return newClientWith(t, ClientOptions{}, ms...)
}

func newClientWith(t *testing.T, o ClientOptions, ms ...Member) *Client {
	t.Helper()
	c := New("/ws", ms, o)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// Reflection helpers: fill every item id field of a value and collect them
// back, so the id mapping is checked against every field the types have
// now and will have later.

var (
	itemType = reflect.TypeOf(model.Item{})
	timeType = reflect.TypeOf(time.Time{})
)

// filled returns a T whose item id fields (every string field named
// ItemID, and model.Item.ID) are id, with every struct slice holding two
// elements and every struct pointer allocated.
func filled[T any](id string) T {
	var v T
	fillIDs(reflect.ValueOf(&v).Elem(), id, 0)
	return v
}

func isIDField(t reflect.Type, sf reflect.StructField) bool {
	return sf.Type.Kind() == reflect.String && (sf.Name == "ItemID" || (t == itemType && sf.Name == "ID"))
}

func fillIDs(v reflect.Value, id string, depth int) {
	if depth > 10 {
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.Type().Elem().Kind() != reflect.Struct {
			return
		}
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillIDs(v.Elem(), id, depth+1)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.Struct {
			return
		}
		s := reflect.MakeSlice(v.Type(), 2, 2)
		for i := range 2 {
			fillIDs(s.Index(i), id, depth+1)
		}
		v.Set(s)
	case reflect.Struct:
		if v.Type() == timeType {
			return
		}
		for i := range v.NumField() {
			sf := v.Type().Field(i)
			if !sf.IsExported() {
				continue
			}
			if isIDField(v.Type(), sf) {
				v.Field(i).SetString(id)
				continue
			}
			fillIDs(v.Field(i), id, depth+1)
		}
	}
}

// idField is one collected item id (or, for a model.Item, its Repo).
type idField struct {
	path  string
	value string
	repo  bool // the field is model.Item.Repo
}

// collectIDs returns every item id field under v.
func collectIDs(v any) []idField {
	var out []idField
	collect(reflect.ValueOf(v), "", &out, 0)
	return out
}

func collect(v reflect.Value, path string, out *[]idField, depth int) {
	if depth > 10 || !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			collect(v.Elem(), path, out, depth+1)
		}
	case reflect.Slice:
		for i := range v.Len() {
			collect(v.Index(i), path+"["+strconv.Itoa(i)+"]", out, depth+1)
		}
	case reflect.Struct:
		if v.Type() == timeType {
			return
		}
		for i := range v.NumField() {
			sf := v.Type().Field(i)
			if !sf.IsExported() {
				continue
			}
			p := path + "." + sf.Name
			switch {
			case isIDField(v.Type(), sf):
				*out = append(*out, idField{path: p, value: v.Field(i).String()})
			case v.Type() == itemType && sf.Name == "Repo":
				*out = append(*out, idField{path: p, value: v.Field(i).String(), repo: true})
			default:
				collect(v.Field(i), p, out, depth+1)
			}
		}
	}
}

// checkIDs fails unless v has at least one item id and every one is id
// (and every Item.Repo is repo).
func checkIDs(t *testing.T, name string, v any, id, repo string) {
	t.Helper()
	fields := collectIDs(v)
	n := 0
	for _, f := range fields {
		want := id
		if f.repo {
			want = repo
		} else {
			n++
		}
		if f.value != want {
			t.Errorf("%s%s = %q, want %q", name, f.path, f.value, want)
		}
	}
	if n == 0 {
		t.Errorf("%s: no item id fields found", name)
	}
}
