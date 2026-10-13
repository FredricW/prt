package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
	if model == "" {
		model = DefaultOllamaModel
	}
	return &Ollama{Host: OllamaBaseURL(host), Name: model, HC: &http.Client{Timeout: 60 * time.Second}, Batch: 64}
}

// OllamaBaseURL reads an OLLAMA_HOST value the way Ollama's own client does
// (envconfig.Host): scheme defaults to http, host to 127.0.0.1, and the
// port to 11434 when no scheme is given (80/443 for an explicit
// http/https scheme). "0.0.0.0" → http://0.0.0.0:11434, ":11434" →
// http://127.0.0.1:11434, "" → DefaultOllamaHost. A path is kept; the
// result has no trailing slash.
func OllamaBaseURL(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return DefaultOllamaHost
	}
	defaultPort := "11434"
	scheme, hostport, ok := strings.Cut(s, "://")
	switch {
	case !ok:
		scheme, hostport = "http", s
	case scheme == "http":
		defaultPort = "80"
	case scheme == "https":
		defaultPort = "443"
	}
	hostport, path, _ := strings.Cut(hostport, "/")
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = "127.0.0.1", defaultPort
		if ip := net.ParseIP(strings.Trim(hostport, "[]")); ip != nil {
			host = ip.String()
		} else if hostport != "" {
			host = hostport
		}
	}
	if host == "" {
		host = "127.0.0.1"
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		port = defaultPort
	}
	u := url.URL{Scheme: scheme, Host: net.JoinHostPort(host, port)}
	if path != "" {
		u.Path = "/" + path
	}
	return strings.TrimRight(u.String(), "/")
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
