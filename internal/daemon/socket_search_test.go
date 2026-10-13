package daemon

import (
	"context"
	"reflect"
	"testing"

	"github.com/fredricw/prt/internal/model"
	"github.com/fredricw/prt/internal/proto"
)

// utcHits normalises the hits' times so a JSON round trip compares equal.
func utcHits(r proto.SearchResult) proto.SearchResult {
	hits := make([]proto.SearchHit, len(r.Hits))
	for i, h := range r.Hits {
		h.UpdatedAt = h.UpdatedAt.UTC()
		hits[i] = h
	}
	r.Hits = hits
	return r
}

// Search, Preview and RemovePreview go Socket → Serve → InProcess and come
// back as the in-process calls answer them (hits with Listed, State, times
// and notes intact).
func TestSocketSearchMatchesInProcess(t *testing.T) {
	ctx := context.Background()
	backing := newClient(t, newTestStore(t), searchGit(), searchGH(), nil)
	captureJobs(backing)
	put(t, backing.d.Store, model.Item{ID: model.PRItemID(483), Kind: model.KindPR, Number: 483, Source: model.SourceRanked, Section: model.SectionInvolved})
	s, _, _ := startServer(t, backing)

	for _, p := range []proto.SearchParams{
		{Query: "feat", Source: proto.SearchLocal},
		{Query: "session", Source: proto.SearchOpen},
		{Query: "session", Source: proto.SearchClosed, Limit: 1},
		{Query: "#999", Source: proto.SearchOpen},
	} {
		want, err := backing.Search(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.Search(ctx, p)
		if err != nil {
			t.Fatalf("%+v: %v", p, err)
		}
		if !reflect.DeepEqual(utcHits(got), utcHits(want)) {
			t.Errorf("%+v over the socket:\n got %+v\nwant %+v", p, got, want)
		}
	}
	res, err := s.Search(ctx, proto.SearchParams{Query: "session", Source: proto.SearchOpen})
	if err != nil || len(res.Hits) != 2 || res.Hits[0].Listed || !res.Hits[1].Listed || res.Hits[0].State != "open" {
		t.Errorf("listed/state lost on the wire: %+v, %v", res, err)
	}

	p, err := s.Preview(ctx, "#482")
	if err != nil || p.ID != model.PRItemID(482) || p.Source != model.SourcePreview || p.Title != "Migrate session to JWT" {
		t.Fatalf("preview = %+v, %v", p, err)
	}
	if err := s.RemovePreview(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := backing.d.Store.Item(ctx, p.ID); err == nil {
		t.Error("RemovePreview over the socket kept the preview")
	}
	// A listed item survives a preview-only removal.
	if err := s.RemovePreview(ctx, model.PRItemID(483)); err != nil {
		t.Fatal(err)
	}
	if _, err := backing.d.Store.Item(ctx, model.PRItemID(483)); err != nil {
		t.Errorf("RemovePreview over the socket removed a listed item: %v", err)
	}
}

// Without GitHub the note travels over the socket.
func TestSocketSearchNote(t *testing.T) {
	backing := newClient(t, newTestStore(t), searchGit(), nil, nil)
	s, _, _ := startServer(t, backing)
	res, err := s.Search(context.Background(), proto.SearchParams{Query: "x", Source: proto.SearchOpen})
	if err != nil || res.Note != "GitHub unavailable" || len(res.Hits) != 0 {
		t.Errorf("search = %+v, %v", res, err)
	}
}
