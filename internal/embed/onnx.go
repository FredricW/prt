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
// a tokenizer, and mean pooling. Until then Ready and every Embed call
// return ErrUnavailable, and FromSpec("onnx:…") falls back to Ollama.
type ONNX struct {
	Path string
}

var _ Embedder = (*ONNX)(nil)

// NewONNX returns the (stub) ONNX adapter for the model file at path.
func NewONNX(path string) *ONNX { return &ONNX{Path: path} }

// Model implements Embedder.
func (o *ONNX) Model() string { return "onnx:" + o.Path }

// Ready reports whether the runtime and model can be loaded (nil when
// they can). FromSpec checks it once, so one process never mixes vectors
// of two models under one cache key. Always ErrUnavailable for now.
func (o *ONNX) Ready() error {
	if !onnxImplemented {
		return fmt.Errorf("%w: onnx adapter not implemented", ErrUnavailable)
	}
	return nil
}

// onnxImplemented flips when the TODO above is done.
var onnxImplemented = false

// Embed implements Embedder; always ErrUnavailable for now.
func (o *ONNX) Embed(context.Context, []string) ([][]float32, error) {
	return nil, o.Ready()
}
