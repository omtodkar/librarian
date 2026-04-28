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
	// users table is not defined in the fixture — edge must be unresolved.
	if v, _ := meta["unresolved"].(bool); !v {
		t.Errorf("expected unresolved=true for edge to undefined table; got metadata: %v", meta)
	}
	if meta["target_name"] == nil {
		t.Errorf("expected target_name in metadata for undefined table; got: %v", meta)
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

// TestPlpgsqlBodyRefs_CrossFileFuncFirst verifies that body_references edges
// are resolved correctly when the function file is processed BEFORE the table
// file (alphabetical ordering: "a_funcs.sql" sorts before "b_tables.sql").
//
// resolveBodyRefTarget marks the edge unresolved=true during per-file
// processing because the table node does not yet exist. The post-graph-pass
// buildBodyReferencesResolutionEdges resolver must strip the marker once all
// files have been indexed.
func TestPlpgsqlBodyRefs_CrossFileFuncFirst(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		// Named "a_" so the function file sorts before the table file.
		"a_funcs.sql": `
CREATE FUNCTION public.process_orders() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  SELECT * FROM orders;
END;
$$;
`,
		"b_tables.sql": `CREATE TABLE public.orders (id SERIAL PRIMARY KEY);`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	funcID := "sym:public.process_orders"
	tableID := "sym:public.orders"
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
		t.Fatalf("expected edge %s → %s after post-pass resolution; edges: %+v", funcID, tableID, edges)
	}

	var meta map[string]any
	if err := json.Unmarshal([]byte(found.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal edge metadata: %v", err)
	}
	if v, _ := meta["unresolved"].(bool); v {
		t.Errorf("edge should not be unresolved after post-graph-pass resolution (function-first ordering)")
	}
	if meta["target_name"] != nil {
		t.Errorf("target_name should be removed after resolution; got: %v", meta["target_name"])
	}
}

// TestPlpgsqlBodyRefs_PartialOnMalformedBody verifies that when
// plpgsqlExtractRefs returns ok=false (malformed PL/pgSQL body), the function
// Unit gets Metadata["partial"]=true on its symbol node and no body_references
// edges are emitted for it. A well-formed function in the same file should
// still index cleanly.
func TestPlpgsqlBodyRefs_PartialOnMalformedBody(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"funcs.sql": `
CREATE TABLE public.logs (id SERIAL PRIMARY KEY);

-- Well-formed function — should index cleanly.
CREATE FUNCTION public.good_func() RETURNS void LANGUAGE plpgsql AS $$
BEGIN
  SELECT * FROM logs;
END;
$$;

-- Malformed PL/pgSQL body: tree-sitter SQL grammar parses this as a valid
-- function (bare SQL SELECT is accepted), but pg_query.ParsePlPgSqlToJSON
-- rejects it because SELECT without BEGIN/END is not valid PL/pgSQL syntax.
-- This is the only reliable way to trigger partial=true in the integration
-- path since tree-sitter and pg_query agree on most syntactically invalid cases.
CREATE FUNCTION public.bad_func() RETURNS void LANGUAGE plpgsql AS $$ SELECT 1 $$;
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// bad_func symbol should exist (the function unit is always emitted).
	badFuncID := "sym:public.bad_func"
	node, err := s.GetNode(badFuncID)
	if err != nil {
		t.Fatalf("GetNode(%s): %v", badFuncID, err)
	}
	if node == nil {
		t.Fatalf("expected symbol node %s to exist; got nil", badFuncID)
	}

	var nodeMeta map[string]any
	if err := json.Unmarshal([]byte(node.Metadata), &nodeMeta); err != nil {
		t.Fatalf("unmarshal node metadata: %v (%s)", err, node.Metadata)
	}
	if v, _ := nodeMeta["partial"].(bool); !v {
		t.Errorf("expected partial=true on %s node metadata; got: %v", badFuncID, nodeMeta)
	}

	// No body_references edges should be emitted for bad_func.
	edges, err := s.Neighbors(badFuncID, "out", store.EdgeKindBodyReferences)
	if err != nil {
		t.Fatalf("Neighbors(%s): %v", badFuncID, err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 body_references edges for malformed function; got %d: %+v", len(edges), edges)
	}

	// good_func should still have a body_references edge to logs.
	goodFuncID := "sym:public.good_func"
	goodEdges, err := s.Neighbors(goodFuncID, "out", store.EdgeKindBodyReferences)
	if err != nil {
		t.Fatalf("Neighbors(%s): %v", goodFuncID, err)
	}
	if len(goodEdges) == 0 {
		t.Errorf("expected body_references edges for good_func; got none")
	}
}

// TestPlpgsqlBodyRefs_LanguageSQLNoBodyRefs verifies that a LANGUAGE sql
// function (not plpgsql) never produces body_references edges. The PL/pgSQL
// walker must only be invoked for LANGUAGE plpgsql.
func TestPlpgsqlBodyRefs_LanguageSQLNoBodyRefs(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"schema.sql": `
CREATE TABLE public.users (id SERIAL PRIMARY KEY, name TEXT);

CREATE FUNCTION public.get_user_count() RETURNS bigint LANGUAGE sql AS $$
  SELECT count(*) FROM users;
$$;
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	funcID := "sym:public.get_user_count"
	edges, err := s.Neighbors(funcID, "out", store.EdgeKindBodyReferences)
	if err != nil {
		t.Fatalf("Neighbors(%s): %v", funcID, err)
	}
	if len(edges) != 0 {
		t.Errorf("expected 0 body_references edges for LANGUAGE sql function; got %d: %+v", len(edges), edges)
	}
}
