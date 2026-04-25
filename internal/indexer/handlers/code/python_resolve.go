package code

import (
	"os"
	"path/filepath"
	"strings"

	"librarian/internal/indexer"
)

// Relative-import resolver for Python. Converts grammar-emitted Reference.Target
// values like ".utils" and "..pkg.Thing" into absolute dotted paths like
// "mypkg.sub.utils" and "mypkg.pkg.Thing" so one symbol imported by both the
// relative and absolute form lands on a single graph node.
//
// Resolution proceeds in three tiers, in priority order:
//  1. python.src_roots config match (handles src-layout + PEP 420 namespace
//     packages without requiring __init__.py).
//  2. __init__.py walk (traditional packages).
//  3. Virtual package from project-relative directory (last-resort fallback
//     when neither 1 nor 2 identifies a package).
//
// See containingPackage for the detection itself and resolveRelativeTarget for
// the pure string rewrite.

// pyKeywords is the set of reserved words in Python 3. The virtual-package
// fallback rejects directory names that are keywords (e.g. `class/a.py`) so
// the synthesized dotted path stays a valid Python identifier.
var pyKeywords = map[string]struct{}{
	"False": {}, "None": {}, "True": {}, "and": {}, "as": {},
	"assert": {}, "async": {}, "await": {}, "break": {}, "class": {},
	"continue": {}, "def": {}, "del": {}, "elif": {}, "else": {},
	"except": {}, "finally": {}, "for": {}, "from": {}, "global": {},
	"if": {}, "import": {}, "in": {}, "is": {}, "lambda": {},
	"nonlocal": {}, "not": {}, "or": {}, "pass": {}, "raise": {},
	"return": {}, "try": {}, "while": {}, "with": {}, "yield": {},
}

// isValidPyIdentifier reports whether s is a legal Python identifier — starts
// with [A-Za-z_], rest is [A-Za-z0-9_], and is not a keyword. Empty strings
// fail. Used by the virtual-package fallback to reject directories whose names
// aren't import-safe.
func isValidPyIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_':
			continue
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			continue
		case i > 0 && r >= '0' && r <= '9':
			continue
		default:
			return false
		}
	}
	if _, reserved := pyKeywords[s]; reserved {
		return false
	}
	return true
}

// resolveRelativeTarget rewrites a single import target. anchor is the dotted
// containing-package parts of the source file; raw is the grammar-emitted
// Target. Non-relative raws pass through unchanged; absolute imports are not
// our problem.
//
// The rewrite:
//   - Count leading dots k. k=0 → pass-through.
//   - Strip dots to get the "tail" (module.name, module.*, or empty).
//   - Anchor drops (k-1) trailing components: `from .` stays in place,
//     `from ..` steps up one level, etc.
//   - Join anchor + tail parts with ".". Wildcard '*' preserved verbatim.
//   - Over-dotted (k-1 exceeds anchor depth) clamps anchor to empty and emits
//     the bare tail.
func resolveRelativeTarget(anchor []string, raw string) string {
	k := 0
	for k < len(raw) && raw[k] == '.' {
		k++
	}
	if k == 0 {
		return raw
	}
	tail := raw[k:]

	stepUp := k - 1
	effective := anchor
	if stepUp >= len(effective) {
		effective = nil
	} else if stepUp > 0 {
		effective = anchor[:len(anchor)-stepUp]
	}

	parts := make([]string, 0, len(effective)+2)
	parts = append(parts, effective...)
	for _, seg := range strings.Split(tail, ".") {
		if seg == "" {
			continue
		}
		parts = append(parts, seg)
	}
	if len(parts) == 0 {
		// Bare "." or ".." with no anchor — no real symbol to target. Grammar
		// never emits these today (from-imports always carry a name), but
		// returning "" would violate the non-empty-Target invariant. Pass the
		// raw form through so the downstream dot-check fires with a clear
		// message instead of silent corruption.
		return raw
	}
	return strings.Join(parts, ".")
}

// virtualPackageFromRelDir synthesises a dotted package from a project-relative
// directory path. Used as the last-resort fallback when no src_root matches and
// no __init__.py chain exists. Returns (nil, false) if any directory component
// isn't a valid Python identifier — in that case the resolver strips dots
// instead of guessing.
func virtualPackageFromRelDir(relDir string) ([]string, bool) {
	relDir = filepath.Clean(relDir)
	if relDir == "." || relDir == "" {
		return nil, true
	}
	parts := strings.Split(filepath.ToSlash(relDir), "/")
	for _, p := range parts {
		if !isValidPyIdentifier(p) {
			return nil, false
		}
	}
	return parts, true
}

// packageFromSrcRoot returns the dotted package parts for absPath if it sits
// under any of srcRoots. Returns (nil, false) when no root matches.
//
// Matching is by filepath.Clean prefix. Example: srcRoots=["/proj/src"] and
// absPath="/proj/src/mypkg/sub/a.py" yields ["mypkg", "sub"] (the file's
// parent directory relative to the root). A file sitting directly in a
// src_root (e.g., "/proj/src/a.py") yields an empty parts slice and true.
func packageFromSrcRoot(absPath string, srcRoots []string) ([]string, bool) {
	absPath = filepath.Clean(absPath)
	fileDir := filepath.Dir(absPath)
	for _, root := range srcRoots {
		root = filepath.Clean(root)
		if root == "" || root == "." {
			continue
		}
		rel, err := filepath.Rel(root, fileDir)
		if err != nil {
			continue
		}
		rel = filepath.Clean(rel)
		if rel == "." {
			return nil, true
		}
		// filepath.Rel returning a path starting with ".." means absPath is
		// outside this root.
		if strings.HasPrefix(rel, "..") {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		valid := true
		for _, p := range parts {
			if !isValidPyIdentifier(p) {
				valid = false
				break
			}
		}
		if !valid {
			// Matched a root but the subpath isn't import-safe. Treat as
			// no-match so the resolver falls through to the next tier.
			continue
		}
		return parts, true
	}
	return nil, false
}

// packageFromInitWalk walks upward from filepath.Dir(absPath) collecting
// directory basenames while each ancestor contains an __init__.py. Returns
// the dotted parts with the root-most element first. Uses statFn for testing;
// pass nil to use os.Stat.
//
// Stops at the first ancestor that does NOT contain __init__.py (or the
// filesystem root), so src/mypkg/a.py with mypkg/__init__.py but no
// src/__init__.py returns ["mypkg"].
func packageFromInitWalk(absPath string, statFn func(string) error) []string {
	if statFn == nil {
		statFn = func(p string) error { _, err := os.Stat(p); return err }
	}
	var parts []string
	dir := filepath.Dir(filepath.Clean(absPath))
	for {
		if dir == "" || dir == "/" || dir == "." {
			break
		}
		// Reached a filesystem root (e.g., "C:\\" on Windows — parent equals self).
		if parent := filepath.Dir(dir); parent == dir {
			break
		}
		if err := statFn(filepath.Join(dir, "__init__.py")); err != nil {
			break
		}
		parts = append([]string{filepath.Base(dir)}, parts...)
		dir = filepath.Dir(dir)
	}
	return parts
}

// containingPackage returns the dotted Python package path for the file at
// absPath. Runs the three-tier detection: src_roots → __init__.py walk →
// virtual directory fallback. projectRoot is used for the virtual fallback
// (to compute a project-relative directory); pass "" to skip the fallback
// and return an empty package for files without markers.
//
// statFn is injected for testability; nil means use os.Stat.
func containingPackage(absPath, projectRoot string, srcRoots []string, statFn func(string) error) []string {
	if parts, ok := packageFromSrcRoot(absPath, srcRoots); ok {
		return parts
	}
	if parts := packageFromInitWalk(absPath, statFn); len(parts) > 0 {
		return parts
	}
	if projectRoot == "" {
		return nil
	}
	relDir, err := filepath.Rel(projectRoot, filepath.Dir(filepath.Clean(absPath)))
	if err != nil {
		return nil
	}
	if strings.HasPrefix(relDir, "..") {
		return nil
	}
	if parts, ok := virtualPackageFromRelDir(relDir); ok {
		return parts
	}
	return nil
}

// ResolveImports rewrites relative-form Reference.Target values in refs to
// absolute dotted paths using the file's location and ctx.PythonSrcRoots. Refs
// with Kind != "import" or without leading dots pass through unchanged.
//
// The method receiver is PythonGrammar so the code.ParseCtx dispatcher can
// type-assert via the importResolver interface; the resolver itself is
// entirely pure + package-internal helpers above.
//
// projectRoot for the virtual-package fallback is inferred from ctx.AbsPath and
// the file's relative path: the prefix of AbsPath that corresponds to stripping
// `path` from the end. Empty AbsPath disables resolution (Parse backward-compat).
func (*PythonGrammar) ResolveImports(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference {
	if ctx.AbsPath == "" {
		return refs
	}
	projectRoot := inferProjectRoot(ctx.AbsPath, path)
	anchor := containingPackage(ctx.AbsPath, projectRoot, ctx.PythonSrcRoots, nil)

	for i, r := range refs {
		if r.Kind != "import" {
			continue
		}
		if !strings.HasPrefix(r.Target, ".") {
			continue
		}
		refs[i].Target = resolveRelativeTarget(anchor, r.Target)
	}
	return refs
}

// inferProjectRoot returns the directory prefix of absPath that corresponds to
// stripping the project-relative path suffix. For absPath="/proj/pkg/a.py" and
// path="pkg/a.py" the result is "/proj". Returns "" if path isn't a suffix of
// absPath (unexpected but defensive).
func inferProjectRoot(absPath, relPath string) string {
	absClean := filepath.ToSlash(filepath.Clean(absPath))
	relClean := filepath.ToSlash(filepath.Clean(relPath))
	if relClean == "." || relClean == "" {
		return filepath.Dir(absPath)
	}
	if strings.HasSuffix(absClean, "/"+relClean) {
		trimmed := strings.TrimSuffix(absClean, "/"+relClean)
		return filepath.FromSlash(trimmed)
	}
	return ""
}
