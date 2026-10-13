package embed

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// OLLAMA_HOST is read the way Ollama's client reads it: a bare host gets
// the default port 11434 (OLLAMA_HOST=0.0.0.0 is common for the server).
func TestOllamaBaseURL(t *testing.T) {
	tests := map[string]string{
		"":                       DefaultOllamaHost,
		"0.0.0.0":                "http://0.0.0.0:11434",
		":11434":                 "http://127.0.0.1:11434",
		"localhost":              "http://localhost:11434",
		"example.com:5000":       "http://example.com:5000",
		"http://h:1":             "http://h:1",
		"http://h":               "http://h:80",
		"https://h":              "https://h:443",
		"https://h:8443/ollama/": "https://h:8443/ollama",
		"[::1]":                  "http://[::1]:11434",
		"[::1]:9":                "http://[::1]:9",
		" 10.0.0.2 ":             "http://10.0.0.2:11434",
	}
	for in, want := range tests {
		if got := OllamaBaseURL(in); got != want {
			t.Errorf("OllamaBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
	t.Setenv("OLLAMA_HOST", "0.0.0.0")
	if got := NewOllama("", "").Host; got != "http://0.0.0.0:11434" {
		t.Errorf("NewOllama with OLLAMA_HOST=0.0.0.0: host %q", got)
	}
}

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
	// The ONNX stub is never ready: the spec's ONNX → Ollama fallback, with
	// the Ollama model id as the cache key.
	e, err = FromSpec("onnx:models/code.onnx", "http://h:1")
	if err != nil || e.Model() != "ollama:"+DefaultOllamaModel {
		t.Fatalf("onnx spec = %v (%T), %v; want the Ollama fallback", e, e, err)
	}
	if _, ok := e.(*Ollama); !ok {
		t.Fatalf("onnx spec built %T", e)
	}
}
