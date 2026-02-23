---
title: Architecture
type: architecture
description: System architecture overview covering the data model, data flow, project structure, and key design decisions.
---

# Architecture

Librarian is a Go CLI that indexes project documentation into [HelixDB](https://helix-db.com) (a graph + vector database) and exposes it to AI coding tools via the [Model Context Protocol](https://modelcontextprotocol.io) (MCP).

## System Overview

```
 Markdown files          Librarian CLI           HelixDB
┌──────────────┐    ┌────────────────────┐    ┌──────────────┐
│ docs/*.md    │───>│  walker / parser   │───>│  Document    │
│              │    │  chunker / refs    │    │  DocChunk    │
└──────────────┘    └────────────────────┘    │  CodeFile    │
                            │                 │  (edges)     │
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

1. **`librarian index`** - Walks a docs directory, parses markdown, chunks content, extracts code references, and stores everything in HelixDB.
2. **`librarian serve`** - Starts an MCP server over stdio that exposes search, retrieval, and update tools backed by HelixDB.

## Project Structure

```
cmd/
  root.go          CLI entrypoint, global flags, Viper config init
  init.go          `librarian init` - deploy HelixDB schema
  index.go         `librarian index` - run the indexing pipeline
  search.go        `librarian search` - CLI vector search
  status.go        `librarian status` - show index statistics
  serve.go         `librarian serve` - start MCP stdio server

internal/
  config/
    config.go      Configuration struct, defaults, Viper binding

  embedding/
    gemini.go      Gemini text-embedding-004 client (Embedder interface)

  indexer/
    walker.go      Filesystem walk, file filtering, exclude patterns
    parser.go      Goldmark-based markdown parsing, frontmatter, AST walk
    chunker.go     Section-aware chunking with paragraph fallback
    references.go  Regex-based code file reference extraction
    indexer.go      Orchestrator: hash check, store, build edges

  helix/
    client.go      HelixDB client (helix-go SDK wrapper)
    types.go       Go types for Document, DocChunk, CodeFile
    documents.go   Document CRUD operations
    chunks.go      Chunk add/search/list operations
    codefiles.go   CodeFile + References + RelatedDoc operations

  mcpserver/
    server.go          MCP server setup (mcp-go SDK)
    search_docs.go     search_docs tool
    get_document.go    get_document tool
    get_context.go     get_context tool (intelligence briefing)
    list_documents.go  list_documents tool
    update_docs.go     update_docs tool

db/
  schema.hx        HelixDB schema (embedded at build time)
  queries.hx       HelixDB query definitions (embedded at build time)
```

## Data Model

The HelixDB schema defines three node/vector types and three edge types, forming a graph that connects documents, their vector chunks, and the code files they reference.

```
                    ┌──────────────┐
               ┌───>│   DocChunk   │  (vector node - searchable)
  HasChunk     │    │              │
               │    │ file_path    │
┌──────────┐───┘    │ section_*    │
│ Document │        │ content      │
│          │        │ token_count  │
│ file_path│        └──────────────┘
│ title    │
│ doc_type │───┐    ┌──────────────┐
│ summary  │   │    │   CodeFile   │
│ headings │   └───>│              │
│ content_ │  Refs  │ file_path    │
│   hash   │        │ language     │
│ chunk_   │        └──────────────┘
│   count  │
│ indexed_ │
│   at     │───┐
└──────────┘   │    ┌──────────────┐
               └───>│  Document    │
          RelatedDoc│  (another)   │
                    └──────────────┘
```

### Node Types

| Type | Kind | Fields |
|------|------|--------|
| `Document` | Node (`N`) | `file_path` (indexed), `title`, `doc_type`, `summary`, `headings`, `frontmatter`, `content_hash`, `chunk_count`, `indexed_at` |
| `DocChunk` | Vector (`V`) | `file_path`, `section_heading`, `section_hierarchy`, `chunk_index`, `content`, `token_count` |
| `CodeFile` | Node (`N`) | `file_path` (indexed), `language`, `last_referenced_at` |

### Edge Types

| Edge | From | To | Properties |
|------|------|----|------------|
| `HasChunk` | `Document` | `DocChunk` | _(none)_ |
| `References` | `Document` | `CodeFile` | `context` (the source line containing the reference) |
| `RelatedDoc` | `Document` | `Document` | `relation_type` (e.g. `"shared_code_references"`) |

`DocChunk` is declared as a **vector node** (`V`), which enables vector similarity search. Embeddings are generated client-side using the Gemini API and passed as raw vectors to HelixDB's `AddV` and `SearchV` operations.

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
                  create Document node + DocChunk vectors + HasChunk edges,
                  extract code references → CodeFile nodes + References edges,
                  build RelatedDoc edges between docs sharing code references
```

See [Indexing Pipeline](indexing.md) for full details on each stage.

## Key Design Decisions

### Section-aware chunking over fixed-window

Fixed-window chunking (e.g., every 512 tokens) splits text without regard for semantic boundaries, producing chunks that start mid-paragraph or mid-section. Librarian instead splits at H2 heading boundaries, keeping each section as a coherent unit. When a section exceeds `max_tokens`, it falls back to splitting at paragraph boundaries (`\n\n`). This produces chunks that align with how authors organize information.

### Graph edges for code references

Documentation frequently references source files (e.g., `internal/helix/client.go`). Rather than treating these as plain text, Librarian extracts them as structured `CodeFile` nodes connected by `References` edges. This enables the `get_context` tool to traverse the graph: search for chunks, find their source documents, follow `References` edges to discover relevant code files, and follow `RelatedDoc` edges to surface related documentation.

### Client-side Gemini embeddings

Embedding generation happens client-side using the Gemini `gemini-embedding-001` API (3072 dimensions). The `internal/embedding` package provides an `Embedder` interface with a `GeminiEmbedder` implementation. During indexing, each chunk is embedded before being stored as a raw vector via `AddV`. During search, the query is embedded before being passed to `SearchV`. This avoids requiring an `OPENAI_API_KEY` in the HelixDB Docker container and gives direct control over the embedding model. A `GEMINI_API_KEY` environment variable (or `embedding.api_key` in config) is required.

### Content hashing for incremental indexing

Each document's raw content is hashed with SHA-256. On subsequent index runs, Librarian compares the stored hash with the current file's hash and skips unchanged documents. This makes re-indexing fast for large documentation sets where only a few files change between runs. The `--force` flag bypasses this check when a full re-index is needed.

### MCP over stdio

The MCP server uses stdio transport (`server.ServeStdio`), which is the standard transport for local tool servers in Claude Code and Cursor. This avoids the complexity of HTTP servers, port management, and authentication for what is fundamentally a local development tool.
