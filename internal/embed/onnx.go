package embed

import (
	"context"
	"fmt"
)

// ONNX will run a ~130M-parameter code embedding model in-process through
// onnxruntime_go.
//
// TODO(embed-onnx): implement. Needs: choosing a model whose licence allows
// bundling (spec open question), the onnxruntime shared library at runtime,
// a tokenizer, and mean pooling. Until then every call returns ErrUnavailable
// and the pipeline falls back to Ollama (if configured) or skips stage 3.
type ONNX struct {
	Path string
}

var _ Embedder = (*ONNX)(nil)

// NewONNX returns the (stub) ONNX adapter for the model file at path.
func NewONNX(path string) *ONNX { return &ONNX{Path: path} }

// Model implements Embedder.
func (o *ONNX) Model() string { return "onnx:" + o.Path }

// Embed implements Embedder; always ErrUnavailable for now.
func (o *ONNX) Embed(context.Context, []string) ([][]float32, error) {
	return nil, fmt.Errorf("%w: onnx adapter not implemented", ErrUnavailable)
}
