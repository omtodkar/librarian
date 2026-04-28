package indexer

// plpgsql_resolve_internal_test.go — unit tests for resolveBodyRefTarget,
// the internal helper that implements column→table fallback and unresolved
// marking for body_references edges (lib-o5dn.3).

import (
	"testing"

	"librarian/internal/store"
)

// TestResolveBodyRefTarget_FoundPrimary verifies that when the target node
// exists in the store, the Reference is returned unchanged.
func TestResolveBodyRefTarget_FoundPrimary(t *testing.T) {
	idx, s := newImplementsRPCInternalIndexer(t)
	if err := s.UpsertNode(store.Node{
		ID:   "sym:public.users",
		Kind: store.NodeKindSymbol,
	}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	ref := Reference{
		Kind:     store.EdgeKindBodyReferences,
		Source:   "sym:public.myfunc",
		Target:   "sym:public.users",
		Metadata: map[string]any{"op": "read"},
	}
	got := idx.resolveBodyRefTarget(ref)
	if got.Target != "sym:public.users" {
		t.Errorf("Target = %q, want sym:public.users", got.Target)
	}
	if v, _ := got.Metadata["unresolved"].(bool); v {
		t.Errorf("unresolved should not be set when target exists")
	}
}

// TestResolveBodyRefTarget_ColumnFallback verifies that when the column-level
// target is not found but the table-level node exists, the target is updated
// to the table-level node ID.
func TestResolveBodyRefTarget_ColumnFallback(t *testing.T) {
	idx, s := newImplementsRPCInternalIndexer(t)
	// Only the table node exists — no column node.
	if err := s.UpsertNode(store.Node{
		ID:   "sym:public.orders",
		Kind: store.NodeKindSymbol,
	}); err != nil {
		t.Fatalf("UpsertNode table: %v", err)
	}

	ref := Reference{
		Kind:     store.EdgeKindBodyReferences,
		Source:   "sym:public.myfunc",
		Target:   "sym:public.orders.amount", // column-level — not in store
		Metadata: map[string]any{"op": "read"},
	}
	got := idx.resolveBodyRefTarget(ref)
	if got.Target != "sym:public.orders" {
		t.Errorf("Target = %q after fallback, want sym:public.orders", got.Target)
	}
	if v, _ := got.Metadata["unresolved"].(bool); v {
		t.Errorf("unresolved should not be set after successful table fallback")
	}
}

// TestResolveBodyRefTarget_DoubleMiss verifies that when neither the column
// nor the table node exists, unresolved=true and target_name are set.
func TestResolveBodyRefTarget_DoubleMiss(t *testing.T) {
	idx, _ := newImplementsRPCInternalIndexer(t)

	ref := Reference{
		Kind:     store.EdgeKindBodyReferences,
		Source:   "sym:public.myfunc",
		Target:   "sym:public.ghost_table",
		Metadata: map[string]any{"op": "write"},
	}
	got := idx.resolveBodyRefTarget(ref)
	if got.Target != "sym:public.ghost_table" {
		t.Errorf("Target = %q, want original sym:public.ghost_table", got.Target)
	}
	if v, _ := got.Metadata["unresolved"].(bool); !v {
		t.Errorf("unresolved should be true on double miss")
	}
	if got.Metadata["target_name"] != "public.ghost_table" {
		t.Errorf("target_name = %v, want public.ghost_table", got.Metadata["target_name"])
	}
	// Original op is preserved.
	if got.Metadata["op"] != "write" {
		t.Errorf("op = %v, want write (original metadata preserved)", got.Metadata["op"])
	}
}

// TestResolveBodyRefTarget_NonSymTarget verifies that non-sym: targets
// (pending_execute, trigger_special) are returned unchanged without a
// store lookup.
func TestResolveBodyRefTarget_NonSymTarget(t *testing.T) {
	idx, _ := newImplementsRPCInternalIndexer(t)

	ref := Reference{
		Kind:     store.EdgeKindBodyReferences,
		Source:   "sym:public.myfunc",
		Target:   "<dynamic>", // pending_execute raw expression
		Metadata: map[string]any{"op": "write", "pending_execute": true},
	}
	got := idx.resolveBodyRefTarget(ref)
	if got.Target != "<dynamic>" {
		t.Errorf("Target = %q, want <dynamic> (non-sym: targets unchanged)", got.Target)
	}
	if v, _ := got.Metadata["unresolved"].(bool); v {
		t.Errorf("unresolved should not be set for non-sym: target")
	}
}
