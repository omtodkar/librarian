---
title: Architecture
type: architecture
description: System architecture overview — the FileHandler abstraction, the SQLite + sqlite-vec store, the graph spine, and the command surface exposed as CLI and MCP.
---

# Architecture

Librarian is a Go CLI that indexes a project's documentation **and** source code into an embedded SQLite database (with [sqlite-vec](https://github.com/asg017/sqlite-vec) for vector search), then exposes the index via both a rich CLI and an opt-in MCP server.

## System overview

```
Files (9+ formats)            Librarian CLI           SQLite + sqlite-vec
┌──────────────────┐   ┌─────────────────────┐   ┌────────────────────┐
│ docs/*.md        │   │  walker → handler   │   │ documents          │
│ *.go .py .java   │──>│  parse → chunk      │──>│ doc_chunks         │
│ .ts .tsx .js .kt │   │  signals + refs     │   │ doc_chunk_vectors  │
│ .swift .yaml     │   │  store + graph pass │   │ code_files + refs  │
│ .docx .xlsx .pptx│   │                     │   │ graph_nodes/_edges │
│ .pdf             │   └──────────┬──────────┘   └──────────┬─────────┘
└──────────────────┘              │                         │
                                  ▼                         │
                         ┌────────────────┐                 │
                         │  CLI surface   │<────────────────┤
                         │  search/graph  │  queries        │
                         └────────────────┘                 │
                         ┌────────────────┐                 │
                         │  MCP server    │<────────────────┘
                         │  (stdio)       │  via tools
                         └────────────────┘
```

Everything lives in a project-local **workspace** at `.librarian/` (discovered by walking up from the current directory). `librarian init` bootstraps one; every other command requires one.

## Entry points

| Mode | Command | Notes |
|---|---|---|
| Indexing | `librarian index [dir]` | Walks `docs_dir` + code, stores everything |
| CLI search | `librarian search`, `context`, `neighbors`, `path`, `explain`, `list`, `doc`, `report`, `status` | See [CLI Reference](cli.md) |
| MCP server | `librarian mcp serve` | Stdio JSON-RPC; 5 tools — see [MCP Tools](mcp-tools.md) |
| Platform install | `librarian install` | Writes assistant-platform pointers (CLAUDE.md, AGENTS.md, …) — see [CLI Reference](cli.md#librarian-install) |

## Project structure

```
cmd/                         Cobra CLI — 14 subcommands
  root.go                    global flags, Viper init, workspace discovery
  init.go index.go search.go context.go list.go doc.go status.go update.go
  neighbors.go path.go explain.go report.go      # graph commands
  install.go                 assistant-platform integration pointers
  mcp.go                     `librarian mcp serve`

internal/
  config/                    Config struct, Viper binding, per-handler sub-configs
  workspace/                 .librarian/ discovery and path helpers
  embedding/                 Gemini + OpenAI-compatible providers
  indexer/
    handler.go               FileHandler interface + ParsedDoc/Unit/Signal/Reference shapes
    registry.go              Extension → handler map
    walker.go                Filesystem walk + exclude patterns
    chunker.go               Shared ChunkSections: token-aware section splitting w/ paragraph fallback
    indexer.go               Orchestrator: hash check → parse → chunk → embed → store → graph edges
    parser.go                Goldmark markdown AST walk (shared w/ markdown handler)
    diagrams.go tables.go    Markdown-specific: diagram/table linearisation for embedding
    emphasis.go signals.go   Signal extraction (warnings, rationale, risk markers, annotations)
    references.go            Code-path regex extraction for markdown references
    handlers/
      markdown/              .md, .markdown — baseline goldmark handler
      code/                  Tree-sitter grammars: Go, Python, Java, JS, TS, TSX, Kotlin, Swift (+ Dart binding vendored for lib-wji.3)
      config/                YAML / JSON / TOML / XML / properties / env
      office/                DOCX / XLSX / PPTX
      pdf/                   PDF via go-pdfium (WebAssembly)
      defaults/              Aggregator: blank-imports every handler
  store/
    store.go                 Open/close, schema, vec0 lazy init
    documents.go             Document CRUD
    chunks.go                Chunk insert + SearchChunks (over-fetch + re-rank)
    codefiles.go             code_files + refs
    graph.go                 graph_nodes + graph_edges + BFS shortest-path
    types.go                 Shared structs
  analytics/                 Community detection + centrality for `librarian report`
  report/                    HTML + JSON + markdown report generators
  install/                   `librarian install` — platform registry + idempotent writes
  mcpserver/                 MCP tool implementations (mcp-go SDK)

db/
  migrations.sql             Embedded SQLite schema

docs/                        This documentation (indexed by Librarian itself)
```

## Data model

Eight persistent tables plus one virtual table. Three primary entity tables (`documents`, `code_files`, `graph_nodes`) and three relational tables (`doc_chunks`, `refs`, `graph_edges`), plus the lazy-created vector table.

```
                         ┌────────────────┐
          doc_id FK ┌───>│   doc_chunks   │ (content, signal_meta)
                    │    └────────┬───────┘
                    │             │ 1:1
                    │             ▼
                    │    ┌──────────────────────┐
                    │    │  doc_chunk_vectors   │ vec0 virtual, float32 BLOBs
                    │    │  (created lazily)    │
                    │    └──────────────────────┘
┌────────────┐──────┘    ┌────────────────┐
│ documents  │──┐        │  code_files    │
│ id(uuid)   │  └refs───>│  id(uuid)      │
│ title      │           │  file_path     │
│ doc_type   │           │  language      │
│ headings   │           └────────┬───────┘
│ content_   │                    │
│   hash     │           ┌────────┴──────────┐
│ chunk_count│           │                   ▼
└────────────┘    ┌──────┴─────────┐  ┌──────────────┐
                  │  graph_nodes   │  │  graph_edges │
                  │  id (namespaced│  │  from_node   │
                  │    "doc:…",    │  │  to_node     │
                  │    "file:…",   │  │  kind        │
                  │    "sym:…",    │  │  weight      │
                  │    "key:…")    │  └──────────────┘
                  │  kind, label   │  typed edges:
                  │  source_path   │   mentions, shared_code_ref
                  │  metadata      │   contains, import, call
                  └────────────────┘   inherits, requires, part
```

See [Storage Layer](storage.md) for full schema + operations and [Handlers](handlers.md) for how each format projects into these tables.

## Indexing flow

`librarian index` runs two passes, scoped to different roots:

1. **Docs pass** over `docs_dir` — produces chunks + vectors + `mentions` edges from doc prose to referenced code files. This is the knowledge base: `search_docs` and `get_context` query here.
2. **Graph pass** over `ProjectRoot` (the workspace root) — parses every source file the walker hasn't excluded, projects code symbols into `graph_nodes{kind=symbol}` with `contains` edges from their file, and emits `import` / `call` / `inherits` / `requires` / `part` edges. The `inherits` edge covers class/interface/protocol parent relationships across every grammar (Java `extends`/`implements`, Python class bases, JS/TS class and interface heritage, Go interface embedding, Kotlin delegation_specifier heuristic, Swift per-flavor heuristic over inheritance_specifier nodes, Dart class heritage with extends+implements+with); the flavor lives in `Edge.Metadata.relation` (`extends` / `implements` / `mixes` / `conforms` / `embeds`). `requires` (Dart) is a distinct edge kind for `mixin M on Base` use-site constraints — kept separate from inheritance. `part` (Dart) routes file-join declarations (code_file → code_file). No chunks, no vectors — structural only, so the knowledge base stays curated prose.

```
 PASS 1: docs pass (docs_dir)
 input file ──> WalkDocs ──> registry.HandlerFor(ext)
                                   │
                                   ▼
                             FileHandler.Parse  ──> ParsedDoc
                                   │
                                   ▼
                             FileHandler.Chunk  ──> []Chunk
                                   │
                                   ▼
                             embedder.Embed     ──> []float64 per chunk
                                   │
                                   ▼
                             store.Add*         ──> documents + chunks + vectors
                                                    + code_files + refs
                                                    + graph_nodes per doc/file
                                                    + mentions / shared_code_ref edges

 PASS 2: graph pass (ProjectRoot)
 source file ──> WalkGraph ──> hard excludes + gitignore + graph.exclude_patterns
                                   │
                                   ▼
                             hash-gate: code_files.content_hash
                                   │  (skip if unchanged)
                                   ▼
                             FileHandler.Parse  ──> ParsedDoc
                                   │
                                   ▼
                             DeleteSymbolsForFile (wipe stale)
                                   │
                                   ▼
                             UpsertNode per symbol Unit
                             UpsertEdge contains (file → symbol)
                             UpsertEdge per parsed.Ref (imports/calls/…)
```

Content-hash gates run before Parse in both passes, so re-indexing is fast when only a handful of files change. Document-semantic formats (markdown, docx, xlsx, pptx, pdf) are skipped by the graph pass so files already handled by the docs pass aren't double-indexed. See [Indexing Pipeline](indexing.md) for stage-by-stage detail.

## Key design decisions

### Single abstraction for every format

One `FileHandler` interface; every format-specific package registers at import time. Adding a format is a self-contained subpackage plus one blank-import line in `internal/indexer/handlers/defaults`. The walker, store, signal extraction, re-ranker, and MCP server need no changes.

Office formats and PDFs convert to markdown internally and delegate `Parse` + `Chunk` to the markdown handler — this keeps chunking + signal logic in one place regardless of source format. See [Handlers](handlers.md).

### Embedded SQLite + sqlite-vec

Single-file database, zero external dependencies. The `vec0` virtual table is created **lazily** on first insert with dimensions derived from the live embedding model — no hardcoded dimension config, no re-index penalty for switching model families within a compatible dimension.

### Graph spine across formats

Every indexed thing — documents, code files, code symbols, config keys — projects into a `graph_node` with a namespaced id (`doc:…`, `file:…`, `sym:…`, `key:…`). Typed `graph_edges` (`mentions`, `shared_code_ref`, `contains`, `import`, `call`, `inherits`, `requires`, `part`) connect them. CLI commands `neighbors`, `path`, and `context` walk this graph; `report` emits topology summaries (god nodes, communities, cross-cluster bridges). `neighbors --edge-kind=<kind>` filters by edge kind (repeatable). See [Storage Layer](storage.md#graph-layer).

*Inspired by [graphify](https://github.com/graphify/graphify).*

### Section-aware chunking with paragraph fallback

`ChunkSections` is the shared splitter: one chunk per H2 section (or code symbol, or YAML key block) until a section exceeds `max_tokens`, then splits at paragraph boundaries — never mid-paragraph. Overlap lines span chunk boundaries for retrieval continuity. A context header (doc title + section hierarchy + signal line) prefixes each chunk's embedding text so the vector captures where it lives in the document, not just what it says.

### Signal-aware re-ranking

Each chunk carries `signal_meta` JSON: warnings, decisions, TODO/FIXME, risk markers (deprecated, breaking-change, unsafe), code annotations (`@Deprecated`, `@Transactional`), emphasis terms. After the vector KNN fetch, a weighted re-rank (`0.90 × vector + 0.10 × metadata boost`) promotes chunks with actionable signals — so a query for "authentication" will surface the chunk that mentions a **decision** about OAuth ahead of a neutral paragraph of the same semantic distance. When `rerank.provider` is configured, the top-N signal-ranked candidates are then passed through a cross-encoder (`POST /rerank`) for a final re-order before truncation to `limit`. On any error or timeout the cross-encoder step is skipped and the signal-ranked result is returned unchanged. See [Storage Layer](storage.md#search-re-ranking) and [Configuration](configuration.md#rerank).

### Pluggable embedding providers

`Embedder` interface with two implementations: Gemini (Google) and OpenAI-compatible (LM Studio, Ollama, vLLM, or any `/v1/embeddings` endpoint). Selection is one config field. See [Embedding](embedding.md).

### Incremental indexing

SHA-256 content hash per file; unchanged files skip `Parse` entirely. The docs pass stores its hash in `documents.content_hash`; the graph pass in `code_files.content_hash`. `--force` bypasses both for a full re-index. Unit-level granularity within a code file is a future improvement.

### Separate docs and graph passes

The CLI / MCP pitch is "help an AI understand your codebase" — that works only when the knowledge base (prose) and the code graph (every source file) are decoupled. One walk over `docs_dir` gets you search but no symbols; one walk over the project root gets you symbols but pollutes search with boilerplate. So both run in one `librarian index` invocation, each scoped to its own root. `--skip-docs` / `--skip-graph` let you run just one when iterating.

### MCP over stdio, opt-in

`librarian mcp serve` is an optional subcommand. The primary UX is the CLI + file-based platform pointers (`librarian install`). MCP is there for assistants that prefer structured tool calls over shelling out to the CLI. Stdio transport keeps it trivial to launch — no ports, no auth.
