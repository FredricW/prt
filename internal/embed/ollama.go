package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultOllamaHost is used when OLLAMA_HOST is unset.
const DefaultOllamaHost = "http://127.0.0.1:11434"

// DefaultOllamaModel is the local embedding model.
const DefaultOllamaModel = "embeddinggemma-2"

// Ollama calls a local Ollama server's /api/embed.
type Ollama struct {
	Host  string
	Name  string
	HC    *http.Client
	Batch int // texts per request; default 64
}

var _ Embedder = (*Ollama)(nil)

// NewOllama builds an adapter. Empty host falls back to OLLAMA_HOST, then the default.
func NewOllama(host, model string) *Ollama {
	if host == "" {
		host = os.Getenv("OLLAMA_HOST")
	}
	if host == "" {
		host = DefaultOllamaHost
	}
	if !strings.Contains(host, "://") {
		host = "http://" + host
	}
	if model == "" {
		model = DefaultOllamaModel
	}
	return &Ollama{Host: strings.TrimRight(host, "/"), Name: model, HC: &http.Client{Timeout: 60 * time.Second}, Batch: 64}
}

// Model implements Embedder.
func (o *Ollama) Model() string { return "ollama:" + o.Name }

type ollamaReq struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type ollamaResp struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error,omitempty"`
}

// Embed implements Embedder.
func (o *Ollama) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	batch := o.Batch
	if batch <= 0 {
		batch = 64
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += batch {
		end := min(start+batch, len(texts))
		vecs, err := o.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

func (o *Ollama) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(ollamaReq{Model: o.Name, Input: texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.Host+"/api/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.HC.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: ollama: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: ollama read: %w", ErrUnavailable, err)
	}
	var r ollamaResp
	_ = json.Unmarshal(raw, &r)
	if resp.StatusCode/100 != 2 {
		// 404 "model not found" and 5xx both mean "no embeddings for now".
		return nil, fmt.Errorf("%w: ollama HTTP %d: %s", ErrUnavailable, resp.StatusCode, r.Error)
	}
	if len(r.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embed: ollama returned %d vectors for %d inputs", len(r.Embeddings), len(texts))
	}
	return r.Embeddings, nil
}
