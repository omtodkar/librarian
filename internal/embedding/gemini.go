package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

// Embedder generates vector embeddings from text. Model() returns the
// resolved model identifier (after any default fallback) so the store layer
// can detect config-level model swaps that would otherwise corrupt the vec0
// index — see internal/store/store.go's ensureVecTable.
//
// EmbedBatch is the hot-path method used by the indexer: one HTTP round trip
// per N chunks instead of one per chunk. Implementations are responsible for
// preserving input order in the returned slice regardless of how the
// underlying API orders its response. Embed remains for single-query use
// (search / MCP query embedding) where wrapping through a 1-element batch
// would add allocations for no gain.
type Embedder interface {
	Embed(text string) ([]float64, error)
	EmbedBatch(texts []string) ([][]float64, error)
	Model() string
}

// defaultGeminiModel is the current recommended Gemini embedding model —
// multimodal, 3072-dim by default. Used when config doesn't pin a model.
const defaultGeminiModel = "gemini-embedding-2"

// defaultBatchSize is the fallback when config leaves embedding.batch_size
// at zero. 100 matches Gemini's documented batchEmbedContents limit and
// stays comfortably under every compliant OpenAI-style server's cap.
const defaultBatchSize = 100

// geminiBatchMax is Gemini's hard ceiling on batchEmbedContents items per
// call. A configured batch_size above this is silently clamped.
const geminiBatchMax = 100

// GeminiEmbedder calls the Gemini :embedContent / :batchEmbedContents APIs.
type GeminiEmbedder struct {
	apiKey             string
	model              string
	batchSize          int
	maxRetries         int
	maxParallelBatches int
	batchFallback      bool
	client             *http.Client
}

// NewGeminiEmbedder creates a GeminiEmbedder. Model defaults to
// defaultGeminiModel when empty. apiKey falls back to GEMINI_API_KEY.
// batchSize <= 0 resolves to defaultBatchSize; values above geminiBatchMax
// clamp down. maxRetries controls 429 retry behavior (0 = disabled).
// maxParallelBatches controls wave concurrency (<=1 = serial).
func NewGeminiEmbedder(apiKey, model string, batchSize, maxRetries, maxParallelBatches int) (*GeminiEmbedder, error) {
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("Gemini API key is required: set embedding.api_key in .librarian/config.yaml or GEMINI_API_KEY env var")
	}
	if model == "" {
		model = defaultGeminiModel
	}
	return &GeminiEmbedder{
		apiKey:             apiKey,
		model:              model,
		batchSize:          resolveBatchSize(batchSize, geminiBatchMax),
		maxRetries:         maxRetries,
		maxParallelBatches: maxParallelBatches,
		client:             &http.Client{},
	}, nil
}

// resolveBatchSize applies the default/clamp policy shared by both providers:
// zero or negative → default (100); anything above hardMax → hardMax.
// Extracted so TestBatchSizeResolution can pin the contract once.
func resolveBatchSize(configured, hardMax int) int {
	if configured <= 0 {
		return defaultBatchSize
	}
	if configured > hardMax {
		return hardMax
	}
	return configured
}

type geminiRequest struct {
	Content geminiContent `json:"content"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiResponse struct {
	Embedding geminiEmbedding `json:"embedding"`
	Error     *geminiError    `json:"error,omitempty"`
}

type geminiEmbedding struct {
	Values []float64 `json:"values"`
}

type geminiError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// geminiBatchRequest wraps N single-embed requests for batchEmbedContents.
// Each inner request includes the fully-qualified "models/<name>" string
// per the API spec.
type geminiBatchRequest struct {
	Requests []geminiBatchItem `json:"requests"`
}

type geminiBatchItem struct {
	Model   string        `json:"model"`
	Content geminiContent `json:"content"`
}

type geminiBatchResponse struct {
	Embeddings []geminiEmbedding `json:"embeddings"`
	Error      *geminiError      `json:"error,omitempty"`
}

// Model returns the resolved model string (after the defaultGeminiModel
// fallback applied in the constructor).
func (e *GeminiEmbedder) Model() string { return e.model }

// Embed embeds a single text. Single-query path is interactive; 429 surfaces
// immediately to the caller rather than retrying with backoff.
func (e *GeminiEmbedder) Embed(text string) ([]float64, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/" + e.model + ":embedContent?key=" + e.apiKey

	reqBody := geminiRequest{
		Content: geminiContent{
			Parts: []geminiPart{{Text: text}},
		},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	resp, err := e.client.Post(url, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("calling Gemini API: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Gemini API error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	var geminiResp geminiResponse
	if err := json.Unmarshal(respBytes, &geminiResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if geminiResp.Error != nil {
		return nil, fmt.Errorf("Gemini API error: %s", geminiResp.Error.Message)
	}

	if len(geminiResp.Embedding.Values) == 0 {
		return nil, fmt.Errorf("Gemini API returned empty embedding")
	}

	return geminiResp.Embedding.Values, nil
}

// EmbedBatch sends up to batchSize texts per HTTP call to :batchEmbedContents.
// Input order is preserved in the returned slice. When maxParallelBatches > 1,
// waves execute concurrently (bounded by that limit); per-wave error isolation
// means a failed wave does not cancel already-running siblings — all started
// waves complete before the first error is returned.
func (e *GeminiEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return [][]float64{}, nil
	}
	url := "https://generativelanguage.googleapis.com/v1beta/models/" + e.model + ":batchEmbedContents?key=" + e.apiKey
	modelRef := "models/" + e.model

	out := make([][]float64, len(texts))

	type span struct{ start, end int }
	var spans []span
	for s := 0; s < len(texts); s += e.batchSize {
		en := s + e.batchSize
		if en > len(texts) {
			en = len(texts)
		}
		spans = append(spans, span{s, en})
	}

	doWave := func(start, end int) error {
		wave := texts[start:end]
		reqBody := geminiBatchRequest{Requests: make([]geminiBatchItem, len(wave))}
		for i, t := range wave {
			reqBody.Requests[i] = geminiBatchItem{
				Model:   modelRef,
				Content: geminiContent{Parts: []geminiPart{{Text: t}}},
			}
		}
		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshaling batch request: %w", err)
		}
		resp, respBytes, err := retryOn429(e.maxRetries, func() (*http.Response, error) {
			return e.client.Post(url, "application/json", bytes.NewReader(bodyBytes))
		})
		if err != nil {
			// Network-level failure: not a fallback trigger.
			return fmt.Errorf("calling Gemini batch API: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			if e.batchFallback && is4xxFallback(resp.StatusCode) {
				fallbackItems(e.Embed, wave, start, out)
				return nil
			}
			return fmt.Errorf("Gemini batch API error (status %d): %s", resp.StatusCode, string(respBytes))
		}
		var batchResp geminiBatchResponse
		if err := json.Unmarshal(respBytes, &batchResp); err != nil {
			return fmt.Errorf("parsing batch response: %w", err)
		}
		if batchResp.Error != nil {
			return fmt.Errorf("Gemini batch API error: %s", batchResp.Error.Message)
		}
		if len(batchResp.Embeddings) != len(wave) {
			if e.batchFallback {
				fallbackItems(e.Embed, wave, start, out)
				return nil
			}
			return fmt.Errorf("Gemini batch API returned %d embeddings for %d inputs", len(batchResp.Embeddings), len(wave))
		}
		// Populate successful items; detect partial-success 200 when fallback enabled.
		hasMixed := false
		for i, emb := range batchResp.Embeddings {
			if len(emb.Values) == 0 {
				if !e.batchFallback {
					return fmt.Errorf("Gemini batch API returned empty embedding at index %d", start+i)
				}
				hasMixed = true
				continue
			}
			out[start+i] = emb.Values
		}
		if hasMixed {
			// Nil slots remain for items with empty Values; fall back per-item.
			fallbackItems(e.Embed, wave, start, out)
		}
		return nil
	}

	if e.maxParallelBatches <= 1 {
		for _, sp := range spans {
			if err := doWave(sp.start, sp.end); err != nil {
				return nil, err
			}
		}
		return out, nil
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	sem := make(chan struct{}, e.maxParallelBatches)
	for _, sp := range spans {
		sp := sp
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := doWave(sp.start, sp.end); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}
