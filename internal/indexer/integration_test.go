package indexer_test

import (
	"os"
	"path/filepath"
	"testing"

	"librarian/internal/config"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults" // register markdown, config, code handlers
	"librarian/internal/store"
)

// fakeEmbedder returns a fixed-dimension zero vector for every input. Good
// enough for integration tests that care about whether the pipeline wires
// end-to-end, not about vector similarity.
type fakeEmbedder struct {
	dim   int
	model string
}

func (f fakeEmbedder) Embed(text string) ([]float64, error) {
	_ = text
	return make([]float64, f.dim), nil
}

// Model satisfies the Embedder interface. Defaults to "fake-embedder" when
// the test didn't set a specific name, so every caller that doesn't care
// about mismatch detection still compiles.
func (f fakeEmbedder) Model() string {
	if f.model == "" {
		return "fake-embedder"
	}
	return f.model
}

// TestIntegration_IndexGoFile exercises the full walker → code handler →
// chunker → store path for a .go file. Covers the code-handler-via-defaults-
// registration path that's otherwise only smoke-tested by unit tests.
func TestIntegration_IndexGoFile(t *testing.T) {
	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goFile := filepath.Join(docsDir, "svc.go")
	contents := []byte(`package svc

// Auth handles authentication.
type Auth struct {
	db string
}

// Validate checks a credential pair.
func (a *Auth) Validate(user, pass string) bool {
	return user == "root"
}
`)
	if err := os.WriteFile(goFile, contents, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cfg := &config.Config{
		DocsDir: docsDir,
		DBPath:  filepath.Join(tmp, "test.db"),
		Chunking: config.ChunkingConfig{
			MaxTokens:    512,
			OverlapLines: 0,
			MinTokens:    1,
		},
		CodeFilePatterns: []string{"*.go"},
	}

	idx := indexer.New(s, cfg, fakeEmbedder{dim: 4})
	result, err := idx.IndexDirectory(docsDir, true)
	if err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}
	if result.DocumentsIndexed != 1 {
		t.Errorf("DocumentsIndexed = %d, want 1", result.DocumentsIndexed)
	}
	if result.ChunksCreated == 0 {
		t.Errorf("ChunksCreated = 0, want > 0; errors: %v", result.Errors)
	}

	docs, err := s.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 stored document, got %d", len(docs))
	}
	stored := docs[0]
	if stored.Title != "svc" {
		t.Errorf("Title = %q, want %q", stored.Title, "svc")
	}
	if stored.DocType != "code" {
		t.Errorf("DocType = %q, want %q", stored.DocType, "code")
	}
	if stored.ChunkCount == 0 {
		t.Errorf("ChunkCount = 0, want > 0")
	}
}
