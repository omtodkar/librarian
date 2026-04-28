package indexer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"librarian/internal/store"
)

// buildGenericFQNResolutionEdges is the post-graph-pass resolver (lib-udam.3)
// that rewrites bare-name inheritance targets to their fully-qualified symbol
// IDs using the workspace-wide symbol index.
//
// Runs after all per-file projection passes so every real symbol node exists
// in the store before resolution begins. Two classes of placeholder edges are
// handled in a single scan:
//
//  1. Explicitly-unresolved edges (Metadata["unresolved"]=true) — emitted by
//     ResolveParents when a bare parent name (`extends Bar`) has no matching
//     import in the same file. Covers same-package Java siblings and any
//     grammar whose same-file resolver gave up.
//  2. Stale-stem edges — edges whose `to` node is a placeholder created by
//     UpsertPlaceholderNode (source_path is a dotted FQN or bare name, not a
//     real file path). Covers TS re-export chains where jsLocalNamedBindings
//     resolved `Bar` to `utils.Bar` (module-stem.Member) but utils.ts only
//     re-exports Bar from another file; the real symbol lives elsewhere.
//
// Resolution order for each placeholder edge:
//
//  a. Java same-package: strip the last segment from the `from` node's FQN to
//     get the package prefix; probe for pkg + "." + bareName.
//  b. Workspace short-name lookup: if exactly one real symbol has the same
//     short name, emit a resolved edge. When multiple candidates share the
//     name, emit one edge per candidate with Metadata["ambiguous_resolution"]=true
//     so callers can filter by confidence.
//
// On a successful resolution the old placeholder edge is deleted and a new
// resolved edge is upserted with "unresolved" removed from the metadata. The
// placeholder node itself is left for the orphan sweep to clean up.
//
// Missing symbols (e.g. java.lang.Object from un-indexed JDK sources) are
// left as placeholders — no match in shortNameMap → no change.
func (idx *Indexer) buildGenericFQNResolutionEdges(result *GraphResult) {
	// Build the workspace short-name map and real-symbol set from all symbol
	// nodes. A node is "real" when its source_path looks like a file path
	// (contains "/" or "\"); placeholder nodes written by UpsertPlaceholderNode
	// have source_path = ref.Target (a bare name or dotted FQN without slashes).
	allSymbols, err := idx.store.ListNodesByKind(store.NodeKindSymbol)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("fqn resolver: list symbols: %s", err))
		return
	}

	realSymbolIDs := make(map[string]bool, len(allSymbols))
	shortNameMap := make(map[string][]string) // bareName → []FQN (real symbols only)

	for _, n := range allSymbols {
		if !fqnIsRealSourcePath(n.SourcePath) {
			continue
		}
		realSymbolIDs[n.ID] = true
		fqn := strings.TrimPrefix(n.ID, "sym:")
		if fqn == n.ID {
			continue // defensive: only sym: nodes participate
		}
		short := fqnShortName(fqn)
		if short == "" {
			continue
		}
		shortNameMap[short] = append(shortNameMap[short], fqn)
	}

	// Scan all inherits edges. Skip edges whose `to` is already a real symbol.
	allInheritsEdges, err := idx.store.ListEdgesByKind(store.EdgeKindInherits)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("fqn resolver: list inherits edges: %s", err))
		return
	}

	for _, e := range allInheritsEdges {
		if realSymbolIDs[e.To] {
			continue // target is already a real symbol
		}
		if !strings.HasPrefix(e.To, "sym:") {
			continue // not a symbol target (requires/part edges won't appear here, but guard anyway)
		}
		idx.resolveOneFQNEdge(e, realSymbolIDs, shortNameMap, result)
	}
}

// resolveOneFQNEdge attempts to resolve a single inherits edge whose `to`
// node is a placeholder. Delegates to replaceInheritsEdge on success.
func (idx *Indexer) resolveOneFQNEdge(
	e store.Edge,
	realSymbolIDs map[string]bool,
	shortNameMap map[string][]string,
	result *GraphResult,
) {
	// Only resolve symbol-scoped edges (from = sym:...). File-scoped inherits
	// (e.g. proto `extend Base {}` which uses file: as the from node) must
	// not be cross-language resolved — the target would match Java/TS symbols
	// that have no semantic relationship to the proto extension.
	if !strings.HasPrefix(e.From, "sym:") {
		return
	}

	targetFQN := strings.TrimPrefix(e.To, "sym:")
	bareName := fqnShortName(targetFQN)
	if bareName == "" {
		return
	}

	// (a) Java same-package: extract package from the `from` FQN and probe
	// pkg + "." + bareName. The early-return above guarantees e.From starts
	// with "sym:", so the CutPrefix always succeeds.
	fromFQN, _ := strings.CutPrefix(e.From, "sym:")
	pkg := fqnPackage(fromFQN)
	if pkg != "" {
		candidate := store.SymbolNodeID(pkg + "." + bareName)
		if realSymbolIDs[candidate] {
			idx.replaceFQNEdge(e, candidate, false, result)
			return
		}
	}

	// (b) Workspace-wide short-name lookup.
	candidates := shortNameMap[bareName]
	switch len(candidates) {
	case 0:
		// No match — leave placeholder edge as-is.
	case 1:
		idx.replaceFQNEdge(e, store.SymbolNodeID(candidates[0]), false, result)
	default:
		// Ambiguous: emit edges for all candidates with the ambiguous marker.
		sorted := make([]string, len(candidates))
		copy(sorted, candidates)
		sort.Strings(sorted)
		idx.replaceFQNEdgeAmbiguous(e, sorted, result)
	}
}

// replaceFQNEdge upserts a resolved inherits edge and deletes the old
// placeholder edge. If ambiguous=true, the edge metadata is annotated with
// "ambiguous_resolution":true. Skips the delete when old and new target are
// the same (idempotent re-resolution).
func (idx *Indexer) replaceFQNEdge(e store.Edge, resolvedTo string, ambiguous bool, result *GraphResult) {
	newMeta := fqnBuildResolvedMeta(e.Metadata, ambiguous)
	if err := idx.store.UpsertEdge(store.Edge{
		From:     e.From,
		To:       resolvedTo,
		Kind:     e.Kind,
		Weight:   e.Weight,
		Metadata: newMeta,
	}); err != nil {
		result.Errors = append(result.Errors,
			fmt.Sprintf("fqn resolver: upsert resolved edge %s→%s: %s", e.From, resolvedTo, err))
		return
	}
	if e.To != resolvedTo {
		if err := idx.store.DeleteEdge(e.From, e.To, e.Kind); err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("fqn resolver: delete placeholder edge %s→%s: %s", e.From, e.To, err))
		}
		result.EdgesAdded++
	}
}

// replaceFQNEdgeAmbiguous emits one inherits edge per candidate (all with
// "ambiguous_resolution":true), then deletes the original placeholder edge.
func (idx *Indexer) replaceFQNEdgeAmbiguous(e store.Edge, candidates []string, result *GraphResult) {
	newMeta := fqnBuildResolvedMeta(e.Metadata, true)
	emitted := 0
	for _, fqn := range candidates {
		to := store.SymbolNodeID(fqn)
		if err := idx.store.UpsertEdge(store.Edge{
			From:     e.From,
			To:       to,
			Kind:     e.Kind,
			Weight:   e.Weight,
			Metadata: newMeta,
		}); err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("fqn resolver: upsert ambiguous edge %s→%s: %s", e.From, to, err))
			continue
		}
		emitted++
	}
	// Only delete the placeholder when ALL candidate upserts succeeded.
	// Partial failure (some succeeded, some failed) leaves the placeholder in
	// place so the relationship is still visible; the next re-index will
	// retry the failed candidates.
	if emitted == len(candidates) {
		if err := idx.store.DeleteEdge(e.From, e.To, e.Kind); err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("fqn resolver: delete placeholder edge %s→%s: %s", e.From, e.To, err))
		}
	}
	if emitted > 0 {
		result.EdgesAdded += emitted
	}
}

// fqnBuildResolvedMeta returns updated edge metadata with "unresolved" removed
// and optionally "ambiguous_resolution":true added. Returns a valid JSON object
// on all paths.
func fqnBuildResolvedMeta(originalMeta string, ambiguous bool) string {
	if originalMeta == "" || originalMeta == "{}" {
		if ambiguous {
			return `{"ambiguous_resolution":true}`
		}
		return "{}"
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal([]byte(originalMeta), &meta); err != nil {
		// Unparseable metadata: return a minimal JSON object.
		if ambiguous {
			return `{"ambiguous_resolution":true}`
		}
		return "{}"
	}
	delete(meta, "unresolved")
	if ambiguous {
		meta["ambiguous_resolution"] = json.RawMessage("true")
	}
	return marshalSortedRawMetadata(meta)
}

// fqnIsRealSourcePath reports whether a symbol node's source_path looks like
// a real file path (contains "/" or "\") rather than a placeholder FQN or
// bare name. UpsertNode writes source_path = file.FilePath for real symbols;
// UpsertPlaceholderNode writes source_path = ref.Target (a dotted FQN or bare
// class name, never containing a path separator).
func fqnIsRealSourcePath(sourcePath string) bool {
	return strings.ContainsAny(sourcePath, "/\\")
}

// fqnShortName returns the last dot-separated segment of a FQN.
// "com.example.Foo" → "Foo", "Foo" → "Foo", "" → "".
func fqnShortName(fqn string) string {
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		return fqn[i+1:]
	}
	return fqn
}

// fqnPackage returns the package/namespace prefix of a dotted FQN (all
// segments except the last). "com.example.Foo" → "com.example", "Foo" → "".
func fqnPackage(fqn string) string {
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		return fqn[:i]
	}
	return ""
}
