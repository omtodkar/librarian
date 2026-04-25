package store

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
)

func (s *Store) AddChunk(input AddChunkInput) (*DocChunk, error) {
	signalMeta := input.SignalMeta
	if signalMeta == "" {
		signalMeta = "{}"
	}

	// Run the (model, dim) mismatch check BEFORE the chunk row is inserted.
	// Otherwise a config-level model swap would leave every doc's first
	// chunk row committed with no vector — invisible to search but visible
	// to GetChunksForDocument.
	if err := s.ensureVecTable(input.Model, len(input.Vector)); err != nil {
		return nil, fmt.Errorf("add_chunk ensure_vec_table: %w", err)
	}

	res, err := s.db.Exec(`
		INSERT INTO doc_chunks (file_path, section_heading, section_hierarchy, chunk_index, content, token_count, doc_id, signal_meta)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		input.FilePath, input.SectionHeading, input.SectionHierarchy,
		input.ChunkIndex, input.Content, input.TokenCount, input.DocID, signalMeta,
	)
	if err != nil {
		return nil, fmt.Errorf("add_chunk: %w", err)
	}

	chunkID, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("add_chunk last_insert_id: %w", err)
	}

	vecBytes := float64sToFloat32Bytes(input.Vector)
	_, err = s.db.Exec(`INSERT INTO doc_chunk_vectors (chunk_id, embedding) VALUES (?, ?)`,
		chunkID, vecBytes)
	if err != nil {
		return nil, fmt.Errorf("add_chunk vector: %w", err)
	}

	return &DocChunk{
		ID:               strconv.FormatInt(chunkID, 10),
		FilePath:         input.FilePath,
		SectionHeading:   input.SectionHeading,
		SectionHierarchy: input.SectionHierarchy,
		ChunkIndex:       input.ChunkIndex,
		Content:          input.Content,
		TokenCount:       input.TokenCount,
		SignalMeta:       signalMeta,
	}, nil
}

type scoredChunk struct {
	chunk      DocChunk
	distance   float64
	finalScore float64
}

func (s *Store) SearchChunks(vector []float64, limit int) ([]DocChunk, error) {
	// vec0 may be absent on a fresh DB, between ClearVectorState and the
	// first AddChunk of a reindex, or in a long-lived process (MCP server)
	// that held an open Store while another process dropped the table.
	// Returning an empty result matches "no matches" semantics instead of
	// surfacing a sqlite "no such table" error.
	if !s.vecTableReady {
		return nil, nil
	}
	vecBytes := float64sToFloat32Bytes(vector)

	// Over-fetch candidates for re-ranking
	fetchLimit := limit * 3
	if fetchLimit < 10 {
		fetchLimit = 10
	}

	rows, err := s.db.Query(`
		SELECT c.id, c.file_path, c.section_heading, c.section_hierarchy, c.chunk_index, c.content, c.token_count, c.signal_meta, v.distance
		FROM doc_chunk_vectors v
		JOIN doc_chunks c ON c.id = v.chunk_id
		WHERE v.embedding MATCH ?
		  AND k = ?
		ORDER BY v.distance`, vecBytes, fetchLimit)
	if err != nil {
		return nil, fmt.Errorf("search_chunks: %w", err)
	}
	defer rows.Close()

	var candidates []scoredChunk
	for rows.Next() {
		var sc scoredChunk
		var id int64
		if err := rows.Scan(&id, &sc.chunk.FilePath, &sc.chunk.SectionHeading,
			&sc.chunk.SectionHierarchy, &sc.chunk.ChunkIndex, &sc.chunk.Content,
			&sc.chunk.TokenCount, &sc.chunk.SignalMeta, &sc.distance); err != nil {
			return nil, fmt.Errorf("search_chunks scan: %w", err)
		}
		sc.chunk.ID = strconv.FormatInt(id, 10)
		candidates = append(candidates, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return rerankWithSignals(candidates, limit), nil
}

func rerankWithSignals(candidates []scoredChunk, limit int) []DocChunk {
	for i := range candidates {
		vectorScore := 1.0 - candidates[i].distance
		boost := computeMetadataBoost(candidates[i].chunk.SignalMeta)
		candidates[i].finalScore = 0.90*vectorScore + 0.10*boost
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].finalScore > candidates[j].finalScore
	})

	if len(candidates) > limit {
		candidates = candidates[:limit]
	}

	chunks := make([]DocChunk, len(candidates))
	for i, sc := range candidates {
		chunks[i] = sc.chunk
	}
	return chunks
}

func computeMetadataBoost(signalMetaJSON string) float64 {
	if signalMetaJSON == "" || signalMetaJSON == "{}" {
		return 0.0
	}

	// Fields mirror the JSON shape emitted by SignalsToJSON (signals.go).
	// Extending this struct is safe; unknown keys are ignored by Unmarshal.
	var signals struct {
		InlineLabels []string `json:"inline_labels"`
		RiskMarkers  []string `json:"risk_markers"`
		Todos        []string `json:"todos"`
		Rationale    []string `json:"rationale"`
	}
	if err := json.Unmarshal([]byte(signalMetaJSON), &signals); err != nil {
		return 0.0
	}

	boost := 0.0
	highValueLabels := map[string]bool{
		"warning":   true,
		"decision":  true,
		"important": true,
	}

	for _, label := range signals.InlineLabels {
		if highValueLabels[label] {
			boost += 0.3
		} else {
			boost += 0.1
		}
	}
	for range signals.RiskMarkers {
		boost += 0.2
	}
	// TODOs and rationale (WHY/NOTE) carry caveats or design intent the author
	// flagged; a small boost surfaces them without drowning out real warnings.
	for range signals.Todos {
		boost += 0.05
	}
	for range signals.Rationale {
		boost += 0.05
	}

	if boost > 1.0 {
		boost = 1.0
	}
	return boost
}

func (s *Store) GetChunksForDocument(docID string) ([]DocChunk, error) {
	rows, err := s.db.Query(`
		SELECT id, file_path, section_heading, section_hierarchy, chunk_index, content, token_count, signal_meta
		FROM doc_chunks WHERE doc_id = ? ORDER BY chunk_index`, docID)
	if err != nil {
		return nil, fmt.Errorf("get_chunks_for_document: %w", err)
	}
	defer rows.Close()

	var chunks []DocChunk
	for rows.Next() {
		var chunk DocChunk
		var id int64
		if err := rows.Scan(&id, &chunk.FilePath, &chunk.SectionHeading,
			&chunk.SectionHierarchy, &chunk.ChunkIndex, &chunk.Content, &chunk.TokenCount,
			&chunk.SignalMeta); err != nil {
			return nil, fmt.Errorf("get_chunks_for_document scan: %w", err)
		}
		chunk.ID = strconv.FormatInt(id, 10)
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

// float64sToFloat32Bytes converts a []float64 vector to little-endian []byte
// of float32 values, as expected by sqlite-vec.
func float64sToFloat32Bytes(vec []float64) []byte {
	buf := make([]byte, len(vec)*4)
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(v)))
	}
	return buf
}
