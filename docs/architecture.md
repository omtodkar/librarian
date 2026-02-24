---
title: Architecture
type: architecture
description: System architecture overview covering the data model, data flow, project structure, and key design decisions.
---

# Architecture

Librarian is a Go CLI that indexes project documentation into an embedded SQLite database with [sqlite-vec](https://github.com/asg017/sqlite-vec) for vector search, and exposes it to AI coding tools via the [Model Context Protocol](https://modelcontextprotocol.io) (MCP).

## System Overview

```
 Markdown files          Librarian CLI           SQLite + sqlite-vec
┌──────────────┐    ┌────────────────────┐    ┌──────────────┐
│ docs/*.md    │───>│  walker / parser   │───>│  documents   │
│              │    │  chunker / refs    │    │  doc_chunks  │
└──────────────┘    └────────────────────┘    │  code_files  │
                            │                 │  (relations) │
                            │                 └──────┬───────┘
                    ┌───────┴────────┐               │
                    │  MCP Server    │<──────────────-┘
                    │  (stdio)       │  queries / vector search
                    └───────┬────────┘
                            │
                    AI coding tools
                    (Claude Code, Cursor, etc.)
```

The CLI has two modes of operation:

1. **`librarian index`** - Walks a docs directory, parses markdown, chunks content, extracts code references, and stores everything in SQLite.
2. **`librarian serve`** - Starts an MCP server over stdio that exposes search, retrieval, and update tools backed by SQLite.

## Project Structure

```
cmd/
  root.go          CLI entrypoint, global flags, Viper config init
  init.go          `librarian init` - create SQLite database
  index.go         `librarian index` - run the indexing pipeline
  search.go        `librarian search` - CLI vector search
  status.go        `librarian status` - show index statistics
  serve.go         `librarian serve` - start MCP stdio server

internal/
  config/
    config.go      Configuration struct, defaults, Viper binding

  embedding/
    gemini.go      Gemini embedding client (Embedder interface)

  indexer/
    walker.go      Filesystem walk, file filtering, exclude patterns
    parser.go      Goldmark-based markdown parsing, frontmatter, AST walk
    chunker.go     Section-aware chunking with paragraph fallback
    diagrams.go    Diagram detection and label extraction (Mermaid, PlantUML, ASCII)
    tables.go      Table linearization for better embeddings
    emphasis.go    Bold text signal extraction for metadata and re-ranking
    references.go  Regex-based code file reference extraction
    indexer.go      Orchestrator: hash check, store, build edges

  store/
    store.go       SQLite database open/close, schema init
    types.go       Go types for Document, DocChunk, CodeFile, input structs
    documents.go   Document CRUD operations
    chunks.go      Chunk add/search/list operations + vec0 vector storage
    codefiles.go   CodeFile + refs + related_docs operations

  mcpserver/
    server.go          MCP server setup (mcp-go SDK)
    search_docs.go     search_docs tool
    get_document.go    get_document tool
    get_context.go     get_context tool (intelligence briefing)
    list_documents.go  list_documents tool
    update_docs.go     update_docs tool

db/
  migrations.sql   SQLite schema (embedded at build time)
```

## Data Model

The SQLite schema uses six tables: three primary entity tables and three relationship tables, using standard relational joins to model the connections between documents, chunks, and code files.

```
                    ┌──────────────┐
               ┌───>│  doc_chunks  │  (content, linked by doc_id FK)
  doc_id FK    │    │              │
               │    │ file_path    │
┌──────────┐───┘    │ section_*    │     ┌──────────────────┐
│documents │        │ content      │────>│doc_chunk_vectors │
│          │        │ token_count  │     │ (vec0 virtual)   │
│ file_path│        └──────────────┘     │ embedding[3072]  │
│ title    │                             └──────────────────┘
│ doc_type │───┐    ┌──────────────┐
│ summary  │   │    │  code_files  │
│ headings │   └───>│              │
│ content_ │  refs  │ file_path    │
│   hash   │        │ language     │
│ chunk_   │        └──────────────┘
│   count  │
│ indexed_ │
│   at     │───┐
└──────────┘   │    ┌──────────────┐
               └───>│  documents   │
        related_docs│  (another)   │
                    └──────────────┘
```

### Tables

| Table | Purpose |
|-------|---------|
| `documents` | Document metadata: file path, title, type, content hash, chunk count |
| `doc_chunks` | Chunk content linked to documents via `doc_id` foreign key, with `signal_meta` for emphasis signals |
| `doc_chunk_vectors` | vec0 virtual table storing float32[3072] embeddings for similarity search |
| `code_files` | Source files referenced in documentation |
| `refs` | Junction table connecting documents to code files (with context) |
| `related_docs` | Junction table connecting documents that share code references |

### Key Relationships

| Relationship | Mechanism |
|-------------|-----------|
| Document → Chunks | `doc_chunks.doc_id` FK with `ON DELETE CASCADE` |
| Document → CodeFiles | `refs` junction table |
| Document → Document | `related_docs` junction table |

`doc_chunk_vectors` is a **vec0 virtual table** that enables vector similarity search. Embeddings are generated client-side using the Gemini API and stored as float32 arrays. The `MATCH` operator performs KNN search.

## Data Flow

When `librarian index` runs, a markdown file moves through four pipeline stages:

```
 docs/auth.md
      │
      ▼
 1. Walk ──────── Find all .md/.markdown files, apply exclude patterns
      │
      ▼
 2. Parse ─────── Goldmark AST walk: extract frontmatter, build section hierarchy
      │
      ▼
 3. Chunk ─────── Section-aware splitting at H2 boundaries, paragraph fallback,
      │           overlap between chunks, context header prepended for embedding
      ▼
 4. Store ─────── SHA-256 content hash check (skip if unchanged),
                  generate Gemini embeddings client-side for each chunk,
                  INSERT into documents + doc_chunks + doc_chunk_vectors,
                  extract code references → INSERT into code_files + refs,
                  build related_docs entries for docs sharing code references
```

See [Indexing Pipeline](indexing.md) for full details on each stage.

## Key Design Decisions

### Embedded SQLite + sqlite-vec

SQLite with the sqlite-vec extension provides an embedded single-file database with vector search, eliminating external dependencies like Docker or separate database servers. The database file lives at `.librarian/librarian.db` and is created automatically by `librarian init`.

### Section-aware chunking over fixed-window

Fixed-window chunking (e.g., every 512 tokens) splits text without regard for semantic boundaries, producing chunks that start mid-paragraph or mid-section. Librarian instead splits at H2 heading boundaries, keeping each section as a coherent unit. When a section exceeds `max_tokens`, it falls back to splitting at paragraph boundaries (`\n\n`). This produces chunks that align with how authors organize information.

### Relational tables for graph-like edges

Documentation frequently references source files (e.g., `internal/store/store.go`). Rather than treating these as plain text, Librarian extracts them as structured `code_files` rows connected via `refs`. This enables the `get_context` tool to join across tables: search for chunks, find their source documents, join to `refs` to discover relevant code files, and join to `related_docs` to surface related documentation.

### Client-side Gemini embeddings

Embedding generation happens client-side using the Gemini `gemini-embedding-001` API (3072 dimensions). The `internal/embedding` package provides an `Embedder` interface with a `GeminiEmbedder` implementation. During indexing, each chunk is embedded and the resulting float64 vector is converted to float32 before being stored in the vec0 virtual table. During search, the query is embedded and matched against stored vectors using sqlite-vec's KNN search.

### Content hashing for incremental indexing

Each document's raw content is hashed with SHA-256. On subsequent index runs, Librarian compares the stored hash with the current file's hash and skips unchanged documents. This makes re-indexing fast for large documentation sets where only a few files change between runs. The `--force` flag bypasses this check when a full re-index is needed.

### MCP over stdio

The MCP server uses stdio transport (`server.ServeStdio`), which is the standard transport for local tool servers in Claude Code and Cursor. This avoids the complexity of HTTP servers, port management, and authentication for what is fundamentally a local development tool.
