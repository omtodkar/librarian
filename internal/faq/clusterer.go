package faq

import (
	"fmt"
	"math"

	"librarian/internal/embedding"
)

// cosineSimilarity returns the cosine similarity between two equal-length
// vectors. Returns 0 if either vector has zero magnitude or lengths differ.
func cosineSimilarity(a, b []float64) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, magA, magB float64
	for i := range a {
		dot += a[i] * b[i]
		magA += a[i] * a[i]
		magB += b[i] * b[i]
	}
	if magA == 0 || magB == 0 {
		return 0
	}
	return dot / (math.Sqrt(magA) * math.Sqrt(magB))
}

// Cluster groups sources into clusters of near-duplicates. Two sources are
// placed in the same cluster when their question embeddings have cosine
// similarity >= threshold. Uses a greedy single-pass algorithm: the first
// unassigned source starts a new cluster and absorbs all subsequent sources
// that are similar to it.
//
// Each cluster in the returned slice has at least one element. The slice is
// empty (not nil) when sources is empty.
func Cluster(sources []Source, embedder embedding.Embedder, threshold float64) ([][]Source, error) {
	if len(sources) == 0 {
		return [][]Source{}, nil
	}

	texts := make([]string, len(sources))
	for i, s := range sources {
		texts[i] = s.Text
	}

	vecs, err := embedder.EmbedBatch(texts)
	if err != nil {
		return nil, fmt.Errorf("embedding sources: %w", err)
	}
	if len(vecs) != len(sources) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d sources", len(vecs), len(sources))
	}

	assigned := make([]bool, len(sources))
	var clusters [][]Source

	for i := range sources {
		if assigned[i] {
			continue
		}
		// Start a new cluster anchored at sources[i].
		cluster := []Source{sources[i]}
		assigned[i] = true
		for j := i + 1; j < len(sources); j++ {
			if assigned[j] {
				continue
			}
			if cosineSimilarity(vecs[i], vecs[j]) >= threshold {
				cluster = append(cluster, sources[j])
				assigned[j] = true
			}
		}
		clusters = append(clusters, cluster)
	}

	return clusters, nil
}
