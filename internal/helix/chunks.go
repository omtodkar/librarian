package helix

import (
	"fmt"

	helixdb "github.com/HelixDB/helix-go"
)

type AddChunkInput struct {
	Content          string `json:"content"`
	FilePath         string `json:"file_path"`
	SectionHeading   string `json:"section_heading"`
	SectionHierarchy string `json:"section_hierarchy"`
	ChunkIndex       uint32 `json:"chunk_index"`
	TokenCount       uint32 `json:"token_count"`
	DocID            string `json:"doc_id"`
}

func (c *Client) AddChunk(input AddChunkInput) (*DocChunk, error) {
	res, err := c.hx.Query("add_chunk", helixdb.WithData(input))
	if err != nil {
		return nil, fmt.Errorf("add_chunk: %w", err)
	}

	var chunk DocChunk
	if err := res.Scan(helixdb.WithDest("chunk", &chunk)); err != nil {
		return nil, fmt.Errorf("add_chunk scan: %w", err)
	}
	return &chunk, nil
}

func (c *Client) SearchChunks(query string, limit int) ([]DocChunk, error) {
	res, err := c.hx.Query("search_chunks", helixdb.WithData(map[string]any{
		"query": query,
		"limit": int64(limit),
	}))
	if err != nil {
		return nil, fmt.Errorf("search_chunks: %w", err)
	}

	var chunks []DocChunk
	if err := res.Scan(helixdb.WithDest("chunks", &chunks)); err != nil {
		return nil, fmt.Errorf("search_chunks scan: %w", err)
	}
	return chunks, nil
}

func (c *Client) GetChunksForDocument(docID string) ([]DocChunk, error) {
	res, err := c.hx.Query("get_chunks_for_document", helixdb.WithData(map[string]any{
		"doc_id": docID,
	}))
	if err != nil {
		return nil, fmt.Errorf("get_chunks_for_document: %w", err)
	}

	var chunks []DocChunk
	if err := res.Scan(helixdb.WithDest("chunks", &chunks)); err != nil {
		return nil, fmt.Errorf("get_chunks_for_document scan: %w", err)
	}
	return chunks, nil
}
