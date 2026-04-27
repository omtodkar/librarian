package embedding

import (
	"fmt"

	"librarian/internal/config"
)

// NewEmbedder creates an Embedder based on the provider in the config.
func NewEmbedder(cfg config.EmbeddingConfig) (Embedder, error) {
	switch cfg.Provider {
	case "gemini":
		e, err := NewGeminiEmbedder(cfg.APIKey, cfg.Model, cfg.BatchSize, cfg.MaxRetries, cfg.MaxParallelBatches)
		if err != nil {
			return nil, err
		}
		e.batchFallback = cfg.BatchFallback
		return e, nil
	case "openai":
		e, err := NewOpenAIEmbedder(cfg.BaseURL, cfg.Model, cfg.APIKey, cfg.BatchSize, cfg.MaxRetries, cfg.MaxParallelBatches)
		if err != nil {
			return nil, err
		}
		e.batchFallback = cfg.BatchFallback
		return e, nil
	default:
		return nil, fmt.Errorf("unknown embedding provider %q: supported providers are \"gemini\" and \"openai\"", cfg.Provider)
	}
}

// NewReranker creates a Reranker based on the provider in the config.
// Returns (nil, nil) when cfg.Provider is empty — callers check for nil to
// determine whether reranking is enabled.
func NewReranker(cfg config.RerankConfig) (Reranker, error) {
	if cfg.Provider == "" {
		return nil, nil
	}
	switch cfg.Provider {
	case "openai":
		return NewOpenAIReranker(cfg.BaseURL, cfg.Model, cfg.APIKey, cfg.TimeoutMs)
	default:
		return nil, fmt.Errorf("unknown rerank provider %q: supported providers are \"openai\"", cfg.Provider)
	}
}
