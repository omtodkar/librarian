package summarizer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const defaultGeminiModel = "gemini-2.0-flash"

const summarizePrompt = "In 1-2 sentences, summarize the key point of the following documentation excerpt. Be concise and specific.\n\n"

// GeminiSummarizer calls the Gemini generateContent API for text summarization.
type GeminiSummarizer struct {
	apiKey string
	model  string
	client *http.Client
}

// NewGeminiSummarizer creates a GeminiSummarizer. Model defaults to
// defaultGeminiModel when empty. apiKey falls back to GEMINI_API_KEY.
func NewGeminiSummarizer(apiKey, model string) (*GeminiSummarizer, error) {
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("Gemini API key is required: set summarization.api_key in .librarian/config.yaml or GEMINI_API_KEY env var")
	}
	if model == "" {
		model = defaultGeminiModel
	}
	return &GeminiSummarizer{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{},
	}, nil
}

type geminiGenerateRequest struct {
	Contents []geminiContent `json:"contents"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerateResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
	Error      *geminiAPIError   `json:"error,omitempty"`
}

type geminiCandidate struct {
	Content geminiContent `json:"content"`
}

type geminiAPIError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

func (s *GeminiSummarizer) Summarize(text string) (string, error) {
	url := "https://generativelanguage.googleapis.com/v1beta/models/" + s.model + ":generateContent?key=" + s.apiKey

	reqBody := geminiGenerateRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: summarizePrompt + text}}},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	resp, err := s.client.Post(url, "application/json", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("calling Gemini generateContent API: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Gemini generateContent API error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	var genResp geminiGenerateResponse
	if err := json.Unmarshal(respBytes, &genResp); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}
	if genResp.Error != nil {
		return "", fmt.Errorf("Gemini API error: %s", genResp.Error.Message)
	}
	if len(genResp.Candidates) == 0 || len(genResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("Gemini generateContent returned empty response")
	}

	return strings.TrimSpace(genResp.Candidates[0].Content.Parts[0].Text), nil
}
