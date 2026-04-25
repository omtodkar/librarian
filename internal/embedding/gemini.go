package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Embedder generates vector embeddings from text. Model() returns the
// resolved model identifier (after any default fallback) so the store layer
// can detect config-level model swaps that would otherwise corrupt the vec0
// index — see internal/store/store.go's ensureVecTable.
type Embedder interface {
	Embed(text string) ([]float64, error)
	Model() string
}

// defaultGeminiModel is the current recommended Gemini embedding model —
// multimodal, 3072-dim by default. Used when config doesn't pin a model.
const defaultGeminiModel = "gemini-embedding-2"

// GeminiEmbedder calls the Gemini :embedContent API.
type GeminiEmbedder struct {
	apiKey string
	model  string
	client *http.Client
}

// NewGeminiEmbedder creates a GeminiEmbedder. Model defaults to
// defaultGeminiModel when empty. apiKey falls back to GEMINI_API_KEY.
func NewGeminiEmbedder(apiKey, model string) (*GeminiEmbedder, error) {
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
		apiKey: apiKey,
		model:  model,
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

// Model returns the resolved model string (after the defaultGeminiModel
// fallback applied in the constructor).
func (e *GeminiEmbedder) Model() string { return e.model }

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
