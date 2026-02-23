package store

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
)

func (s *Store) AddChunk(input AddChunkInput) (*DocChunk, error) {
	res, err := s.db.Exec(`
		INSERT INTO doc_chunks (file_path, section_heading, section_hierarchy, chunk_index, content, token_count, doc_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		input.FilePath, input.SectionHeading, input.SectionHierarchy,
		input.ChunkIndex, input.Content, input.TokenCount, input.DocID,
	)
	if err != nil {
		return nil, fmt.Errorf("add_chunk: %w", err)
	}

	chunkID, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("add_chunk last_insert_id: %w", err)
	}

	// Insert the vector into the vec0 virtual table
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
	}, nil
}

func (s *Store) SearchChunks(vector []float64, limit int) ([]DocChunk, error) {
	vecBytes := float64sToFloat32Bytes(vector)

	rows, err := s.db.Query(`
		SELECT c.id, c.file_path, c.section_heading, c.section_hierarchy, c.chunk_index, c.content, c.token_count
		FROM doc_chunk_vectors v
		JOIN doc_chunks c ON c.id = v.chunk_id
		WHERE v.embedding MATCH ?
		  AND k = ?
		ORDER BY v.distance`, vecBytes, limit)
	if err != nil {
		return nil, fmt.Errorf("search_chunks: %w", err)
	}
	defer rows.Close()

	var chunks []DocChunk
	for rows.Next() {
		var chunk DocChunk
		var id int64
		if err := rows.Scan(&id, &chunk.FilePath, &chunk.SectionHeading,
			&chunk.SectionHierarchy, &chunk.ChunkIndex, &chunk.Content, &chunk.TokenCount); err != nil {
			return nil, fmt.Errorf("search_chunks scan: %w", err)
		}
		chunk.ID = strconv.FormatInt(id, 10)
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

func (s *Store) GetChunksForDocument(docID string) ([]DocChunk, error) {
	rows, err := s.db.Query(`
		SELECT id, file_path, section_heading, section_hierarchy, chunk_index, content, token_count
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
			&chunk.SectionHierarchy, &chunk.ChunkIndex, &chunk.Content, &chunk.TokenCount); err != nil {
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
