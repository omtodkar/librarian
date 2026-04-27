package indexer

// sql_graph_routing_test.go — verifies that EdgeKindContains emitted by the
// SQL grammar's sqlExtractColumn is correctly routed through graphTargetID and
// graphNodeKindFromRef so it reaches the graph store as a sym:table→sym:col
// edge.
//
// Analogous to TestGraphTargetID_ImplementsRPCRoutesToSymbol in
// implements_rpc_internal_test.go — a direct unit test of the routing helpers
// rather than an end-to-end fixture index, per the established pattern.

import (
	"testing"

	"librarian/internal/store"
)

// TestGraphTargetID_ContainsRoutesToSymbol asserts that a Reference with
// Kind="contains" (emitted by the SQL grammar for table→column edges) resolves
// its target to a sym: node, not a file: node or anything else.
func TestGraphTargetID_ContainsRoutesToSymbol(t *testing.T) {
	ref := Reference{Kind: store.EdgeKindContains, Target: "public.users.id"}
	if got, want := graphTargetID(ref), store.SymbolNodeID("public.users.id"); got != want {
		t.Errorf("graphTargetID(contains).target = %q, want %q", got, want)
	}
	if got, want := graphNodeKindFromRef(ref), store.NodeKindSymbol; got != want {
		t.Errorf("graphNodeKindFromRef(contains).kind = %q, want %q", got, want)
	}
}

// TestGraphTargetID_ReferencesRoutesToSymbol asserts that EdgeKindReferences
// (SQL FK edges) resolves to sym: nodes — verifying the FK routing added in
// lib-do0 round 1.
func TestGraphTargetID_ReferencesRoutesToSymbol(t *testing.T) {
	ref := Reference{Kind: store.EdgeKindReferences, Target: "public.users.id"}
	if got, want := graphTargetID(ref), store.SymbolNodeID("public.users.id"); got != want {
		t.Errorf("graphTargetID(references).target = %q, want %q", got, want)
	}
	if got, want := graphNodeKindFromRef(ref), store.NodeKindSymbol; got != want {
		t.Errorf("graphNodeKindFromRef(references).kind = %q, want %q", got, want)
	}
}
