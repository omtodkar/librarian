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
// Each extension has exactly one primary handler (Register / last-registered-wins) plus
// an optional set of additional handlers (RegisterAdditional). HandlerFor returns the
// primary; HandlersFor returns primary + all additional in registration order. The graph
// pass uses HandlersFor so multiple handlers can contribute symbols for the same file
// (e.g. the TypeScript grammar handler and the connect-es stub handler for *_connect.ts).
//
// Registry is safe for concurrent use.
type Registry struct {
	mu          sync.RWMutex
	handlers    []FileHandler
	byExt       map[string]FileHandler   // extension → primary handler
	byExtExtra  map[string][]FileHandler // extension → additional handlers
}

// NewRegistry returns an empty Registry. Most callers should use the package-level Default
// registry via RegisterDefault and DefaultRegistry.
func NewRegistry() *Registry {
	return &Registry{
		byExt:      make(map[string]FileHandler),
		byExtExtra: make(map[string][]FileHandler),
	}
}

// Register adds a handler as the primary for its extensions. Extension collisions resolve
// on last-registered-wins semantics — callers should register each handler once from a
// stable location (typically package init).
func (r *Registry) Register(h FileHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers = append(r.handlers, h)
	for _, ext := range h.Extensions() {
		r.byExt[strings.ToLower(ext)] = h
	}
}

// RegisterAdditional registers h as an additional handler for its extensions. Unlike
// Register, this does not replace the primary handler — both the primary and every
// additional handler run during graph-pass dispatch via HandlersFor. Multiple additional
// handlers per extension are collected in registration order.
//
// Typical use: a specialised handler that emits extra symbols for a subset of files
// sharing an extension with a general-purpose grammar (e.g. the connect-es stub handler
// registered on ".ts" alongside the TypeScript grammar).
func (r *Registry) RegisterAdditional(h FileHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers = append(r.handlers, h)
	for _, ext := range h.Extensions() {
		ext = strings.ToLower(ext)
		r.byExtExtra[ext] = append(r.byExtExtra[ext], h)
	}
}

// HandlerFor returns the primary handler registered for the file's extension, or nil if
// none. Extension matching is case-insensitive.
func (r *Registry) HandlerFor(path string) FileHandler {
	ext := strings.ToLower(filepath.Ext(path))
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byExt[ext]
}

// HandlersFor returns all handlers (primary first, then additional in registration order)
// for the file's extension. Returns nil when no handler is registered for the extension.
// Use this instead of HandlerFor when all handlers for a file should run — the graph pass
// calls this so additional handlers (e.g. connect-es) can contribute symbols alongside
// the primary grammar handler.
func (r *Registry) HandlersFor(path string) []FileHandler {
	ext := strings.ToLower(filepath.Ext(path))
	r.mu.RLock()
	defer r.mu.RUnlock()
	primary := r.byExt[ext]
	extra := r.byExtExtra[ext]
	if primary == nil && len(extra) == 0 {
		return nil
	}
	out := make([]FileHandler, 0, 1+len(extra))
	if primary != nil {
		out = append(out, primary)
	}
	out = append(out, extra...)
	return out
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

// RegisterDefaultAdditional is shorthand for DefaultRegistry().RegisterAdditional(h).
func RegisterDefaultAdditional(h FileHandler) {
	defaultRegistry.RegisterAdditional(h)
}
