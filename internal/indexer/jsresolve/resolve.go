// Package jsresolve implements the TypeScript/JavaScript relative-import
// resolver that was previously in internal/indexer/handlers/code/javascript_resolve.go.
// Extracted so both handlers/code and indexer can import it without a cycle.
package jsresolve

import (
	"path/filepath"
	"strings"
)

// extPriority is the extension probe order: TS-family first, JS-family as fallback.
var extPriority = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

// knownExts is the set form of extPriority for constant-time membership checks.
var knownExts = func() map[string]bool {
	m := make(map[string]bool, len(extPriority))
	for _, e := range extPriority {
		m[e] = true
	}
	return m
}()

// jsLikeExts are the JS-family extensions whose imports should be probed
// against TS-family siblings first (NodeNext / moduleResolution: bundler).
var jsLikeExts = map[string]bool{".js": true, ".jsx": true, ".mjs": true, ".cjs": true}

// tsSiblings maps each JS-like extension to the TS-family peers to probe
// before falling back to the literal extension.
var tsSiblings = map[string][]string{
	".js":  {".ts", ".tsx"},
	".jsx": {".tsx"},
	".mjs": {".mts"},
	".cjs": {".cts"},
}

// Resolve resolves a relative ES-module specifier against the source file's
// absolute path, returning the absolute path of the matching file or
// ("", false) if no candidate exists.
//
// Probe order:
//  1. If spec carries an explicit known extension:
//     a. For JS-family extensions, try TS siblings first (NodeNext pattern).
//     b. Then try the literal extension.
//  2. If spec has no extension, probe each entry in extPriority.
//  3. If nothing matches and the path is a directory, try index.EXT in the
//     same priority order.
//
// statFn is injected for tests; pass StatIsFile for real filesystem probes.
func Resolve(spec, sourceAbs string, statFn func(string) bool) (string, bool) {
	srcDir := filepath.Dir(sourceAbs)
	candidate := filepath.Clean(filepath.Join(srcDir, spec))
	ext := filepath.Ext(candidate)

	if jsLikeExts[ext] {
		stem := strings.TrimSuffix(candidate, ext)
		for _, sib := range tsSiblings[ext] {
			if statFn(stem + sib) {
				return stem + sib, true
			}
		}
		if statFn(candidate) {
			return candidate, true
		}
		return "", false
	}

	if knownExts[ext] {
		if statFn(candidate) {
			return candidate, true
		}
		stem := strings.TrimSuffix(candidate, ext)
		for _, sib := range []string{".ts", ".tsx", ".mts", ".cts"} {
			if sib == ext {
				continue
			}
			if statFn(stem + sib) {
				return stem + sib, true
			}
		}
		return "", false
	}

	for _, e := range extPriority {
		if statFn(candidate + e) {
			return candidate + e, true
		}
	}

	for _, e := range extPriority {
		idx := filepath.Join(candidate, "index"+e)
		if statFn(idx) {
			return idx, true
		}
	}
	return "", false
}
