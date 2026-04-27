package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// seedChunk inserts one chunk with the given model/dim through the normal
// AddChunk path. Returns the doc id so tests can cascade-delete if needed.
func seedChunk(t *testing.T, s *Store, model string, dim int) string {
	t.Helper()
	doc, err := s.AddDocument(AddDocumentInput{
		FilePath: "docs/a.md",
		Title:    "A",
	})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	if _, err := s.AddChunk(AddChunkInput{
		Vector:     make([]float64, dim),
		Content:    "body",
		FilePath:   doc.FilePath,
		ChunkIndex: 0,
		DocID:      doc.ID,
		Model:      model,
	}); err != nil {
		t.Fatalf("AddChunk: %v", err)
	}
	return doc.ID
}

// TestAddChunk_FirstInsertWritesMeta pins the happy-path first-ever insert:
// after one AddChunk, embedding_meta has exactly the (model, dimension) pair
// that produced the vector. Without this, the mismatch detector has nothing
// to compare against on future runs.
func TestAddChunk_FirstInsertWritesMeta(t *testing.T) {
	s := newTestStore(t)
	seedChunk(t, s, "gemini-embedding-2", 768)

	got := map[string]string{}
	rows, err := s.db.Query(`SELECT key, value FROM embedding_meta`)
	if err != nil {
		t.Fatalf("querying embedding_meta: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[k] = v
	}
	if got["model"] != "gemini-embedding-2" {
		t.Errorf("embedding_meta.model: got %q want %q", got["model"], "gemini-embedding-2")
	}
	if got["dimension"] != "768" {
		t.Errorf("embedding_meta.dimension: got %q want %q", got["dimension"], "768")
	}
}

// TestAddChunk_SameModelSameDimIsNoOp pins that repeated inserts don't
// duplicate embedding_meta rows — the PRIMARY KEY + ON CONFLICT guards that,
// and the cache short-circuit means the Exec never fires past the first
// write. A regression here would grow embedding_meta unbounded.
func TestAddChunk_SameModelSameDimIsNoOp(t *testing.T) {
	s := newTestStore(t)
	seedChunk(t, s, "gemini-embedding-2", 768)

	// Insert a second chunk on the same doc with the same model/dim.
	doc, err := s.GetDocumentByPath("docs/a.md")
	if err != nil || doc == nil {
		t.Fatalf("GetDocumentByPath: %v", err)
	}
	if _, err := s.AddChunk(AddChunkInput{
		Vector: make([]float64, 768), Content: "more", FilePath: doc.FilePath,
		ChunkIndex: 1, DocID: doc.ID, Model: "gemini-embedding-2",
	}); err != nil {
		t.Fatalf("second AddChunk: %v", err)
	}

	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM embedding_meta`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("embedding_meta rows: got %d want 2 (model + dimension)", count)
	}
}

// TestAddChunk_DimensionMismatchErrors pins the core bug this feature fixes:
// a different-dimension insert must refuse, not silently corrupt the vec0
// table. The error must mention the recovery command so users have a path
// forward without reading source.
func TestAddChunk_DimensionMismatchErrors(t *testing.T) {
	s := newTestStore(t)
	seedChunk(t, s, "model-a", 768)

	doc, err := s.GetDocumentByPath("docs/a.md")
	if err != nil || doc == nil {
		t.Fatalf("GetDocumentByPath: %v", err)
	}
	_, err = s.AddChunk(AddChunkInput{
		Vector: make([]float64, 3072), Content: "x", FilePath: doc.FilePath,
		ChunkIndex: 1, DocID: doc.ID, Model: "model-a",
	})
	if err == nil {
		t.Fatal("AddChunk with different dim: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error should describe the mismatch; got: %v", err)
	}
	if !strings.Contains(err.Error(), "reindex --rebuild-vectors") {
		t.Errorf("error should surface the recovery command; got: %v", err)
	}
	// Regression: AddChunk used to insert the doc_chunks row before the
	// mismatch check, leaving an orphan on failure. Seed added 1 chunk; a
	// failed AddChunk must not add a second.
	var chunkCount int
	if err := s.db.QueryRow(`SELECT count(*) FROM doc_chunks`).Scan(&chunkCount); err != nil {
		t.Fatalf("counting doc_chunks: %v", err)
	}
	if chunkCount != 1 {
		t.Errorf("doc_chunks rows after failed AddChunk: got %d want 1 (orphan row from old ordering?)", chunkCount)
	}
}

// TestAddChunk_ModelMismatchErrors pins the same-dim / different-model case.
// Without this check, two OpenAI 1536-dim models would blend vectors from
// distinct semantic spaces and search would quietly degrade.
func TestAddChunk_ModelMismatchErrors(t *testing.T) {
	s := newTestStore(t)
	seedChunk(t, s, "model-a", 768)

	doc, err := s.GetDocumentByPath("docs/a.md")
	if err != nil || doc == nil {
		t.Fatalf("GetDocumentByPath: %v", err)
	}
	_, err = s.AddChunk(AddChunkInput{
		Vector: make([]float64, 768), Content: "x", FilePath: doc.FilePath,
		ChunkIndex: 1, DocID: doc.ID, Model: "model-b",
	})
	if err == nil {
		t.Fatal("AddChunk with different model: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "model-a") || !strings.Contains(err.Error(), "model-b") {
		t.Errorf("error should name both stored and active models; got: %v", err)
	}
}

// TestOpen_PopulatesEmbedMetaFromDB pins the read path that every
// incremental re-index depends on: after Open on a DB with prior meta,
// the in-memory cache must be populated by loadEmbeddingMeta before any
// AddChunk call. Without this test, a regression in loadEmbeddingMeta
// (empty cache on reopen) would silently bypass the mismatch check on
// the next run and re-write meta with whatever model is currently active.
func TestOpen_PopulatesEmbedMetaFromDB(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "reopen.db")

	s1, err := Open(dbPath, nil, 0)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	seedChunk(t, s1, "model-a", 768)
	s1.Close()

	s2, err := Open(dbPath, nil, 0)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	if s2.embedMeta.model != "model-a" {
		t.Errorf("embedMeta.model after reopen: got %q want %q", s2.embedMeta.model, "model-a")
	}
	if s2.embedMeta.dim != 768 {
		t.Errorf("embedMeta.dim after reopen: got %d want 768", s2.embedMeta.dim)
	}

	// A mismatch attempt on the reopened store must surface the same error
	// as the in-process case — proves the cache is authoritative, not stale.
	doc, _ := s2.GetDocumentByPath("docs/a.md")
	_, err = s2.AddChunk(AddChunkInput{
		Vector: make([]float64, 3072), Content: "x", FilePath: doc.FilePath,
		ChunkIndex: 1, DocID: doc.ID, Model: "model-a",
	})
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("expected mismatch error on reopened store; got: %v", err)
	}
}

// TestVerifyActiveEmbedder pins the up-front mismatch check used by the
// indexer to fail fast before any embedding API calls. Without this short
// circuit every chunk in the docs pass paid a full Embed() before the
// per-chunk ensureVecTable check fired — ~110 wasted API calls per failed
// run on a mid-sized docs/ dir.
func TestVerifyActiveEmbedder(t *testing.T) {
	s := newTestStore(t)

	// Fresh DB — no meta recorded yet. Any model passes.
	if err := s.VerifyActiveEmbedder("anything"); err != nil {
		t.Errorf("fresh DB should accept any model; got %v", err)
	}

	seedChunk(t, s, "model-a", 768)

	if err := s.VerifyActiveEmbedder("model-a"); err != nil {
		t.Errorf("matching model should pass; got %v", err)
	}
	err := s.VerifyActiveEmbedder("model-b")
	if err == nil {
		t.Fatal("differing model should fail, got nil")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error should describe mismatch; got: %v", err)
	}
	if !strings.Contains(err.Error(), "reindex --rebuild-vectors") {
		t.Errorf("error should surface recovery command; got: %v", err)
	}
}

// TestDeleteDocument_ToleratesMissingVecTable pins the bug 1 fix: after
// ClearVectorState drops doc_chunk_vectors, DeleteDocument must not error
// on the now-absent table. Without the vecTableReady guard, the reindex
// recovery path was broken end-to-end (AddDocument UNIQUE conflict on
// re-insert because DeleteDocument silently failed).
func TestDeleteDocument_ToleratesMissingVecTable(t *testing.T) {
	s := newTestStore(t)
	docID := seedChunk(t, s, "model-a", 768)

	if err := s.ClearVectorState(); err != nil {
		t.Fatalf("ClearVectorState: %v", err)
	}
	// vec0 is now dropped. DeleteDocument's legacy first-step would error
	// with "no such table: doc_chunk_vectors" here.
	if err := s.DeleteDocument(docID); err != nil {
		t.Errorf("DeleteDocument after ClearVectorState: got %v, want nil", err)
	}
	var docCount int
	if err := s.db.QueryRow(`SELECT count(*) FROM documents`).Scan(&docCount); err != nil {
		t.Fatalf("count documents: %v", err)
	}
	if docCount != 0 {
		t.Errorf("documents rows after delete: got %d want 0", docCount)
	}
}

// TestClearVectorState_DropsEverything pins the recovery primitive used by
// `librarian reindex --rebuild-vectors`. After Clear, the vec0 table is
// gone, embedding_meta is empty, doc_chunks is empty, but documents (and
// code_files) are preserved so the reindex can reuse existing rows.
func TestClearVectorState_DropsEverything(t *testing.T) {
	s := newTestStore(t)
	seedChunk(t, s, "model-a", 768)

	if err := s.ClearVectorState(); err != nil {
		t.Fatalf("ClearVectorState: %v", err)
	}

	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='doc_chunk_vectors'`).Scan(&name)
	if err == nil {
		t.Error("doc_chunk_vectors should be dropped")
	}

	var embedCount, chunkCount, docCount int
	_ = s.db.QueryRow(`SELECT count(*) FROM embedding_meta`).Scan(&embedCount)
	_ = s.db.QueryRow(`SELECT count(*) FROM doc_chunks`).Scan(&chunkCount)
	_ = s.db.QueryRow(`SELECT count(*) FROM documents`).Scan(&docCount)
	if embedCount != 0 {
		t.Errorf("embedding_meta rows after Clear: got %d want 0", embedCount)
	}
	if chunkCount != 0 {
		t.Errorf("doc_chunks rows after Clear: got %d want 0", chunkCount)
	}
	if docCount != 1 {
		t.Errorf("documents rows after Clear: got %d want 1 (preserved)", docCount)
	}

	// In-memory cache must reflect the clear, so the next AddChunk treats it
	// as a first-ever insert and writes the new meta.
	if s.vecTableReady {
		t.Error("vecTableReady should be false after Clear")
	}
	if s.embedMeta.model != "" || s.embedMeta.dim != 0 {
		t.Errorf("embedMeta should be zeroed after Clear; got %+v", s.embedMeta)
	}

	// Post-clear insert with a different model should succeed and re-populate
	// the cache with the new values — the end-to-end recovery invariant.
	doc, _ := s.GetDocumentByPath("docs/a.md")
	if _, err := s.AddChunk(AddChunkInput{
		Vector: make([]float64, 3072), Content: "x", FilePath: "docs/a.md",
		ChunkIndex: 0, DocID: doc.ID, Model: "model-b",
	}); err != nil {
		t.Fatalf("post-clear AddChunk: %v", err)
	}
	if s.embedMeta.model != "model-b" || s.embedMeta.dim != 3072 {
		t.Errorf("cache after post-clear insert: got %+v want {model-b, 3072}", s.embedMeta)
	}
}
