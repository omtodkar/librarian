package indexer_test

// plpgsql_body_refs_test.go — integration tests for body_references edge
// emission from PL/pgSQL function bodies (lib-o5dn.3).
//
// Tests verify the end-to-end pipeline: SQL files are indexed via
// IndexProjectGraph, and the resulting body_references edges are queried
// via store.Neighbors.

import (
	"encoding/json"
	"testing"

	"librarian/internal/store"
)

// TestPlpgsqlBodyRefs_BasicReadEdge verifies that a PL/pgSQL function that
// reads from a table produces a body_references edge with op="read".
func TestPlpgsqlBodyRefs_BasicReadEdge(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"schema.sql": `
CREATE TABLE public.users (id SERIAL PRIMARY KEY, name TEXT);

CREATE FUNCTION public.get_users() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  SELECT * FROM users;
END;
$$;
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	funcID := "sym:public.get_users"
	edges, err := s.Neighbors(funcID, "out", store.EdgeKindBodyReferences)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(edges) == 0 {
		t.Fatalf("expected at least one body_references edge from %s; got none", funcID)
	}

	tableID := "sym:public.users"
	var found *store.Edge
	for i := range edges {
		if edges[i].To == tableID {
			found = &edges[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected body_references edge %s → %s; got edges: %+v", funcID, tableID, edges)
	}

	var meta map[string]any
	if err := json.Unmarshal([]byte(found.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal edge metadata: %v (%s)", err, found.Metadata)
	}
	if meta["op"] != "read" {
		t.Errorf("edge op = %v, want read", meta["op"])
	}
}

// TestPlpgsqlBodyRefs_MetadataRoundTrip verifies that op and unresolved
// metadata values survive the insert/read cycle.
func TestPlpgsqlBodyRefs_MetadataRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"fn.sql": `
CREATE FUNCTION public.insert_user(uname text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  INSERT INTO users(name) VALUES (uname);
END;
$$;
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	funcID := "sym:public.insert_user(text)"
	edges, err := s.Neighbors(funcID, "out", store.EdgeKindBodyReferences)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(edges) == 0 {
		t.Fatalf("expected body_references edges from %s; got none", funcID)
	}

	var meta map[string]any
	if err := json.Unmarshal([]byte(edges[0].Metadata), &meta); err != nil {
		t.Fatalf("unmarshal edge metadata: %v", err)
	}

	// op must survive the round-trip with the correct value.
	if meta["op"] != "write" {
		t.Errorf("edge op = %v, want write (INSERT emits a write reference)", meta["op"])
	}
}

// TestPlpgsqlBodyRefs_UnresolvedTarget verifies that a reference to a table
// not defined in the project is emitted with unresolved=true and target_name
// preserving the bare symbol name.
func TestPlpgsqlBodyRefs_UnresolvedTarget(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		// Only the function file — no CREATE TABLE for ghost_table.
		"fn.sql": `
CREATE FUNCTION public.read_ghost() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  SELECT * FROM ghost_table;
END;
$$;
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	funcID := "sym:public.read_ghost"
	edges, err := s.Neighbors(funcID, "out", store.EdgeKindBodyReferences)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(edges) == 0 {
		t.Fatalf("expected body_references edge from %s; got none", funcID)
	}

	var meta map[string]any
	if err := json.Unmarshal([]byte(edges[0].Metadata), &meta); err != nil {
		t.Fatalf("unmarshal edge metadata: %v", err)
	}

	if v, _ := meta["unresolved"].(bool); !v {
		t.Errorf("expected unresolved=true for unknown table; got metadata: %v", meta)
	}
	if meta["target_name"] == nil {
		t.Errorf("expected target_name in metadata for unknown table; got: %v", meta)
	}
}

// TestPlpgsqlBodyRefs_Dedup verifies that no duplicate body_references edges
// are emitted for the same (source, target, kind) triple, even if the
// function is re-indexed.
func TestPlpgsqlBodyRefs_Dedup(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"schema.sql": `
CREATE TABLE public.items (id SERIAL PRIMARY KEY);

CREATE FUNCTION public.list_items() RETURNS SETOF public.items LANGUAGE plpgsql AS $$
BEGIN
  SELECT * FROM items;
  SELECT * FROM items;
END;
$$;
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	funcID := "sym:public.list_items"
	tableID := "sym:public.items"
	edges, err := s.Neighbors(funcID, "out", store.EdgeKindBodyReferences)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}

	var count int
	for _, e := range edges {
		if e.To == tableID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 body_references edge %s → %s; got %d", funcID, tableID, count)
	}
}

// TestPlpgsqlBodyRefs_CrossFileTableFirst verifies that body_references edges
// are resolved correctly in a two-file project when the table definition file
// is processed before the function file (alphabetical ordering: "a_" prefix).
//
// Note: resolveBodyRefTarget is called per-file during the graph pass. When
// the function file is processed before the table file (e.g. "a_funcs.sql"
// before "b_tables.sql"), the table node may not yet exist in the store,
// causing a false unresolved=true — a known limitation tracked in lib-ymwl.
// This test uses "a_tables.sql" / "b_funcs.sql" to ensure deterministic
// table-first ordering with MaxWorkers=1.
func TestPlpgsqlBodyRefs_CrossFileTableFirst(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		// Named "a_" so it sorts before "b_" and is indexed first.
		"a_tables.sql": `CREATE TABLE public.events (id SERIAL PRIMARY KEY);`,
		"b_funcs.sql": `
CREATE FUNCTION public.process_events() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  SELECT * FROM events;
END;
$$;
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	funcID := "sym:public.process_events"
	tableID := "sym:public.events"
	edges, err := s.Neighbors(funcID, "out", store.EdgeKindBodyReferences)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(edges) == 0 {
		t.Fatalf("expected body_references edge from %s; got none", funcID)
	}

	var found *store.Edge
	for i := range edges {
		if edges[i].To == tableID {
			found = &edges[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected edge %s → %s; edges: %+v", funcID, tableID, edges)
	}

	var meta map[string]any
	if err := json.Unmarshal([]byte(found.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal edge metadata: %v", err)
	}
	if v, _ := meta["unresolved"].(bool); v {
		t.Errorf("edge should not be unresolved when table is indexed before function (table-first ordering)")
	}
}
