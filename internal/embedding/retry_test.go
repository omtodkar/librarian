package embedding

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// zeroSleepDelay patches retryBaseDelay to 0 and retrySleep to a no-op for
// the duration of the test so retries complete without real pauses.
func zeroSleepDelay(t *testing.T) {
	t.Helper()
	origDelay := retryBaseDelay
	origSleep := retrySleep
	retryBaseDelay = 0
	retrySleep = func(time.Duration) {}
	t.Cleanup(func() {
		retryBaseDelay = origDelay
		retrySleep = origSleep
	})
}

// geminiSequentialMock returns a Gemini-shaped handler that responds with
// the given status code sequence (last entry repeats), recording each call.
type geminiSequentialMock struct {
	statuses []int
	calls    atomic.Int32
	dim      int
}

func (m *geminiSequentialMock) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		idx := int(m.calls.Add(1)) - 1
		if idx >= len(m.statuses) {
			idx = len(m.statuses) - 1
		}
		status := m.statuses[idx]
		if status != http.StatusOK {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":{"code":429,"message":"Resource exhausted"}}`))
			return
		}
		var req geminiBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resp := geminiBatchResponse{Embeddings: make([]geminiEmbedding, len(req.Requests))}
		for i := range resp.Embeddings {
			resp.Embeddings[i] = geminiEmbedding{Values: make([]float64, m.dim)}
		}
		json.NewEncoder(w).Encode(resp)
	}
}

// openAISequentialMock returns an OpenAI-shaped handler with the same sequence logic.
type openAISequentialMock struct {
	statuses []int
	calls    atomic.Int32
	dim      int
}

func (m *openAISequentialMock) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		idx := int(m.calls.Add(1)) - 1
		if idx >= len(m.statuses) {
			idx = len(m.statuses) - 1
		}
		status := m.statuses[idx]
		if status != http.StatusOK {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
			return
		}
		var req openAIBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data := make([]openAIEmbeddingData, len(req.Input))
		for i := range req.Input {
			data[i] = openAIEmbeddingData{Embedding: make([]float64, m.dim), Index: i}
		}
		json.NewEncoder(w).Encode(openAIEmbeddingResponse{Data: data})
	}
}

// --- Gemini retry tests ---

func TestGeminiEmbedder_RetrySucceedsAfterOneRateLimit(t *testing.T) {
	zeroSleepDelay(t)
	mock := &geminiSequentialMock{statuses: []int{429, 200}, dim: 2}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	e, err := NewGeminiEmbedder("test-key", "test-model", 100, 3, 1)
	if err != nil {
		t.Fatalf("NewGeminiEmbedder: %v", err)
	}
	e.client = &http.Client{Transport: rewriteTransport{to: srv.URL}}

	out, err := e.EmbedBatch([]string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("output length: got %d want 2", len(out))
	}
	if got := int(mock.calls.Load()); got != 2 {
		t.Errorf("server calls: got %d want 2", got)
	}
}

func TestGeminiEmbedder_RetrySucceedsAfterTwoRateLimits(t *testing.T) {
	zeroSleepDelay(t)
	mock := &geminiSequentialMock{statuses: []int{429, 429, 200}, dim: 2}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	e, err := NewGeminiEmbedder("test-key", "test-model", 100, 3, 1)
	if err != nil {
		t.Fatalf("NewGeminiEmbedder: %v", err)
	}
	e.client = &http.Client{Transport: rewriteTransport{to: srv.URL}}

	out, err := e.EmbedBatch([]string{"x"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("output length: got %d want 1", len(out))
	}
	if got := int(mock.calls.Load()); got != 3 {
		t.Errorf("server calls: got %d want 3", got)
	}
}

func TestGeminiEmbedder_RetryExhaustsAndReturnsError(t *testing.T) {
	zeroSleepDelay(t)
	mock := &geminiSequentialMock{statuses: []int{429}, dim: 2}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	e, err := NewGeminiEmbedder("test-key", "test-model", 100, 2, 1)
	if err != nil {
		t.Fatalf("NewGeminiEmbedder: %v", err)
	}
	e.client = &http.Client{Transport: rewriteTransport{to: srv.URL}}

	_, err = e.EmbedBatch([]string{"x"})
	if err == nil {
		t.Fatal("expected error after retry exhaustion, got nil")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Errorf("error should mention 429; got: %v", err)
	}
	// 1 initial attempt + 2 retries = 3 total
	if got := int(mock.calls.Load()); got != 3 {
		t.Errorf("server calls: got %d want 3 (1 + 2 retries)", got)
	}
}

func TestGeminiEmbedder_RetryDisabledByZeroMaxRetries(t *testing.T) {
	mock := &geminiSequentialMock{statuses: []int{429}, dim: 2}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	e, err := NewGeminiEmbedder("test-key", "test-model", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewGeminiEmbedder: %v", err)
	}
	e.client = &http.Client{Transport: rewriteTransport{to: srv.URL}}

	_, err = e.EmbedBatch([]string{"x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := int(mock.calls.Load()); got != 1 {
		t.Errorf("server calls: got %d want 1 (retry disabled)", got)
	}
}

// --- OpenAI retry tests ---

func TestOpenAIEmbedder_RetrySucceedsAfterOneRateLimit(t *testing.T) {
	zeroSleepDelay(t)
	mock := &openAISequentialMock{statuses: []int{429, 200}, dim: 2}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 3, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	out, err := e.EmbedBatch([]string{"a", "b"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("output length: got %d want 2", len(out))
	}
	if got := int(mock.calls.Load()); got != 2 {
		t.Errorf("server calls: got %d want 2", got)
	}
}

func TestOpenAIEmbedder_RetryExhaustsAndReturnsError(t *testing.T) {
	zeroSleepDelay(t)
	mock := &openAISequentialMock{statuses: []int{429}, dim: 2}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 1, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	_, err = e.EmbedBatch([]string{"x"})
	if err == nil {
		t.Fatal("expected error after retry exhaustion, got nil")
	}
	// 1 initial + 1 retry = 2 total
	if got := int(mock.calls.Load()); got != 2 {
		t.Errorf("server calls: got %d want 2", got)
	}
}

func TestOpenAIEmbedder_RetryDisabledByZeroMaxRetries(t *testing.T) {
	mock := &openAISequentialMock{statuses: []int{429}, dim: 2}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 100, 0, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}
	_, err = e.EmbedBatch([]string{"x"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := int(mock.calls.Load()); got != 1 {
		t.Errorf("server calls: got %d want 1 (retry disabled)", got)
	}
}

// --- Retry-After header test ---

func TestRetryOn429_RespectsRetryAfterHeader(t *testing.T) {
	var recordedSleep time.Duration
	origSleep := retrySleep
	retrySleep = func(d time.Duration) { recordedSleep = d }
	defer func() { retrySleep = origSleep }()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}"))
	}))
	defer srv.Close()

	resp, _, err := retryOn429(1, func() (*http.Response, error) {
		return http.Get(srv.URL)
	})
	if err != nil {
		t.Fatalf("retryOn429: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("final status: got %d want 200", resp.StatusCode)
	}
	if recordedSleep != 5*time.Second {
		t.Errorf("sleep duration: got %v want 5s (from Retry-After header)", recordedSleep)
	}
}

// --- jitteredDelay unit tests ---

func TestJitteredDelay_RetryAfterOverrides(t *testing.T) {
	d := jitteredDelay(10*time.Second, "7")
	if d != 7*time.Second {
		t.Errorf("got %v want 7s", d)
	}
}

func TestJitteredDelay_InvalidRetryAfterFallsBackToJitter(t *testing.T) {
	d := jitteredDelay(4*time.Second, "not-a-number")
	if d < 0 || d >= 4*time.Second {
		t.Errorf("jitter out of range: got %v, want in [0, 4s)", d)
	}
}

func TestJitteredDelay_ZeroDelayReturnsZero(t *testing.T) {
	d := jitteredDelay(0, "")
	if d != 0 {
		t.Errorf("got %v want 0", d)
	}
}

func TestJitteredDelay_NegativeDelayReturnsZero(t *testing.T) {
	d := jitteredDelay(-time.Second, "")
	if d != 0 {
		t.Errorf("got %v want 0", d)
	}
}

// --- Backoff progression test ---

func TestRetryOn429_BackoffProgression(t *testing.T) {
	// Patch only retrySleep; retryBaseDelay is intentionally left at its real
	// 1s value to exercise the delay = min(delay*2, 30s) doubling path with a
	// non-zero starting value (all existing tests zero retryBaseDelay out).
	var sleeps []time.Duration
	origSleep := retrySleep
	retrySleep = func(d time.Duration) { sleeps = append(sleeps, d) }
	defer func() { retrySleep = origSleep }()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	resp, _, err := retryOn429(3, func() (*http.Response, error) {
		return http.Get(srv.URL)
	})
	if err != nil {
		t.Fatalf("retryOn429: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("final status: got %d want 200", resp.StatusCode)
	}
	if len(sleeps) != 3 {
		t.Fatalf("retrySleep call count: got %d want 3", len(sleeps))
	}
	// delay starts at 1s and doubles each retry: caps are 1s, 2s, 4s.
	// jitteredDelay draws from [0, cap), so each recorded sleep must be < cap.
	caps := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	for i, cap := range caps {
		if sleeps[i] >= cap {
			t.Errorf("sleep[%d] = %v: want < %v (cap doubled from previous)", i, sleeps[i], cap)
		}
	}
}

// --- Multi-wave 429 tests ---

func TestGeminiEmbedder_MultiWave_Wave2RetriesAfter429(t *testing.T) {
	zeroSleepDelay(t)
	// 3 calls: wave-1 → 200, wave-2 attempt-1 → 429, wave-2 retry → 200.
	mock := &geminiSequentialMock{statuses: []int{200, 429, 200}, dim: 2}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	// batchSize=2, 3 inputs → wave-1: texts[0:2], wave-2: texts[2:3].
	e, err := NewGeminiEmbedder("test-key", "test-model", 2, 1, 1)
	if err != nil {
		t.Fatalf("NewGeminiEmbedder: %v", err)
	}
	e.client = &http.Client{Transport: rewriteTransport{to: srv.URL}}

	out, err := e.EmbedBatch([]string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("output length: got %d want 3", len(out))
	}
	if got := int(mock.calls.Load()); got != 3 {
		t.Errorf("server calls: got %d want 3 (wave-1 ok + wave-2 429 + wave-2 retry ok)", got)
	}
}

func TestOpenAIEmbedder_MultiWave_Wave2RetriesAfter429(t *testing.T) {
	zeroSleepDelay(t)
	// 3 calls: wave-1 → 200, wave-2 attempt-1 → 429, wave-2 retry → 200.
	mock := &openAISequentialMock{statuses: []int{200, 429, 200}, dim: 2}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	// batchSize=2, 3 inputs → wave-1: texts[0:2], wave-2: texts[2:3].
	e, err := NewOpenAIEmbedder(srv.URL, "test-model", "", 2, 1, 1)
	if err != nil {
		t.Fatalf("NewOpenAIEmbedder: %v", err)
	}

	out, err := e.EmbedBatch([]string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("output length: got %d want 3", len(out))
	}
	if got := int(mock.calls.Load()); got != 3 {
		t.Errorf("server calls: got %d want 3 (wave-1 ok + wave-2 429 + wave-2 retry ok)", got)
	}
}
