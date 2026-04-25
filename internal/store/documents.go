package store

import (
	"fmt"

	"github.com/google/uuid"
)

func (s *Store) AddDocument(input AddDocumentInput) (*Document, error) {
	id := uuid.New().String()

	_, err := s.db.Exec(`
		INSERT INTO documents (id, file_path, title, doc_type, summary, headings, frontmatter, content_hash, chunk_count)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, input.FilePath, input.Title, input.DocType, input.Summary,
		input.Headings, input.Frontmatter, input.ContentHash, input.ChunkCount,
	)
	if err != nil {
		return nil, fmt.Errorf("add_document: %w", err)
	}

	return s.getDocumentByID(id)
}

func (s *Store) GetDocumentByPath(filePath string) (*Document, error) {
	var doc Document
	err := s.db.QueryRow(`
		SELECT id, file_path, title, doc_type, summary, headings, frontmatter, content_hash, chunk_count, indexed_at
		FROM documents WHERE file_path = ?`, filePath,
	).Scan(&doc.ID, &doc.FilePath, &doc.Title, &doc.DocType, &doc.Summary,
		&doc.Headings, &doc.Frontmatter, &doc.ContentHash, &doc.ChunkCount, &doc.IndexedAt)
	if err != nil {
		return nil, fmt.Errorf("get_document_by_path: %w", err)
	}
	return &doc, nil
}

func (s *Store) ListDocuments() ([]Document, error) {
	rows, err := s.db.Query(`
		SELECT id, file_path, title, doc_type, summary, headings, frontmatter, content_hash, chunk_count, indexed_at
		FROM documents ORDER BY file_path`)
	if err != nil {
		return nil, fmt.Errorf("list_documents: %w", err)
	}
	defer rows.Close()

	var docs []Document
	for rows.Next() {
		var doc Document
		if err := rows.Scan(&doc.ID, &doc.FilePath, &doc.Title, &doc.DocType, &doc.Summary,
			&doc.Headings, &doc.Frontmatter, &doc.ContentHash, &doc.ChunkCount, &doc.IndexedAt); err != nil {
			return nil, fmt.Errorf("list_documents scan: %w", err)
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func (s *Store) DeleteDocument(docID string) error {
	// Delete vector entries for this document's chunks first (not cascaded
	// because sqlite-vec virtual tables don't participate in FK CASCADE).
	// Guard on vecTableReady because ClearVectorState drops the table
	// entirely; a subsequent reindex would otherwise error "no such table"
	// here and leave the caller's AddDocument to hit a UNIQUE conflict.
	if s.vecTableReady {
		_, err := s.db.Exec(`
			DELETE FROM doc_chunk_vectors WHERE chunk_id IN (
				SELECT id FROM doc_chunks WHERE doc_id = ?
			)`, docID)
		if err != nil {
			return fmt.Errorf("delete_document vectors: %w", err)
		}
	}

	// Delete the document's graph node; graph_edges cascade via FK.
	_, err := s.db.Exec(`DELETE FROM graph_nodes WHERE id = ?`, DocNodeID(docID))
	if err != nil {
		return fmt.Errorf("delete_document graph_node: %w", err)
	}

	// Delete refs + chunks + document. refs and doc_chunks cascade via FK on
	// documents; explicit deletes here preserve the prior idempotency guarantee.
	_, err = s.db.Exec(`DELETE FROM refs WHERE doc_id = ?`, docID)
	if err != nil {
		return fmt.Errorf("delete_document refs: %w", err)
	}
	_, err = s.db.Exec(`DELETE FROM doc_chunks WHERE doc_id = ?`, docID)
	if err != nil {
		return fmt.Errorf("delete_document chunks: %w", err)
	}
	_, err = s.db.Exec(`DELETE FROM documents WHERE id = ?`, docID)
	if err != nil {
		return fmt.Errorf("delete_document: %w", err)
	}
	return nil
}

func (s *Store) getDocumentByID(id string) (*Document, error) {
	var doc Document
	err := s.db.QueryRow(`
		SELECT id, file_path, title, doc_type, summary, headings, frontmatter, content_hash, chunk_count, indexed_at
		FROM documents WHERE id = ?`, id,
	).Scan(&doc.ID, &doc.FilePath, &doc.Title, &doc.DocType, &doc.Summary,
		&doc.Headings, &doc.Frontmatter, &doc.ContentHash, &doc.ChunkCount, &doc.IndexedAt)
	if err != nil {
		return nil, fmt.Errorf("get_document_by_id: %w", err)
	}
	return &doc, nil
}
