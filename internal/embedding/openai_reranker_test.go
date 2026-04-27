package embedding

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// serveRerank starts a test server that responds to POST /rerank with the
// provided response body and status code.
func serveRerank(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body)) //nolint:errcheck
	}))
}

// TestOpenAIReranker_HappyPath_OutOfOrder verifies that out-of-order index
// fields in the response are mapped back to the correct input positions.
func TestOpenAIReranker_HappyPath_OutOfOrder(t *testing.T) {
	// Server returns results in reverse order: index 1 first, then index 0.
	srv := serveRerank(t, 200, `{
		"results": [
			{"index": 1, "relevance_score": 0.9},
			{"index": 0, "relevance_score": 0.3}
		]
	}`)
	defer srv.Close()

	r, err := NewOpenAIReranker(srv.URL, "test-model", "", 1000)
	if err != nil {
		t.Fatalf("NewOpenAIReranker: %v", err)
	}

	scores, err := r.Rerank("query", []string{"doc-a", "doc-b"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(scores) != 2 {
		t.Fatalf("expected 2 scores, got %d", len(scores))
	}
	if scores[0] != 0.3 {
		t.Errorf("scores[0] (doc-a): want 0.3, got %v", scores[0])
	}
	if scores[1] != 0.9 {
		t.Errorf("scores[1] (doc-b): want 0.9, got %v", scores[1])
	}
}

// TestOpenAIReranker_Non200_ReturnsError verifies that a non-200 HTTP status
// causes Rerank to return an error.
func TestOpenAIReranker_Non200_ReturnsError(t *testing.T) {
	srv := serveRerank(t, 503, `{"message":"service unavailable"}`)
	defer srv.Close()

	r, err := NewOpenAIReranker(srv.URL, "m", "", 1000)
	if err != nil {
		t.Fatalf("NewOpenAIReranker: %v", err)
	}

	_, err = r.Rerank("q", []string{"a"})
	if err == nil {
		t.Fatal("expected error on non-200 status, got nil")
	}
}

// TestOpenAIReranker_CountMismatch_ReturnsError verifies that a response with
// a different number of results than documents causes an error.
func TestOpenAIReranker_CountMismatch_ReturnsError(t *testing.T) {
	// Only 1 result for 2 documents.
	srv := serveRerank(t, 200, `{"results":[{"index":0,"relevance_score":0.5}]}`)
	defer srv.Close()

	r, err := NewOpenAIReranker(srv.URL, "m", "", 1000)
	if err != nil {
		t.Fatalf("NewOpenAIReranker: %v", err)
	}

	_, err = r.Rerank("q", []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error on count mismatch, got nil")
	}
}

// TestOpenAIReranker_MalformedJSON_ReturnsError verifies that a response with
// invalid JSON causes an error.
func TestOpenAIReranker_MalformedJSON_ReturnsError(t *testing.T) {
	srv := serveRerank(t, 200, `not-json`)
	defer srv.Close()

	r, err := NewOpenAIReranker(srv.URL, "m", "", 1000)
	if err != nil {
		t.Fatalf("NewOpenAIReranker: %v", err)
	}

	_, err = r.Rerank("q", []string{"a"})
	if err == nil {
		t.Fatal("expected error on malformed JSON, got nil")
	}
}

// TestOpenAIReranker_DuplicateIndex_ReturnsError verifies that a response
// containing two results with the same index causes an error.
func TestOpenAIReranker_DuplicateIndex_ReturnsError(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"results": []any{
			map[string]any{"index": 0, "relevance_score": 0.8},
			map[string]any{"index": 0, "relevance_score": 0.2},
		},
	})
	srv := serveRerank(t, 200, string(body))
	defer srv.Close()

	r, err := NewOpenAIReranker(srv.URL, "m", "", 1000)
	if err != nil {
		t.Fatalf("NewOpenAIReranker: %v", err)
	}

	_, err = r.Rerank("q", []string{"a", "b"})
	if err == nil {
		t.Fatal("expected error on duplicate index, got nil")
	}
}

// TestOpenAIReranker_EmptyDocuments_ReturnsNil verifies the short-circuit for
// zero-length document slices.
func TestOpenAIReranker_EmptyDocuments_ReturnsNil(t *testing.T) {
	// Server should never be called — but set it up to detect unexpected calls.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("unexpected HTTP call for empty documents")
		w.WriteHeader(500)
	}))
	defer srv.Close()

	r, err := NewOpenAIReranker(srv.URL, "m", "", 1000)
	if err != nil {
		t.Fatalf("NewOpenAIReranker: %v", err)
	}

	scores, err := r.Rerank("q", nil)
	if err != nil {
		t.Fatalf("expected nil error for empty docs, got: %v", err)
	}
	if scores != nil {
		t.Errorf("expected nil scores for empty docs, got: %v", scores)
	}
}
