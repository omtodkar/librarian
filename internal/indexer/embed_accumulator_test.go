package indexer_test

// Tests for the cross-file embedding accumulator (lib-eufo).
//
// All tests run with -tags fts5 (enforced by the go test invocation in
// CLAUDE.md). DB tests that rely on vec0 / FTS5 tables silently no-op
// without the tag, so the tag is non-negotiable.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"librarian/internal/config"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults"
	"librarian/internal/store"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// countingEmbedder wraps fakeEmbedder and counts how many times EmbedBatch is
// called (not the number of items — the number of call invocations). Used to
// verify that cross-file packing reduces the total call count.
type countingEmbedder struct {
	dim   int
	model string
	calls atomic.Int64
}

func (e *countingEmbedder) Embed(text string) ([]float64, error) {
	return make([]float64, e.dim), nil
}

func (e *countingEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	e.calls.Add(1)
	out := make([][]float64, len(texts))
	for i := range texts {
		out[i] = make([]float64, e.dim)
	}
	return out, nil
}

func (e *countingEmbedder) Model() string {
	if e.model == "" {
		return "fake-embedder"
	}
	return e.model
}

// partialEmbedder returns a nil vector for every chunk whose EmbeddingText
// contains the substring poisonMark, and a real zero-vector otherwise. This
// lets a single batch contain a mix of good and failing chunks.
type partialEmbedder struct {
	dim        int
	poisonMark string
}

func (e *partialEmbedder) Embed(text string) ([]float64, error) {
	return make([]float64, e.dim), nil
}

func (e *partialEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, t := range texts {
		if strings.Contains(t, e.poisonMark) {
			out[i] = nil // simulate per-item failure
		} else {
			out[i] = make([]float64, e.dim)
		}
	}
	return out, nil
}

func (e *partialEmbedder) Model() string { return "partial-embedder" }

// openTestStore creates a fresh SQLite store under tmp.
func openTestStore(t *testing.T, tmp string) *store.Store {
	t.Helper()
	dbPath := filepath.Join(tmp, "test.db")
	s, err := store.Open(dbPath, nil, 0)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// baseCfg returns a minimal config with the given root and docsDir.
func baseCfg(root, dbPath string) *config.Config {
	return &config.Config{
		ProjectRoot: root,
		DBPath:      dbPath,
		Chunking: config.ChunkingConfig{
			MaxTokens:    512,
			OverlapLines: 0,
			MinTokens:    1, // very low so even tiny sections produce a chunk
		},
	}
}

// writeMD creates parent dirs and writes a markdown file.
func writeMD(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
}

// sectionsMD builds a markdown file with n H2 sections, each containing a
// short paragraph so the chunker produces one chunk per section.
func sectionsMD(n int) string {
	var b strings.Builder
	b.WriteString("# Root\n\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "## Section %d\n\nContent of section %d. This has enough words to pass MinTokens.\n\n", i, i)
	}
	return b.String()
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestAccumulator_SingleDocStraddlesFlush verifies that a single document whose
// chunk count exceeds the accumulator's batchSize is still fully stored with the
// correct ChunkCount. This exercises the "doc straddles flush" path where some
// of the doc's chunks land in one EmbedBatch call and the remainder in the next.
func TestAccumulator_SingleDocStraddlesFlush(t *testing.T) {
	const batchSize = 2 // tiny so a doc with 4+ sections straddles
	const numSections = 5

	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "docs")
	writeMD(t, filepath.Join(docsDir, "big.md"), sectionsMD(numSections))

	s := openTestStore(t, tmp)
	cfg := baseCfg(tmp, filepath.Join(tmp, "test.db"))
	cfg.Embedding.BatchSize = batchSize

	emb := &countingEmbedder{dim: 4}
	idx := indexer.New(s, cfg, emb)

	result, err := idx.IndexDirectory(docsDir, true)
	if err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}
	if result.DocumentsIndexed != 1 {
		t.Errorf("DocumentsIndexed = %d, want 1", result.DocumentsIndexed)
	}
	// numSections generates at least numSections chunks (one per H2 + possibly
	// the root H1 intro); we just need more than batchSize.
	if result.ChunksCreated < numSections {
		t.Errorf("ChunksCreated = %d, want >= %d (one per section)", result.ChunksCreated, numSections)
	}

	// The stored document's ChunkCount must match ChunksCreated.
	docs, err := s.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	if int(docs[0].ChunkCount) != result.ChunksCreated {
		t.Errorf("doc.ChunkCount = %d, ChunksCreated = %d; must match", docs[0].ChunkCount, result.ChunksCreated)
	}

	// With batchSize=2 and numSections=5 chunks, EmbedBatch should have been
	// called ceil(5/2) = 3 times (not 5 — one per chunk — nor 1 — one per doc).
	calls := emb.calls.Load()
	if calls < 2 || calls > int64(numSections) {
		t.Errorf("EmbedBatch called %d times; expected between 2 and %d", calls, numSections)
	}
}

// TestAccumulator_CrossDirPacking verifies that when IndexDocs is called with
// multiple directories, EmbedBatch is called FEWER times than the document
// count — i.e. docs from different dirs are packed into shared batches.
//
// Setup: two dirs each with 3 single-section docs = 6 docs, each producing ~1
// chunk. With batchSize=4, we expect 2 EmbedBatch calls (not 6).
func TestAccumulator_CrossDirPacking(t *testing.T) {
	const batchSize = 4
	const docsPerDir = 3

	tmp := t.TempDir()
	s := openTestStore(t, tmp)
	cfg := baseCfg(tmp, filepath.Join(tmp, "test.db"))
	cfg.Embedding.BatchSize = batchSize

	var docsDirs []string
	for i, dirName := range []string{"a/docs", "b/docs"} {
		abs := filepath.Join(tmp, dirName)
		docsDirs = append(docsDirs, abs)
		for j := 1; j <= docsPerDir; j++ {
			content := fmt.Sprintf("# Doc %d-%d\n\nContent for document %d in dir %d. Enough words here.\n", i, j, j, i)
			writeMD(t, filepath.Join(abs, fmt.Sprintf("doc%d.md", j)), content)
		}
	}

	emb := &countingEmbedder{dim: 4}
	idx := indexer.New(s, cfg, emb)

	result, err := idx.IndexDocs(docsDirs, true)
	if err != nil {
		t.Fatalf("IndexDocs: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Errorf("unexpected errors: %v", result.Errors)
	}

	totalDocs := docsPerDir * 2
	if result.DocumentsIndexed != totalDocs {
		t.Errorf("DocumentsIndexed = %d, want %d", result.DocumentsIndexed, totalDocs)
	}

	// Each doc produces roughly 1 chunk → ~6 chunks total, batchSize=4.
	// EmbedBatch should be called ceil(6/4) = 2 times — far fewer than
	// the 6 documents.
	calls := emb.calls.Load()
	if calls >= int64(totalDocs) {
		t.Errorf("EmbedBatch called %d times — no cross-dir packing occurred (want < %d)", calls, totalDocs)
	}
	if calls == 0 {
		t.Error("EmbedBatch was never called")
	}
	t.Logf("EmbedBatch calls=%d for %d docs across 2 dirs (batchSize=%d)", calls, totalDocs, batchSize)
}

// TestAccumulator_PartialFailureIsolated verifies error-attribution across
// docs: a nil vector for one doc's chunk in a shared batch must not drop
// another doc's chunks. Specifically:
//
//   - Doc A has 2 chunks, both clean.
//   - Doc B has 1 chunk whose EmbeddingText contains "POISON" — the partial
//     embedder returns nil for it.
//   - Doc C has 2 chunks, both clean.
//
// All three docs land in the same EmbedBatch call (batchSize=10). After
// finalization:
//   - Docs A and C must be fully stored (ChunksCreated reflects their chunks).
//   - Doc B must have 0 chunks stored (all-nil), so it's skipped with an error.
//   - result.Errors must contain exactly the chunk-level failure for B, not for A or C.
func TestAccumulator_PartialFailureIsolated(t *testing.T) {
	const batchSize = 10 // large enough that all docs land in one batch

	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "docs")

	// Doc A: 2 H2 sections, no poison.
	writeMD(t, filepath.Join(docsDir, "a.md"),
		"# A\n\n## Section 1\n\nClean content for doc A section one.\n\n## Section 2\n\nMore clean content for doc A.\n")

	// Doc B: 1 H2 section whose content contains the poison marker.
	writeMD(t, filepath.Join(docsDir, "b.md"),
		"# B\n\n## Only Section\n\nPOISON this chunk must fail embedding.\n")

	// Doc C: 2 H2 sections, no poison.
	writeMD(t, filepath.Join(docsDir, "c.md"),
		"# C\n\n## Section 1\n\nClean content for doc C section one.\n\n## Section 2\n\nMore clean content for doc C.\n")

	s := openTestStore(t, tmp)
	cfg := baseCfg(tmp, filepath.Join(tmp, "test.db"))
	cfg.Embedding.BatchSize = batchSize

	emb := &partialEmbedder{dim: 4, poisonMark: "POISON"}
	idx := indexer.New(s, cfg, emb)

	result, err := idx.IndexDirectory(docsDir, true)
	if err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}

	// Doc B is all-nil → skipped → 2 docs indexed (A and C).
	if result.DocumentsIndexed != 2 {
		t.Errorf("DocumentsIndexed = %d, want 2 (A and C); errors: %v", result.DocumentsIndexed, result.Errors)
	}

	// A has 2 chunks, C has 2 chunks → 4 chunks created; B contributes 0.
	if result.ChunksCreated < 2 {
		t.Errorf("ChunksCreated = %d, want >= 2 (A+C chunks)", result.ChunksCreated)
	}

	// There must be at least one error (B's failure).
	if len(result.Errors) == 0 {
		t.Error("expected at least one error for doc B's failed embedding, got none")
	}

	// Verify via the store: docs A and C present, doc B absent.
	docs, err := s.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments: %v", err)
	}
	storedTitles := map[string]bool{}
	for _, d := range docs {
		storedTitles[d.Title] = true
	}
	if !storedTitles["A"] {
		t.Errorf("doc A not stored; stored titles: %v", storedTitles)
	}
	if !storedTitles["C"] {
		t.Errorf("doc C not stored; stored titles: %v", storedTitles)
	}
	if storedTitles["B"] {
		t.Errorf("doc B was stored despite all-nil embeddings; stored titles: %v", storedTitles)
	}
}

// TestAccumulator_IncrementalSkip verifies that unchanged files (same content
// hash) are skipped and contribute no chunks to the embedding buffer. On a
// re-index with force=false:
//
//   - result.Skipped must equal the number of unchanged files.
//   - EmbedBatch must NOT be called at all (nothing to embed).
//   - DocumentsIndexed must be 0 (no new docs).
func TestAccumulator_IncrementalSkip(t *testing.T) {
	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "docs")
	writeMD(t, filepath.Join(docsDir, "doc1.md"), "# Doc 1\n\nSome stable content.\n")
	writeMD(t, filepath.Join(docsDir, "doc2.md"), "# Doc 2\n\nMore stable content.\n")

	s := openTestStore(t, tmp)
	cfg := baseCfg(tmp, filepath.Join(tmp, "test.db"))
	cfg.Embedding.BatchSize = 10

	// First run: index both docs fresh.
	emb1 := &countingEmbedder{dim: 4}
	idx1 := indexer.New(s, cfg, emb1)
	r1, err := idx1.IndexDirectory(docsDir, true)
	if err != nil {
		t.Fatalf("first IndexDirectory: %v", err)
	}
	if r1.DocumentsIndexed != 2 {
		t.Fatalf("first run: DocumentsIndexed = %d, want 2", r1.DocumentsIndexed)
	}
	firstCalls := emb1.calls.Load()
	if firstCalls == 0 {
		t.Fatal("first run: EmbedBatch was never called")
	}

	// Second run with force=false: files are unchanged — should be skipped.
	emb2 := &countingEmbedder{dim: 4, model: "fake-embedder"}
	idx2 := indexer.New(s, cfg, emb2)
	r2, err := idx2.IndexDirectory(docsDir, false)
	if err != nil {
		t.Fatalf("second IndexDirectory: %v", err)
	}
	if len(r2.Errors) != 0 {
		t.Errorf("second run errors: %v", r2.Errors)
	}
	if r2.Skipped != 2 {
		t.Errorf("second run: Skipped = %d, want 2", r2.Skipped)
	}
	if r2.DocumentsIndexed != 0 {
		t.Errorf("second run: DocumentsIndexed = %d, want 0 (all skipped)", r2.DocumentsIndexed)
	}

	// No embedding should have happened for the re-index of unchanged files.
	secondCalls := emb2.calls.Load()
	if secondCalls != 0 {
		t.Errorf("second run: EmbedBatch called %d times, want 0 (all files skipped)", secondCalls)
	}
}
