package indexer

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Registry maps file extensions to FileHandler implementations. Implementations register
// themselves (typically via package init) so the core pipeline stays handler-agnostic.
//
// Registry is safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	handlers []FileHandler
	byExt    map[string]FileHandler
}

// NewRegistry returns an empty Registry. Most callers should use the package-level Default
// registry via RegisterDefault and DefaultRegistry.
func NewRegistry() *Registry {
	return &Registry{byExt: make(map[string]FileHandler)}
}

// Register adds a handler. Extension collisions resolve on last-registered-wins semantics —
// callers should register each handler once from a stable location (typically package init).
func (r *Registry) Register(h FileHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers = append(r.handlers, h)
	for _, ext := range h.Extensions() {
		r.byExt[strings.ToLower(ext)] = h
	}
}

// HandlerFor returns the handler registered for the file's extension, or nil if none.
// Extension matching is case-insensitive.
func (r *Registry) HandlerFor(path string) FileHandler {
	ext := strings.ToLower(filepath.Ext(path))
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byExt[ext]
}

// Extensions returns all registered extensions (lowercase, with leading dot), sorted
// alphabetically for stable output. The walker uses this to pre-filter candidate files.
func (r *Registry) Extensions() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	exts := make([]string, 0, len(r.byExt))
	for ext := range r.byExt {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	return exts
}

// Handlers returns a snapshot of registered handlers in registration order, for
// introspection (logging, CLI list commands).
func (r *Registry) Handlers() []FileHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]FileHandler, len(r.handlers))
	copy(out, r.handlers)
	return out
}

// defaultRegistry is the package-level registry. Handlers register into it via
// RegisterDefault, typically from package init; the indexer pipeline reads it when
// dispatching files.
var defaultRegistry = NewRegistry()

// DefaultRegistry returns the package-level registry.
func DefaultRegistry() *Registry {
	return defaultRegistry
}

// RegisterDefault is shorthand for DefaultRegistry().Register(h).
func RegisterDefault(h FileHandler) {
	defaultRegistry.Register(h)
}
