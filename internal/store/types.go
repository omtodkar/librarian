package store

import "time"

type Document struct {
	ID          string    `json:"id"`
	FilePath    string    `json:"file_path"`
	Title       string    `json:"title"`
	DocType     string    `json:"doc_type"`
	Summary     string    `json:"summary"`
	Headings    string    `json:"headings"`
	Frontmatter string    `json:"frontmatter"`
	ContentHash string    `json:"content_hash"`
	ChunkCount  uint32    `json:"chunk_count"`
	IndexedAt   time.Time `json:"indexed_at"`
}

type DocChunk struct {
	ID               string `json:"id"`
	FilePath         string `json:"file_path"`
	SectionHeading   string `json:"section_heading"`
	SectionHierarchy string `json:"section_hierarchy"`
	ChunkIndex       uint32 `json:"chunk_index"`
	Content          string `json:"content"`
	TokenCount       uint32 `json:"token_count"`
	SignalMeta       string `json:"signal_meta"`
}

type CodeFile struct {
	ID               string    `json:"id"`
	FilePath         string    `json:"file_path"`
	Language         string    `json:"language"`
	RefType          string    `json:"ref_type"`
	ContentHash      string    `json:"content_hash"`
	LastReferencedAt time.Time `json:"last_referenced_at"`
}

type AddDocumentInput struct {
	FilePath    string `json:"file_path"`
	Title       string `json:"title"`
	DocType     string `json:"doc_type"`
	Summary     string `json:"summary"`
	Headings    string `json:"headings"`
	Frontmatter string `json:"frontmatter"`
	ContentHash string `json:"content_hash"`
	ChunkCount  uint32 `json:"chunk_count"`
}

type AddChunkInput struct {
	Vector           []float64 `json:"vector"`
	Content          string    `json:"content"`
	FilePath         string    `json:"file_path"`
	SectionHeading   string    `json:"section_heading"`
	SectionHierarchy string    `json:"section_hierarchy"`
	ChunkIndex       uint32    `json:"chunk_index"`
	TokenCount       uint32    `json:"token_count"`
	DocID            string    `json:"doc_id"`
	SignalMeta       string    `json:"signal_meta"`
	// Model is the embedding model identifier that produced Vector. Threaded
	// through from the indexer so the store can detect config-level model
	// swaps that would otherwise silently corrupt the vec0 index.
	Model string `json:"model"`
}
