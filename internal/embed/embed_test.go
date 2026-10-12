package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOllamaEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ollamaReq
		if r.URL.Path != "/api/embed" || json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, `{"error":"bad"}`, http.StatusBadRequest)
			return
		}
		resp := ollamaResp{}
		for range req.Input {
			resp.Embeddings = append(resp.Embeddings, []float32{1, 0})
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	o := NewOllama(srv.URL, "m")
	o.Batch = 2
	vecs, err := o.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil || len(vecs) != 3 {
		t.Fatalf("vecs = %v, %v", vecs, err)
	}
	if o.Model() != "ollama:m" {
		t.Fatal(o.Model())
	}
}

func TestOllamaMissingModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := NewOllama(srv.URL, "m").Embed(context.Background(), []string{"a"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestONNXStubUnavailable(t *testing.T) {
	if _, err := NewONNX("x.onnx").Embed(context.Background(), []string{"a"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestFakeCosine(t *testing.T) {
	v, _ := (&Fake{}).Embed(context.Background(), []string{"rename userId", "rename userId", "bump lodash"})
	if Cosine(v[0], v[1]) < 0.99 || Cosine(v[0], v[2]) > 0.5 {
		t.Fatalf("cosines: %v %v", Cosine(v[0], v[1]), Cosine(v[0], v[2]))
	}
}

func TestFromSpec(t *testing.T) {
	e, err := FromSpec("ollama:embeddinggemma-2", "http://h:1")
	if err != nil || e.Model() != "ollama:embeddinggemma-2" {
		t.Fatalf("%v %v", e, err)
	}
	if _, err := FromSpec("bogus", ""); err == nil {
		t.Fatal("want error")
	}
}
