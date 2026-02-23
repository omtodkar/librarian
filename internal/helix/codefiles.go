package helix

import (
	"fmt"

	helixdb "github.com/HelixDB/helix-go"
)

func (c *Client) AddCodeFile(filePath, language string) (*CodeFile, error) {
	res, err := c.hx.Query("add_code_file", helixdb.WithData(map[string]any{
		"file_path": filePath,
		"language":  language,
	}))
	if err != nil {
		return nil, fmt.Errorf("add_code_file: %w", err)
	}

	var cf CodeFile
	if err := res.Scan(helixdb.WithDest("code_file", &cf)); err != nil {
		return nil, fmt.Errorf("add_code_file scan: %w", err)
	}
	return &cf, nil
}

func (c *Client) GetCodeFileByPath(filePath string) (*CodeFile, error) {
	res, err := c.hx.Query("get_code_file_by_path", helixdb.WithData(map[string]any{
		"file_path": filePath,
	}))
	if err != nil {
		return nil, fmt.Errorf("get_code_file_by_path: %w", err)
	}

	var cf CodeFile
	if err := res.Scan(helixdb.WithDest("code_file", &cf)); err != nil {
		return nil, fmt.Errorf("get_code_file_by_path scan: %w", err)
	}
	return &cf, nil
}

func (c *Client) AddReference(docID, codeFileID, context string) error {
	_, err := c.hx.Query("add_reference", helixdb.WithData(map[string]any{
		"doc_id":       docID,
		"code_file_id": codeFileID,
		"context":      context,
	}))
	if err != nil {
		return fmt.Errorf("add_reference: %w", err)
	}
	return nil
}

func (c *Client) GetReferencedCodeFiles(docID string) ([]CodeFile, error) {
	res, err := c.hx.Query("get_referenced_code_files", helixdb.WithData(map[string]any{
		"doc_id": docID,
	}))
	if err != nil {
		return nil, fmt.Errorf("get_referenced_code_files: %w", err)
	}

	var files []CodeFile
	if err := res.Scan(helixdb.WithDest("code_files", &files)); err != nil {
		return nil, fmt.Errorf("get_referenced_code_files scan: %w", err)
	}
	return files, nil
}

func (c *Client) AddRelatedDoc(fromDocID, toDocID, relationType string) error {
	_, err := c.hx.Query("add_related_doc", helixdb.WithData(map[string]any{
		"from_doc_id":   fromDocID,
		"to_doc_id":     toDocID,
		"relation_type": relationType,
	}))
	if err != nil {
		return fmt.Errorf("add_related_doc: %w", err)
	}
	return nil
}

func (c *Client) GetRelatedDocuments(docID string) ([]Document, error) {
	res, err := c.hx.Query("get_related_documents", helixdb.WithData(map[string]any{
		"doc_id": docID,
	}))
	if err != nil {
		return nil, fmt.Errorf("get_related_documents: %w", err)
	}

	var docs []Document
	if err := res.Scan(helixdb.WithDest("related", &docs)); err != nil {
		return nil, fmt.Errorf("get_related_documents scan: %w", err)
	}
	return docs, nil
}
