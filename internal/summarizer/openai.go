package summarizer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAISummarizer calls an OpenAI-compatible chat completions API for
// text summarization (LM Studio, Ollama, vLLM, OpenAI proper, etc.).
type OpenAISummarizer struct {
	baseURL string
	model   string
	apiKey  string
	client  *http.Client
}

// NewOpenAISummarizer creates an OpenAISummarizer. baseURL defaults to
// http://localhost:1234/v1 if empty.
func NewOpenAISummarizer(baseURL, model, apiKey string) (*OpenAISummarizer, error) {
	if baseURL == "" {
		baseURL = "http://localhost:1234/v1"
	}
	baseURL = strings.TrimRight(baseURL, "/")
	if model == "" {
		return nil, fmt.Errorf("summarization model is required for openai provider: set summarization.model in .librarian/config.yaml")
	}
	return &OpenAISummarizer{
		baseURL: baseURL,
		model:   model,
		apiKey:  apiKey,
		client:  &http.Client{},
	}, nil
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Error   *openAIError `json:"error,omitempty"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func (s *OpenAISummarizer) Summarize(text string) (string, error) {
	url := s.baseURL + "/chat/completions"

	reqBody := chatRequest{
		Model: s.model,
		Messages: []chatMessage{
			{
				Role:    "system",
				Content: "You are a documentation summarizer. Respond with exactly 1-2 sentences summarizing the key point. Be concise and specific.",
			},
			{
				Role:    "user",
				Content: text,
			},
		},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshaling request: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("calling chat completions API: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("chat completions API error (status %d): %s", resp.StatusCode, string(respBytes))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBytes, &chatResp); err != nil {
		return "", fmt.Errorf("parsing response: %w", err)
	}
	if chatResp.Error != nil {
		return "", fmt.Errorf("chat completions API error: %s", chatResp.Error.Message)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("chat completions API returned no choices")
	}

	return strings.TrimSpace(chatResp.Choices[0].Message.Content), nil
}
