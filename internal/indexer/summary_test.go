package indexer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"librarian/internal/config"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults"
	"librarian/internal/store"
	"librarian/internal/summarizer"
)

// countingSummarizer wraps a fixed summary string and records how many
// times Summarize was called. Used to verify cache-hit behaviour.
type countingSummarizer struct {
	summary string
	calls   int
}

func (c *countingSummarizer) Summarize(_ string) (string, error) {
	c.calls++
	return c.summary, nil
}

// TestSummarization_CacheHitOnSecondRun indexes the same content twice and
// asserts that the summarizer is only called during the first run. On the
// second run the cache should satisfy every lookup.
func TestSummarization_CacheHitOnSecondRun(t *testing.T) {
	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const mdContent = `# Auth Guide

## Login Flow

The login flow uses OAuth 2.0 with PKCE. The client redirects to the
authorization server, receives a code, and exchanges it for tokens.

## Token Storage

Tokens are stored in an encrypted local store. Access tokens expire
after one hour; refresh tokens rotate on each use.
`
	if err := os.WriteFile(filepath.Join(docsDir, "auth.md"), []byte(mdContent), 0o644); err != nil {
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

	fakeSummarizer := &countingSummarizer{summary: "Short summary."}

	// First run: every chunk is a cache miss → summarizer called.
	idx1 := indexer.New(s, cfg, fakeEmbedder{dim: 4})
	idx1.SetSummarizer(fakeSummarizer)
	result1, err := idx1.IndexDirectory(docsDir, true)
	if err != nil {
		t.Fatalf("first IndexDirectory: %v", err)
	}
	if result1.ChunksCreated == 0 {
		t.Fatalf("expected chunks on first run; errors: %v", result1.Errors)
	}
	callsAfterFirst := fakeSummarizer.calls
	if callsAfterFirst == 0 {
		t.Errorf("expected summarizer calls on first run, got 0")
	}

	// Second run with force=true: content unchanged → all cache hits → 0
	// new summarizer calls.
	fakeSummarizer.calls = 0
	idx2 := indexer.New(s, cfg, fakeEmbedder{dim: 4})
	idx2.SetSummarizer(fakeSummarizer)
	result2, err := idx2.IndexDirectory(docsDir, true)
	if err != nil {
		t.Fatalf("second IndexDirectory: %v", err)
	}
	if result2.ChunksCreated == 0 {
		t.Fatalf("expected chunks on second run; errors: %v", result2.Errors)
	}
	if fakeSummarizer.calls != 0 {
		t.Errorf("second run: expected 0 summarizer calls (all cache hits), got %d", fakeSummarizer.calls)
	}
}

// TestSummarization_SummaryStoredAndShorterThanBody indexes a markdown fixture
// with a fake summarizer that returns a known short summary, then queries the
// stored chunks and verifies the summary field is populated and shorter than
// the content body.
func TestSummarization_SummaryStoredAndShorterThanBody(t *testing.T) {
	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Fixture with a long body so the summary is clearly shorter.
	longBody := strings.Repeat("This is a detailed explanation of how the authentication system works. ", 20)
	mdContent := "# Guide\n\n## Section\n\n" + longBody

	if err := os.WriteFile(filepath.Join(docsDir, "guide.md"), []byte(mdContent), 0o644); err != nil {
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

	const shortSummary = "Authentication system overview."
	fakeSummarizer := &countingSummarizer{summary: shortSummary}

	idx := indexer.New(s, cfg, fakeEmbedder{dim: 4})
	idx.SetSummarizer(fakeSummarizer)
	result, err := idx.IndexDirectory(docsDir, true)
	if err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}
	if result.ChunksCreated == 0 {
		t.Fatalf("no chunks created; errors: %v", result.Errors)
	}

	docs, err := s.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}

	chunks, err := s.GetChunksForDocument(docs[0].ID)
	if err != nil {
		t.Fatalf("GetChunksForDocument: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	for _, chunk := range chunks {
		// Assert the fake summarizer's exact output is stored verbatim.
		if chunk.Summary != shortSummary {
			t.Errorf("chunk %s: summary = %q, want %q", chunk.ID, chunk.Summary, shortSummary)
			continue
		}
		// The mock output is much shorter than the body — assert the ratio.
		// This catches any regression where summary and content are swapped.
		summaryWords := len(strings.Fields(chunk.Summary))
		bodyWords := len(strings.Fields(chunk.Content))
		if bodyWords > 0 && summaryWords*5 > bodyWords {
			t.Errorf("chunk %s: summary (%d words) is more than 20%% of body (%d words)",
				chunk.ID, summaryWords, bodyWords)
		}
	}

	// Verify summary flows through the vector-search path (same code path as
	// the search_docs MCP handler). SearchChunks must return Summary populated.
	zeroVec := make([]float64, 4)
	searchResults, err := s.SearchChunks("", zeroVec, 10)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(searchResults) == 0 {
		t.Fatal("SearchChunks returned no results")
	}
	for _, c := range searchResults {
		if c.Summary != shortSummary {
			t.Errorf("SearchChunks result %s: summary = %q, want %q", c.ID, c.Summary, shortSummary)
		}
	}
}

// TestSummarization_Noop verifies that the default Noop summarizer leaves
// summary fields empty — no API calls, no errors.
func TestSummarization_Noop(t *testing.T) {
	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(docsDir, "simple.md"), []byte("# Title\n\n## Sec\n\nSome text here.\n"), 0o644); err != nil {
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

	// Use default Noop (no SetSummarizer call).
	idx := indexer.New(s, cfg, fakeEmbedder{dim: 4})
	idx.SetSummarizer(summarizer.Noop{})
	result, err := idx.IndexDirectory(docsDir, true)
	if err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}
	if result.ChunksCreated == 0 {
		t.Fatalf("no chunks; errors: %v", result.Errors)
	}

	docs, err := s.ListDocuments()
	if err != nil {
		t.Fatal(err)
	}
	chunks, err := s.GetChunksForDocument(docs[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	for _, chunk := range chunks {
		if chunk.Summary != "" {
			t.Errorf("Noop: chunk %s has non-empty summary %q", chunk.ID, chunk.Summary)
		}
	}
}
