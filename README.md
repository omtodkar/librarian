# Librarian

Semantic documentation search for your project, powered by SQLite + [sqlite-vec](https://github.com/asg017/sqlite-vec) and exposed to AI coding tools via [MCP](https://modelcontextprotocol.io).

Librarian indexes markdown files into a searchable vector database. AI tools like Claude Code and Cursor can then search, retrieve, and update your documentation through MCP tools.

## Prerequisites

- **Go 1.25+**
- **Gemini API key** for embedding generation

## Quick Start

### 1. Install Librarian

```sh
go install librarian@latest
```

Or build from source:

```sh
git clone <repo-url> && cd librarian
go build -o librarian .
```

### 2. Set your Gemini API key

```sh
export GEMINI_API_KEY="your-key-here"
```

Or add it to `.librarian.yaml`:

```yaml
embedding:
  api_key: "your-key-here"
```

### 3. Initialize in your project

```sh
cd your-project
librarian init
```

This creates a `.librarian/` directory containing the SQLite database with the schema applied.

### 4. Index your documentation

```sh
librarian index
```

This walks `docs/` (configurable), parses markdown files, generates Gemini embeddings, and stores everything in SQLite.

### 5. Search

```sh
librarian search "authentication flow"
```

### 6. Connect to your AI coding tool

Start the MCP server and configure your tool to use it.

**Claude Code** -- add to `.mcp.json` in your project root:

```json
{
  "mcpServers": {
    "librarian": {
      "command": "librarian",
      "args": ["serve"]
    }
  }
}
```

**Cursor** -- add to `.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "librarian": {
      "command": "librarian",
      "args": ["serve"]
    }
  }
}
```

## CLI Commands

| Command | Description |
|---------|-------------|
| `librarian init` | Initialize the SQLite database |
| `librarian index [docs-dir]` | Parse and index documentation |
| `librarian search <query>` | Search indexed documentation from the CLI |
| `librarian status` | Show index statistics (document count, chunk count) |
| `librarian serve` | Start the MCP stdio server |

### `librarian init`

Creates the `.librarian/` directory and initializes the SQLite database with the schema.

```sh
librarian init
librarian init --db-path path/to/librarian.db  # override the default database path
```

### `librarian index`

Indexes markdown files into SQLite. Unchanged files are skipped automatically via content hashing.

```sh
librarian index                  # index from configured docs_dir (default: docs/)
librarian index path/to/docs     # index from a specific directory
librarian index --force           # re-index all files, ignoring content hashes
librarian index --dry-run         # show what would be indexed without making changes
librarian index --json            # output results as JSON
```

### `librarian search`

Vector similarity search from the command line.

```sh
librarian search "how does auth work"
librarian search --limit 10 "API endpoints"
librarian search --json "configuration"
```

### `librarian status`

Shows how many documents and chunks are in the index.

```sh
librarian status
librarian status --json
```

### `librarian serve`

Starts an MCP server over stdio. This is what AI coding tools connect to.

```sh
librarian serve
```

## MCP Tools

When connected via MCP, AI tools have access to 5 tools:

| Tool | Description |
|------|-------------|
| `search_docs` | Semantic search across indexed documentation |
| `get_document` | Read the full content of a specific document |
| `get_context` | Deep briefing: search + related docs and code references |
| `list_documents` | List all indexed documents with metadata |
| `update_docs` | Write/update a markdown file and re-index it |

`get_context` is the most powerful tool -- it combines vector search with relational joins to find relevant chunks, their source documents, referenced code files, and related documentation.

## Configuration

Librarian is configured through `.librarian.yaml`, environment variables, and CLI flags. Priority order (highest first):

1. CLI flags
2. Environment variables
3. `.librarian.yaml`
4. Built-in defaults

### `.librarian.yaml`

Place this in your project root. All fields are optional.

```yaml
# Directory containing documentation to index
docs_dir: docs

# Path to the SQLite database file
db_path: .librarian/librarian.db

# Embedding configuration
embedding:
  provider: gemini
  api_key: ""            # or set GEMINI_API_KEY env var

# Chunking strategy
chunking:
  max_tokens: 512        # max tokens per chunk before splitting
  min_tokens: 50         # chunks smaller than this are discarded
  overlap_lines: 3       # lines from previous chunk prepended to next

# File extensions recognized as code references
code_file_patterns:
  - "*.go"
  - "*.ts"
  - "*.py"
  - "*.rs"
  - "*.java"
  - "*.rb"

# Glob patterns for files/directories to skip
exclude_patterns:
  - "node_modules/**"
  - ".git/**"
  - "vendor/**"
```

### Configuration Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `docs_dir` | `string` | `"docs"` | Path to documentation directory |
| `db_path` | `string` | `".librarian/librarian.db"` | Path to the SQLite database file |
| `embedding.provider` | `string` | `"gemini"` | Embedding provider |
| `embedding.api_key` | `string` | `""` | API key (falls back to `GEMINI_API_KEY` env var) |
| `chunking.max_tokens` | `int` | `512` | Max tokens per chunk |
| `chunking.min_tokens` | `int` | `50` | Min tokens per chunk |
| `chunking.overlap_lines` | `int` | `3` | Overlap lines between chunks |
| `code_file_patterns` | `[]string` | `["*.go", "*.ts", ...]` | Recognized code file extensions |
| `exclude_patterns` | `[]string` | `["node_modules/**", ...]` | Paths to exclude from indexing |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `GEMINI_API_KEY` | Gemini API key for embeddings |
| `LIBRARIAN_DOCS_DIR` | Documentation directory |
| `LIBRARIAN_DB_PATH` | Path to SQLite database file |
| `LIBRARIAN_EMBEDDING_API_KEY` | Embedding API key (alternative to `GEMINI_API_KEY`) |
| `LIBRARIAN_CHUNKING_MAX_TOKENS` | Max tokens per chunk |
| `LIBRARIAN_CHUNKING_MIN_TOKENS` | Min tokens per chunk |
| `LIBRARIAN_CHUNKING_OVERLAP_LINES` | Overlap lines between chunks |

### CLI Global Flags

| Flag | Description |
|------|-------------|
| `--config <path>` | Path to config file (default: `.librarian.yaml`) |
| `--db-path <path>` | Path to SQLite database file |

## How It Works

Librarian uses a 4-stage indexing pipeline:

1. **Walk** -- Find all `.md`/`.markdown` files, apply exclude patterns
2. **Parse** -- Goldmark AST walk: extract frontmatter, build section hierarchy
3. **Chunk** -- Section-aware splitting at H2 boundaries with paragraph fallback
4. **Store** -- Generate Gemini embeddings, store documents + vector chunks + relationships in SQLite

Data is stored across several tables:

- **documents** stores metadata (title, type, content hash)
- **doc_chunks** stores chunk content linked to documents via foreign key
- **doc_chunk_vectors** (vec0 virtual table) stores embeddings for similarity search
- **code_files** represents source files referenced in documentation
- **refs** connects documents to code files they mention
- **related_docs** connects documents that reference the same code files

The `get_context` MCP tool joins across these tables to provide comprehensive briefings that include relevant chunks, source documents, referenced code, and related documentation.

### Incremental Indexing

Each document is hashed with SHA-256. On subsequent runs, unchanged documents are skipped. Use `--force` to re-index everything.

### Frontmatter

For best results, add frontmatter to your markdown files:

```yaml
---
title: Authentication Guide
type: guide
description: How authentication works in the application.
---
```

| Field | Effect |
|-------|--------|
| `title` | Document title in search results. Falls back to first H1 heading |
| `type` | Stored as `doc_type`. Used for filtering in `list_documents`. Defaults to `"guide"` |
| `description` | Stored as `summary`. Falls back to the first paragraph |

## Project Structure

```
cmd/
  root.go          CLI entrypoint, global flags, Viper config
  init.go          librarian init
  index.go         librarian index
  search.go        librarian search
  status.go        librarian status
  serve.go         librarian serve

internal/
  config/          Configuration struct and defaults
  embedding/       Gemini embedding client (Embedder interface)
  indexer/         Walk, parse, chunk, store pipeline
  store/           SQLite + sqlite-vec storage layer
  mcpserver/       MCP tool implementations

db/
  migrations.sql   SQLite schema (embedded at build time)
```
