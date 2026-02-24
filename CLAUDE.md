# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Librarian — semantic documentation search for projects, powered by SQLite + sqlite-vec, exposed via MCP. Written in Go.

## Build & Test

```sh
go build -o librarian .          # build binary
go test ./...                    # run all tests
go test ./internal/indexer       # run tests for a specific package
go test -v -run TestEmphasis     # run a specific test by name
make build                       # same as go build via Makefile
```

CGo is required (`CGO_ENABLED=1`, the Go default) because both `mattn/go-sqlite3` and `sqlite-vec` are C libraries.

## Librarian MCP

This project has a librarian MCP server configured in `.mcp.json`. Use it to understand the codebase:

- `search_docs` — semantic search across indexed docs (start here for most questions)
- `get_context` — deep briefing with related code files and documents (use for architecture/design questions)
- `get_document` — read a full doc by file path
- `list_documents` — browse all indexed docs

Prefer these tools over raw file reads when looking for how something works or where something is implemented.

## Architecture

### Dependency wiring

Commands in `cmd/` share a global `*config.Config` initialized via Cobra/Viper. Each command builds its dependency chain:

```
config.Load() → embedding.NewEmbedder(cfg.Embedding) → store.Open(cfg.DBPath) → indexer.New(store, cfg, embedder)
```

### Indexing pipeline (`internal/indexer/`)

Four stages run per file: **Walk** → **Parse** → **Chunk** → **Store**.

- **Walk** (`walker.go`): finds `.md`/`.markdown` files, applies exclude patterns.
- **Parse** (`parser.go`): goldmark AST walk extracting frontmatter, headings, sections, diagrams, tables, and emphasis signals. Diagrams/tables are converted to natural language summaries for better embedding quality.
- **Chunk** (`chunker.go`): splits by section at H2 boundaries, falls back to paragraph splitting when sections exceed `MaxTokens`. Enriches embedding text with document title, section hierarchy, and signal labels.
- **Store**: generates embeddings, stores documents + chunks + code references. After all files are indexed, a second pass (`buildRelatedDocEdges`) creates document-to-document graph edges based on shared code references.

Content hash (SHA-256) skips unchanged files on re-index.

### Store layer (`internal/store/`)

- Schema in `db/migrations.sql` is embedded at build time via `//go:embed`.
- The `vec0` virtual table (sqlite-vec) is created lazily on the first chunk insert because dimensions depend on the embedding model.
- Search uses over-fetch + re-rank: fetches `3×limit` candidates from vector search, boosts scores with metadata signals (warnings, decisions, risk markers), then returns the top N.
- Float64 embeddings from APIs are converted to little-endian float32 bytes for sqlite-vec storage.

### Embedding providers (`internal/embedding/`)

`Embedder` interface with a single `Embed(text string) ([]float64, error)` method. Two implementations: `GeminiEmbedder` and `OpenAIEmbedder` (for any OpenAI-compatible API like LM Studio). Provider selected by `NewEmbedder()` factory in `provider.go` based on `cfg.Provider`.

### MCP server (`internal/mcpserver/`)

Stdio-based JSON-RPC server using `mcp-go`. Each tool is registered in its own file and wired in `Serve()` in `server.go`. The `get_context` tool is the most complex — it joins chunks, documents, code references, and related docs for a comprehensive briefing.

## Adding new components

- **New embedding provider**: implement `Embedder` interface in `internal/embedding/`, add case to `NewEmbedder()` factory.
- **New MCP tool**: create file in `internal/mcpserver/`, register in `Serve()`.
