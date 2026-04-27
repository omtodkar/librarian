package store

import (
	"fmt"
	"strconv"
	"strings"
)

// GetChunkSummariesByHashes returns a map of contentHash → summary for all
// hashes found in summary_cache. Hashes not in the cache are absent from the
// returned map (not an error). Callers use the missing entries to determine
// which summaries still need to be generated.
func (s *Store) GetChunkSummariesByHashes(hashes []string) (map[string]string, error) {
	if len(hashes) == 0 {
		return map[string]string{}, nil
	}

	placeholders := make([]string, len(hashes))
	args := make([]any, len(hashes))
	for i, h := range hashes {
		placeholders[i] = "?"
		args[i] = h
	}

	rows, err := s.db.Query(
		`SELECT content_hash, summary FROM summary_cache WHERE content_hash IN (`+
			strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("get_chunk_summaries_by_hashes: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string, len(hashes))
	for rows.Next() {
		var hash, summary string
		if err := rows.Scan(&hash, &summary); err != nil {
			return nil, fmt.Errorf("get_chunk_summaries_by_hashes scan: %w", err)
		}
		result[hash] = summary
	}
	return result, rows.Err()
}

// UpsertChunkSummary stores a summary keyed by content hash. Safe to call
// multiple times for the same hash — ON CONFLICT updates the summary in place.
func (s *Store) UpsertChunkSummary(contentHash, summary string) error {
	_, err := s.db.Exec(`
		INSERT INTO summary_cache (content_hash, summary) VALUES (?, ?)
		ON CONFLICT(content_hash) DO UPDATE SET summary = excluded.summary`,
		contentHash, summary)
	if err != nil {
		return fmt.Errorf("upsert_chunk_summary: %w", err)
	}
	return nil
}

// GetChunksByIDs returns the full DocChunk rows for the given IDs. IDs not
// found in the store are silently omitted from the result.
func (s *Store) GetChunksByIDs(ids []string) ([]DocChunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	rows, err := s.db.Query(
		`SELECT id, file_path, section_heading, section_hierarchy, chunk_index, content, summary, token_count, signal_meta
		 FROM doc_chunks WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("get_chunks_by_ids: %w", err)
	}
	defer rows.Close()

	var chunks []DocChunk
	for rows.Next() {
		var chunk DocChunk
		var id int64
		if err := rows.Scan(&id, &chunk.FilePath, &chunk.SectionHeading,
			&chunk.SectionHierarchy, &chunk.ChunkIndex, &chunk.Content, &chunk.Summary,
			&chunk.TokenCount, &chunk.SignalMeta); err != nil {
			return nil, fmt.Errorf("get_chunks_by_ids scan: %w", err)
		}
		chunk.ID = strconv.FormatInt(id, 10)
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

