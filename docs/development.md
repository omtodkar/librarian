---
title: Development Guide
type: guide
description: How to build, test, and develop Librarian from source.
---

# Development Guide

## Prerequisites

- **Go 1.25+**
- **Gemini API key** for embedding generation (set `GEMINI_API_KEY` env var)
- **CGO enabled** — required for SQLite and sqlite-vec (CGO_ENABLED=1, the Go default)

## Building

### From source

```sh
git clone <repo-url> && cd librarian
make build
```

This produces a `librarian` binary in the project root.

### Install to GOPATH

```sh
make install
```

This runs `go install .`, placing the binary in `$GOPATH/bin`.

### Clean

```sh
make clean
```

Removes the built binary.

## Makefile Targets

| Target | Command | Description |
|--------|---------|-------------|
| `build` | `go build -o librarian .` | Build binary in project root |
| `install` | `go install .` | Install to `$GOPATH/bin` |
| `clean` | `rm -f librarian` | Remove built binary |
| `test` | `go test ./...` | Run all tests |

## Running Tests

```sh
make test
```

Or directly:

```sh
go test ./...
```

### Test Coverage

Tests currently cover the indexer package:

| File | What it tests |
|------|--------------|
| `internal/indexer/diagrams_test.go` | Diagram detection, label extraction for Mermaid/PlantUML/ASCII |
| `internal/indexer/emphasis_test.go` | Bold text signal extraction and classification |
| `internal/indexer/tables_test.go` | Markdown and HTML table processing and linearization |

### Test Fixtures

Test fixtures live in `testdata/docs/`:
- `testdata/docs/auth.md` — Sample authentication guide
- `testdata/docs/architecture.md` — Sample architecture document

## Project Layout

```
cmd/                  CLI commands (Cobra)
internal/
  config/             Configuration struct and Viper binding
  embedding/          Embedding generation (Gemini API)
  indexer/            4-stage indexing pipeline
  store/              SQLite + sqlite-vec storage layer
  mcpserver/          MCP tool implementations (mcp-go SDK)
db/
  migrations.sql      SQLite schema (embedded at build time)
  embed.go            go:embed directive for migrations.sql
docs/                 Project documentation (indexed by Librarian itself)
testdata/             Test fixtures
```

## Key Dependencies

| Dependency | Purpose |
|-----------|---------|
| `github.com/spf13/cobra` | CLI framework |
| `github.com/spf13/viper` | Configuration management |
| `github.com/mattn/go-sqlite3` | SQLite driver (CGO) |
| `github.com/asg017/sqlite-vec-go-bindings` | sqlite-vec extension for vector search |
| `github.com/yuin/goldmark` | Markdown parser |
| `github.com/mark3labs/mcp-go` | MCP server SDK |
| `github.com/google/uuid` | UUID generation for document/code file IDs |

## Local Development Workflow

1. Make changes to source files
2. Run tests: `make test`
3. Build: `make build`
4. Test locally:
   ```sh
   ./librarian init
   ./librarian index
   ./librarian search "your query"
   ./librarian status
   ```
5. Test MCP server: configure `.mcp.json` to point to your local binary and restart your AI coding tool
