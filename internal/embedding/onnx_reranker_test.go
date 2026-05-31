//go:build onnx

// These tests exercise the real in-process ONNX reranker and therefore require
// the model + ONNX Runtime to be present in the shared cache. They are gated
// behind the `onnx` build tag (mirroring the `infinity` tag for the HTTP
// reranker) and skip when the model has not been pulled, so CI never downloads
// hundreds of MB.
//
// Run locally after `librarian models pull`:
//
//	go test -tags 'fts5 onnx' ./internal/embedding -run TestOnnx

package embedding

import (
	"testing"

	"librarian/internal/models"
)

func TestOnnxReranker_RanksRelevantHigher(t *testing.T) {
	id := models.DefaultRerankerID()
	if !models.IsPulled(id) {
		t.Skipf("model %q not pulled; run `librarian models pull %s`", id, id)
	}

	r, err := NewOnnxReranker(id, "", 0)
	if err != nil {
		t.Fatalf("NewOnnxReranker: %v", err)
	}
	defer r.Close()

	if r.Model() != id {
		t.Errorf("Model() = %q, want %q", r.Model(), id)
	}

	query := "how does auth token rotation work"
	docs := []string{
		"The refresh token is rotated on every use and the previous token is immediately revoked.", // relevant
		"Our office cafeteria serves lunch between noon and 2pm on weekdays.",                      // irrelevant
		"Access tokens expire after 15 minutes; rotation issues a new refresh token each cycle.",   // relevant
	}
	scores, err := r.Rerank(query, docs)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != len(docs) {
		t.Fatalf("got %d scores for %d docs", len(scores), len(docs))
	}

	// Both relevant docs must outscore the irrelevant one.
	if scores[0] <= scores[1] || scores[2] <= scores[1] {
		t.Errorf("relevant docs did not outrank irrelevant: %v", scores)
	}
}

func TestOnnxReranker_EmptyDocs(t *testing.T) {
	id := models.DefaultRerankerID()
	if !models.IsPulled(id) {
		t.Skipf("model %q not pulled", id)
	}
	r, err := NewOnnxReranker(id, "", 0)
	if err != nil {
		t.Fatalf("NewOnnxReranker: %v", err)
	}
	defer r.Close()

	scores, err := r.Rerank("anything", nil)
	if err != nil {
		t.Fatalf("Rerank(nil): %v", err)
	}
	if scores != nil {
		t.Errorf("expected nil scores for empty docs, got %v", scores)
	}
}

func TestNewOnnxReranker_UnknownModel(t *testing.T) {
	if _, err := NewOnnxReranker("not-a-real-model", "", 0); err == nil {
		t.Fatal("expected error for unknown model")
	}
}
