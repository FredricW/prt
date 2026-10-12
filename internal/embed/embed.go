// Package embed turns change-unit text into vectors (pipeline stage 3).
//
// Adapters: Ollama (/api/embed, implemented), ONNX (stub, see onnx.go) and
// Fake (deterministic, for tests). If no adapter is available the pipeline
// skips stage 3 and groups come from structural links only.
package embed

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
)

// ErrUnavailable means no embedding model can be used right now.
var ErrUnavailable = errors.New("embed: model unavailable")

// Embedder embeds a batch of texts. Vectors are returned in input order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Model returns a stable id (adapter + model name) used in cache keys.
	Model() string
}

// Cosine returns the cosine similarity of two vectors (0 if either is zero).
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// FromSpec builds an Embedder from a config spec "ollama:<model>" or
// "onnx:<path>". ollamaHost may be empty (then OLLAMA_HOST or the default).
func FromSpec(spec, ollamaHost string) (Embedder, error) {
	kind, arg, ok := strings.Cut(spec, ":")
	if !ok {
		return nil, fmt.Errorf("embed: bad model spec %q (want ollama:<model> or onnx:<path>)", spec)
	}
	switch kind {
	case "ollama":
		return NewOllama(ollamaHost, arg), nil
	case "onnx":
		return NewONNX(arg), nil
	}
	return nil, fmt.Errorf("embed: unknown adapter %q", kind)
}
