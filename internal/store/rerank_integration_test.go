//go:build infinity

package store

// TestRerank_InfinityEndpoint is an integration test that requires a running
// Infinity server (make infinity-start). It verifies that a literal-match
// chunk which is farther in embedding space than a paraphrase loses in vector-
// only mode but wins when cross-encoder reranking is enabled.
//
// Run locally: make infinity-start && go test -tags=infinity ./internal/store -run TestRerank -v
import (
	"math"
	"path/filepath"
	"strings"
	"testing"

	"librarian/internal/embedding"
)

func TestRerank_InfinityEndpoint(t *testing.T) {
	const (
		infinityURL  = "http://127.0.0.1:7997"
		rerankModel  = "Alibaba-NLP/gte-reranker-modernbert-base"
		limit        = 3
	)

	// ---- seed corpus -------------------------------------------------------
	// queryVec points along the first axis.
	queryVec := []float64{1, 0, 0, 0}

	// 5 "paraphrase" chunks: vectors are close to queryVec (small angles).
	// None contain the literal query token.
	paraphraseContents := []string{
		"embedding similarity measures how close two vectors are in the space",
		"semantic search retrieves documents by meaning rather than keyword overlap",
		"dense vector retrieval maps queries and passages to the same embedding space",
		"approximate nearest-neighbor lookup over high-dimensional float vectors",
		"bi-encoder models produce independent embeddings for query and document",
	}

	// 1 "literal" chunk: vector is orthogonal (worst cosine distance) but
	// contains the exact phrase "sqlite-vec".
	literalContent := "sqlite-vec is a SQLite extension providing vector search over float32 embeddings"

	// ---- store without reranker (baseline) ---------------------------------
	sBase, err := Open(filepath.Join(t.TempDir(), "base.db"), nil, 0)
	if err != nil {
		t.Fatalf("Open base: %v", err)
	}
	defer sBase.Close()

	docBase, err := sBase.AddDocument(AddDocumentInput{FilePath: "docs/r.md", Title: "R"})
	if err != nil {
		t.Fatalf("AddDocument base: %v", err)
	}

	for i, c := range paraphraseContents {
		angle := float64(i+1) * 0.05
		vec := []float64{math.Cos(angle), math.Sin(angle), 0, 0}
		if _, err := sBase.AddChunk(AddChunkInput{
			Vector:     vec,
			Content:    c,
			FilePath:   docBase.FilePath,
			ChunkIndex: uint32(i),
			DocID:      docBase.ID,
			Model:      "m",
		}); err != nil {
			t.Fatalf("AddChunk paraphrase[%d]: %v", i, err)
		}
	}
	// Literal chunk: orthogonal vector (worst embedding match).
	if _, err := sBase.AddChunk(AddChunkInput{
		Vector:     []float64{0, 0, 0, 1},
		Content:    literalContent,
		FilePath:   docBase.FilePath,
		ChunkIndex: uint32(len(paraphraseContents)),
		DocID:      docBase.ID,
		Model:      "m",
	}); err != nil {
		t.Fatalf("AddChunk literal: %v", err)
	}

	// Without reranking: literal chunk scores 0.9*(1-1.0) = 0 and must NOT
	// appear in the top results.
	baseResults, err := sBase.SearchChunks("what is sqlite-vec", queryVec, limit)
	if err != nil {
		t.Fatalf("SearchChunks base: %v", err)
	}
	for _, c := range baseResults {
		if strings.Contains(c.Content, "sqlite-vec") {
			t.Error("vector-only: literal chunk must not appear in top results (orthogonal vector)")
		}
	}

	// ---- store with Infinity reranker --------------------------------------
	reranker, err := embedding.NewOpenAIReranker(infinityURL, rerankModel, "", 3000)
	if err != nil {
		t.Fatalf("NewOpenAIReranker: %v", err)
	}

	sRanked, err := Open(filepath.Join(t.TempDir(), "ranked.db"), reranker, 20)
	if err != nil {
		t.Fatalf("Open ranked: %v", err)
	}
	defer sRanked.Close()

	docR, err := sRanked.AddDocument(AddDocumentInput{FilePath: "docs/r.md", Title: "R"})
	if err != nil {
		t.Fatalf("AddDocument ranked: %v", err)
	}
	for i, c := range paraphraseContents {
		angle := float64(i+1) * 0.05
		vec := []float64{math.Cos(angle), math.Sin(angle), 0, 0}
		if _, err := sRanked.AddChunk(AddChunkInput{
			Vector:     vec,
			Content:    c,
			FilePath:   docR.FilePath,
			ChunkIndex: uint32(i),
			DocID:      docR.ID,
			Model:      "m",
		}); err != nil {
			t.Fatalf("AddChunk reranked paraphrase[%d]: %v", i, err)
		}
	}
	if _, err := sRanked.AddChunk(AddChunkInput{
		Vector:     []float64{0, 0, 0, 1},
		Content:    literalContent,
		FilePath:   docR.FilePath,
		ChunkIndex: uint32(len(paraphraseContents)),
		DocID:      docR.ID,
		Model:      "m",
	}); err != nil {
		t.Fatalf("AddChunk reranked literal: %v", err)
	}

	// With Infinity reranking: the literal chunk should surface to rank #1
	// because the cross-encoder recognises the query "what is sqlite-vec"
	// matches the literal chunk's content more strongly than the paraphrases.
	rerankedResults, err := sRanked.SearchChunks("what is sqlite-vec", queryVec, limit)
	if err != nil {
		t.Fatalf("SearchChunks reranked: %v", err)
	}
	if len(rerankedResults) == 0 {
		t.Fatal("reranked search returned no results")
	}
	if !strings.Contains(rerankedResults[0].Content, "sqlite-vec") {
		t.Errorf("with Infinity reranking, literal chunk must be rank #1; got: %q", rerankedResults[0].Content)
	}
}
