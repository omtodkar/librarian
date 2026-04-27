package indexer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"librarian/internal/store"
)

// pyTypeVarKindHintMarker is the metadata substring that uniquely identifies
// TypeVar symbol nodes — written by python.go's PostProcess pass.
const pyTypeVarKindHintMarker = `"kind_hint":"typevar"`

// pyPendingCrossModuleMarker is the metadata key written by python.go's
// PostProcess when a type arg has an import binding to another module but
// couldn't be resolved same-module. The post-graph-pass resolver below
// consumes this marker after all TypeVar nodes have been projected.
const pyPendingCrossModuleMarker = `"type_args_pending_cross_module"`

// buildPythonTypeVarCrossModuleEdges is the post-graph-pass resolver for
// lib-0pa.5. It resolves cross-module TypeVar references in Generic[T] class
// bases:
//
//  1. Lists all TypeVar symbol nodes (those whose graph_nodes.metadata
//     contains "kind_hint":"typevar" — written by lib-0pa.2's PostProcess).
//  2. Lists every inherits edge whose metadata contains
//     "type_args_pending_cross_module" — a map of {localAlias: canonicalPath}
//     written by python.go's PostProcess for args that couldn't be resolved
//     same-module but have an import binding.
//  3. For each pending candidate, probes the store for sym:<canonicalPath>.
//     If the node exists as a TypeVar, its sym: ID is appended to the edge's
//     type_args_resolved and the pending key is removed from the metadata.
//  4. Upserts the updated edge.
//
// Runs after buildDartCallRPCEdges so all TypeVar nodes from every indexed
// file exist in the store before resolution begins. Missing TypeVar nodes
// (e.g. typing_extensions.T from an un-indexed external package) leave the
// pending arg absent from type_args_resolved, satisfying the "absent when no
// match" invariant from lib-0pa.2.
func (idx *Indexer) buildPythonTypeVarCrossModuleEdges(result *GraphResult) {
	// Step 1: collect all known TypeVar node IDs into a set.
	typeVarNodes, err := idx.store.ListSymbolNodesWithMetadataContaining(pyTypeVarKindHintMarker)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("python typevar cross-module resolver: list typevar nodes: %s", err))
		return
	}
	typeVarSet := make(map[string]bool, len(typeVarNodes))
	for _, n := range typeVarNodes {
		typeVarSet[n.ID] = true
	}

	// Step 2: find inherits edges with pending cross-module candidates.
	pendingEdges, err := idx.store.ListEdgesWithMetadataContaining("inherits", pyPendingCrossModuleMarker)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("python typevar cross-module resolver: list pending edges: %s", err))
		return
	}

	// Step 3+4: resolve and upsert.
	for _, e := range pendingEdges {
		updated, changed := resolvePendingTypeVarEdge(e, typeVarSet)
		if !changed {
			continue
		}
		if err := idx.store.UpsertEdge(updated); err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("python typevar cross-module resolver: upsert edge %s→%s: %s", e.From, e.To, err))
		} else {
			result.EdgesAdded++
		}
	}
}

// resolvePendingTypeVarEdge reads the type_args_pending_cross_module map from
// e.Metadata, resolves any candidates present in typeVarSet, merges new
// resolutions into type_args_resolved, and removes the pending key.
//
// Returns (updated edge, true) after consuming the pending key, regardless of
// whether any TypeVar was resolved — the key is always removed to prevent
// repeated no-op scans on subsequent IndexProjectGraph runs. Returns
// (original edge, false) only when metadata cannot be parsed or the pending
// map is absent or empty.
func resolvePendingTypeVarEdge(e store.Edge, typeVarSet map[string]bool) (store.Edge, bool) {
	var meta map[string]json.RawMessage
	if err := json.Unmarshal([]byte(e.Metadata), &meta); err != nil {
		return e, false
	}
	pendingRaw, ok := meta["type_args_pending_cross_module"]
	if !ok {
		return e, false
	}

	var pending map[string]string // localAlias → canonicalDottedPath
	if err := json.Unmarshal(pendingRaw, &pending); err != nil || len(pending) == 0 {
		return e, false
	}

	// Collect already-resolved IDs so we don't duplicate.
	var resolvedList []string
	if existing, ok := meta["type_args_resolved"]; ok {
		_ = json.Unmarshal(existing, &resolvedList)
	}
	resolvedSet := make(map[string]bool, len(resolvedList))
	for _, r := range resolvedList {
		resolvedSet[r] = true
	}

	// Attempt resolution: pending map preserves the insertion order of type_args
	// but map iteration in Go is non-deterministic. Sort by local alias name so
	// the order of newly appended IDs is stable and test-friendly.
	aliases := make([]string, 0, len(pending))
	for alias := range pending {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	anyNew := false
	for _, alias := range aliases {
		symID := store.SymbolNodeID(pending[alias])
		if typeVarSet[symID] && !resolvedSet[symID] {
			resolvedList = append(resolvedList, symID)
			resolvedSet[symID] = true
			anyNew = true
		}
	}

	// Always remove the pending key — it has been consumed regardless of
	// resolution outcome. This prevents repeated no-op scans on subsequent
	// IndexProjectGraph runs when all pending candidates are unresolvable
	// (e.g., imports from external packages not indexed in the project).
	delete(meta, "type_args_pending_cross_module")

	if anyNew {
		resolvedBytes, err := json.Marshal(resolvedList)
		if err != nil {
			return e, false
		}
		meta["type_args_resolved"] = resolvedBytes
	}

	e.Metadata = marshalSortedRawMetadata(meta)
	return e, true
}

// marshalSortedRawMetadata serialises map[string]json.RawMessage with
// sorted keys, matching the deterministic ordering that refMetadataJSON
// enforces on the write path. Sorted key order is required because the
// edge is stored with INSERT OR REPLACE — non-deterministic JSON would
// trigger real disk writes even when the logical metadata is unchanged.
func marshalSortedRawMetadata(m map[string]json.RawMessage) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteByte('{')
	first := true
	for _, k := range keys {
		if !first {
			b.WriteByte(',')
		}
		first = false
		kb, err := json.Marshal(k)
		if err != nil {
			continue
		}
		b.Write(kb)
		b.WriteByte(':')
		b.Write(m[k])
	}
	b.WriteByte('}')
	return b.String()
}
