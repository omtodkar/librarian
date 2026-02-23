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
	db *sql.DB
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

	return &Store{db: sqlDB}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
