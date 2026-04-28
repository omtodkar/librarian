package indexer

// body_refs_resolver.go — post-graph-pass resolver for body_references edges (lib-ymwl).
//
// resolveBodyRefTarget is called per-file during indexGraphFileDirect, which
// can produce false unresolved=true on body_references edges when the target
// table lives in a separate file that is processed AFTER the function file.
//
// buildBodyReferencesResolutionEdges re-scans all body_references edges that
// carry unresolved=true after the full graph pass so every candidate sym: node
// exists in the store. Edges whose targets are now present have the unresolved
// marker stripped; edges whose target changed (column→table fallback that only
// succeeds now) have the old edge deleted and a new edge with the corrected
// To field upserted. Genuinely missing symbols are left unchanged.

import (
	"encoding/json"
	"fmt"
	"strings"

	"librarian/internal/store"
)

// isBodyRefPlaceholder reports whether a graph node is a placeholder created
// by UpsertPlaceholderNode for a body_references target that was not yet
// indexed when the function file was processed.
//
// targetNodeMetadataJSON sets {"unresolved":true} on the placeholder node's
// metadata when the per-file resolveBodyRefTarget marks the edge unresolved.
// Real symbol nodes written by UpsertNode via unitMetadataJSON never carry
// "unresolved":true in their metadata, so this substring check is reliable.
func isBodyRefPlaceholder(n *store.Node) bool {
	return strings.Contains(n.Metadata, `"unresolved":true`)
}

// buildBodyReferencesResolutionEdges is the post-graph-pass resolver that
// repairs body_references edges incorrectly marked unresolved=true due to
// file-ordering during per-file processing.
//
// During per-file processing, resolveBodyRefTarget probes the store for the
// target table node. If the function file is indexed before the table file,
// the node does not yet exist and the edge receives unresolved=true. After
// the full graph pass, all table nodes exist; this resolver re-probes and
// removes the unresolved marker (or rewires the To field when a column-level
// fallback now succeeds).
//
// Correctness invariant: when the defining file (table file) is indexed
// after the function file in the same graph pass, the table symbol node
// transitions from placeholder to real via UpsertNode (ON CONFLICT DO UPDATE),
// which overwrites the placeholder's {"unresolved":true} node metadata with the
// real symbol's clean metadata before this resolver runs. isBodyRefPlaceholder
// therefore returns false for those nodes, and the edge is repaired. For tables
// genuinely absent from the project, the placeholder node retains its
// {"unresolved":true} metadata and the resolver leaves the edge unchanged.
func (idx *Indexer) buildBodyReferencesResolutionEdges(result *GraphResult) {
	edges, err := idx.store.ListEdgesWithMetadataContaining(
		store.EdgeKindBodyReferences, `"unresolved":true`)
	if err != nil {
		result.Errors = append(result.Errors,
			fmt.Sprintf("body_refs resolver: list edges: %s", err))
		return
	}

	for _, edge := range edges {
		var rawMeta map[string]json.RawMessage
		if err := json.Unmarshal([]byte(edge.Metadata), &rawMeta); err != nil {
			continue
		}
		// Defensive: skip if unresolved is not literally true.
		if string(rawMeta["unresolved"]) != "true" {
			continue
		}
		// Non-sym: targets (pending_execute, trigger_special) are handled by
		// other resolver passes; leave them as-is.
		if !strings.HasPrefix(edge.To, "sym:") {
			continue
		}

		resolvedTo := edge.To
		resolved := false

		if n, _ := idx.store.GetNode(edge.To); n != nil && !isBodyRefPlaceholder(n) {
			// Primary target now exists as a real symbol (not a placeholder) —
			// strip the unresolved marker.
			resolved = true
		} else {
			// Column-level fallback: sym:schema.table.column → sym:schema.table.
			bare := strings.TrimPrefix(edge.To, "sym:")
			parts := strings.Split(bare, ".")
			if len(parts) >= 3 {
				tableID := "sym:" + strings.Join(parts[:len(parts)-1], ".")
				if tbl, _ := idx.store.GetNode(tableID); tbl != nil && !isBodyRefPlaceholder(tbl) {
					resolvedTo = tableID
					resolved = true
				}
			}
		}

		if !resolved {
			continue // Genuinely unresolved — leave unchanged.
		}

		// Remove unresolved and target_name; keep all other metadata keys.
		delete(rawMeta, "unresolved")
		delete(rawMeta, "target_name")
		cleanMetadata := marshalSortedRawMetadata(rawMeta)

		if err := idx.store.UpsertEdge(store.Edge{
			From:     edge.From,
			To:       resolvedTo,
			Kind:     store.EdgeKindBodyReferences,
			Weight:   edge.Weight,
			Metadata: cleanMetadata,
		}); err != nil {
			result.Errors = append(result.Errors,
				fmt.Sprintf("body_refs resolver: upsert edge %s→%s: %s", edge.From, resolvedTo, err))
			continue
		}
		if resolvedTo != edge.To {
			// Target changed: delete the stale edge after the new one is committed.
			if err := idx.store.DeleteEdge(edge.From, edge.To, store.EdgeKindBodyReferences); err != nil {
				result.Errors = append(result.Errors,
					fmt.Sprintf("body_refs resolver: delete old edge %s→%s: %s", edge.From, edge.To, err))
			}
			result.EdgesAdded++
		}
	}
}
