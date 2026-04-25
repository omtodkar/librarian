package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pressly/goose/v3"

	"librarian/db"
)

func init() {
	sqlite_vec.Auto()
	// goose.SetBaseFS/SetDialect/SetLogger write to package-level globals.
	// Set them here rather than on each Open so parallel Opens can't race.
	// Dialect and FS never vary for librarian — always sqlite3 + our embed.
	goose.SetLogger(goose.NopLogger())
	goose.SetBaseFS(db.MigrationsFS)
	if err := goose.SetDialect("sqlite3"); err != nil {
		// SetDialect only errors on an unknown dialect string; "sqlite3"
		// is known. A panic here means a goose-version change broke the
		// API, which surfaces at import time — what we want.
		panic(fmt.Sprintf("goose.SetDialect: %v", err))
	}
}

type Store struct {
	db            *sql.DB
	vecTableReady bool
	// embedMeta caches the (model, dim) recorded in embedding_meta — populated
	// by loadEmbeddingMeta on Open and refreshed on first-ever insert.
	// Zero-value means "never recorded"; ensureVecTable uses model=="" as the
	// first-insert sentinel.
	embedMeta embedMetaCache
}

type embedMetaCache struct {
	model string
	dim   int
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

	if err := goose.Up(sqlDB, "migrations"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("applying migrations: %w", err)
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

	// Load embedding_meta into the in-memory cache so ensureVecTable can
	// compare on every chunk insert without a round-trip. A fresh DB has
	// no rows — the cache stays zero-valued and the first insert writes
	// the meta.
	if err := s.loadEmbeddingMeta(); err != nil {
		sqlDB.Close()
		return nil, err
	}

	// Crash-window recovery: vec0 created but embedding_meta never written
	// means the previous run died between the DDL and the compound INSERT
	// in ensureVecTable. The vec table's dimension is unknown to this run;
	// dropping it lets the next AddChunk re-create at the active model's
	// dimension. Without this, a model/dim-swap after the crash would
	// either error cryptically at the sqlite-vec level or (same-dim,
	// different-model) silently corrupt the semantic space.
	if s.vecTableReady && s.embedMeta.model == "" {
		if _, err := sqlDB.Exec(`DROP TABLE IF EXISTS doc_chunk_vectors`); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("recovering from inconsistent vec0 state: %w", err)
		}
		s.vecTableReady = false
	}

	return s, nil
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

// ensureVecTable verifies the active (model, dim) pair against what was
// recorded on the first index run, creates the doc_chunk_vectors vec0 table
// if it doesn't exist, and records the meta on first-ever insert. Called
// from AddChunk; the three concerns are co-located because they share the
// "first chunk ever" fast path — a split-out verifier would run the same
// meta query twice.
//
// The mismatch error is the single user-facing surface for "you swapped the
// embedding model in config.yaml"; its wording must point at the recovery
// command so users don't have to guess.
func (s *Store) ensureVecTable(model string, dim int) error {
	// model == "" is the single first-ever-insert sentinel. writeEmbeddingMeta
	// writes (model, dim) atomically via a compound INSERT, so a populated
	// cache always has both or neither — gating on model alone is sufficient
	// and avoids an asymmetric check.
	if s.embedMeta.model != "" {
		if s.embedMeta.model != model || s.embedMeta.dim != dim {
			return embeddingMismatchError(s.embedMeta.model, s.embedMeta.dim, model, dim)
		}
	}

	if !s.vecTableReady {
		vecSQL := fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS doc_chunk_vectors USING vec0(
    chunk_id INTEGER PRIMARY KEY,
    embedding float[%d]
)`, dim)
		if _, err := s.db.Exec(vecSQL); err != nil {
			return fmt.Errorf("creating vector table: %w", err)
		}
		s.vecTableReady = true
	}

	if s.embedMeta.model == "" {
		if err := s.writeEmbeddingMeta(model, dim); err != nil {
			return err
		}
		s.embedMeta.model = model
		s.embedMeta.dim = dim
	}
	return nil
}

// loadEmbeddingMeta pulls the two rows from embedding_meta into the cache.
// Missing rows leave the cache zero-valued, which ensureVecTable interprets
// as "first-ever insert — safe to record on write".
func (s *Store) loadEmbeddingMeta() error {
	rows, err := s.db.Query(`SELECT key, value FROM embedding_meta`)
	if err != nil {
		return fmt.Errorf("reading embedding_meta: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return fmt.Errorf("scanning embedding_meta: %w", err)
		}
		switch k {
		case "model":
			s.embedMeta.model = v
		case "dimension":
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("parsing embedding_meta.dimension %q: %w", v, err)
			}
			s.embedMeta.dim = n
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterating embedding_meta: %w", err)
	}
	return nil
}

// writeEmbeddingMeta upserts the model + dimension rows. Uses ON CONFLICT so
// ClearVectorState followed by a reindex can overwrite cleanly, even though
// the normal first-insert path writes into an empty table.
func (s *Store) writeEmbeddingMeta(model string, dim int) error {
	if _, err := s.db.Exec(
		`INSERT INTO embedding_meta(key, value) VALUES (?, ?), (?, ?)
         ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		"model", model, "dimension", strconv.Itoa(dim),
	); err != nil {
		return fmt.Errorf("writing embedding_meta: %w", err)
	}
	return nil
}

// ClearVectorState drops every trace of the current embedding model so a
// reindex can repopulate with a different one. Transactional: either every
// piece is gone (vec0, embedding_meta rows, doc_chunks rows) or nothing is,
// so a crash mid-reindex leaves a recoverable state. documents and code_files
// are preserved — re-indexing rewrites their chunks.
func (s *Store) ClearVectorState() error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DROP TABLE IF EXISTS doc_chunk_vectors`); err != nil {
		return fmt.Errorf("dropping vec table: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM embedding_meta`); err != nil {
		return fmt.Errorf("clearing embedding_meta: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM doc_chunks`); err != nil {
		return fmt.Errorf("clearing doc_chunks: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	s.vecTableReady = false
	s.embedMeta = embedMetaCache{}
	return nil
}

// embeddingMismatchError produces the single user-facing message for model
// or dimension swaps. Kept as a helper so tests can match on substrings
// without coupling to the exact format.
func embeddingMismatchError(storedModel string, storedDim int, activeModel string, activeDim int) error {
	return fmt.Errorf(
		"embedding model/dimension mismatch: index was built with %q (%d-dim), "+
			"config now specifies %q (%d-dim); "+
			"run 'librarian reindex --rebuild-vectors' to drop the vector table "+
			"and re-embed every chunk",
		storedModel, storedDim, activeModel, activeDim)
}

func (s *Store) Close() error {
	return s.db.Close()
}
