package indexer

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"librarian/internal/config"
	"librarian/internal/store"
)

// newTestEdgesIndexer builds a minimal Indexer + Store pair for internal
// buildTestEdges tests. Uses a file-backed SQLite DB so the raw-connection
// sabotage helpers can operate on the same file.
func newTestEdgesIndexer(t *testing.T) (*Indexer, *store.Store, string) {
	t.Helper()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, ".librarian", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cfg := &config.Config{
		ProjectRoot: tmp,
		Graph: config.GraphConfig{
			TestEdges: config.TestEdgesConfig{Enabled: true},
		},
	}
	return NewWithRegistry(s, cfg, nil, DefaultRegistry()), s, dbPath
}

// TestBuildTestEdges_ListNodesByKindError verifies that a ListNodesByKind
// failure is appended to result.Errors and does not panic.
func TestBuildTestEdges_ListNodesByKindError(t *testing.T) {
	idx, s, _ := newTestEdgesIndexer(t)
	// Close the store so every DB call fails with "sql: database is closed".
	s.Close()

	result := &GraphResult{}
	idx.buildTestEdges(result) // must not panic
	if len(result.Errors) == 0 {
		t.Error("expected at least one error from closed DB, got none")
	}
}

// TestBuildTestEdges_UpsertEdgeError verifies that a UpsertEdge failure is
// appended to result.Errors, does not panic, and does not increment
// EdgesAdded for the failed edge.
//
// Technique: seed graph_nodes via a fully-open store, then sabotage the DB
// by dropping graph_edges via a raw sql.DB connection (the store's connection
// is kept open so ListNodesByKind still works). When buildTestEdges attempts
// UpsertEdge it gets "no such table: graph_edges" and appends to result.Errors.
func TestBuildTestEdges_UpsertEdgeError(t *testing.T) {
	idx, _, dbPath := newTestEdgesIndexer(t)

	// Seed: upsert a test file node and its SUT node so testSubjectLinker
	// returns a candidate when buildTestEdges runs.
	if err := idx.store.UpsertNode(store.Node{
		ID: store.CodeFileNodeID("pkg/auth_test.go"), Kind: store.NodeKindCodeFile,
		Label: "pkg/auth_test.go", SourcePath: "pkg/auth_test.go",
	}); err != nil {
		t.Fatalf("seed test file node: %v", err)
	}
	if err := idx.store.UpsertNode(store.Node{
		ID: store.CodeFileNodeID("pkg/auth.go"), Kind: store.NodeKindCodeFile,
		Label: "pkg/auth.go", SourcePath: "pkg/auth.go",
	}); err != nil {
		t.Fatalf("seed SUT node: %v", err)
	}

	// Sabotage: open a second raw connection to the same file and drop
	// graph_edges so UpsertEdge will fail while graph_nodes remains intact.
	// The store package already registered the sqlite3 driver; no extra import needed.
	rawDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := rawDB.Exec("DROP TABLE graph_edges"); err != nil {
		t.Fatalf("drop graph_edges: %v", err)
	}
	rawDB.Close()

	result := &GraphResult{}
	idx.buildTestEdges(result) // must not panic

	if len(result.Errors) == 0 {
		t.Error("expected at least one error from dropped graph_edges table, got none")
	}
	if result.EdgesAdded != 0 {
		t.Errorf("want EdgesAdded=0 after UpsertEdge failure, got %d", result.EdgesAdded)
	}
}
