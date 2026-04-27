package embedding

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- Gemini fallback tests ---

// TestGeminiEmbedder_BatchFallbackOn4xx verifies that a 400 batch response
// triggers per-item Embed() calls when batchFallback is enabled. Items whose
// individual Embed() succeeds land normally; the one that fails stays nil.
func TestGeminiEmbedder_BatchFallbackOn4xx(t *testing.T) {
	const badText = "bad-item"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "batchEmbedContents") {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"code":400,"message":"bad request"}}`))
			return
		}
		// Single :embedContent path
		var req geminiRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad req", http.StatusBadRequest)
			return
		}
		text := ""
		if len(req.Content.Parts) > 0 {
			text = req.Content.Parts[0].Text
		}
		if text == badText {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(geminiResponse{
			Embedding: geminiEmbedding{Values: []float64{1.0, 2.0}},
		})
	}))
	defer srv.Close()

	e, err := NewGeminiEmbedder("test-key", "test-model", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewGeminiEmbedder: %v", err)
	}
	e.batchFallback = true
	e.client = &http.Client{Transport: rewriteTransport{to: srv.URL}}

	texts := []string{"text-1", badText, "text-3"}
	out, err := e.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("EmbedBatch returned error: %v; want nil (partial result)", err)
	}
	if len(out) != 3 {
		t.Fatalf("output length: got %d want 3", len(out))
	}
	if out[0] == nil {
		t.Error("out[0] should be non-nil (fallback succeeded)")
	}
	if out[1] != nil {
		t.Errorf("out[1] (bad-item) should be nil; got %v", out[1])
	}
	if out[2] == nil {
		t.Error("out[2] should be non-nil (fallback succeeded)")
	}
}

// TestGeminiEmbedder_BatchFallbackDisabledOn4xx confirms that without
// batchFallback a 400 batch response still surfaces as an error.
func TestGeminiEmbedder_BatchFallbackDisabledOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"code":400,"message":"bad request"}}`))
	}))
	defer srv.Close()

	e, err := NewGeminiEmbedder("test-key", "test-model", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewGeminiEmbedder: %v", err)
	}
	e.client = &http.Client{Transport: rewriteTransport{to: srv.URL}}

	_, err = e.EmbedBatch([]string{"a", "b"})
	if err == nil {
		t.Fatal("expected error on 400 without fallback; got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400; got: %v", err)
	}
}

// TestGeminiEmbedder_BatchFallbackNotTriggeredOn429 confirms that 429 (rate
// limit) does NOT trigger the per-item fallback — retryOn429 handles those.
func TestGeminiEmbedder_BatchFallbackNotTriggeredOn429(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"code":429,"message":"rate limited"}}`))
	}))
	defer srv.Close()

	e, err := NewGeminiEmbedder("test-key", "test-model", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewGeminiEmbedder: %v", err)
	}
	e.batchFallback = true
	e.client = &http.Client{Transport: rewriteTransport{to: srv.URL}}

	_, err = e.EmbedBatch([]string{"a"})
	if err == nil {
		t.Fatal("expected error on 429 (not fallback territory); got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention 429; got: %v", err)
	}
}

// TestGeminiEmbedder_PartialSuccess200Fallback verifies that a 200 response
// with some empty embeddings triggers per-item fallback when enabled. Good
// items from the batch land directly; bad items are retried individually.
func TestGeminiEmbedder_PartialSuccess200Fallback(t *testing.T) {
	const badText = "bad-item"
	var batchCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "batchEmbedContents") {
			batchCalled = true
			var req geminiBatchRequest
			json.NewDecoder(r.Body).Decode(&req)
			resp := geminiBatchResponse{Embeddings: make([]geminiEmbedding, len(req.Requests))}
			for i, item := range req.Requests {
				text := ""
				if len(item.Content.Parts) > 0 {
					text = item.Content.Parts[0].Text
				}
				if text != badText {
					resp.Embeddings[i] = geminiEmbedding{Values: []float64{float64(i + 1), 0}}
				}
				// bad-item slot stays with empty Values — partial success
			}
			json.NewEncoder(w).Encode(resp)
			return
		}
		// Single :embedContent — bad-item always fails individually too.
		var req geminiRequest
		json.NewDecoder(r.Body).Decode(&req)
		text := ""
		if len(req.Content.Parts) > 0 {
			text = req.Content.Parts[0].Text
		}
		if text == badText {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		json.NewEncoder(w).Encode(geminiResponse{
			Embedding: geminiEmbedding{Values: []float64{9.0, 0}},
		})
	}))
	defer srv.Close()

	e, err := NewGeminiEmbedder("test-key", "test-model", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewGeminiEmbedder: %v", err)
	}
	e.batchFallback = true
	e.client = &http.Client{Transport: rewriteTransport{to: srv.URL}}

	texts := []string{"good-1", badText, "good-2"}
	out, err := e.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("EmbedBatch returned error: %v; want nil (partial result)", err)
	}
	if !batchCalled {
		t.Error("batch endpoint should have been called")
	}
	if len(out) != 3 {
		t.Fatalf("output length: got %d want 3", len(out))
	}
	if out[0] == nil {
		t.Error("out[0] (good-1) should be non-nil")
	}
	if out[1] != nil {
		t.Errorf("out[1] (bad-item) should be nil (individual embed also failed); got %v", out[1])
	}
	if out[2] == nil {
		t.Error("out[2] (good-2) should be non-nil")
	}
}

// --- OpenAI fallback tests ---

// TestOpenAIEmbedder_BatchFallbackOn4xx mirrors the Gemini test: 400 batch
// triggers per-item Embed() calls; only the faulty item stays nil.
func TestOpenAIEmbedder_BatchFallbackOn4xx(t *testing.T) {
	const badText = "bad-item"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input interface{} `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)

		// Distinguish batch (input is array) from single (input is string) by type.
		switch body.Input.(type) {
		case []interface{}:
			// Batch call
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request"}}`))
		default:
			// Single Embed() call
			var singleBody struct {
				Input string `json:"input"`
			}
			// Re-decode from raw; simpler to just check if it was an array above.
			if body.Input == badText {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = singleBody
			resp := openAIEmbeddingResponse{Data: []openAIEmbeddingData{
				{Embedding: []float64{1.0, 2.0}, Index: 0},
			}}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	e.batchFallback = true

	texts := []string{"text-1", badText, "text-3"}
	out, err := e.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("EmbedBatch returned error: %v; want nil (partial result)", err)
	}
	if len(out) != 3 {
		t.Fatalf("output length: got %d want 3", len(out))
	}
	if out[0] == nil {
		t.Error("out[0] should be non-nil (fallback succeeded)")
	}
	if out[1] != nil {
		t.Errorf("out[1] (bad-item) should be nil; got %v", out[1])
	}
	if out[2] == nil {
		t.Error("out[2] should be non-nil (fallback succeeded)")
	}
}

// TestOpenAIEmbedder_PartialSuccess200Fallback verifies that a 200 response
// with per-item error fields triggers fallback for the failed items only.
// Good items from the batch response land directly; the bad item is retried
// individually (and also fails), leaving its slot nil.
func TestOpenAIEmbedder_PartialSuccess200Fallback(t *testing.T) {
	const badText = "bad-item"
	var batchCallCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input interface{} `json:"input"`
		}
		json.NewDecoder(r.Body).Decode(&body)

		switch inp := body.Input.(type) {
		case []interface{}:
			batchCallCount++
			// Partial-success: return error for bad-item by index.
			data := make([]openAIEmbeddingData, len(inp))
			for i, raw := range inp {
				text, _ := raw.(string)
				if text == badText {
					data[i] = openAIEmbeddingData{
						Index: i,
						Error: &openAIError{Message: "item failed", Type: "invalid_request"},
					}
				} else {
					data[i] = openAIEmbeddingData{
						Embedding: []float64{float64(i + 1), 0},
						Index:     i,
					}
				}
			}
			json.NewEncoder(w).Encode(openAIEmbeddingResponse{Data: data})
		default:
			// Single Embed() — bad-item always fails individually too.
			if body.Input == badText {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			json.NewEncoder(w).Encode(openAIEmbeddingResponse{
				Data: []openAIEmbeddingData{{Embedding: []float64{9.0, 0}, Index: 0}},
			})
		}
	}))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	e.batchFallback = true

	texts := []string{"good-1", badText, "good-2"}
	out, err := e.EmbedBatch(texts)
	if err != nil {
		t.Fatalf("EmbedBatch returned error: %v; want nil (partial result)", err)
	}
	if batchCallCount != 1 {
		t.Errorf("batch endpoint called %d times; want 1", batchCallCount)
	}
	if len(out) != 3 {
		t.Fatalf("output length: got %d want 3", len(out))
	}
	if out[0] == nil {
		t.Error("out[0] (good-1) should be non-nil")
	}
	if out[1] != nil {
		t.Errorf("out[1] (bad-item) should be nil (individual embed also failed); got %v", out[1])
	}
	if out[2] == nil {
		t.Error("out[2] (good-2) should be non-nil")
	}
}

// TestOpenAIEmbedder_BatchFallbackDisabledOn4xx confirms that without
// batchFallback a 400 batch response still surfaces as an error.
func TestOpenAIEmbedder_BatchFallbackDisabledOn4xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request"}}`))
	}))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	_, err = e.EmbedBatch([]string{"a", "b"})
	if err == nil {
		t.Fatal("expected error on 400 without fallback; got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("error should mention 400; got: %v", err)
	}
}

// TestOpenAIEmbedder_BatchFallbackNotTriggeredOn5xx confirms that 5xx server
// errors do NOT trigger per-item fallback (only 4xx does, not 429).
func TestOpenAIEmbedder_BatchFallbackNotTriggeredOn5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"server error","type":"server_error"}}`))
	}))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	e.batchFallback = true

	_, err = e.EmbedBatch([]string{"a"})
	if err == nil {
		t.Fatal("expected error on 500 (not fallback territory); got nil")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention 500; got: %v", err)
	}
}

// --- is4xxFallback unit tests ---

func TestIs4xxFallback(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{200, false},
		{399, false},
		{400, true},
		{401, true},
		{403, true},
		{429, false}, // rate-limit: retryOn429 territory
		{499, true},
		{500, false},
		{503, false},
	}
	for _, tc := range cases {
		got := is4xxFallback(tc.code)
		if got != tc.want {
			t.Errorf("is4xxFallback(%d) = %v; want %v", tc.code, got, tc.want)
		}
	}
}
