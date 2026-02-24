---
title: Storage Layer
type: reference
description: How the SQLite + sqlite-vec storage layer works, covering database initialization, document and chunk operations, vector search, re-ranking, and relationship management.
---

# Storage Layer

The storage layer (`internal/store/`) manages all interactions with the SQLite database. It handles document CRUD, chunk storage with vector embeddings, code file tracking, and relationship management.

## Database Initialization

`store.Open(dbPath)` in `internal/store/store.go` performs three steps:

1. Creates the parent directory if it doesn't exist
2. Opens a SQLite connection with WAL mode and foreign keys enabled via query string: `?_journal_mode=WAL&_foreign_keys=on`
3. Applies the schema from `db/migrations.sql` (embedded at build time via `db/embed.go`)

The `doc_chunk_vectors` vec0 table is **not** created during `Open()`. It is created lazily on the first vector insert via `ensureVecTable()`, with dimensions derived from the actual embedding model output. This avoids hardcoding a vector size and ensures the table always matches the active model.

The sqlite-vec extension is loaded automatically via `sqlite_vec.Auto()` in the package `init()` function, which registers the `vec0` virtual table type.

## Schema

The database has six tables. The full DDL is in `db/migrations.sql`.

| Table | Primary Key | Purpose |
|-------|-------------|---------|
| `documents` | `id` (UUID text) | Document metadata |
| `doc_chunks` | `id` (autoincrement int) | Chunk content, linked to documents via `doc_id` FK |
| `doc_chunk_vectors` | `chunk_id` (int) | vec0 virtual table with float embeddings (dimensions set by embedding model) |
| `code_files` | `id` (UUID text) | Source files referenced in documentation |
| `refs` | `(doc_id, code_file_id)` | Junction table connecting documents to code files |
| `related_docs` | `(from_doc_id, to_doc_id)` | Junction table connecting related documents |

## Document Operations

Implemented in `internal/store/documents.go`.

### AddDocument

Inserts a new document with a generated UUID. Returns the full `Document` struct read back from the database (to capture `indexed_at` default).

### GetDocumentByPath

Looks up a document by its `file_path` column. Used during indexing to check if a document already exists for incremental indexing.

### ListDocuments

Returns all documents ordered by `file_path`. Used by the `list_documents` MCP tool.

### DeleteDocument

Deletes a document and all associated data in this order:
1. Delete vector entries from `doc_chunk_vectors` for the document's chunks
2. Delete `related_docs` edges (both directions)
3. Delete `refs` edges
4. Delete `doc_chunks` rows
5. Delete the `documents` row

This explicit ordering handles the vec0 virtual table, which doesn't participate in SQLite's CASCADE behavior.

## Chunk Operations

Implemented in `internal/store/chunks.go`.

### AddChunk

Inserts a chunk into `doc_chunks` with its metadata (file path, section heading, section hierarchy, content, token count, signal metadata). Calls `ensureVecTable()` to lazily create the vec0 table if it doesn't exist yet, then inserts the embedding vector.

Vectors arrive as `[]float64` from the embedding provider and are converted to little-endian `[]byte` of float32 values via `float64sToFloat32Bytes`, as sqlite-vec expects float32 binary format.

### GetChunksForDocument

Returns all chunks for a document, ordered by `chunk_index`. Used by internal operations.

### SearchChunks

Vector similarity search — the core of Librarian's search functionality. The process:

1. Convert the query vector from `[]float64` to float32 bytes
2. Over-fetch candidates: request `limit * 3` results (minimum 10) from sqlite-vec
3. Query sqlite-vec using `WHERE embedding MATCH ? AND k = ?` with `ORDER BY distance`
4. Re-rank candidates using metadata signals
5. Return the top `limit` results

The over-fetching allows the re-ranker to promote relevant results that sqlite-vec's pure vector distance might rank slightly lower.

## Search Re-Ranking

After sqlite-vec returns candidates ranked by vector distance, `rerankWithSignals` applies a weighted scoring formula:

```
finalScore = 0.90 * vectorScore + 0.10 * metadataBoost
```

Where `vectorScore = 1.0 - distance` (converting distance to similarity).

### Metadata Boost Calculation

`computeMetadataBoost` parses the `signal_meta` JSON from each chunk and computes a boost score:

| Signal Type | Boost per Signal |
|-------------|-----------------|
| High-value inline labels (`warning`, `decision`, `important`) | +0.3 |
| Other inline labels (`note`, `example`, `todo`, etc.) | +0.1 |
| Risk markers (`deprecated`, `breaking-change`, `unsafe`, etc.) | +0.2 |

The boost is capped at 1.0.

This means chunks containing warnings, decisions, or deprecation notices get a ranking lift, surfacing actionable information over plain descriptions. Since the metadata weight is only 10%, it adjusts ordering within similar vector distances rather than overriding semantic relevance.

## Code File Operations

Implemented in `internal/store/codefiles.go`.

### AddCodeFile

Inserts a code file reference with a UUID, file path, language, and reference type (`file`, `directory`, or `pattern`).

### GetCodeFileByPath

Looks up a code file by its `file_path`. Used during indexing to avoid creating duplicate entries.

### AddReference

Inserts or replaces a row in the `refs` junction table connecting a document to a code file, with a `context` string describing where the reference appears.

### GetReferencedCodeFiles

Joins `refs` with `code_files` to return all code files referenced by a given document. Used by the `get_context` MCP tool.

### AddRelatedDoc

Inserts or replaces a row in the `related_docs` junction table. Used after indexing to connect documents that share code references.

### GetRelatedDocuments

Joins `related_docs` with `documents` to return all documents related to a given document. Used by the `get_context` MCP tool.

## Vector Format

sqlite-vec expects embeddings as binary blobs of little-endian float32 values. The embedding providers return `[]float64`, so conversion happens at the store boundary:

```go
func float64sToFloat32Bytes(vec []float64) []byte {
    buf := make([]byte, len(vec)*4)
    for i, v := range vec {
        binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(v)))
    }
    return buf
}
```

The size per chunk depends on the embedding model (e.g., 3072 dims * 4 bytes = 12,288 bytes for Gemini, 768 dims * 4 bytes = 3,072 bytes for nomic-embed-text).
