package store

import "strings"

// ApplyTokenBudget filters chunks to those that fit within the token budget.
// Chunks are consumed in rank order (the caller is responsible for pre-sorting);
// the first chunk whose cumulative token count would exceed budget is dropped
// along with all subsequent chunks — no truncation.
//
// budget <= 0 disables filtering and returns chunks unchanged.
// TokenCount from the DB is used when non-zero; otherwise a whitespace-split
// heuristic (words / 0.75, same formula as the indexer's estimateTokens) is
// applied as a fallback for legacy rows that predate token-count storage.
func ApplyTokenBudget(chunks []DocChunk, budget int) []DocChunk {
	if budget <= 0 {
		return chunks
	}
	result := make([]DocChunk, 0, len(chunks))
	var total int
	for _, c := range chunks {
		tc := int(c.TokenCount)
		if tc == 0 {
			tc = approxTokens(c.Content)
		}
		if total+tc > budget {
			break
		}
		total += tc
		result = append(result, c)
	}
	return result
}

// approxTokens mirrors estimateTokens in internal/indexer/chunker.go.
// Duplicated here because the store package cannot import the indexer package
// (circular dependency: indexer → store → indexer).
func approxTokens(text string) int {
	words := len(strings.Fields(text))
	tokens := int(float64(words) / 0.75)
	if tokens == 0 && len(text) > 0 {
		tokens = 1
	}
	return tokens
}
