package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"

	"librarian/db"
)

func init() {
	sqlite_vec.Auto()
}

type Store struct {
	db             *sql.DB
	vecTableReady  bool
}

// Open opens (or creates) a SQLite database at dbPath, loads the sqlite-vec
// extension, enables WAL mode and foreign keys, and applies the schema.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Apply schema
	if _, err := sqlDB.Exec(db.MigrationsSQL); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}

	s := &Store{db: sqlDB}

	// Check if the vector table already exists (from a previous indexing run)
	var name string
	err = sqlDB.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='doc_chunk_vectors'`).Scan(&name)
	if err == nil {
		s.vecTableReady = true
	}

	return s, nil
}

// ensureVecTable creates the doc_chunk_vectors virtual table sized to the
// given vector dimensions. Called lazily on first vector insert so the
// dimension is always derived from the actual embedding model output.
func (s *Store) ensureVecTable(dimensions int) error {
	if s.vecTableReady {
		return nil
	}
	vecSQL := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS doc_chunk_vectors USING vec0(
    chunk_id INTEGER PRIMARY KEY,
    embedding float[%d]
)`, dimensions)
	if _, err := s.db.Exec(vecSQL); err != nil {
		return fmt.Errorf("creating vector table: %w", err)
	}
	s.vecTableReady = true
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
