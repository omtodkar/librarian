// Package summarizer provides a text-generation interface for producing
// 1-2 line summaries of documentation chunks. Used by the indexer to
// pre-compute summaries stored in doc_chunks.summary.
package summarizer

// Summarizer generates a short (1-2 sentence) summary for a text chunk.
type Summarizer interface {
	Summarize(text string) (string, error)
}

// Noop returns empty string for every input. Default when no summarization
// provider is configured.
type Noop struct{}

func (Noop) Summarize(_ string) (string, error) { return "", nil }
