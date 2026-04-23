package store

import (
	"fmt"

	"github.com/google/uuid"
)

func (s *Store) AddCodeFile(filePath, language, refType string) (*CodeFile, error) {
	id := uuid.New().String()

	_, err := s.db.Exec(`
		INSERT INTO code_files (id, file_path, language, ref_type) VALUES (?, ?, ?, ?)`,
		id, filePath, language, refType,
	)
	if err != nil {
		return nil, fmt.Errorf("add_code_file: %w", err)
	}

	var cf CodeFile
	err = s.db.QueryRow(`
		SELECT id, file_path, language, ref_type, last_referenced_at
		FROM code_files WHERE id = ?`, id,
	).Scan(&cf.ID, &cf.FilePath, &cf.Language, &cf.RefType, &cf.LastReferencedAt)
	if err != nil {
		return nil, fmt.Errorf("add_code_file read-back: %w", err)
	}
	return &cf, nil
}

func (s *Store) GetCodeFileByPath(filePath string) (*CodeFile, error) {
	var cf CodeFile
	err := s.db.QueryRow(`
		SELECT id, file_path, language, ref_type, last_referenced_at
		FROM code_files WHERE file_path = ?`, filePath,
	).Scan(&cf.ID, &cf.FilePath, &cf.Language, &cf.RefType, &cf.LastReferencedAt)
	if err != nil {
		return nil, fmt.Errorf("get_code_file_by_path: %w", err)
	}
	return &cf, nil
}

func (s *Store) GetReferencedCodeFiles(docID string) ([]CodeFile, error) {
	rows, err := s.db.Query(`
		SELECT cf.id, cf.file_path, cf.language, cf.ref_type, cf.last_referenced_at
		FROM refs r
		JOIN code_files cf ON cf.id = r.code_file_id
		WHERE r.doc_id = ?`, docID)
	if err != nil {
		return nil, fmt.Errorf("get_referenced_code_files: %w", err)
	}
	defer rows.Close()

	var files []CodeFile
	for rows.Next() {
		var cf CodeFile
		if err := rows.Scan(&cf.ID, &cf.FilePath, &cf.Language, &cf.RefType, &cf.LastReferencedAt); err != nil {
			return nil, fmt.Errorf("get_referenced_code_files scan: %w", err)
		}
		files = append(files, cf)
	}
	return files, rows.Err()
}

func (s *Store) AddReference(docID, codeFileID, context string) error {
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO refs (doc_id, code_file_id, context) VALUES (?, ?, ?)`,
		docID, codeFileID, context,
	)
	if err != nil {
		return fmt.Errorf("add_reference: %w", err)
	}
	return nil
}

// GetRelatedDocuments returns documents reached via a shared_code_ref edge from
// (or to — these edges are semantically symmetric) the given document. Backed by
// graph_edges; related_docs was dropped.
func (s *Store) GetRelatedDocuments(docID string) ([]Document, error) {
	nodeID := DocNodeID(docID)
	rows, err := s.db.Query(`
		SELECT DISTINCT d.id, d.file_path, d.title, d.doc_type, d.summary, d.headings, d.frontmatter, d.content_hash, d.chunk_count, d.indexed_at
		FROM graph_edges e
		JOIN documents d ON (
			(e.from_node = ? AND d.id = substr(e.to_node, 5))
			OR
			(e.to_node = ? AND d.id = substr(e.from_node, 5))
		)
		WHERE e.kind = ? AND d.id != ?
		ORDER BY d.title`,
		nodeID, nodeID, EdgeKindSharedCodeRef, docID)
	if err != nil {
		return nil, fmt.Errorf("get_related_documents: %w", err)
	}
	defer rows.Close()

	var docs []Document
	for rows.Next() {
		var doc Document
		if err := rows.Scan(&doc.ID, &doc.FilePath, &doc.Title, &doc.DocType, &doc.Summary,
			&doc.Headings, &doc.Frontmatter, &doc.ContentHash, &doc.ChunkCount, &doc.IndexedAt); err != nil {
			return nil, fmt.Errorf("get_related_documents scan: %w", err)
		}
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}
