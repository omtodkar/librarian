package helix

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
}

type CodeFile struct {
	ID               string    `json:"id"`
	FilePath         string    `json:"file_path"`
	Language         string    `json:"language"`
	LastReferencedAt time.Time `json:"last_referenced_at"`
}
