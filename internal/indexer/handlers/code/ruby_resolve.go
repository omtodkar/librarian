package code

import (
	"path/filepath"
	"strings"

	"librarian/internal/indexer"
)

// Relative-require resolver for Ruby. Rewrites grammar-emitted ImportRef
// targets that start with "relative:" into an absolute project-relative path
// so `require_relative 'config'` from two different directories maps to
// distinct, canonical graph nodes rather than both resolving to the same
// bare target string "config".
//
// Resolution: strip "relative:" prefix, then join the importing file's
// directory with the raw path. The ".rb" extension is appended when the path
// has no extension, matching Ruby's own `require_relative` semantics.
//
// Only relative targets are touched; plain require / load targets pass through
// as-is (they refer to gem names or load-path entries, not local files).

// ResolveImports implements importResolver for RubyGrammar. Called by
// ParseCtx after grammar.Imports() so the file's on-disk path is available
// via ctx.AbsPath.
func (*RubyGrammar) ResolveImports(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference {
	if ctx.AbsPath == "" {
		// Legacy Parse path (grammar-level tests with no context) — skip.
		return refs
	}
	projectRoot := inferProjectRoot(ctx.AbsPath, path)

	for i, r := range refs {
		if r.Kind != "import" || !strings.HasPrefix(r.Target, "relative:") {
			continue
		}
		relPath := strings.TrimPrefix(r.Target, "relative:")
		resolved := resolveRubyRelative(ctx.AbsPath, projectRoot, relPath)
		if resolved != "" {
			refs[i].Target = resolved
		}
	}
	return refs
}

// resolveRubyRelative resolves a require_relative path against the directory
// of the importing file. Returns the project-relative path (no leading slash).
// Appends ".rb" when the path has no extension. Returns "" when resolution
// fails (e.g., outside the project root).
func resolveRubyRelative(absPath, projectRoot, relPath string) string {
	dir := filepath.Dir(filepath.Clean(absPath))
	abs := filepath.Join(dir, relPath)
	// Add .rb extension if missing.
	if filepath.Ext(abs) == "" {
		abs += ".rb"
	}
	if projectRoot == "" {
		return filepath.ToSlash(abs)
	}
	rel, err := filepath.Rel(projectRoot, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		// Outside project root — keep absolute form so it still lands on a
		// unique node rather than a collision-prone bare name.
		return filepath.ToSlash(abs)
	}
	return filepath.ToSlash(rel)
}
