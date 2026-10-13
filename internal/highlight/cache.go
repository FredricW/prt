package highlight

import (
	"container/list"
	"crypto/sha256"
	"sync"
)

// Default cache sizes: whole files (by language and content hash) and
// single lines (by language and text).
const (
	DefaultFiles = 64
	DefaultLines = 2048
)

// Highlighter is Lines and Line with a bounded LRU cache in front. It is
// safe for concurrent use; the zero value is not usable, call New.
type Highlighter struct {
	mu    sync.Mutex
	files *lru[fileKey, [][]Span]
	lines *lru[lineKey, []Span]
}

type fileKey struct {
	lang string
	sum  [sha256.Size]byte
}

type lineKey struct{ lang, text string }

// New returns a Highlighter caching at most files whole files and lines
// single lines (values below 1 use the defaults).
func New(files, lines int) *Highlighter {
	if files < 1 {
		files = DefaultFiles
	}
	if lines < 1 {
		lines = DefaultLines
	}
	return &Highlighter{files: newLRU[fileKey, [][]Span](files), lines: newLRU[lineKey, []Span](lines)}
}

// Lines is the package Lines, cached per (lang, content hash).
func (h *Highlighter) Lines(lang string, src []byte) [][]Span {
	if !parseable(lang) {
		return nil
	}
	k := fileKey{lang: lang, sum: sha256.Sum256(src)}
	h.mu.Lock()
	v, ok := h.files.get(k)
	h.mu.Unlock()
	if ok {
		return v
	}
	v = Lines(lang, src)
	h.mu.Lock()
	h.files.put(k, v)
	h.mu.Unlock()
	return v
}

// Line is the package Line, cached per (lang, text).
func (h *Highlighter) Line(lang, text string) []Span {
	if !parseable(lang) || text == "" {
		return nil
	}
	k := lineKey{lang: lang, text: text}
	h.mu.Lock()
	v, ok := h.lines.get(k)
	h.mu.Unlock()
	if ok {
		return v
	}
	v = Line(lang, text)
	h.mu.Lock()
	h.lines.put(k, v)
	h.mu.Unlock()
	return v
}

// parseable reports whether lang has a grammar (so unknown languages never
// fill the cache).
func parseable(lang string) bool {
	switch lang {
	case "go", "ts", "tsx", "js", "python":
		return true
	}
	return false
}

// lru is a small least-recently-used map (not safe for concurrent use).
type lru[K comparable, V any] struct {
	max   int
	order *list.List // front: most recent; values are *entry[K, V]
	items map[K]*list.Element
}

type entry[K comparable, V any] struct {
	k K
	v V
}

func newLRU[K comparable, V any](n int) *lru[K, V] {
	return &lru[K, V]{max: n, order: list.New(), items: make(map[K]*list.Element, n)}
}

func (c *lru[K, V]) get(k K) (V, bool) {
	e, ok := c.items[k]
	if !ok {
		var zero V
		return zero, false
	}
	c.order.MoveToFront(e)
	return e.Value.(*entry[K, V]).v, true
}

func (c *lru[K, V]) put(k K, v V) {
	if e, ok := c.items[k]; ok {
		e.Value.(*entry[K, V]).v = v
		c.order.MoveToFront(e)
		return
	}
	c.items[k] = c.order.PushFront(&entry[K, V]{k: k, v: v})
	for c.order.Len() > c.max {
		old := c.order.Back()
		c.order.Remove(old)
		delete(c.items, old.Value.(*entry[K, V]).k)
	}
}

// len is the number of cached entries (tests).
func (c *lru[K, V]) len() int { return c.order.Len() }
