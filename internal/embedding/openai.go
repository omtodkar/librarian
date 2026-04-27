package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// openaiBatchMax is the absolute ceiling for OpenAI-compatible batch calls.
// OpenAI proper documents 2048 per request for text-embedding-3-* models;
// some self-hosted servers (LM Studio, Ollama, vLLM) are more conservative.
// The config-level default stays at 100 via defaultBatchSize so local
// servers are safe out of the box; power users on OpenAI can raise it.
const openaiBatchMax = 2048

// OpenAIEmbedder calls an OpenAI-compatible embeddings API (LM Studio, Ollama, vLLM, etc.).
type OpenAIEmbedder struct {
	baseURL            string
	model              string
	apiKey             string
	batchSize          int
	maxRetries         int
	maxParallelBatches int
	batchFallback      bool
	client             *http.Client
}

// NewOpenAIEmbedder creates an OpenAIEmbedder. baseURL defaults to
// http://localhost:1234/v1 (LM Studio's default) if empty. batchSize <= 0
// resolves to defaultBatchSize; values above openaiBatchMax clamp down.
// maxRetries controls 429 retry behavior (0 = disabled).
// maxParallelBatches controls wave concurrency (<=1 = serial).
func NewOpenAIEmbedder(baseURL, model, apiKey string, batchSize, maxRetries, maxParallelBatches int) (*OpenAIEmbedder, error) {
	if baseURL == "" {
		baseURL = "http://localhost:1234/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if model == "" {
		return nil, fmt.Errorf("embedding model is required for openai provider: set embedding.model in .librarian/config.yaml")
	}
	return &OpenAIEmbedder{
		baseURL:            baseURL,
		model:              model,
		apiKey:             apiKey,
		batchSize:          resolveBatchSize(batchSize, openaiBatchMax),
		maxRetries:         maxRetries,
		maxParallelBatches: maxParallelBatches,
		client:             &http.Client{},
	}, nil
}

type openAIEmbeddingRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// openAIBatchRequest differs from the single form only in Input type — keep
// the types separate so the single-query Embed path doesn't carry array
// ceremony.
type openAIBatchRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbeddingResponse struct {
	Data  []openAIEmbeddingData `json:"data"`
	Error *openAIError          `json:"error,omitempty"`
}

type openAIEmbeddingData struct {
	Embedding []float64    `json:"embedding"`
	Index     int          `json:"index"`
	Error     *openAIError `json:"error,omitempty"` // per-item error in partial-success responses
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Model returns the configured model string (constructor required non-empty).
func (e *OpenAIEmbedder) Model() string { return e.model }

// Embed embeds a single text. Single-query path is interactive; 429 surfaces
// immediately to the caller rather than retrying with backoff.
func (e *OpenAIEmbedder) Embed(text string) ([]float64, error) {
	url := e.baseURL + "/embeddings"

	reqBody := openAIEmbeddingRequest{
		Model: e.model,
		Input: text,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling embeddings API: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings API error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	var oaiResp openAIEmbeddingResponse
	if err := json.Unmarshal(respBytes, &oaiResp); err != nil {
		return nil, fmt.Errorf("parsing response: %w", err)
	}

	if oaiResp.Error != nil {
		return nil, fmt.Errorf("embeddings API error: %s", oaiResp.Error.Message)
	}

	if len(oaiResp.Data) == 0 || len(oaiResp.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embeddings API returned empty embedding")
	}

	return oaiResp.Data[0].Embedding, nil
}

// EmbedBatch sends up to batchSize texts per HTTP call. Unlike Gemini, the
// OpenAI spec allows data[] to arrive in arbitrary order with an `index` field
// marking the original position — we write each datum directly by that index
// so the output slice maps 1:1 to the input without a sort pass. When
// maxParallelBatches > 1, waves execute concurrently; a failed wave does not
// cancel running siblings.
func (e *OpenAIEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return [][]float64{}, nil
	}
	url := e.baseURL + "/embeddings"

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
		bodyBytes, err := json.Marshal(openAIBatchRequest{Model: e.model, Input: wave})
		if err != nil {
			return fmt.Errorf("marshaling batch request: %w", err)
		}
		resp, respBytes, err := retryOn429(e.maxRetries, func() (*http.Response, error) {
			req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
			if err != nil {
				return nil, fmt.Errorf("creating batch request: %w", err)
			}
			req.Header.Set("Content-Type", "application/json")
			if e.apiKey != "" {
				req.Header.Set("Authorization", "Bearer "+e.apiKey)
			}
			return e.client.Do(req)
		})
		if err != nil {
			// Network-level failure: not a fallback trigger.
			return fmt.Errorf("calling batch embeddings API: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			if e.batchFallback && is4xxFallback(resp.StatusCode) {
				fallbackItems(e.Embed, wave, start, out)
				return nil
			}
			return fmt.Errorf("batch embeddings API error (status %d): %s", resp.StatusCode, string(respBytes))
		}
		var batchResp openAIEmbeddingResponse
		if err := json.Unmarshal(respBytes, &batchResp); err != nil {
			return fmt.Errorf("parsing batch response: %w", err)
		}
		if batchResp.Error != nil {
			return fmt.Errorf("batch embeddings API error: %s", batchResp.Error.Message)
		}
		if !e.batchFallback {
			// Strict mode: exact count required, every embedding must be valid.
			if len(batchResp.Data) != len(wave) {
				return fmt.Errorf("batch API returned %d embeddings for %d inputs", len(batchResp.Data), len(wave))
			}
			for _, d := range batchResp.Data {
				if d.Index < 0 || d.Index >= len(wave) {
					return fmt.Errorf("batch embeddings API returned out-of-range index %d (wave size %d)", d.Index, len(wave))
				}
				if len(d.Embedding) == 0 {
					return fmt.Errorf("batch embeddings API returned empty embedding at index %d", start+d.Index)
				}
				if out[start+d.Index] != nil {
					return fmt.Errorf("batch embeddings API returned duplicate index %d", d.Index)
				}
				out[start+d.Index] = d.Embedding
			}
			return nil
		}
		// Fallback-enabled: populate valid items; leave nil slots for failed ones.
		// OpenAI's `index` field maps each datum to its wave-relative position.
		for _, d := range batchResp.Data {
			if d.Index < 0 || d.Index >= len(wave) {
				continue // out-of-range index: fallbackItems will handle the nil slot
			}
			if d.Error != nil || len(d.Embedding) == 0 {
				continue // failed item: leave slot nil for fallbackItems
			}
			out[start+d.Index] = d.Embedding
		}
		// If any slot is nil (missing, errored, or empty), fall back per-item.
		needsFallback := false
		for i := start; i < end; i++ {
			if out[i] == nil {
				needsFallback = true
				break
			}
		}
		if needsFallback {
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
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("batch wave panic: %v", r)
					}
					mu.Unlock()
				}
			}()
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
