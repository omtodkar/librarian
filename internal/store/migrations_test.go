package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"
)

// TestOpen_FreshDB pins the happy path: Open on an empty path creates every
// librarian table + the goose_db_version tracker, and stamps version 1 as
// applied. If goose silently no-ops (e.g. migrations directory misspelled)
// this test catches it.
func TestOpen_FreshDB(t *testing.T) {
	s := newTestStore(t)

	// goose_db_version must exist with exactly one applied row at version 1.
	var maxVersion int64
	if err := s.db.QueryRow(`SELECT max(version_id) FROM goose_db_version WHERE is_applied = 1`).Scan(&maxVersion); err != nil {
		t.Fatalf("reading goose_db_version: %v", err)
	}
	if maxVersion != 2 {
		t.Errorf("max applied version_id: got %d want 2", maxVersion)
	}

	// Every baseline table must exist after goose.Up. Include embedding_meta
	// so a regression that drops it from 0001 fails this test rather than
	// later at the first AddChunk (cryptic "no such table" from a live run).
	// doc_chunks_fts is the FTS5 virtual table added in migration 0002.
	wantTables := []string{"documents", "doc_chunks", "code_files", "refs", "graph_nodes", "graph_edges", "embedding_meta", "doc_chunks_fts"}
	for _, tbl := range wantTables {
		var name string
		err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing after Open: %v", tbl, err)
		}
	}

	// code_files.content_hash must be present — baselined into 0001 rather
	// than retained as a fossil ALTER. Catches regressions where a future
	// edit moves the column out of the baseline.
	var colCount int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM pragma_table_info('code_files') WHERE name='content_hash'`,
	).Scan(&colCount); err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	if colCount != 1 {
		t.Errorf("code_files.content_hash: got %d columns want 1", colCount)
	}
}

// TestOpen_RerunIsNoOp re-opens the same DB twice; the second call must NOT
// re-apply migration 1 (goose guards via goose_db_version). Pins the common
// case — every librarian CLI invocation opens the DB fresh.
func TestOpen_RerunIsNoOp(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()

	// goose writes version_id=0 (table-creation baseline) plus one row per
	// applied migration. We filter to version_id=1 so the assertion survives
	// goose's baseline row. A second goose.Up is a no-op (current version
	// already 1); a spurious down+up cycle would add two more rows for
	// version_id=1, yielding 3.
	var rowCount int
	if err := s2.db.QueryRow(`SELECT count(*) FROM goose_db_version WHERE version_id=1`).Scan(&rowCount); err != nil {
		t.Fatalf("counting goose rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("goose_db_version rows for version_id=1: got %d want 1", rowCount)
	}
}

// TestOpen_PreGooseDBIsRefused pins the legacy-refusal path: a DB created by
// the old MigrationsSQL blob (librarian tables present, no goose_db_version)
// must be rejected with an actionable error, not silently re-migrated into a
// ghost state.
func TestOpen_PreGooseDBIsRefused(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	sqlDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	// Minimal pre-goose fingerprint: the detector only probes for `documents`
	// + absence of goose_db_version, so a single CREATE TABLE is enough to
	// trigger the refusal path without dragging in the full baseline.
	if _, err := sqlDB.Exec(`CREATE TABLE documents (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("seeding legacy shape: %v", err)
	}
	sqlDB.Close()

	_, err = Open(dbPath)
	if err == nil {
		t.Fatal("Open: expected error for pre-goose DB, got nil")
	}
	if !strings.Contains(err.Error(), "pre-goose version of librarian") {
		t.Errorf("Open error should call out the pre-goose condition; got: %v", err)
	}
	if !strings.Contains(err.Error(), "librarian index") {
		t.Errorf("Open error should include the remediation (librarian index); got: %v", err)
	}
}

// TestGooseDown_RestoresEmptyState pins Down symmetry — after rolling back all
// migrations to version 0, every librarian table is gone. A broken Down would
// leave orphan tables that a fresh Up would then trip over via unique constraints.
func TestGooseDown_RestoresEmptyState(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "rollback.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	// Dialect + BaseFS are set in store.go's init(), so no test setup needed.
	// DownTo(0) rolls back every migration in reverse order.
	if err := goose.DownTo(s.db, "migrations", 0); err != nil {
		t.Fatalf("goose.DownTo(0): %v", err)
	}

	for _, tbl := range []string{"documents", "doc_chunks", "code_files", "refs", "graph_nodes", "graph_edges", "embedding_meta", "doc_chunks_fts"} {
		var name string
		err := s.db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, tbl,
		).Scan(&name)
		if err == nil {
			t.Errorf("table %q still exists after Down", tbl)
		}
	}
}

// TestOpen_DialectErrorPath documents why the goose.SetDialect error path
// cannot be directly injected: the dialect is set exactly once via sync.Once
// with the hard-coded string "sqlite3", which goose always accepts. The error
// path in initDialect guards against a future goose API change. What this
// test pins instead is that Open returns an error — not a panic — on a
// pre-existing failure condition (the legacy-DB refusal), proving that the
// whole Open error-return chain is wired correctly.
func TestOpen_ReturnsErrorNotPanic(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy2.db")
	sqlDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if _, err := sqlDB.Exec(`CREATE TABLE documents (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	sqlDB.Close()

	_, err = Open(dbPath)
	if err == nil {
		t.Fatal("Open: expected error, got nil")
	}
	// Verify the call returned (not panicked) — reaching this line is the proof.
}
