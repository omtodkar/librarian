package indexer

import (
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// filenamePattern pairs a filepath.Match glob (tested against the file's basename)
// with its handler. Used for files that have no predictable extension, such as
// "Dockerfile" (exact) or "Dockerfile.*" (any suffix).
type filenamePattern struct {
	pattern string
	handler FileHandler
}

// Registry maps file extensions to FileHandler implementations. Implementations register
// themselves (typically via package init) so the core pipeline stays handler-agnostic.
//
// Each extension has exactly one primary handler (Register / last-registered-wins) plus
// an optional set of additional handlers (RegisterAdditional). HandlerFor returns the
// primary; HandlersFor returns primary + all additional in registration order. The graph
// pass uses HandlersFor so multiple handlers can contribute symbols for the same file
// (e.g. the TypeScript grammar handler and the connect-es stub handler for *_connect.ts).
//
// Handlers for extension-less files (Dockerfile, Makefile, …) or filename families
// (Dockerfile.*) register via RegisterByFilenameGlob. HandlerFor tests these patterns
// against the file's basename when extension-keyed lookup returns nothing.
//
// Registry is safe for concurrent use.
type Registry struct {
	mu               sync.RWMutex
	handlers         []FileHandler
	byExt            map[string]FileHandler   // extension → primary handler
	byExtExtra       map[string][]FileHandler // extension → additional handlers
	byFilenameGlob   []filenamePattern        // basename glob patterns → handler
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
//
// IMPORTANT: Extensions reports only primary-handler extensions (byExt). The walker
// uses Extensions to pre-filter candidate files, so an extension registered ONLY via
// RegisterAdditional (with no primary handler on that extension) will be silently
// excluded from the walk. RegisterAdditional is safe only when every extension it
// declares already has a primary handler registered via Register; callers should
// ensure this at registration time.
func (r *Registry) RegisterAdditional(h FileHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers = append(r.handlers, h)
	for _, ext := range h.Extensions() {
		ext = strings.ToLower(ext)
		r.byExtExtra[ext] = append(r.byExtExtra[ext], h)
	}
}

// RegisterByFilenameGlob registers h as the handler for files whose basename matches
// any of the given filepath.Match patterns (e.g. "Dockerfile", "Dockerfile.*").
// This is the right choice for extension-less files or filename families that cannot
// be keyed by extension alone.
//
// Pattern collisions follow last-registered-wins semantics matching Register.
// h is appended to the handlers slice for Handlers() introspection only when not
// already present (prevents duplicate entries when a handler calls both Register and
// RegisterByFilenameGlob from the same init).
// Unlike Register, this does not populate byExt — pattern-registered handlers are
// invisible to callers that enumerate extensions (e.g. legacy callers of Extensions()).
func (r *Registry) RegisterByFilenameGlob(h FileHandler, patterns ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Deduplicate: only append to handlers if not already registered (e.g. via Register).
	found := false
	for _, existing := range r.handlers {
		if existing == h {
			found = true
			break
		}
	}
	if !found {
		r.handlers = append(r.handlers, h)
	}
	for _, p := range patterns {
		r.byFilenameGlob = append(r.byFilenameGlob, filenamePattern{pattern: p, handler: h})
	}
}

// HandlerFor returns the primary handler registered for the file's extension, or nil if
// none. Extension matching is case-insensitive. When no extension-keyed handler is found,
// HandlerFor falls back to basename glob patterns registered via RegisterByFilenameGlob.
func (r *Registry) HandlerFor(path string) FileHandler {
	ext := strings.ToLower(filepath.Ext(path))
	r.mu.RLock()
	defer r.mu.RUnlock()
	if h := r.byExt[ext]; h != nil {
		return h
	}
	base := filepath.Base(path)
	for _, fp := range r.byFilenameGlob {
		if ok, _ := filepath.Match(fp.pattern, base); ok {
			return fp.handler
		}
	}
	return nil
}

// HandlersFor returns all handlers (primary first, then additional in registration order)
// for the file's extension. Returns nil when no handler is registered for the extension.
// Use this instead of HandlerFor when all handlers for a file should run — the graph pass
// calls this so additional handlers (e.g. connect-es) can contribute symbols alongside
// the primary grammar handler.
//
// When extension-keyed lookup yields nothing, HandlersFor falls back to basename glob
// patterns registered via RegisterByFilenameGlob (same fallback as HandlerFor).
func (r *Registry) HandlersFor(path string) []FileHandler {
	ext := strings.ToLower(filepath.Ext(path))
	r.mu.RLock()
	defer r.mu.RUnlock()
	primary := r.byExt[ext]
	extra := r.byExtExtra[ext]
	if primary == nil && len(extra) == 0 {
		base := filepath.Base(path)
		for _, fp := range r.byFilenameGlob {
			if ok, _ := filepath.Match(fp.pattern, base); ok {
				return []FileHandler{fp.handler}
			}
		}
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

// RegisterDefaultByFilenameGlob is shorthand for DefaultRegistry().RegisterByFilenameGlob(h, patterns...).
func RegisterDefaultByFilenameGlob(h FileHandler, patterns ...string) {
	defaultRegistry.RegisterByFilenameGlob(h, patterns...)
}
