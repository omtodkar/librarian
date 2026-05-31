package indexer_test

import (
	"os"
	"path/filepath"
	"testing"

	"librarian/internal/config"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults" // register markdown handler
	"librarian/internal/store"
)

// TestIndexDocs_MultiDir_DistinctPaths verifies the core correctness guarantee
// of the multi-doc-dirs feature (lib-fka.2):
//
//  1. Two sibling docs dirs each containing a file with the same basename
//     (DOCTRINE.md) can be indexed without triggering a UNIQUE-constraint error
//     on documents.file_path.
//  2. The stored file_paths are ProjectRoot-relative and forward-slash
//     normalised (e.g. "a/docs/DOCTRINE.md" and "b/docs/DOCTRINE.md").
//  3. Both documents are retrievable from the store after indexing.
//  4. The aggregated IndexResult reflects both documents.
func TestIndexDocs_MultiDir_DistinctPaths(t *testing.T) {
	// Workspace layout:
	//   <root>/
	//     a/docs/DOCTRINE.md
	//     b/docs/DOCTRINE.md
	//     .librarian/test.db
	root := t.TempDir()

	dirs := []struct {
		rel     string // directory relative to root
		content string
	}{
		{"a/docs", "# Doctrine A\n\nA's doctrine content.\n"},
		{"b/docs", "# Doctrine B\n\nB's doctrine content.\n"},
	}

	for _, d := range dirs {
		abs := filepath.Join(root, d.rel)
		if err := os.MkdirAll(abs, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		docFile := filepath.Join(abs, "DOCTRINE.md")
		if err := os.WriteFile(docFile, []byte(d.content), 0o644); err != nil {
			t.Fatalf("write %s: %v", docFile, err)
		}
	}

	dbPath := filepath.Join(root, ".librarian", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cfg := &config.Config{
		ProjectRoot: root,
		DBPath:      dbPath,
		Chunking: config.ChunkingConfig{
			MaxTokens:    512,
			OverlapLines: 0,
			MinTokens:    1,
		},
	}

	idx := indexer.New(s, cfg, fakeEmbedder{dim: 4})

	// Build the list of absolute docs dirs.
	docsDirs := []string{
		filepath.Join(root, "a", "docs"),
		filepath.Join(root, "b", "docs"),
	}

	result, err := idx.IndexDocs(docsDirs, true)
	if err != nil {
		t.Fatalf("IndexDocs: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Errorf("IndexDocs returned errors: %v", result.Errors)
	}

	// Both documents must have been indexed.
	if result.DocumentsIndexed != 2 {
		t.Errorf("DocumentsIndexed = %d, want 2", result.DocumentsIndexed)
	}
	if result.ChunksCreated == 0 {
		t.Errorf("ChunksCreated = 0, want > 0 (embedding produced no chunks)")
	}

	// Retrieve all stored documents and assert on their file_paths.
	docs, err := s.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 stored documents, got %d: %+v", len(docs), docs)
	}

	// Build a set of stored paths for assertion.
	storedPaths := map[string]bool{}
	for _, d := range docs {
		storedPaths[d.FilePath] = true
	}

	// Paths must be ProjectRoot-relative and forward-slash normalised.
	const wantA = "a/docs/DOCTRINE.md"
	const wantB = "b/docs/DOCTRINE.md"

	if !storedPaths[wantA] {
		t.Errorf("expected stored path %q, got paths: %v", wantA, storedPaths)
	}
	if !storedPaths[wantB] {
		t.Errorf("expected stored path %q, got paths: %v", wantB, storedPaths)
	}

	// Paths must be distinct (no collision).
	if storedPaths[wantA] && storedPaths[wantB] && wantA == wantB {
		t.Error("stored paths are identical — UNIQUE constraint would have fired")
	}

	// Both documents must be retrievable by their stored path.
	docA, err := s.GetDocumentByPath(wantA)
	if err != nil || docA == nil {
		t.Errorf("GetDocumentByPath(%q): doc=%v err=%v", wantA, docA, err)
	}
	docB, err := s.GetDocumentByPath(wantB)
	if err != nil || docB == nil {
		t.Errorf("GetDocumentByPath(%q): doc=%v err=%v", wantB, docB, err)
	}
}
