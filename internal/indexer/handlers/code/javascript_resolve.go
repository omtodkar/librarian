package code

import (
	"os"
	"path/filepath"
	"strings"

	"librarian/internal/indexer"
)

// Relative-import resolver for JS/TS/TSX, symmetrical to Python's (see
// python_resolve.go). ES modules map 1:1 to files, so resolved specifiers
// land on file: graph nodes matching CodeFileNodeID — this unifies the
// "who imports file X?" query with the existing code_file node graph.
// Bare specifiers (npm packages like "lodash", "@scope/pkg") pass through
// as ext: nodes so the sym: namespace stays exclusive to in-project code
// symbols.
//
// Extension priority is TS-first, JSX/TSX as fallbacks. This ensures the
// canonical *source* file wins in mixed JS+TS projects (an `./utils`
// specifier with both `utils.ts` and transpiled `utils.js` on disk lands
// on the `.ts` source). NodeNext's explicit `.js` imports that point at TS
// sources are rewritten via the ext-stripping branch below.
var jsExtPriority = []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}

// jsKnownExts is the set form of jsExtPriority — constant-time membership
// checks during the "explicit-extension with rewrite" branch.
var jsKnownExts = func() map[string]bool {
	m := make(map[string]bool, len(jsExtPriority))
	for _, e := range jsExtPriority {
		m[e] = true
	}
	return m
}()

// jsJSLikeExts are the JS-family extensions that, when specified explicitly
// in an import, should be probed against TS-family siblings first
// (NodeNext / moduleResolution: bundler conventions — TS sources live
// beside their .js output, but imports in source say .js).
var jsJSLikeExts = map[string]bool{".js": true, ".jsx": true, ".mjs": true, ".cjs": true}

// jsTSSiblings for each JS-like extension lists the TS-family sources to
// probe before falling back to the literal extension.
var jsTSSiblings = map[string][]string{
	".js":  {".ts", ".tsx"},
	".jsx": {".tsx"},
	".mjs": {".mts"},
	".cjs": {".cts"},
}

// ResolveImports rewrites Reference.Target for JS/TS import refs: relative
// specifiers ("./utils", "../lib/foo") become project-relative resolved
// file paths, and bare specifiers (npm packages) stay as-is but get tagged
// via Metadata["node_kind"] so graphTargetID can route them to the correct
// namespace. Absolute specifiers ("/abs/path") are treated as unresolvable
// and left raw — they don't arise from conventional JS/TS sources.
//
// Unresolvable relative specifiers (target file doesn't exist on disk) are
// also left raw — the grammar-invariant check will flag them so the user
// gets a clear signal rather than a silently-wrong graph.
func (*jsLikeGrammar) ResolveImports(refs []indexer.Reference, path string, ctx indexer.ParseContext) []indexer.Reference {
	if ctx.AbsPath == "" || ctx.ProjectRoot == "" {
		return refs
	}
	for i, r := range refs {
		if r.Kind != "import" {
			continue
		}
		spec := r.Target
		if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
			// Bare (npm) or absolute specifier — tag for graph projection,
			// leave target unchanged. Bare covers "lodash", "@scope/pkg",
			// tsconfig paths aliases like "@/foo" (until we honour paths).
			tagNodeKind(&refs[i], "external")
			continue
		}
		resolvedAbs, ok := resolveJSImport(spec, ctx.AbsPath, statIsFile)
		if !ok {
			continue
		}
		rel, err := filepath.Rel(ctx.ProjectRoot, resolvedAbs)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "../") {
			// Resolved outside the project root — don't emit an edge that
			// would escape the workspace boundary.
			continue
		}
		refs[i].Target = rel
		tagNodeKind(&refs[i], "code_file")
	}
	return refs
}

// tagNodeKind attaches a Metadata["node_kind"] hint without overwriting any
// existing tag (grammar-level metadata like "member"/"default"/"namespace"
// survives because those keys differ from "node_kind"; an existing "node_kind"
// set upstream is preserved). Shared helper so the resolver's bare-vs-resolved
// branches don't drift, and so a future extension that calls this twice
// doesn't silently stomp the earlier decision.
func tagNodeKind(ref *indexer.Reference, kind string) {
	if ref.Metadata == nil {
		ref.Metadata = map[string]any{}
	}
	if _, exists := ref.Metadata["node_kind"]; exists {
		return
	}
	ref.Metadata["node_kind"] = kind
}

// resolveJSImport resolves a relative ES-module specifier against the source
// file's absolute path, returning the absolute path of the matching file on
// disk or ("", false) if no candidate matches.
//
// Probe order:
//  1. If spec carries an explicit known extension:
//     a. For JS-family extensions (.js etc.), try TS-family siblings first
//        (NodeNext pattern: source says .js, on-disk is .ts).
//     b. Then try the literal extension.
//  2. If spec has no extension, probe each entry in jsExtPriority in order.
//  3. If nothing matches and the path is a directory, try directory/index.EXT
//     in the same priority.
//
// statFn is injected for tests; pass statIsFile for real filesystem probes.
func resolveJSImport(spec, sourceAbs string, statFn func(string) bool) (string, bool) {
	srcDir := filepath.Dir(sourceAbs)
	candidate := filepath.Clean(filepath.Join(srcDir, spec))
	ext := filepath.Ext(candidate)

	// Tier 1a/b: explicit JS-family extension — try TS siblings before literal.
	if jsJSLikeExts[ext] {
		stem := strings.TrimSuffix(candidate, ext)
		for _, sib := range jsTSSiblings[ext] {
			if statFn(stem + sib) {
				return stem + sib, true
			}
		}
		if statFn(candidate) {
			return candidate, true
		}
		return "", false
	}

	// Tier 1 (TS-family explicit): literal first, then cross-family siblings
	// in case the on-disk file is the .tsx peer of a .ts import.
	if jsKnownExts[ext] {
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

	// Tier 2: no extension on the specifier — probe in priority order.
	for _, e := range jsExtPriority {
		if statFn(candidate + e) {
			return candidate + e, true
		}
	}

	// Tier 3: directory with index file.
	for _, e := range jsExtPriority {
		idx := filepath.Join(candidate, "index"+e)
		if statFn(idx) {
			return idx, true
		}
	}
	return "", false
}

// statIsFile is the production statFn — true iff the path exists and is a
// regular file (directories shouldn't satisfy a module-specifier probe).
func statIsFile(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
