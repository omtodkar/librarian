package summarizer

import (
	"fmt"

	"librarian/internal/config"
)

// New creates a Summarizer from config. Returns Noop when provider is empty
// or "disabled" — summarization is always optional.
func New(cfg config.SummarizationConfig) (Summarizer, error) {
	switch cfg.Provider {
	case "", "disabled":
		return Noop{}, nil
	case "gemini":
		return NewGeminiSummarizer(cfg.APIKey, cfg.Model)
	case "openai":
		return NewOpenAISummarizer(cfg.BaseURL, cfg.Model, cfg.APIKey)
	default:
		return nil, fmt.Errorf("unknown summarization provider %q: supported providers are \"gemini\", \"openai\", and \"disabled\"", cfg.Provider)
	}
}
