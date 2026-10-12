package embed

import (
	"context"
	"hash/fnv"
	"strings"
	"unicode"
)

// Fake embeds text as a bag of hashed tokens (dimension Dim, default 64), so
// texts sharing words get high cosine similarity. Deterministic, no I/O.
type Fake struct {
	Dim   int
	Err   error
	Calls int
}

var _ Embedder = (*Fake)(nil)

// Model implements Embedder.
func (f *Fake) Model() string { return "fake:bag-of-tokens" }

// Embed implements Embedder.
func (f *Fake) Embed(_ context.Context, texts []string) ([][]float32, error) {
	f.Calls++
	if f.Err != nil {
		return nil, f.Err
	}
	dim := f.Dim
	if dim <= 0 {
		dim = 64
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, dim)
		for _, tok := range strings.FieldsFunc(t, func(r rune) bool {
			return r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(strings.ToLower(tok)))
			v[h.Sum32()%uint32(dim)]++
		}
		out[i] = v
	}
	return out, nil
}
