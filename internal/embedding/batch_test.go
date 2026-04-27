package embedding

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestResolveBatchSize pins the shared constructor clamp contract. Without
// this, a config change (e.g., defaultBatchSize move) could silently tip
// every provider's default without any test going red.
func TestResolveBatchSize(t *testing.T) {
	cases := []struct {
		name       string
		configured int
		hardMax    int
		want       int
	}{
		{"zero-uses-default", 0, 100, defaultBatchSize},
		{"negative-uses-default", -5, 100, defaultBatchSize},
		{"in-range-respected", 50, 100, 50},
		{"at-hard-max-respected", 100, 100, 100},
		{"above-hard-max-clamped", 500, 100, 100},
		{"openai-large-hard-max", 1000, openaiBatchMax, 1000},
		{"openai-above-hard-max", 5000, openaiBatchMax, openaiBatchMax},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveBatchSize(tc.configured, tc.hardMax)
			if got != tc.want {
				t.Errorf("resolveBatchSize(%d, %d) = %d; want %d",
					tc.configured, tc.hardMax, got, tc.want)
			}
		})
	}
}

// geminiBatchMock returns an httptest.Server that replays N-dim zero-vectors
// in positional order for every batchEmbedContents request, and records the
// per-request input sizes so tests can assert wave splitting. dim=2 keeps
// the expected payload tiny.
type geminiBatchMock struct {
	requestSizes []int
	dim          int
	status       int // 0 = 200
	errorBody    string
}

func newGeminiBatchMock(dim int) *geminiBatchMock { return &geminiBatchMock{dim: dim} }

func (m *geminiBatchMock) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req geminiBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode batch request: %v", err)
		}
		m.requestSizes = append(m.requestSizes, len(req.Requests))
		if m.status != 0 {
			w.WriteHeader(m.status)
			w.Write([]byte(m.errorBody))
			return
		}
		resp := geminiBatchResponse{Embeddings: make([]geminiEmbedding, len(req.Requests))}
		for i := range req.Requests {
			resp.Embeddings[i] = geminiEmbedding{Values: make([]float64, m.dim)}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// newGeminiTestEmbedder wires a GeminiEmbedder against the mock server by
// pointing its URL via the model-as-URL trick: Gemini's production URL is
// built inside EmbedBatch, so we cheat by rewriting the client's transport
// to redirect all requests to the mock. Simpler than parameterising the URL.
func newGeminiTestEmbedder(t *testing.T, batchSize int, mockURL string) *GeminiEmbedder {
	t.Helper()
	e, err := NewGeminiEmbedder("test-key", "test-model", batchSize, 0)
	if err != nil {
		t.Fatalf("NewGeminiEmbedder: %v", err)
	}
	e.client = &http.Client{Transport: rewriteTransport{to: mockURL}}
	return e
}

type rewriteTransport struct{ to string }

func (r rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Replace scheme+host with the mock server's, preserve path + query so
	// our assertions on ?key=... and /:batchEmbedContents still fire.
	newURL := r.to + req.URL.RequestURI()
	newReq, err := http.NewRequest(req.Method, newURL, req.Body)
	if err != nil {
		return nil, err
	}
	newReq.Header = req.Header
	return http.DefaultClient.Do(newReq)
}

func TestGeminiEmbedder_EmbedBatchSplitsAtConfiguredSize(t *testing.T) {
	mock := newGeminiBatchMock(2)
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()
	e := newGeminiTestEmbedder(t, 100, srv.URL)

	texts := make([]string, 250)
	for i := range texts {
		texts[i] = fmt.Sprintf("chunk-%d", i)
	}
	out, err := e.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(out) != 250 {
		t.Fatalf("output length: got %d want 250", len(out))
	}
	want := []int{100, 100, 50}
	if len(mock.requestSizes) != len(want) {
		t.Fatalf("request count: got %d want %d", len(mock.requestSizes), len(want))
	}
	for i, got := range mock.requestSizes {
		if got != want[i] {
			t.Errorf("request %d size: got %d want %d", i, got, want[i])
		}
	}
}

func TestGeminiEmbedder_EmbedBatchRespectsCustomSize(t *testing.T) {
	mock := newGeminiBatchMock(2)
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()
	e := newGeminiTestEmbedder(t, 50, srv.URL)

	texts := make([]string, 150)
	if _, err := e.EmbedBatch(texts); err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	for i, got := range mock.requestSizes {
		if got != 50 {
			t.Errorf("request %d size: got %d want 50", i, got)
		}
	}
	if len(mock.requestSizes) != 3 {
		t.Errorf("request count: got %d want 3", len(mock.requestSizes))
	}
}

func TestGeminiEmbedder_EmbedBatchClampsToHardMax(t *testing.T) {
	mock := newGeminiBatchMock(2)
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()
	// Configure well above the Gemini cap; expect clamp to 100.
	e := newGeminiTestEmbedder(t, 500, srv.URL)

	if e.batchSize != geminiBatchMax {
		t.Fatalf("batchSize after clamp: got %d want %d", e.batchSize, geminiBatchMax)
	}
	texts := make([]string, 250)
	if _, err := e.EmbedBatch(texts); err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	// 250 / 100 = 3 waves: 100, 100, 50.
	want := []int{100, 100, 50}
	if len(mock.requestSizes) != len(want) {
		t.Fatalf("request count: got %d want %d", len(mock.requestSizes), len(want))
	}
	for i, got := range mock.requestSizes {
		if got != want[i] {
			t.Errorf("request %d size: got %d want %d", i, got, want[i])
		}
	}
}

func TestGeminiEmbedder_EmbedBatchEmptyInput(t *testing.T) {
	mock := newGeminiBatchMock(2)
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()
	e := newGeminiTestEmbedder(t, 100, srv.URL)

	out, err := e.EmbedBatch([]string{})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("output length: got %d want 0", len(out))
	}
	if len(mock.requestSizes) != 0 {
		t.Errorf("empty input should not trigger an HTTP call; got %d", len(mock.requestSizes))
	}
}

func TestGeminiEmbedder_EmbedBatchWholeResponseError(t *testing.T) {
	mock := newGeminiBatchMock(2)
	mock.status = http.StatusTooManyRequests
	mock.errorBody = `{"error": {"code": 429, "message": "Resource exhausted"}}`
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()
	e := newGeminiTestEmbedder(t, 100, srv.URL)

	_, err := e.EmbedBatch([]string{"a", "b"})
	if err == nil {
		t.Fatal("expected error on 429, got nil")
	}
	if !strings.Contains(err.Error(), "Gemini batch") || !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention provider + status; got: %v", err)
	}
}

// TestOpenAIEmbedder_EmbedBatchPreservesOrder — OpenAI spec allows data[]
// to arrive in arbitrary order keyed by `index`. Returning without a
// re-sort would silently map wrong vectors to chunks. Pin the invariant.
func TestOpenAIEmbedder_EmbedBatchPreservesOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req openAIBatchRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		// Return in reversed order: element [0] has Index=2, element [1] has Index=1, etc.
		resp := openAIEmbeddingResponse{Data: make([]openAIEmbeddingData, len(req.Input))}
		for i := range req.Input {
			rev := len(req.Input) - 1 - i
			// Embed a distinctive value so we can verify post-sort ordering.
			resp.Data[i] = openAIEmbeddingData{
				Embedding: []float64{float64(rev), 0},
				Index:     rev,
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 0)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	out, err := e.EmbedBatch([]string{"a", "b", "c", "d"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	for i, vec := range out {
		if vec[0] != float64(i) {
			t.Errorf("output[%d][0]: got %v want %v (sort by index failed)", i, vec[0], float64(i))
		}
	}
}

func TestOpenAIEmbedder_EmbedBatchEmptyInput(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 0)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	out, err := e.EmbedBatch([]string{})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("output length: got %d want 0", len(out))
	}
	if called {
		t.Error("empty input should not trigger an HTTP call")
	}
}
