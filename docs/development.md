---
title: Development Guide
type: guide
description: How to build, test, and develop Librarian from source.
---

# Development Guide

## Prerequisites

- **Go 1.25+** — see `go.mod`
- **CGo enabled** — required for `mattn/go-sqlite3` and `sqlite-vec` (`CGO_ENABLED=1`, the Go default)
- **An embedding provider** for end-to-end tests:
  - A Gemini API key in `LIBRARIAN_EMBEDDING_API_KEY` / `GEMINI_API_KEY`, or
  - A local OpenAI-compatible server (LM Studio, Ollama, vLLM)

## Building

```sh
git clone <repo-url> && cd librarian
make build           # produces ./librarian
```

| Target | Command | Description |
|---|---|---|
| `build` | `go build -o librarian .` | Build binary in the project root |
| `install` | `go install .` | Install to `$GOPATH/bin` |
| `clean` | `rm -f librarian` | Remove the binary |
| `test` | `go test ./...` | Run all tests |

## Running tests

```sh
make test
# or
go test ./...
```

Most packages are unit-testable without network or filesystem setup. A few integration tests spin up a temp workspace; none need an embedding provider to pass.

### Where tests live

| Area | Files |
|---|---|
| Indexer orchestration | `internal/indexer/integration_test.go`, `walker_test.go` |
| Markdown processing | `internal/indexer/diagrams_test.go`, `emphasis_test.go`, `tables_test.go` |
| Code grammars | `internal/indexer/handlers/code/{golang,python,java,javascript,kotlin,swift}_test.go` + `testhelpers_test.go`; vendor sanity: `tree_sitter_dart/binding_test.go` |
| Config handlers | `internal/indexer/handlers/config/config_test.go` |
| Office handlers | `internal/indexer/handlers/office/{docx,xlsx,pptx}_test.go` + `zipfixture_test.go` |
| PDF handler | `internal/indexer/handlers/pdf/{convert,handler}_test.go` (fixtures under `testdata/`) |
| Install | `internal/install/{install,markers,jsonhook,githook,gitignore,skill}_test.go` |
| Store | `internal/store/graph_test.go` and friends |

### Fixtures

- Markdown / config tests build inputs inline.
- Office tests build ZIP fixtures in memory via `buildZip` (`zipfixture_test.go`) — no binary fixtures committed.
- PDF tests use small committed fixtures under `internal/indexer/handlers/pdf/testdata/` (plain, multipage, bookmarks, tagged). `testdata/generate.py` is the source of truth; re-run with reportlab.

## Project layout

```
cmd/                              # Cobra CLI — 16 subcommands
  root.go                         # flags, Viper init, workspace discovery,
                                  # re-registers config-scoped handlers
  init.go index.go search.go context.go doc.go list.go status.go update.go
  reindex.go                      # drop vector state and re-embed (after model change)
  neighbors.go path.go explain.go report.go   # graph surface
  install.go uninstall.go         # platform pointer installer / removal
  mcp.go                          # `librarian mcp serve`

internal/
  config/                         # Config struct, defaults, Viper binding
  workspace/                      # .librarian/ discovery, path helpers
  embedding/                      # Embedder interface + gemini + openai providers
  indexer/
    handler.go                    # FileHandler interface, ParsedDoc shapes
    registry.go                   # Extension → handler dispatch
    walker.go                     # Filesystem walk
    chunker.go                    # Shared token-aware section chunker
    indexer.go                    # Orchestrator: hash → parse → chunk → embed → store
    parser.go                     # Shared Goldmark AST walk (markdown handler internals)
    diagrams.go tables.go         # Markdown-specific embedding transforms
    emphasis.go signals.go        # Signal extraction
    references.go                 # Code-path regex extraction
    handlers/
      markdown/                   # .md, .markdown
      code/                       # Tree-sitter grammars: go, python, java, js, ts, tsx, kotlin, swift (+ dart vendor pending lib-wji.3)
      config/                     # yaml, json, toml, xml, properties, env
      office/                     # docx, xlsx, pptx
      pdf/                        # pdf (go-pdfium WebAssembly)
      defaults/                   # Aggregator blank-import
  store/                          # SQLite + sqlite-vec + graph layer
  analytics/                      # Community detection + centrality for `report`
  report/                         # HTML, JSON, markdown report generators
  install/                        # `librarian install` plumbing
  mcpserver/                      # MCP tool implementations

db/
  migrations/                     # Numbered goose migrations, embedded via db/embed.go
    0001_initial_schema.sql

docs/                             # This documentation (librarian indexes itself)
```

## Key dependencies

| Dependency | Purpose |
|---|---|
| `github.com/spf13/cobra` | CLI framework |
| `github.com/spf13/viper` | Configuration + env binding |
| `github.com/mattn/go-sqlite3` | SQLite driver (CGo) |
| `github.com/asg017/sqlite-vec-go-bindings` | sqlite-vec extension |
| `github.com/yuin/goldmark` + `goldmark-meta` | Markdown parsing + frontmatter |
| `github.com/tree-sitter/go-tree-sitter` | Tree-sitter runtime (ABI 13-15); grammar packages imported per-language |
| `github.com/xuri/excelize/v2` | XLSX parser |
| `github.com/klippa-app/go-pdfium` | PDFium binding (WebAssembly via wazero) |
| `github.com/pelletier/go-toml/v2` | TOML parsing |
| `github.com/mark3labs/mcp-go` | MCP server SDK |
| `github.com/pressly/goose/v3` | SQL migration framework |
| `github.com/google/uuid` | UUIDs for documents / code files |
| `gonum.org/v1/gonum` | Graph algorithms for `librarian report` |

## Local development workflow

```sh
# 1. Make changes
# 2. Run tests
make test

# 3. Build
make build

# 4. Exercise locally
mkdir -p /tmp/libr-dev/docs && cd /tmp/libr-dev
./../path/to/librarian init
./../path/to/librarian index --dry-run
export LIBRARIAN_EMBEDDING_API_KEY=... # or point at a local server
./../path/to/librarian index
./../path/to/librarian search "your query"
./../path/to/librarian status
./../path/to/librarian report
open .librarian/out/graph.html

# 5. MCP iteration
./../path/to/librarian install --all
# (Restart the assistant so it picks up the new .mcp.json)
```

## Adding a new handler

See [Handlers → Where to add a new format](handlers.md#where-to-add-a-new-format). The short version:

1. New subpackage under `internal/indexer/handlers/<format>/` implementing `FileHandler`.
2. `init()` calls `indexer.RegisterDefault(NewFoo(DefaultConfig()))`.
3. One blank-import line in `internal/indexer/handlers/defaults/defaults.go`.
4. Optional: add a `FooConfig` in `internal/config/config.go`, wire `cmd/init.go`'s default YAML, have `cmd/root.go` re-register after `config.Load()` with the user-scoped instance.

No changes to walker / registry / store / CLI / MCP needed — those pick the new handler up automatically via the shared `FileHandler` contract.

## Adding a new MCP tool

1. New file in `internal/mcpserver/` named for the tool.
2. Expose a `register<ToolName>(srv, …)` function.
3. Call it from `Serve()` in `internal/mcpserver/server.go`.
4. Update the instruction string on the `server.NewMCPServer` call so assistants know about the new tool.

## Adding a new CLI command

1. New file in `cmd/` with a Cobra `*cobra.Command` and `init()` calling `rootCmd.AddCommand(…)`.
2. Wire dependencies via the shared `config.Load()` + `store.Open()` + `embedding.NewEmbedder` pattern used by every other command.
3. Document in [CLI Reference](cli.md).

## Adding a migration

Schema changes land as numbered [goose](https://github.com/pressly/goose) migration files under `db/migrations/`.

1. Create `db/migrations/000N_short_description.sql` where `N` is the next free integer (zero-padded to four digits to keep ordering visually correct).
2. Populate both `Up` and `Down` sections with goose's annotation syntax (see any existing migration for the shape). Both use `-- +goose StatementBegin` / `-- +goose StatementEnd` fences when the migration contains multiple statements.
3. The file is picked up automatically on next `go build` via `//go:embed migrations/*.sql` in `db/embed.go` — no Go-side wiring needed.
4. Run `go test ./internal/store/...` to exercise `Open` (which runs `goose.Up`) against a fresh DB and confirm the migration applies cleanly.

Keep migrations append-only: never edit a migration that's shipped, even to fix a typo. If the change is wrong, write a new migration that corrects it.

### Schema migration checklist

Whenever you add or modify a migration, also:

- [ ] Write an upgrade note in [docs/upgrading.md](upgrading.md) — describe what changed and whether users need to run `librarian reindex --rebuild-vectors` or delete their database.
- [ ] Update the version history table in `docs/upgrading.md`.
- [ ] If the change affects vector storage or the `embedding_meta` guard, update [docs/embedding.md](embedding.md) and [docs/storage.md](storage.md) accordingly.

## Beads issue tracker

The project uses **bd (beads)** for work tracking. `bd prime` in a fresh session prints the full command reference. Issues live in `.beads/issues.jsonl` (committed). Any new work should have a matching bd issue so the backlog stays navigable.
