package code

import (
	"path/filepath"
	"strings"

	"librarian/internal/indexer"
	"librarian/internal/indexer/jsresolve"
)

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
		resolvedAbs, ok := resolveJSImport(spec, ctx.AbsPath, jsresolve.StatIsFile)
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

// resolveJSImport is a shim delegating to jsresolve.Resolve. Kept unexported
// so the existing package-internal tests in javascript_resolve_test.go continue
// to compile without changes.
func resolveJSImport(spec, sourceAbs string, statFn func(string) bool) (string, bool) {
	return jsresolve.Resolve(spec, sourceAbs, statFn)
}

