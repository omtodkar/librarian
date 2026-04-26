# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Grammars:** Kotlin (with modifier extraction, inheritance, receiver metadata), Swift (per-flavor `inherits`/`conforms`/`mixes`/`embeds` edges, extension as first-class symbol), Dart (mixin/extension/part-file support, two new edge kinds: `part` and `requires`), and Protocol Buffers (service, RPC, message, and field nodes with `extend` edges)
- **Graph edge kinds:** unified `inherits` edge across all grammars (relation stored in `Edge.Metadata`); `implements_rpc` linking generated-code methods to proto RPC definitions via naming conventions (protoc-gen-go/grpc-go, protoc-gen-dart, @bufbuild/protoc-gen-es); `buf_manifest` graph nodes harvested from `buf.gen.yaml` to tighten `implements_rpc` path matching
- **MCP tool:** `trace_rpc` for end-to-end gRPC RPC discovery — resolves a proto RPC to its generated stub and from there to every implementation in the codebase
- **Python graph:** relative import resolution to absolute symbol paths; automatic `src_root` detection from `pyproject.toml`; package-root memoization across the graph pass
- **JavaScript/TypeScript graph:** relative import specifier resolution to absolute file paths
- **Graph maintenance:** orphan symbol-node sweep (`librarian gc`); auto-triggered on `librarian index --force`
- **Store:** versioned schema migrations via `pressly/goose`; embedding model and vector dimension change detection with automatic re-index prompt
- **Embedding:** configurable batch size for API calls (`embedding.batch_size` in config)
- **Local embedding/reranking:** Infinity server integration as a GPU-optional, privacy-preserving local alternative to cloud embedding providers
- **Install:** `librarian uninstall` command with `--full` flag for complete workspace and integration removal
- **Indexer:** `--skip-docs` and `--skip-graph` flags to run either indexing pass independently

### Fixed
- Three bugs in the reindex flow exposed by live testing: stale chunk rows, mismatched vector dimensions on re-embed, and a graph-pass race on concurrent file updates

### Security
- Added `SECURITY.md` with coordinated disclosure policy, 90-day embargo window, and private reporting instructions via GitHub Private Vulnerability Reporting

## [0.1.0] - 2026-04-24

### Added
- **Core indexer:** two-pass pipeline — docs pass (chunks + vectors + MCP-searchable) and graph pass (code symbols, imports, call/inherits edges); SHA-256 content hashing skips unchanged files
- **Document handlers:** Markdown (section-aware chunking, diagram/table/emphasis signals), DOCX, XLSX, PPTX (markdown conversion pipeline), PDF (go-pdfium WebAssembly cascade)
- **Code grammars (6):** Go, Python (with PEP 695/613 type-alias extraction), Java (with annotation signals), TypeScript, TSX, and JavaScript — all via tree-sitter with typed symbol units
- **Config format handlers:** YAML, JSON, `.properties`, `.env`, TOML, XML
- **Graph analytics:** Louvain community detection, god-node identification, cross-cluster edge surfacing; outputs as `GRAPH_REPORT.md`, `graph.html`, and `graph.json`
- **Store layer:** SQLite + sqlite-vec with `vec0` virtual table, signal-weighted KNN re-ranking, float32 vector storage
- **Embedding providers:** Gemini and OpenAI-compatible (LM Studio, Ollama, any `/v1/embeddings` endpoint)
- **MCP server:** stdio JSON-RPC tools — `search_docs`, `get_context`, `get_document`, `list_documents`, `update_docs`
- **CLI commands:** `init`, `index`, `update`, `search`, `context`, `doc`, `list`, `status`, `neighbors`, `path`, `explain`, `report`, `install`, `mcp serve`; all commands support `--json` for machine-readable output
- **Platform install:** one-command integration for Claude Code, GitHub Copilot / Codex, Cursor, Gemini CLI, OpenCode, and Aider (`librarian install`)
- **Workspace discovery:** `.librarian/` config + ignore file + SQLite database; auto-discovered by walking up from CWD; bootstrapped by `librarian init`
