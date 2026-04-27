package indexer

import (
	"encoding/json"
	"testing"

	"librarian/internal/store"
)

// Tests for resolvePendingTypeVarEdge live here (package indexer) so the
// unexported function is reachable without a test-only export shim.
// Mirrors the implements_rpc_internal_test.go pattern.

// makeEdge builds a store.Edge whose Metadata is serialised from m.
func makeEdge(from, to string, m map[string]any) store.Edge {
	b, _ := json.Marshal(m)
	return store.Edge{From: from, To: to, Kind: "inherits", Metadata: string(b)}
}

// TestResolvePendingTypeVarEdge_CleanupOnly covers the anyNew=false path:
// the pending TypeVar is not in the TypeVar set (e.g. external package),
// so type_args_resolved stays absent but type_args_pending_cross_module is
// still removed (cleanup-only upsert).
func TestResolvePendingTypeVarEdge_CleanupOnly(t *testing.T) {
	e := makeEdge("sym:repo.Foo", "sym:Generic", map[string]any{
		"relation":                      "extends",
		"type_args":                     []string{"T"},
		"type_args_pending_cross_module": map[string]string{"T": "typing_extensions.T"},
	})

	// TypeVar set does NOT contain sym:typing_extensions.T.
	typeVarSet := map[string]bool{"sym:mylib.types.T": true}

	updated, changed, anyNew := resolvePendingTypeVarEdge(e, typeVarSet)

	if !changed {
		t.Fatalf("expected changed=true (pending key must be cleaned up); got false")
	}
	if anyNew {
		t.Errorf("expected anyNew=false (no TypeVar resolved); got true")
	}

	// type_args_resolved must be absent.
	var meta map[string]json.RawMessage
	if err := json.Unmarshal([]byte(updated.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal updated metadata: %v", err)
	}
	if _, ok := meta["type_args_resolved"]; ok {
		t.Errorf("expected type_args_resolved absent after cleanup-only; metadata: %s", updated.Metadata)
	}
	// type_args_pending_cross_module must be removed.
	if _, ok := meta["type_args_pending_cross_module"]; ok {
		t.Errorf("expected type_args_pending_cross_module removed; metadata: %s", updated.Metadata)
	}
}

// TestResolvePendingTypeVarEdge_Deduplication covers the case where the
// TypeVar is already in type_args_resolved — it must not be duplicated.
func TestResolvePendingTypeVarEdge_Deduplication(t *testing.T) {
	e := makeEdge("sym:repo.Foo", "sym:Generic", map[string]any{
		"relation":                      "extends",
		"type_args":                     []string{"T"},
		"type_args_resolved":            []string{"sym:mylib.types.T"},
		"type_args_pending_cross_module": map[string]string{"T": "mylib.types.T"},
	})

	typeVarSet := map[string]bool{"sym:mylib.types.T": true}

	updated, changed, anyNew := resolvePendingTypeVarEdge(e, typeVarSet)

	if !changed {
		t.Fatalf("expected changed=true (pending key must be cleaned up)")
	}
	if anyNew {
		t.Errorf("expected anyNew=false (already resolved, no new entry); got true")
	}

	var meta map[string]json.RawMessage
	if err := json.Unmarshal([]byte(updated.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// type_args_resolved must still contain exactly one entry (no duplicate).
	var resolved []string
	if err := json.Unmarshal(meta["type_args_resolved"], &resolved); err != nil {
		t.Fatalf("unmarshal type_args_resolved: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != "sym:mylib.types.T" {
		t.Errorf("type_args_resolved = %v, want [sym:mylib.types.T] (no duplicate)", resolved)
	}
	if _, ok := meta["type_args_pending_cross_module"]; ok {
		t.Errorf("expected type_args_pending_cross_module removed; metadata: %s", updated.Metadata)
	}
}

// TestResolvePendingTypeVarEdge_AbsentPending covers the fast-path where the
// edge has no type_args_pending_cross_module key — must return (e, false, false).
func TestResolvePendingTypeVarEdge_AbsentPending(t *testing.T) {
	e := makeEdge("sym:repo.Foo", "sym:Generic", map[string]any{
		"relation":  "extends",
		"type_args": []string{"T"},
	})

	_, changed, anyNew := resolvePendingTypeVarEdge(e, map[string]bool{})

	if changed || anyNew {
		t.Errorf("expected (false, false) for edge without pending key; got changed=%v anyNew=%v", changed, anyNew)
	}
}

// TestResolvePendingTypeVarEdge_Resolved covers the happy path: pending
// TypeVar is in the TypeVar set, so type_args_resolved is populated and
// the pending key is removed.
func TestResolvePendingTypeVarEdge_Resolved(t *testing.T) {
	e := makeEdge("sym:repo.Foo", "sym:Generic", map[string]any{
		"relation":                      "extends",
		"type_args":                     []string{"T"},
		"type_args_pending_cross_module": map[string]string{"T": "mylib.types.T"},
	})

	typeVarSet := map[string]bool{"sym:mylib.types.T": true}

	updated, changed, anyNew := resolvePendingTypeVarEdge(e, typeVarSet)

	if !changed {
		t.Fatalf("expected changed=true")
	}
	if !anyNew {
		t.Errorf("expected anyNew=true (TypeVar was resolved)")
	}

	var meta map[string]json.RawMessage
	if err := json.Unmarshal([]byte(updated.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	var resolved []string
	if err := json.Unmarshal(meta["type_args_resolved"], &resolved); err != nil {
		t.Fatalf("unmarshal type_args_resolved: %v", err)
	}
	if len(resolved) != 1 || resolved[0] != "sym:mylib.types.T" {
		t.Errorf("type_args_resolved = %v, want [sym:mylib.types.T]", resolved)
	}
	if _, ok := meta["type_args_pending_cross_module"]; ok {
		t.Errorf("expected type_args_pending_cross_module absent after resolution; metadata: %s", updated.Metadata)
	}
}
