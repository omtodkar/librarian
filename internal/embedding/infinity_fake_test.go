package embedding

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// infinityResponse builds a fake Infinity /embeddings response with dim-dimensional
// vectors. Infinity's response shape differs from OpenAI only in the top-level
// "object"/"model"/"usage" fields; the data[] shape is identical.
func infinityResponse(dim int, indices ...int) openAIEmbeddingResponse {
	data := make([]openAIEmbeddingData, len(indices))
	for i, idx := range indices {
		vec := make([]float64, dim)
		for j := range vec {
			vec[j] = float64(idx)*0.1 + float64(j)*0.01
		}
		data[i] = openAIEmbeddingData{Embedding: vec, Index: idx}
	}
	return openAIEmbeddingResponse{Data: data}
}

// infinityFakeServer mimics Infinity's /embeddings endpoint. It records the
// last request path so tests can assert the client is NOT sending to /v1/embeddings.
type infinityFakeServer struct {
	dim      int
	lastPath string
}

func (f *infinityFakeServer) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		f.lastPath = r.URL.Path

		// Only serve /embeddings — reject anything else (e.g. /v1/embeddings).
		if r.URL.Path != "/embeddings" {
			http.NotFound(w, r)
			return
		}

		var req openAIBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			// Try single-input shape.
			req.Input = []string{""}
		}

		n := len(req.Input)
		if n == 0 {
			n = 1
		}
		indices := make([]int, n)
		for i := range indices {
			indices[i] = i
		}
		resp := infinityResponse(f.dim, indices...)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// TestInfinityFakeServer_Embed verifies the single-text Embed() call against a
// fake Infinity server and checks dimensionality of the returned vector.
func TestInfinityFakeServer_Embed(t *testing.T) {
	const dim = 4
	fake := &infinityFakeServer{dim: dim}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()

	// Infinity baseURL has NO /v1 suffix — the embedder appends /embeddings.
	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	vec, err := e.Embed("hello world")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vec) != dim {
		t.Errorf("dimensionality: got %d want %d", len(vec), dim)
	}
}

// TestInfinityFakeServer_EmbedPath verifies the client sends to /embeddings,
// not /v1/embeddings. This is the critical Infinity path detail — Infinity does
// not have the /v1 prefix that OpenAI-proper uses.
func TestInfinityFakeServer_EmbedPath(t *testing.T) {
	fake := &infinityFakeServer{dim: 2}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	if _, err := e.Embed("probe"); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if fake.lastPath != "/embeddings" {
		t.Errorf("request path: got %q want %q", fake.lastPath, "/embeddings")
	}
}

// TestInfinityFakeServer_V1PathFails demonstrates that pointing the embedder
// at /v1 (as you would for OpenAI-compatible servers) causes the fake Infinity
// server to 404, confirming the paths are distinct and the client correctly
// uses the configured baseURL.
func TestInfinityFakeServer_V1PathFails(t *testing.T) {
	fake := &infinityFakeServer{dim: 2}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()

	// Deliberately add /v1 to simulate an OpenAI-style configuration — the
	// fake Infinity server only answers /embeddings, so this must fail.
	e, err := NewOpenAIEmbedder(srv.URL+"/v1", "test-model", "", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	_, err = e.Embed("probe")
	if err == nil {
		t.Fatal("expected error when sending to /v1/embeddings on an Infinity server; got nil")
	}
	if fake.lastPath != "/v1/embeddings" {
		t.Errorf("expected server to see /v1/embeddings, got %q", fake.lastPath)
	}
}

// TestInfinityFakeServer_EmbedBatch verifies EmbedBatch against a fake Infinity
// server, asserting that the returned slice length and vector dimensionality match.
func TestInfinityFakeServer_EmbedBatch(t *testing.T) {
	const dim = 8
	fake := &infinityFakeServer{dim: dim}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	texts := []string{"doc one", "doc two", "doc three"}
	vecs, err := e.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != len(texts) {
		t.Errorf("output length: got %d want %d", len(vecs), len(texts))
	}
	for i, v := range vecs {
		if len(v) != dim {
			t.Errorf("vecs[%d] dimensionality: got %d want %d", i, len(v), dim)
		}
	}
}
