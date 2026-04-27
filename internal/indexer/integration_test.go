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

// EmbedBatch returns one zero-vector per input. Only the length and order
// contract is exercised by the indexer; vector content doesn't matter for
// wiring tests.
func (f fakeEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i := range texts {
		out[i] = make([]float64, f.dim)
	}
	return out, nil
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

// nilEmbedder returns all-nil vectors to simulate per-item fallback failure.
type nilEmbedder struct{}

func (nilEmbedder) Embed(text string) ([]float64, error) { return nil, nil }
func (nilEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	return make([][]float64, len(texts)), nil // all slots nil
}
func (nilEmbedder) Model() string { return "nil-embedder" }

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

	s, err := store.Open(filepath.Join(tmp, "test.db"), nil, 0)
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

// TestIntegration_AllEmbeddingsFailNoDocInserted verifies that when all chunk
// embeddings return nil (per-item fallback all failed), the document row is
// NOT inserted and ChunkCount stays at zero.
func TestIntegration_AllEmbeddingsFailNoDocInserted(t *testing.T) {
	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mdFile := filepath.Join(docsDir, "guide.md")
	if err := os.WriteFile(mdFile, []byte("# Guide\n\nSome content here.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := store.Open(filepath.Join(tmp, "test.db"), nil, 0)
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
	}

	idx := indexer.New(s, cfg, nilEmbedder{})
	result, err := idx.IndexDirectory(docsDir, true)
	if err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}
	// No document should be stored when all embeddings failed.
	docs, err := s.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected 0 stored documents (all embeddings nil), got %d", len(docs))
	}
	// The failure should be reported as an error, not a successful index.
	if result.DocumentsIndexed != 0 {
		t.Errorf("DocumentsIndexed = %d, want 0 (all embeddings failed)", result.DocumentsIndexed)
	}
	if len(result.Errors) == 0 {
		t.Error("expected at least one error in result when all embeddings fail")
	}
}
