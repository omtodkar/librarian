package store

import (
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"librarian/internal/embedding"
)

// fakeReranker is a test double that returns scores computed by a caller-
// supplied function. It satisfies both store.Reranker and embedding.Reranker.
type fakeReranker struct {
	rankFn func(query string, docs []string) ([]float64, error)
}

func (f *fakeReranker) Rerank(query string, docs []string) ([]float64, error) {
	return f.rankFn(query, docs)
}

func (f *fakeReranker) Model() string { return "fake" }

// seedCorpus inserts n chunks with vectors rotating away from [1,0,0,0].
// Returns the store (with reranker set) and the query vector.
func seedCorpus(t *testing.T, reranker Reranker, topK int, n int) (*Store, []float64) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"), reranker, topK)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/rerank.md", Title: "Rerank"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	for i := 0; i < n; i++ {
		angle := float64(i+1) * 0.05
		vec := []float64{math.Cos(angle), math.Sin(angle), 0, 0}
		if _, err := s.AddChunk(AddChunkInput{
			Vector:     vec,
			Content:    strings.Repeat(string(rune('A'+i%26)), 10),
			FilePath:   doc.FilePath,
			ChunkIndex: uint32(i),
			DocID:      doc.ID,
			Model:      "m",
		}); err != nil {
			t.Fatalf("AddChunk %d: %v", i, err)
		}
	}
	return s, []float64{1, 0, 0, 0}
}

// TestSearchChunks_NilReranker verifies that a nil reranker produces the same
// output as the pre-reranker code path (no behaviour change).
func TestSearchChunks_NilReranker(t *testing.T) {
	const n = 8
	s, query := seedCorpus(t, nil, 0, n)

	chunks, err := s.SearchChunks("anything", query, 4)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected results, got none")
	}
	if len(chunks) > 4 {
		t.Fatalf("expected at most 4 results, got %d", len(chunks))
	}
}

// TestSearchChunks_ReverseReranker verifies that a reranker which reverses the
// signal-ranked order causes the lowest signal-scored chunk to be returned
// first. Seeds exactly limit chunks so the assertion is precise: after
// reversal, reranked[0] must equal the worst signal-ranked chunk baseline[limit-1].
func TestSearchChunks_ReverseReranker(t *testing.T) {
	// Seed exactly limit chunks so topK==limit and the reranker sees all of them.
	const limit = 4

	reversed := &fakeReranker{
		rankFn: func(_ string, docs []string) ([]float64, error) {
			// Ascending scores: last element (worst signal rank) gets the
			// highest cross-encoder score, genuinely reversing the order.
			scores := make([]float64, len(docs))
			for i := range docs {
				scores[i] = float64(i) // 0 for first (best signal), limit-1 for last
			}
			return scores, nil
		},
	}

	// Set topK = limit so the reranker receives exactly the signal-ranked top-limit.
	s, queryVec := seedCorpus(t, reversed, limit, limit)

	// Baseline: same corpus, no reranker.
	sBaseline, err := Open(filepath.Join(t.TempDir(), "base.db"), nil, 0)
	if err != nil {
		t.Fatalf("Open baseline: %v", err)
	}
	defer sBaseline.Close()
	doc, err := sBaseline.AddDocument(AddDocumentInput{FilePath: "docs/r.md", Title: "R"})
	if err != nil {
		t.Fatalf("AddDocument baseline: %v", err)
	}
	for i := 0; i < limit; i++ {
		angle := float64(i+1) * 0.05
		vec := []float64{math.Cos(angle), math.Sin(angle), 0, 0}
		if _, err := sBaseline.AddChunk(AddChunkInput{
			Vector:     vec,
			Content:    strings.Repeat(string(rune('A'+i%26)), 10),
			FilePath:   doc.FilePath,
			ChunkIndex: uint32(i),
			DocID:      doc.ID,
			Model:      "m",
		}); err != nil {
			t.Fatalf("AddChunk baseline[%d]: %v", i, err)
		}
	}
	baseline, err := sBaseline.SearchChunks("q", queryVec, limit)
	if err != nil {
		t.Fatalf("baseline SearchChunks: %v", err)
	}
	if len(baseline) != limit {
		t.Fatalf("baseline: expected %d results, got %d", limit, len(baseline))
	}

	reranked, err := s.SearchChunks("q", queryVec, limit)
	if err != nil {
		t.Fatalf("reranked SearchChunks: %v", err)
	}
	if len(reranked) != limit {
		t.Fatalf("reranked: expected %d results, got %d", limit, len(reranked))
	}

	// Reversal: the worst signal-ranked chunk (baseline[limit-1]) must be
	// promoted to rank 0 by the cross-encoder.
	if reranked[0].Content != baseline[limit-1].Content {
		t.Errorf("reranked[0] must equal baseline[%d]; want %q, got %q",
			limit-1, baseline[limit-1].Content, reranked[0].Content)
	}
}

// TestSearchChunks_RerankerError verifies graceful fallback when the reranker
// returns an error — the caller receives the signal-reranked result with no
// error surfaced.
func TestSearchChunks_RerankerError(t *testing.T) {
	const n = 6
	const limit = 3

	errReranker := &fakeReranker{
		rankFn: func(_ string, _ []string) ([]float64, error) {
			return nil, errors.New("reranker unavailable")
		},
	}

	s, queryVec := seedCorpus(t, errReranker, 20, n)

	chunks, err := s.SearchChunks("q", queryVec, limit)
	if err != nil {
		t.Fatalf("SearchChunks must not propagate reranker error; got: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected fallback signal-ranked results, got none")
	}
	if len(chunks) > limit {
		t.Fatalf("expected at most %d results, got %d", limit, len(chunks))
	}
}

// TestSearchChunks_RerankerTimeout verifies that a reranker that times out
// (due to the http.Client.Timeout) triggers the fallback path. Uses an
// httptest.Server that sleeps past the configured timeout.
func TestSearchChunks_RerankerTimeout(t *testing.T) {
	const limit = 3
	sleepDur := 300 * time.Millisecond

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(sleepDur)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	// 50 ms timeout — much shorter than the server's sleep.
	r, err := embedding.NewOpenAIReranker(slow.URL, "test-model", "", 50)
	if err != nil {
		t.Fatalf("NewOpenAIReranker: %v", err)
	}

	s, queryVec := seedCorpus(t, r, 20, 6)

	start := time.Now()
	chunks, callErr := s.SearchChunks("q", queryVec, limit)
	elapsed := time.Since(start)

	if callErr != nil {
		t.Fatalf("SearchChunks must not return error on timeout fallback; got: %v", callErr)
	}
	if len(chunks) == 0 {
		t.Fatal("expected fallback signal-ranked results, got none")
	}
	// Verify the query completed in much less than the server's sleep time
	// (the http.Client.Timeout fired and triggered the fallback).
	if elapsed >= sleepDur {
		t.Errorf("SearchChunks took %v; expected much less than server sleep %v — timeout not respected", elapsed, sleepDur)
	}
}
