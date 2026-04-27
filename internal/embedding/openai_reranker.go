package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIReranker calls any OpenAI-compatible /rerank endpoint (Infinity,
// LM Studio, etc.). Infinity serves /rerank without a /v1/ prefix — set
// base_url without the /v1 suffix when pointing at Infinity.
type OpenAIReranker struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

// NewOpenAIReranker creates an OpenAIReranker. baseURL defaults to
// http://localhost:7997 (Infinity default) if empty. model is required.
// timeoutMs <= 0 resolves to 3000 ms.
func NewOpenAIReranker(baseURL, model, apiKey string, timeoutMs int) (*OpenAIReranker, error) {
	if baseURL == "" {
		baseURL = "http://localhost:7997"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if model == "" {
		return nil, fmt.Errorf("rerank model is required for openai provider: set rerank.model in .librarian/config.yaml")
	}
	timeout := 3000 * time.Millisecond
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	return &OpenAIReranker{
		baseURL: baseURL,
		model:   model,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: timeout},
	}, nil
}

type openAIRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type openAIRerankResponse struct {
	Results []openAIRerankResult `json:"results"`
	Error   *openAIError         `json:"error,omitempty"`
}

type openAIRerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// Model returns the configured model string (constructor required non-empty).
func (r *OpenAIReranker) Model() string { return r.model }

// Rerank sends documents to the /rerank endpoint and returns one score per
// document in input order. The API may return results out of order; the index
// field maps each result back to the original position.
func (r *OpenAIReranker) Rerank(query string, documents []string) ([]float64, error) {
	if len(documents) == 0 {
		return nil, nil
	}

	url := r.baseURL + "/rerank"

	bodyBytes, err := json.Marshal(openAIRerankRequest{
		Model:     r.model,
		Query:     query,
		Documents: documents,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling rerank request: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("creating rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling rerank API: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading rerank response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rerank API error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	var rerankResp openAIRerankResponse
	if err := json.Unmarshal(respBytes, &rerankResp); err != nil {
		return nil, fmt.Errorf("parsing rerank response: %w", err)
	}

	if rerankResp.Error != nil {
		return nil, fmt.Errorf("rerank API error: %s", rerankResp.Error.Message)
	}

	if len(rerankResp.Results) != len(documents) {
		return nil, fmt.Errorf("rerank API returned %d results for %d documents", len(rerankResp.Results), len(documents))
	}

	// Map back to input order via the index field; guard against duplicates.
	scores := make([]float64, len(documents))
	written := make([]bool, len(documents))
	for _, result := range rerankResp.Results {
		if result.Index < 0 || result.Index >= len(documents) {
			return nil, fmt.Errorf("rerank API returned out-of-range index %d (len=%d)", result.Index, len(documents))
		}
		if written[result.Index] {
			return nil, fmt.Errorf("rerank API returned duplicate index %d", result.Index)
		}
		scores[result.Index] = result.RelevanceScore
		written[result.Index] = true
	}
	return scores, nil
}
