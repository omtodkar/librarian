---
title: Storage Layer
type: reference
description: SQLite + sqlite-vec schema, document and chunk operations, vector search with signal-aware re-ranking, code file tracking, and the graph spine that links documents, code files, and code symbols.
---

# Storage Layer

The storage layer (`internal/store/`) manages all interactions with the SQLite database: document CRUD, chunk storage with vector embeddings, code file tracking, and the graph spine that connects documents, code files, and code symbols.

## Initialisation

`store.Open(dbPath)` in `internal/store/store.go`:

1. Creates the parent directory if missing.
2. Opens a SQLite connection with WAL + foreign keys via query string: `?_journal_mode=WAL&_foreign_keys=on`.
3. Refuses a pre-goose database (librarian tables present, no `goose_db_version`) with a clear error — users should `rm .librarian/librarian.db` and re-index.
4. Applies any pending migrations from `db/migrations/*.sql` via [pressly/goose](https://github.com/pressly/goose) (embedded at build time via `db/embed.go`).

The `doc_chunk_vectors` vec0 virtual table is created **lazily** on the first vector insert via `ensureVecTable()`, with dimensions derived from the actual embedding model's output. This avoids hardcoding a vector size and lets the same binary work with any embedding provider.

The sqlite-vec extension is loaded automatically via `sqlite_vec.Auto()` in the package `init()` function.

## Schema

Seven persistent tables plus the lazy-created vector table, plus goose's own `goose_db_version` tracker. Full DDL in `db/migrations/0001_initial_schema.sql`.

| Table | Primary key | Purpose |
|---|---|---|
| `documents` | `id` (UUID text) | Document metadata: path, title, type, content hash, chunk count |
| `doc_chunks` | `id` (autoincrement) | Chunk content, linked to documents via `doc_id` FK, with `signal_meta` JSON |
| `doc_chunk_vectors` | `chunk_id` (int) | vec0 virtual table, float32 embeddings (dims set by embedding model) |
| `code_files` | `id` (UUID text) | Source files mentioned in documentation |
| `refs` | `(doc_id, code_file_id)` | Junction: documents → code files, with context string |
| `graph_nodes` | `id` (namespaced text) | Generic node for every indexed entity (doc, file, symbol, config key) |
| `graph_edges` | autoincrement | Typed edges between nodes: `mentions`, `shared_code_ref`, `imports`, `calls`, … |
| `embedding_meta` | `key` (text) | Two rows — `model` and `dimension` — recorded on first vec0 write; checked on every `AddChunk` to detect config-level embedding model swaps |

`related_docs` from earlier versions is **superseded** by the graph spine. It isn't in the current baseline migration — users coming from the pre-goose era are asked to re-index from scratch (see "Schema evolution" below).

## Document operations

`internal/store/documents.go`

| Method | Purpose |
|---|---|
| `AddDocument` | Insert with generated UUID; returns the struct read back from the DB |
| `GetDocumentByPath` | Lookup by `file_path`; used by the incremental-indexing hash check |
| `ListDocuments` | All documents ordered by path; backs `list_documents` / `librarian list` |
| `DeleteDocument` | Cascading delete (see below) |

`DeleteDocument` explicitly orders its deletes because `doc_chunk_vectors` is a virtual table and doesn't participate in SQLite's FK cascade:

1. Delete rows from `doc_chunk_vectors` for this doc's chunks
2. Delete `graph_edges` incident to the doc's graph node
3. Delete the doc's `graph_nodes` row
4. Delete `refs`
5. Delete `doc_chunks` (FK-cascaded but explicit for clarity)
6. Delete the `documents` row

## Chunk operations

`internal/store/chunks.go`

### `AddChunk`

Inserts into `doc_chunks` with file path, section heading, section hierarchy, content, token count, chunk index, and `signal_meta` JSON. Calls `ensureVecTable()` to lazily create the vec0 table on the first insert, then inserts the embedding.

Vectors arrive as `[]float64` from the embedding provider and are converted to little-endian `[]byte` of float32 values via `float64sToFloat32Bytes` — sqlite-vec's expected binary format.

### `GetChunksForDocument`

Returns chunks ordered by `chunk_index`. Used internally.

### `SearchChunks` — vector search + re-rank

The core retrieval path.

1. Convert query vector `[]float64` → float32 bytes.
2. **Over-fetch**: request `limit * 3` candidates (minimum 10) from sqlite-vec via `WHERE embedding MATCH ? AND k = ? ORDER BY distance`.
3. **Re-rank**: `rerankWithSignals` computes `finalScore = 0.90 * vectorScore + 0.10 * metadataBoost` for each candidate.
4. Return the top `limit` by final score.

Over-fetching gives the re-ranker room to promote actionable chunks (warnings, decisions, annotated code) slightly above neutral chunks at similar vector distance.

## Search re-ranking

`computeMetadataBoost` parses `signal_meta` JSON per chunk:

| Signal | Boost |
|---|---|
| High-value inline labels (`warning`, `decision`, `important`) | +0.3 each |
| Other inline labels (`note`, `example`, `todo`, …) | +0.1 each |
| Risk markers (`deprecated`, `breaking-change`, `unsafe`, …) | +0.2 each |
| Code annotations (`@Deprecated`, `@Transactional`, …) | +0.1 each |

Boost is capped at 1.0 so no single chunk can dominate. With the metadata weight at only 10%, signals adjust ordering within similar vector distances rather than overriding semantic relevance — a keyword-matched chunk with no signals still outranks an off-topic chunk with many signals.

## Code file operations

`internal/store/codefiles.go`

| Method | Purpose |
|---|---|
| `AddCodeFile` | Insert with generated UUID, path, language, ref type (`file` / `directory` / `pattern`) |
| `GetCodeFileByPath` | Lookup by path; used to dedupe during indexing |
| `AddReference` | Insert/replace `refs` row linking a doc to a code file |
| `GetReferencedCodeFiles` | Join `refs` + `code_files` to list refs out of a doc (used by `get_context`) |

## Graph layer

`internal/store/graph.go`

The graph spine is a generic layer: every indexed thing projects into a `graph_node` with a stable **namespaced id**, and typed `graph_edges` connect them. This lets CLI and MCP tools walk the graph uniformly — "what does `auth.py` connect to?" and "what references `validateToken`?" have the same query shape.

### Node id conventions

| Prefix | Produced by | Example |
|---|---|---|
| `doc:` | `DocNodeID(uuid)` | `doc:3b2c…` |
| `file:` | `CodeFileNodeID(path)` | `file:internal/auth/oauth.go` |
| `sym:` | `SymbolNodeID(fqn)` | `sym:com.acme.Auth.validate` |
| `key:` | `ConfigKeyNodeID(path)` | `key:spring.datasource.url` |
| `ext:` | `ExternalPackageNodeID(spec)` | `ext:lodash`, `ext:@scope/pkg` |
| `bufgen:` | `BufManifestNodeID(protoPath)` | `bufgen:api/auth.proto` |

`NodeIDPrefixes()` is the single source of truth; CLI commands that accept user input (`librarian neighbors X`) use it to auto-expand unqualified names.

### Edge kinds

- `mentions` — document → code_file (docs-pass emits from prose that names a source file)
- `shared_code_ref` — document → document (both reference the same code_file)
- `contains` — code_file → symbol (graph-pass emits one per parsed Unit)
- `import` — code_file → symbol / code_file / external_package (depending on resolver output)
- `call` — symbol → symbol (reserved; not emitted by any grammar today)
- `inherits` — symbol → symbol (class / interface / protocol parent relationship). `Edge.Metadata.relation` carries the flavor: `extends`, `implements`, `mixes` (Dart mixins), `conforms` (Swift protocols), `embeds` (Go interface embedding). `extends` and `implements` remain backward-compatible aliases in `graphTargetID` / `graphNodeKindFromRef` for hand-authored edges and pre-lib-wji.1 data, but new extraction emits `inherits`.
- `implements_rpc` — symbol → symbol (generated-code method → proto rpc declaration). Materialised by the post-graph-pass resolver `buildImplementsRPCEdges` (lib-6wz) via per-language naming conventions: protoc-gen-go/grpc-go emit `pkg.SvcServer.Method` / `pkg.SvcClient.Method` / `pkg.UnimplementedSvcServer.Method`; protoc-gen-connect-go emits `pkg.SvcHandler.Method` (server-side handler interface); protoc-gen-dart emits `pkg.SvcClient.methodName` / `pkg.SvcBase.methodName`; @bufbuild/protoc-gen-es emits `pkg.SvcClient.methodName` / `pkg.Svc.methodName`. lib-4kb tightens matching by combining `buf.gen.yaml` plugin out-dirs with per-proto `option *_package` values into a per-proto-file buf manifest (`bufgen:<proto-path>` node, kind `buf_manifest`); each candidate's `source_path` must live under ANY of the manifest's language-specific prefixes (slice, since protoc-gen-go and protoc-gen-connect-go can share the same language key but use different out-dirs). Missing `buf.gen.yaml` or no prefix for the candidate's language → graceful fallback to lib-6wz's name-only matching for that candidate, so projects without buf still get the lib-6wz behaviour (including its known false positives) unchanged.
- `call_rpc` — symbol → symbol (hand-written call site → proto rpc declaration). Represents a **runtime invocation** of a proto rpc by application code. From = the enclosing function/method symbol containing the call; To = the proto rpc symbol (same target set as `implements_rpc`). Emitted by the post-graph-pass call-site detector `buildCallRPCEdges` (lib-4g2.3) from TypeScript/JavaScript files that use `createPromiseClient` / `createClient` from `@connectrpc/connect` or compatible packages. Kept distinct from `implements_rpc` because the two relationships have different semantics: `implements_rpc` captures **compile-time codegen derivation** (a generated stub is bound to a proto rpc at build time, with no caller interaction), while `call_rpc` captures **runtime invocation** (a hand-written function actively calls the rpc at runtime). An AI assistant asking "who implements this rpc?" should query `implements_rpc`; one asking "who calls this rpc?" should query `call_rpc`. The `trace_rpc` MCP tool exposes both: `Implementations` comes from `implements_rpc` edges and `Callers` includes `call_rpc` direct callers alongside the transitive `call` BFS from implementation nodes. v1 limitation: only file-scoped bindings are detected; see lib-44f for cross-file client re-export resolution.

### Buf codegen manifest (lib-4kb)

The `buf_manifest` node kind holds per-proto-file codegen path prefixes the `implements_rpc` resolver uses to tighten matching. One node per `.proto` in the project, keyed by `bufgen:<proto-path>`. Metadata shape:

```json
{
  "proto_path": "api/auth.proto",
  "proto_package": "auth",
  "lang_prefixes": {"go": ["gen/go/authpb", "gen/connect/authpb"], "dart": ["gen/dart/api"], "ts": ["gen/ts/api"]}
}
```

`lang_prefixes` values are arrays rather than scalars because a language may have more than one plugin with different output directories — for example, a repo using both protoc-gen-go (message types) and protoc-gen-connect-go (service interfaces) will have two Go output trees. The resolver accepts a candidate if its `source_path` falls under **any** prefix in the slice.

Built by `buildBufManifest` (runs between the per-file graph pass and `buildImplementsRPCEdges`) by combining:

- Per-buf.gen.yaml plugin lists — stashed on the yaml file's `code_file` graph_node metadata under `buf_gen` by the yaml handler on detection of a `buf.gen.yaml` / `buf.gen.yml` filename.
- Per-proto file-level `option *_package` values — stashed on the proto file's `code_file` graph_node metadata under `options` by the proto grammar's `PostProcess` hook (lib-cym).

Prefix computation per language:

- `go` with `paths=source_relative` → `<out>/<proto-source-dir>`
- `go` without `paths=source_relative` and with `option go_package` → `<out>/<last-path-segment-of-go_package>`
- `go` with neither → no prefix (resolver falls back to name-only for this language)
- `dart`, `ts` → `<out>/<proto-source-dir>` (both generators mirror the source tree regardless of opts)

Absent a `buf.gen.yaml` anywhere in the project, no `buf_manifest` nodes are emitted and the resolver falls back wholesale to lib-6wz's name-only matching.

### Operations

| Method | Purpose |
|---|---|
| `UpsertNode`, `UpsertEdge` | Idempotent inserts (INSERT OR REPLACE) |
| `GetNode(id)` | Exact id lookup |
| `FindNodes(query, limit)` | Substring search on id / label / source_path (SQL LIKE with wildcard escaping) |
| `Neighbors(id, direction)` | Edges incident to a node (`in` / `out` / both) |
| `ShortestPath(from, to, maxDepth)` | BFS in Go code (not CTE) — avoids CTE escape hazards from ids containing `%`, `_`, `,` |
| `ListNodes` / `ListEdges` | Full dump; used by `librarian report` for community detection + centrality |
| `ListNodesByKindWithMetadataContaining` | Kind-scoped SQL LIKE over `metadata`; backs the buf manifest harvest (`kind=code_file`, substring=`"buf_gen"` or `"options"`) and the implements_rpc rpc-node scan (`kind=symbol`, substring=`"input_type":`). |

## Vector format

sqlite-vec expects embeddings as little-endian float32 blobs. Providers return `[]float64`, so conversion happens at the store boundary:

```go
func float64sToFloat32Bytes(vec []float64) []byte {
    buf := make([]byte, len(vec)*4)
    for i, v := range vec {
        binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(float32(v)))
    }
    return buf
}
```

Size per chunk = `dims × 4 bytes`. For a 3072-dim Gemini embedding that's ~12 KB per chunk; for a 768-dim nomic-embed-text it's ~3 KB.

## Schema evolution

Migrations live under `db/migrations/<N>_<name>.sql` and are tracked by [pressly/goose](https://github.com/pressly/goose) in the `goose_db_version` table. `store.Open` calls `goose.Up` on every start; the tracker makes re-runs a no-op once migrations are applied.

Each file uses goose's annotation syntax:

```sql
-- +goose Up
-- +goose StatementBegin
CREATE TABLE foo ( ... );
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE foo;
-- +goose StatementEnd
```

To add a migration, create `db/migrations/000N_what_you_did.sql` with the next sequential number and both Up/Down sections. The file is picked up automatically via the `//go:embed migrations/*.sql` directive in `db/embed.go`.

Pre-goose databases (created before this framework landed) are rejected at `Open` with a friendly error — users delete `.librarian/librarian.db` and re-index to rebuild from the current baseline.

For the full upgrade and recovery guide, including what to do when changing embedding models, see [Upgrading](upgrading.md).

The `doc_chunk_vectors` vec0 table is deliberately **not** managed by migrations: its dimension is a runtime property of the embedding model, created lazily on first insert.
