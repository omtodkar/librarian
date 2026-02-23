package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Embedder generates vector embeddings from text.
type Embedder interface {
	Embed(text string) ([]float64, error)
}

// GeminiEmbedder calls the Gemini text-embedding-004 API.
type GeminiEmbedder struct {
	apiKey string
	client *http.Client
}

// NewGeminiEmbedder creates a GeminiEmbedder. It uses the provided apiKey,
// falling back to the GEMINI_API_KEY environment variable.
func NewGeminiEmbedder(apiKey string) (*GeminiEmbedder, error) {
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("Gemini API key is required: set embedding.api_key in .librarian.yaml or GEMINI_API_KEY env var")
	}
	return &GeminiEmbedder{
		apiKey: apiKey,
		client: &http.Client{},
	}, nil
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

func (e *GeminiEmbedder) Embed(text string) ([]float64, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-embedding-001:embedContent?key=" + e.apiKey

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
