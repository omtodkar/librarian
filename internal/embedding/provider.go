package embedding

import (
	"fmt"

	"librarian/internal/config"
)

// NewEmbedder creates an Embedder based on the provider in the config.
func NewEmbedder(cfg config.EmbeddingConfig) (Embedder, error) {
	switch cfg.Provider {
	case "gemini":
		return NewGeminiEmbedder(cfg.APIKey, cfg.Model, cfg.BatchSize)
	case "openai":
		return NewOpenAIEmbedder(cfg.BaseURL, cfg.Model, cfg.APIKey, cfg.BatchSize)
	default:
		return nil, fmt.Errorf("unknown embedding provider %q: supported providers are \"gemini\" and \"openai\"", cfg.Provider)
	}
}
