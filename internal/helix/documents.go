package helix

import (
	"fmt"

	helixdb "github.com/HelixDB/helix-go"
)

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

func (c *Client) AddDocument(input AddDocumentInput) (*Document, error) {
	res, err := c.hx.Query("add_document", helixdb.WithData(input))
	if err != nil {
		return nil, fmt.Errorf("add_document: %w", err)
	}

	var doc Document
	if err := res.Scan(helixdb.WithDest("doc", &doc)); err != nil {
		return nil, fmt.Errorf("add_document scan: %w", err)
	}
	return &doc, nil
}

func (c *Client) GetDocumentByPath(filePath string) (*Document, error) {
	res, err := c.hx.Query("get_document_by_path", helixdb.WithData(map[string]any{
		"file_path": filePath,
	}))
	if err != nil {
		return nil, fmt.Errorf("get_document_by_path: %w", err)
	}

	var doc Document
	if err := res.Scan(helixdb.WithDest("doc", &doc)); err != nil {
		return nil, fmt.Errorf("get_document_by_path scan: %w", err)
	}
	return &doc, nil
}

func (c *Client) ListDocuments() ([]Document, error) {
	res, err := c.hx.Query("list_documents")
	if err != nil {
		return nil, fmt.Errorf("list_documents: %w", err)
	}

	var docs []Document
	if err := res.Scan(helixdb.WithDest("docs", &docs)); err != nil {
		return nil, fmt.Errorf("list_documents scan: %w", err)
	}
	return docs, nil
}

func (c *Client) DeleteDocument(docID string) error {
	_, err := c.hx.Query("delete_document", helixdb.WithData(map[string]any{
		"doc_id": docID,
	}))
	if err != nil {
		return fmt.Errorf("delete_document: %w", err)
	}
	return nil
}
