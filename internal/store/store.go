package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"

	"librarian/db"
)

func init() {
	sqlite_vec.Auto()
	// goose chatters into the log package by default. Silence it — the
	// librarian CLI owns all user-facing output; a surprise "OK 0001_..."
	// line from a third-party logger looks like a bug.
	goose.SetLogger(goose.NopLogger())
}

type Store struct {
	db            *sql.DB
	vecTableReady bool
}

// Open opens (or creates) a SQLite database at dbPath, loads the sqlite-vec
// extension, enables WAL mode and foreign keys, and applies any pending
// goose migrations from db.MigrationsFS. Returns an error for pre-goose
// databases — see detectLegacyPreGooseDB.
func Open(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating database directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := detectLegacyPreGooseDB(sqlDB); err != nil {
		sqlDB.Close()
		return nil, err
	}

	if err := applyMigrations(sqlDB); err != nil {
		sqlDB.Close()
		return nil, err
	}

	s := &Store{db: sqlDB}

	// Check if the vector table already exists (from a previous indexing run).
	// Lazy-created on first chunk insert so the dimension matches the live
	// embedding model — kept outside goose because dimensions are a runtime
	// property, not a schema property.
	var name string
	if err := sqlDB.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='doc_chunk_vectors'`,
	).Scan(&name); err == nil {
		s.vecTableReady = true
	}

	return s, nil
}

// applyMigrations runs goose.Up against the embedded migrations directory.
// Split from Open so tests can call it on a raw *sql.DB too.
func applyMigrations(sqlDB *sql.DB) error {
	goose.SetBaseFS(db.MigrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		return fmt.Errorf("setting goose dialect: %w", err)
	}
	if err := goose.Up(sqlDB, "migrations"); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

// detectLegacyPreGooseDB refuses a database that has librarian tables from
// the pre-goose era but no goose_db_version. "Tables exist without goose
// tracking" is the fingerprint of the old MigrationsSQL blob era; running
// goose.Up on it would succeed silently (every CREATE is IF NOT EXISTS) yet
// never record v1 as applied, so every future migration would think it needed
// to re-apply from scratch. Refusing up front with a clear remediation is
// cheaper than debugging the resulting ghost-migration state later.
//
// Fresh databases (no tables at all) pass through — goose.Up populates them.
func detectLegacyPreGooseDB(sqlDB *sql.DB) error {
	var hasGoose bool
	if err := sqlDB.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='goose_db_version')`,
	).Scan(&hasGoose); err != nil {
		return fmt.Errorf("probing for goose_db_version: %w", err)
	}
	if hasGoose {
		return nil
	}
	var hasDocuments bool
	if err := sqlDB.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='documents')`,
	).Scan(&hasDocuments); err != nil {
		return fmt.Errorf("probing for documents table: %w", err)
	}
	if !hasDocuments {
		return nil
	}
	return fmt.Errorf(
		"this database was created by a pre-goose version of librarian and " +
			"is not compatible with the new migration framework; delete " +
			".librarian/librarian.db and re-run 'librarian index' to rebuild it " +
			"(your source files are untouched)")
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
