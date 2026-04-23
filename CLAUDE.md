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


<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:ca08a54f -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd dolt push
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
<!-- END BEADS INTEGRATION -->

<!-- librarian:start - managed by `librarian install`, do not edit -->
## Librarian

This project uses Librarian for semantic search and graph-based code navigation.
See **`.librarian/rules.md`** for the full guidance.

Before exploring with grep/find, try:

- `librarian search "<topic>"` — semantic search over docs + code
- `librarian context "<topic>"` — deep briefing: related docs + code refs
- `librarian neighbors <node>` — what does X connect to?
- `librarian path <from> <to>` — how do these pieces relate?

Read `.librarian/out/GRAPH_REPORT.md` for a topology snapshot (god nodes,
communities, cross-cluster edges).
<!-- librarian:end -->
