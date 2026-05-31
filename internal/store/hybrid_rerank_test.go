package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// seedHybridCorpus inserts n distinct chunks all sharing the query's vector and
// lexical profile, so neither vector KNN, FTS, nor signal boost has a reason to
// prefer one — leaving any reordering attributable to the cross-encoder.
func seedHybridCorpus(t *testing.T, reranker Reranker, topK int, contents []string) (*Store, []float64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "hybrid.db"), reranker, topK)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	queryVec := []float64{1, 0, 0, 0}
	for i, c := range contents {
		doc, err := s.AddDocument(AddDocumentInput{FilePath: string(rune('a'+i)) + ".md", Title: c})
		if err != nil {
			t.Fatalf("AddDocument: %v", err)
		}
		if _, err := s.AddChunk(AddChunkInput{
			Vector: queryVec, Content: c, FilePath: doc.FilePath,
			ChunkIndex: uint32(i), DocID: doc.ID, Model: "m",
		}); err != nil {
			t.Fatalf("AddChunk: %v", err)
		}
	}
	return s, queryVec
}

// TestHybridSearch_AppliesReranker is the regression test for lib-y7vr: with a
// reranker configured, HybridSearch (the DEFAULT search path) must run the
// cross-encoder, not just RRF + signal boost. The fake reranker promotes the
// chunk containing "PICKME"; it is added last, so only a working reranker can
// surface it first.
func TestHybridSearch_AppliesReranker(t *testing.T) {
	called := false
	fr := &fakeReranker{rankFn: func(_ string, docs []string) ([]float64, error) {
		called = true
		scores := make([]float64, len(docs))
		for i, d := range docs {
			if strings.Contains(d, "PICKME") {
				scores[i] = 100
			}
		}
		return scores, nil
	}}

	s, queryVec := seedHybridCorpus(t, fr, 20, []string{
		"alpha content about indexing pipelines",
		"beta content about indexing pipelines",
		"gamma content about indexing pipelines PICKME",
	})

	results, err := s.HybridSearch(queryVec, "indexing pipelines", 3)
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if !called {
		t.Fatal("reranker was never called by HybridSearch (lib-y7vr regression)")
	}
	if len(results) == 0 || !strings.Contains(results[0].Content, "PICKME") {
		t.Fatalf("reranker did not promote the picked chunk to first; got %v", results)
	}
}

// TestHybridSearch_RerankerErrorFallback verifies HybridSearch returns the
// RRF+signal-ranked results (never an error) when the reranker fails.
func TestHybridSearch_RerankerErrorFallback(t *testing.T) {
	fr := &fakeReranker{rankFn: func(_ string, _ []string) ([]float64, error) {
		return nil, errors.New("reranker down")
	}}
	s, queryVec := seedHybridCorpus(t, fr, 20, []string{
		"alpha indexing", "beta indexing", "gamma indexing",
	})

	results, err := s.HybridSearch(queryVec, "indexing", 3)
	if err != nil {
		t.Fatalf("HybridSearch must not propagate reranker error; got: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected fallback results, got none")
	}
}

// TestHybridSearch_NilReranker confirms the no-reranker path is unchanged.
func TestHybridSearch_NilReranker(t *testing.T) {
	s, queryVec := seedHybridCorpus(t, nil, 0, []string{
		"alpha indexing", "beta indexing", "gamma indexing",
	})
	results, err := s.HybridSearch(queryVec, "indexing", 3)
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got none")
	}
}
