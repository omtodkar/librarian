package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
)

// openaiBatchMax is the absolute ceiling for OpenAI-compatible batch calls.
// OpenAI proper documents 2048 per request for text-embedding-3-* models;
// some self-hosted servers (LM Studio, Ollama, vLLM) are more conservative.
// The config-level default stays at 100 via defaultBatchSize so local
// servers are safe out of the box; power users on OpenAI can raise it.
const openaiBatchMax = 2048

// OpenAIEmbedder calls an OpenAI-compatible embeddings API (LM Studio, Ollama, vLLM, etc.).
type OpenAIEmbedder struct {
	baseURL   string
	model     string
	apiKey    string
	batchSize int
	client    *http.Client
}

// NewOpenAIEmbedder creates an OpenAIEmbedder. baseURL defaults to
// http://localhost:1234/v1 (LM Studio's default) if empty. batchSize <= 0
// resolves to defaultBatchSize; values above openaiBatchMax clamp down.
func NewOpenAIEmbedder(baseURL, model, apiKey string, batchSize int) (*OpenAIEmbedder, error) {
	if baseURL == "" {
		baseURL = "http://localhost:1234/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if model == "" {
		return nil, fmt.Errorf("embedding model is required for openai provider: set embedding.model in .librarian/config.yaml")
	}
	return &OpenAIEmbedder{
		baseURL:   baseURL,
		model:     model,
		apiKey:    apiKey,
		batchSize: resolveBatchSize(batchSize, openaiBatchMax),
		client:    &http.Client{},
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
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// Model returns the configured model string (constructor required non-empty).
func (e *OpenAIEmbedder) Model() string { return e.model }

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
// OpenAI spec allows data[] to arrive in arbitrary order with an `index`
// field marking the original position — we sort by that index before
// returning so callers receive a 1:1 mapping to the input slice.
func (e *OpenAIEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return [][]float64{}, nil
	}
	url := e.baseURL + "/embeddings"

	out := make([][]float64, 0, len(texts))
	for start := 0; start < len(texts); start += e.batchSize {
		end := start + e.batchSize
		if end > len(texts) {
			end = len(texts)
		}
		wave := texts[start:end]

		bodyBytes, err := json.Marshal(openAIBatchRequest{Model: e.model, Input: wave})
		if err != nil {
			return nil, fmt.Errorf("marshaling batch request: %w", err)
		}
		req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("creating batch request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if e.apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+e.apiKey)
		}
		resp, err := e.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("calling batch embeddings API: %w", err)
		}
		respBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading batch response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("batch embeddings API error (status %d): %s", resp.StatusCode, string(respBytes))
		}

		var batchResp openAIEmbeddingResponse
		if err := json.Unmarshal(respBytes, &batchResp); err != nil {
			return nil, fmt.Errorf("parsing batch response: %w", err)
		}
		if batchResp.Error != nil {
			return nil, fmt.Errorf("batch embeddings API error: %s", batchResp.Error.Message)
		}
		if len(batchResp.Data) != len(wave) {
			return nil, fmt.Errorf("batch API returned %d embeddings for %d inputs", len(batchResp.Data), len(wave))
		}
		// Spec permits out-of-order data[]; sort by Index so the caller's
		// input order is preserved end-to-end. In practice OpenAI proper
		// returns them sorted, but some self-hosted servers don't.
		sort.Slice(batchResp.Data, func(i, j int) bool {
			return batchResp.Data[i].Index < batchResp.Data[j].Index
		})
		for i, d := range batchResp.Data {
			if len(d.Embedding) == 0 {
				return nil, fmt.Errorf("batch embeddings API returned empty embedding at index %d", start+i)
			}
			out = append(out, d.Embedding)
		}
	}
	return out, nil
}
