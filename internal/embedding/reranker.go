package embedding

// Reranker scores (query, document) pairs jointly in one forward pass —
// capturing token-level interactions the bi-encoder cannot. Model() returns
// the resolved model identifier so callers can log or validate it.
type Reranker interface {
	Rerank(query string, documents []string) ([]float64, error)
	Model() string
}
