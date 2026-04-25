package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAIEmbedder calls an OpenAI-compatible embeddings API (LM Studio, Ollama, vLLM, etc.).
type OpenAIEmbedder struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

// NewOpenAIEmbedder creates an OpenAIEmbedder. baseURL defaults to
// http://localhost:1234/v1 (LM Studio's default) if empty.
func NewOpenAIEmbedder(baseURL, model, apiKey string) (*OpenAIEmbedder, error) {
	if baseURL == "" {
		baseURL = "http://localhost:1234/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if model == "" {
		return nil, fmt.Errorf("embedding model is required for openai provider: set embedding.model in .librarian/config.yaml")
	}
	return &OpenAIEmbedder{
		baseURL: baseURL,
		model:   model,
		apiKey:  apiKey,
		client:  &http.Client{},
	}, nil
}

type openAIEmbeddingRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
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
