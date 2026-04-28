package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3" // register driver for raw-conn tests
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/store"
)

// setupGraphStore creates a temp store and seeds it with a small graph:
//
//	file:a.go --contains--> sym:A
//	sym:A     --inherits--> sym:B
//	sym:B     --call------> sym:C
func setupGraphStore(t *testing.T) *store.Store {
	t.Helper()
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "test.db"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	nodes := []store.Node{
		{ID: "file:a.go", Kind: store.NodeKindCodeFile, Label: "a.go"},
		{ID: "sym:A", Kind: store.NodeKindSymbol, Label: "A"},
		{ID: "sym:B", Kind: store.NodeKindSymbol, Label: "B"},
		{ID: "sym:C", Kind: store.NodeKindSymbol, Label: "C"},
	}
	for _, n := range nodes {
		if err := st.UpsertNode(n); err != nil {
			t.Fatalf("upsert node %s: %v", n.ID, err)
		}
	}

	edges := []store.Edge{
		{From: "file:a.go", To: "sym:A", Kind: store.EdgeKindContains},
		{From: "sym:A", To: "sym:B", Kind: store.EdgeKindInherits},
		{From: "sym:B", To: "sym:C", Kind: store.EdgeKindCall},
	}
	for _, e := range edges {
		if err := st.UpsertEdge(e); err != nil {
			t.Fatalf("upsert edge %s->%s: %v", e.From, e.To, err)
		}
	}
	return st
}

// callTool is a generic helper that registers a tool via a register function,
// then invokes it with the given arguments.
func callTool(t *testing.T, toolName string, register func(*server.MCPServer), args map[string]any) *mcp.CallToolResult {
	t.Helper()
	srv := server.NewMCPServer("librarian", "test")
	register(srv)

	req := mcp.CallToolRequest{}
	req.Params.Name = toolName
	req.Params.Arguments = args

	tools := srv.ListTools()
	tool, ok := tools[toolName]
	if !ok {
		t.Fatalf("tool %q not registered", toolName)
	}
	result, err := tool.Handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

// TestGraphNeighbors_BothDirections verifies that querying "both" returns all
// incident edges for the node.
func TestGraphNeighbors_BothDirections(t *testing.T) {
	st := setupGraphStore(t)
	result := callTool(t, "graph_neighbors",
		func(s *server.MCPServer) { registerGraphNeighbors(s, st) },
		map[string]any{"node_id": "sym:A"},
	)
	if result.IsError {
		t.Fatalf("unexpected error: %s", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("non-JSON response: %v\n%s", err, text)
	}

	edges, ok := out["edges"].([]any)
	if !ok {
		t.Fatalf("edges field missing or wrong type")
	}
	// sym:A has one incoming (contains from file:a.go) and one outgoing (inherits to sym:B).
	if len(edges) != 2 {
		t.Errorf("expected 2 edges, got %d: %s", len(edges), text)
	}
}

// TestGraphNeighbors_OutDirectionFilter verifies that "out" returns only
// outgoing edges.
func TestGraphNeighbors_OutDirectionFilter(t *testing.T) {
	st := setupGraphStore(t)
	result := callTool(t, "graph_neighbors",
		func(s *server.MCPServer) { registerGraphNeighbors(s, st) },
		map[string]any{"node_id": "sym:A", "direction": "out"},
	)
	if result.IsError {
		t.Fatalf("unexpected error: %s", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("non-JSON response: %v", err)
	}

	edges, ok := out["edges"].([]any)
	if !ok {
		t.Fatalf("edges field missing or wrong type")
	}
	if len(edges) != 1 {
		t.Errorf("expected 1 outgoing edge, got %d: %s", len(edges), text)
	}
	edge, ok := edges[0].(map[string]any)
	if !ok {
		t.Fatalf("edge entry is not a map")
	}
	if edge["kind"] != store.EdgeKindInherits {
		t.Errorf("expected inherits edge, got %q", edge["kind"])
	}
}

// TestGraphNeighbors_EdgeKindFilter verifies that edge_kinds filters edges.
func TestGraphNeighbors_EdgeKindFilter(t *testing.T) {
	st := setupGraphStore(t)
	result := callTool(t, "graph_neighbors",
		func(s *server.MCPServer) { registerGraphNeighbors(s, st) },
		map[string]any{
			"node_id":    "sym:A",
			"edge_kinds": []any{"inherits"},
		},
	)
	if result.IsError {
		t.Fatalf("unexpected error: %s", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	if strings.Contains(text, store.EdgeKindContains) {
		t.Errorf("contains edge should be filtered out: %s", text)
	}
	if !strings.Contains(text, store.EdgeKindInherits) {
		t.Errorf("inherits edge should be present: %s", text)
	}
}

// TestGraphNeighbors_InvalidDirection returns an error for invalid direction.
func TestGraphNeighbors_InvalidDirection(t *testing.T) {
	st := setupGraphStore(t)
	result := callTool(t, "graph_neighbors",
		func(s *server.MCPServer) { registerGraphNeighbors(s, st) },
		map[string]any{"node_id": "sym:A", "direction": "sideways"},
	)
	if !result.IsError {
		t.Fatal("expected error for invalid direction")
	}
}

// TestGraphPath_FindsPath verifies that a path from file:a.go to sym:C exists
// and has 3 hops.
func TestGraphPath_FindsPath(t *testing.T) {
	st := setupGraphStore(t)
	result := callTool(t, "graph_path",
		func(s *server.MCPServer) { registerGraphPath(s, st) },
		map[string]any{"from_id": "file:a.go", "to_id": "sym:C"},
	)
	if result.IsError {
		t.Fatalf("unexpected error: %s", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("non-JSON response: %v\n%s", err, text)
	}

	hops, ok := out["hops"].(float64)
	if !ok {
		t.Fatalf("hops field missing or wrong type")
	}
	if int(hops) != 3 {
		t.Errorf("expected 3 hops, got %v: %s", hops, text)
	}

	steps, ok := out["steps"].([]any)
	if !ok {
		t.Fatalf("steps field missing or wrong type")
	}
	if len(steps) == 3 {
		step0, ok := steps[0].(map[string]any)
		if !ok {
			t.Fatalf("steps[0] is not a map")
		}
		if step0["from"] != "file:a.go" {
			t.Errorf("steps[0].from: got %v, want file:a.go", step0["from"])
		}
		step2, ok := steps[2].(map[string]any)
		if !ok {
			t.Fatalf("steps[2] is not a map")
		}
		if step2["to"] != "sym:C" {
			t.Errorf("steps[2].to: got %v, want sym:C", step2["to"])
		}
	}
}

// TestGraphPath_NoPath returns empty steps when no path exists.
func TestGraphPath_NoPath(t *testing.T) {
	st := setupGraphStore(t)
	// sym:C has no outgoing edges, so there is no path from sym:C to file:a.go.
	result := callTool(t, "graph_path",
		func(s *server.MCPServer) { registerGraphPath(s, st) },
		map[string]any{"from_id": "sym:C", "to_id": "file:a.go"},
	)
	if result.IsError {
		t.Fatalf("unexpected error: %s", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("non-JSON response: %v", err)
	}

	hops, ok := out["hops"].(float64)
	if !ok {
		t.Fatalf("hops field missing or wrong type")
	}
	if int(hops) != 0 {
		t.Errorf("expected 0 hops (no path), got %v", hops)
	}
	steps, ok := out["steps"].([]any)
	if !ok {
		t.Fatalf("steps field missing or wrong type")
	}
	if len(steps) != 0 {
		t.Errorf("expected empty steps, got %v", steps)
	}
}

// TestGraphExplain_ReturnsNodeAndEdges verifies that explain returns node
// metadata and groups edges by kind correctly.
func TestGraphExplain_ReturnsNodeAndEdges(t *testing.T) {
	st := setupGraphStore(t)
	result := callTool(t, "graph_explain",
		func(s *server.MCPServer) { registerGraphExplain(s, st) },
		map[string]any{"node_id": "sym:A"},
	)
	if result.IsError {
		t.Fatalf("unexpected error: %s", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("non-JSON response: %v\n%s", err, text)
	}

	node, ok := out["node"].(map[string]any)
	if !ok {
		t.Fatal("node field missing or wrong type")
	}
	if node["id"] != "sym:A" {
		t.Errorf("expected node id sym:A, got %v", node["id"])
	}

	edgeCount, ok := out["edge_count"].(float64)
	if !ok {
		t.Fatalf("edge_count field missing or wrong type, got: %T", out["edge_count"])
	}
	if int(edgeCount) != 2 {
		t.Errorf("expected edge_count=2, got %v: %s", edgeCount, text)
	}

	byKind, ok := out["neighbors_by_kind"].(map[string]any)
	if !ok {
		t.Fatal("neighbors_by_kind field missing or wrong type")
	}
	if _, ok := byKind[store.EdgeKindContains]; !ok {
		t.Errorf("expected contains entry in neighbors_by_kind")
	}
	if _, ok := byKind[store.EdgeKindInherits]; !ok {
		t.Errorf("expected inherits entry in neighbors_by_kind")
	}
}

// TestGraphExplain_NodeNotFound returns an error for an unknown node ID.
func TestGraphExplain_NodeNotFound(t *testing.T) {
	st := setupGraphStore(t)
	result := callTool(t, "graph_explain",
		func(s *server.MCPServer) { registerGraphExplain(s, st) },
		map[string]any{"node_id": "sym:DoesNotExist"},
	)
	if !result.IsError {
		t.Fatal("expected error for unknown node")
	}
	text := toolResultText(t, result)
	if !strings.Contains(text, "node not found") {
		t.Errorf("error should mention 'node not found', got: %s", text)
	}
}

// TestGraphNeighbors_InDirectionFilter verifies that direction=in returns only
// inbound edges. sym:B has one inbound edge (sym:A --inherits--> sym:B).
func TestGraphNeighbors_InDirectionFilter(t *testing.T) {
	st := setupGraphStore(t)
	result := callTool(t, "graph_neighbors",
		func(s *server.MCPServer) { registerGraphNeighbors(s, st) },
		map[string]any{"node_id": "sym:B", "direction": "in"},
	)
	if result.IsError {
		t.Fatalf("unexpected error: %s", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("non-JSON response: %v", err)
	}

	edges, ok := out["edges"].([]any)
	if !ok {
		t.Fatalf("edges field missing or wrong type")
	}
	if len(edges) != 1 {
		t.Errorf("expected 1 inbound edge for sym:B, got %d: %s", len(edges), text)
	}
	edge, ok := edges[0].(map[string]any)
	if !ok {
		t.Fatalf("edge entry is not a map")
	}
	if edge["kind"] != store.EdgeKindInherits {
		t.Errorf("expected inherits edge, got %q", edge["kind"])
	}
	if edge["from"] != "sym:A" {
		t.Errorf("expected from=sym:A, got %q", edge["from"])
	}
}

// TestGraphPath_MaxDepthExhaustion verifies that a path longer than max_depth
// returns empty steps. The fixture has a 3-hop path file:a.go→sym:A→sym:B→sym:C;
// with max_depth=2 the BFS should not reach sym:C.
func TestGraphPath_MaxDepthExhaustion(t *testing.T) {
	st := setupGraphStore(t)
	result := callTool(t, "graph_path",
		func(s *server.MCPServer) { registerGraphPath(s, st) },
		map[string]any{"from_id": "file:a.go", "to_id": "sym:C", "max_depth": 2},
	)
	if result.IsError {
		t.Fatalf("unexpected error: %s", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("non-JSON response: %v", err)
	}

	hops, ok := out["hops"].(float64)
	if !ok {
		t.Fatalf("hops field missing or wrong type")
	}
	if int(hops) != 0 {
		t.Errorf("expected 0 hops when path exceeds max_depth=2, got %v: %s", hops, text)
	}
	steps, ok := out["steps"].([]any)
	if !ok {
		t.Fatalf("steps field missing or wrong type")
	}
	if len(steps) != 0 {
		t.Errorf("expected empty steps when path exceeds max_depth, got %v", steps)
	}
}

// TestGraphExplain_ClosedStoreError verifies that graph_explain returns a
// proper error (rather than panicking) when the GetNode db call fails.
// The store is closed before invoking the tool so that GetNode returns a
// "database is closed" error and the tool returns IsError=true.
func TestGraphExplain_ClosedStoreError(t *testing.T) {
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "closed.db"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Close without registering t.Cleanup — manual close is the point of the test.
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	result := callTool(t, "graph_explain",
		func(s *server.MCPServer) { registerGraphExplain(s, st) },
		map[string]any{"node_id": "sym:Any"},
	)
	if !result.IsError {
		t.Fatal("expected error when store is closed")
	}
}

// TestGraphExplain_NeighborsError verifies that graph_explain returns a
// "neighbors:" error (rather than panicking) when GetNode succeeds but the
// Neighbors query fails. We drop graph_edges via a raw SQLite connection so
// that graph_nodes (queried by GetNode) still exists but graph_edges
// (queried by Neighbors) is gone.
func TestGraphExplain_NeighborsError(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "explain-neighbors.db")
	st, err := store.Open(dbPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// Insert a node so that GetNode returns a result.
	if err := st.UpsertNode(store.Node{ID: "sym:Target", Kind: store.NodeKindSymbol, Label: "Target"}); err != nil {
		t.Fatalf("upsert node: %v", err)
	}

	// Drop graph_edges via a separate raw connection. graph_nodes is untouched,
	// so GetNode succeeds. Neighbors queries graph_edges, which is gone.
	rawDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := rawDB.Exec("DROP TABLE graph_edges"); err != nil {
		t.Fatalf("drop graph_edges: %v", err)
	}
	rawDB.Close()

	result := callTool(t, "graph_explain",
		func(s *server.MCPServer) { registerGraphExplain(s, st) },
		map[string]any{"node_id": "sym:Target"},
	)
	if !result.IsError {
		t.Fatal("expected error when graph_edges is missing")
	}
	text := toolResultText(t, result)
	if !strings.Contains(text, "neighbors:") {
		t.Errorf("error should come from Neighbors call, got: %s", text)
	}
}

// TestGraphNeighbors_ClosedStoreError verifies that graph_neighbors returns a
// proper error (rather than panicking) when the underlying db call fails.
func TestGraphNeighbors_ClosedStoreError(t *testing.T) {
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "closed.db"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	result := callTool(t, "graph_neighbors",
		func(s *server.MCPServer) { registerGraphNeighbors(s, st) },
		map[string]any{"node_id": "sym:Any"},
	)
	if !result.IsError {
		t.Fatal("expected error when store is closed")
	}
}

// TestGraphPath_ClosedStoreError verifies that graph_path returns a proper
// error (rather than panicking) when the underlying db call fails.
func TestGraphPath_ClosedStoreError(t *testing.T) {
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "closed.db"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	result := callTool(t, "graph_path",
		func(s *server.MCPServer) { registerGraphPath(s, st) },
		map[string]any{"from_id": "sym:A", "to_id": "sym:B"},
	)
	if !result.IsError {
		t.Fatal("expected error when store is closed")
	}
}
