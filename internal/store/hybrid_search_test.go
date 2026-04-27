package store

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// TestHybridSearch_LiteralBeatsVectorOnlyResults verifies the core acceptance
// criterion: a chunk containing an exact literal match surfaces at rank #1 in
// hybrid results even when it falls last by vector similarity alone.
//
// Setup: 12 chunks where the first 11 have vectors progressively rotating
// away from the query vector and the 12th has an orthogonal vector (worst
// cosine distance) but contains the literal token "computeHash".
// With limit=4, fetchLimit=max(12,10)=12 — all chunks are fetched by the
// vector pass, so the literal chunk is rank 12 in vector-only ordering.
// Its RRF score from BM25 (rank 1) plus its vector contribution (rank 12)
// yields a total RRF higher than any pure-vector chunk, lifting it to #1.
func TestHybridSearch_LiteralBeatsVectorOnlyResults(t *testing.T) {
	s := newTestStore(t)
	const model = "test-model"
	const dim = 4
	const limit = 4

	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/hybrid.md", Title: "Hybrid"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	// queryVec points along the first axis.
	queryVec := []float64{1, 0, 0, 0}

	// Chunks 0-10: vectors rotate from near-query toward orthogonal; none
	// contain "computeHash". Higher index = worse vector match.
	for i := 0; i < 11; i++ {
		angle := float64(i+1) * 0.08
		vec := []float64{math.Cos(angle), math.Sin(angle), 0, 0}
		_, err := s.AddChunk(AddChunkInput{
			Vector:     vec,
			Content:    fmt.Sprintf("unrelated topic content item number %d in the index", i),
			FilePath:   doc.FilePath,
			ChunkIndex: uint32(i),
			DocID:      doc.ID,
			Model:      model,
		})
		if err != nil {
			t.Fatalf("AddChunk %d: %v", i, err)
		}
	}

	// Chunk 11: orthogonal vector (worst cosine match) + exact literal "computeHash".
	_, err = s.AddChunk(AddChunkInput{
		Vector:     []float64{0, 0, 0, 1},
		Content:    "the computeHash function computes SHA256 digests over input bytes",
		FilePath:   doc.FilePath,
		ChunkIndex: 11,
		DocID:      doc.ID,
		Model:      model,
	})
	if err != nil {
		t.Fatalf("AddChunk literal: %v", err)
	}

	// Vector-only: the literal chunk scores 0 (1-distance = 1-1.0 = 0) and must
	// not appear in the top-4.
	vecOnly, err := s.SearchChunks(queryVec, limit)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	for _, c := range vecOnly {
		if strings.Contains(c.Content, "computeHash") {
			t.Error("literal chunk must not appear in vector-only top-4 (ranks last by cosine distance)")
		}
	}

	// Hybrid: BM25 gives the literal chunk FTS rank-1. Combined RRF score
	// (FTS rank-1 + vector rank-12) exceeds any pure-vector chunk (vector
	// rank-N, no FTS contribution). The literal chunk must be ranked #1.
	hybrid, err := s.HybridSearch(queryVec, "computeHash", limit)
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(hybrid) == 0 {
		t.Fatal("HybridSearch returned 0 results")
	}
	// Strongest assertion: literal chunk must be rank #1.
	if !strings.Contains(hybrid[0].Content, "computeHash") {
		t.Errorf("hybrid[0] must be the literal-match chunk; got: %q", hybrid[0].Content)
	}
}

// TestHybridSearch_FTSTriggerSyncsOnInsert verifies that the migration-0002
// INSERT trigger keeps doc_chunks_fts in sync: after AddChunk the FTS index
// must contain the chunk's content and match the inserted token.
func TestHybridSearch_FTSTriggerSyncsOnInsert(t *testing.T) {
	s := newTestStore(t)
	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/trigger.md", Title: "Trigger"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	const want = "computeHash is available for use"
	_, err = s.AddChunk(AddChunkInput{
		Vector:     []float64{1, 0, 0, 0},
		Content:    want,
		FilePath:   doc.FilePath,
		ChunkIndex: 0,
		DocID:      doc.ID,
		Model:      "m",
	})
	if err != nil {
		t.Fatalf("AddChunk: %v", err)
	}

	var got string
	if err := s.db.QueryRow(
		`SELECT c.content FROM doc_chunks_fts f
		 JOIN doc_chunks c ON c.id = f.rowid
		 WHERE doc_chunks_fts MATCH '"computeHash"'`,
	).Scan(&got); err != nil {
		t.Fatalf("FTS query: %v", err)
	}
	if got != want {
		t.Errorf("FTS content mismatch: got %q want %q", got, want)
	}
}

// TestHybridSearch_FTSTriggerSyncsOnDelete verifies the DELETE trigger: after
// DeleteDocument, the FTS index must no longer contain the deleted chunk's content.
func TestHybridSearch_FTSTriggerSyncsOnDelete(t *testing.T) {
	s := newTestStore(t)
	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/del.md", Title: "Del"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	_, err = s.AddChunk(AddChunkInput{
		Vector:     []float64{1, 0, 0, 0},
		Content:    "uniqueTokenXYZ123 appears only here",
		FilePath:   doc.FilePath,
		ChunkIndex: 0,
		DocID:      doc.ID,
		Model:      "m",
	})
	if err != nil {
		t.Fatalf("AddChunk: %v", err)
	}

	if err := s.DeleteDocument(doc.ID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}

	var count int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM doc_chunks_fts WHERE doc_chunks_fts MATCH '"uniqueTokenXYZ123"'`,
	).Scan(&count); err != nil {
		t.Fatalf("FTS query after delete: %v", err)
	}
	if count != 0 {
		t.Errorf("FTS must not match deleted chunk content, got %d matches", count)
	}
}

// TestHybridSearch_FTSTriggerSyncsOnUpdate verifies the UPDATE trigger: after
// updating a chunk's content via SQL, the FTS index must reflect the new text
// and no longer match the old text.
func TestHybridSearch_FTSTriggerSyncsOnUpdate(t *testing.T) {
	s := newTestStore(t)
	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/upd.md", Title: "Upd"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	_, err = s.AddChunk(AddChunkInput{
		Vector:     []float64{1, 0, 0, 0},
		Content:    "uniqueOldTermQQQ defined here",
		FilePath:   doc.FilePath,
		ChunkIndex: 0,
		DocID:      doc.ID,
		Model:      "m",
	})
	if err != nil {
		t.Fatalf("AddChunk: %v", err)
	}

	// Direct SQL update (no public API — tests the trigger, not a user-facing op).
	if _, err := s.db.Exec(
		`UPDATE doc_chunks SET content = 'uniqueNewTermRRR defined here'
		 WHERE content = 'uniqueOldTermQQQ defined here'`,
	); err != nil {
		t.Fatalf("UPDATE doc_chunks: %v", err)
	}

	var oldCount, newCount int
	_ = s.db.QueryRow(
		`SELECT count(*) FROM doc_chunks_fts WHERE doc_chunks_fts MATCH '"uniqueOldTermQQQ"'`,
	).Scan(&oldCount)
	_ = s.db.QueryRow(
		`SELECT count(*) FROM doc_chunks_fts WHERE doc_chunks_fts MATCH '"uniqueNewTermRRR"'`,
	).Scan(&newCount)

	if oldCount != 0 {
		t.Errorf("FTS must not match old content after UPDATE, got %d matches", oldCount)
	}
	if newCount != 1 {
		t.Errorf("FTS must match new content after UPDATE, got %d matches", newCount)
	}
}

// TestHybridSearch_CorpusIntegration runs 3 hand-picked queries over a
// realistic mini-corpus drawn from librarian's own identifiers. For each
// query the literal-match chunk has the worst cosine distance but must rank
// #1 in hybrid results, confirming end-to-end RRF behaviour with content
// that mirrors the real use case the task was designed to fix.
//
// Design: 11 base chunks have DISTINCT cosine distances from the query vector
// (angle-rotated, ensuring deterministic vector ordering). 3 target chunks
// are orthogonal to the query vector (distance=1, worst possible). With
// limit=5 and fetchLimit=max(15,10)=15, all 14 chunks are fetched by the
// vector pass. The vector-only top-5 includes only base chunks (score > 0).
// In hybrid, BM25 gives each target rank-1 for its own query; combined RRF
// (FTS rank-1 + vector rank-12/13/14) exceeds any pure-vector chunk, so the
// target ranks #1.
func TestHybridSearch_CorpusIntegration(t *testing.T) {
	s := newTestStore(t)
	const model = "test-model"
	const limit = 5 // fetchLimit = max(15,10) = 15 > 14 total → all fetched

	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/corpus.md", Title: "Corpus"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	// queryVec = [1, 0, 0, 0]. Base chunks use angle-rotated vectors so each
	// has a strictly smaller cosine distance than the orthogonal targets.
	queryVec := []float64{1, 0, 0, 0}

	// 11 base chunks: angle from 0 to 0.80 rad, cosine distance 0..0.303.
	// None contain the target identifiers.
	baseContents := []string{
		"SearchChunks runs vector KNN search over the doc_chunk_vectors table using sqlite-vec",
		"AddDocument inserts a new documents row with a UUID primary key",
		"EmbedBatch sends texts to the embedding provider and returns float64 vectors",
		"GetDocumentByPath looks up a document row by its file_path column",
		"DeleteDocument removes chunks, vectors, and graph nodes in one transaction",
		"UpsertNode writes a graph node with the given kind, label, and source_path",
		"GetReferencedCodeFiles joins refs to code_files for a document ID",
		"ClearVectorState drops the vec0 table and resets embedding_meta for reindex",
		"WriteEmbeddingMeta upserts the model name and embedding dimension",
		"ListDocuments returns all documents ordered by file_path ascending",
		"GetChunksForDocument returns chunk rows for a doc_id ordered by chunk_index",
	}
	for i, content := range baseContents {
		angle := float64(i) * 0.08 // 0, 0.08, ..., 0.80 radians
		vec := []float64{math.Cos(angle), math.Sin(angle), 0, 0}
		if _, err := s.AddChunk(AddChunkInput{
			Vector: vec, Content: content,
			FilePath: doc.FilePath, ChunkIndex: uint32(i), DocID: doc.ID, Model: model,
		}); err != nil {
			t.Fatalf("AddChunk base[%d]: %v", i, err)
		}
	}

	// 3 target chunks: orthogonal to queryVec (distance=1). Inserted after
	// base chunks so they hold the highest rowids (→ highest vector ranks when
	// distances tie at 1.0). Each contains a unique identifier not in any base.
	targets := []string{
		"computeHash generates a SHA256 hex digest for incremental indexing content changes",
		"buildFTSQuery sanitizes user input and emits an FTS5 OR-token expression for BM25",
		"rerankWithSignals blends a 0.9 vectorScore with a 0.1 metadata boost for final rank",
	}
	for i, content := range targets {
		if _, err := s.AddChunk(AddChunkInput{
			Vector: []float64{0, 0, 1, 0}, Content: content, // orthogonal to queryVec
			FilePath: doc.FilePath, ChunkIndex: uint32(11 + i), DocID: doc.ID, Model: model,
		}); err != nil {
			t.Fatalf("AddChunk target[%d]: %v", i, err)
		}
	}

	cases := []struct {
		queryText  string // literal token to locate
		wantInTop1 string // expected substring in hybrid[0]
	}{
		{"computeHash", "computeHash"},
		{"buildFTSQuery", "buildFTSQuery"},
		{"rerankWithSignals", "rerankWithSignals"},
	}

	for _, tc := range cases {
		t.Run(tc.queryText, func(t *testing.T) {
			// Vector-only top-5: all 11 base chunks score > 0; all 3 targets
			// score 0.9*(1-1.0)=0 and must not appear.
			vecOnly, err := s.SearchChunks(queryVec, limit)
			if err != nil {
				t.Fatalf("SearchChunks: %v", err)
			}
			for _, c := range vecOnly {
				if strings.Contains(c.Content, tc.queryText) {
					t.Errorf("query %q: target must not appear in vector-only top-%d; got: %q",
						tc.queryText, limit, c.Content)
				}
			}

			// Hybrid: BM25 rank-1 + vector rank-12/13/14 gives higher RRF than
			// any pure-vector chunk (rank-1 only). Target must be rank #1.
			hybrid, err := s.HybridSearch(queryVec, tc.queryText, limit)
			if err != nil {
				t.Fatalf("HybridSearch: %v", err)
			}
			if len(hybrid) == 0 {
				t.Fatalf("HybridSearch returned 0 results for query %q", tc.queryText)
			}
			if !strings.Contains(hybrid[0].Content, tc.wantInTop1) {
				t.Errorf("hybrid[0] for query %q: want content with %q, got: %q",
					tc.queryText, tc.wantInTop1, hybrid[0].Content)
			}
		})
	}
}

// TestHybridSearch_EmptyQueryFallsBackToVector verifies that when the query
// text sanitizes to an empty string, HybridSearch silently falls back to the
// vector ranking alone without returning an error.
func TestHybridSearch_EmptyQueryFallsBackToVector(t *testing.T) {
	s := newTestStore(t)
	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/empty.md", Title: "Empty"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	_, err = s.AddChunk(AddChunkInput{
		Vector:     []float64{1, 0, 0, 0},
		Content:    "some content",
		FilePath:   doc.FilePath,
		ChunkIndex: 0,
		DocID:      doc.ID,
		Model:      "m",
	})
	if err != nil {
		t.Fatalf("AddChunk: %v", err)
	}

	// A query of only FTS special characters sanitizes to "".
	chunks, err := s.HybridSearch([]float64{1, 0, 0, 0}, `"*()"`, 5)
	if err != nil {
		t.Fatalf("HybridSearch with empty sanitized query: %v", err)
	}
	// Should still return the vector result — not error or panic.
	if len(chunks) == 0 {
		t.Error("expected at least one result via vector fallback when query is empty")
	}
}

// TestBuildFTSQuery covers the query sanitizer and double-quoted OR-term builder.
// Each token is wrapped in double quotes to prevent FTS5 operator injection.
func TestBuildFTSQuery(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"computeHash", `"computeHash"`},
		{"search_docs computeHash", `"search_docs" OR "computeHash"`},
		{`"*()"`, ""},
		{"foo bar baz", `"foo" OR "bar" OR "baz"`},
		{"  spaces  ", `"spaces"`},
		// FTS5 operator words must be quoted as literals, not treated as operators.
		{"NOT something", `"NOT" OR "something"`},
		{"AND OR NOT", `"AND" OR "OR" OR "NOT"`},
	}
	for _, tc := range cases {
		got := buildFTSQuery(tc.input)
		if got != tc.want {
			t.Errorf("buildFTSQuery(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
