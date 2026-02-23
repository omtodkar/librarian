---
title: Configuration
type: reference
description: Complete configuration reference for .librarian.yaml, environment variables, and CLI flags.
---

# Configuration

Librarian is configured through a `.librarian.yaml` file, environment variables, and CLI flags. Settings are resolved in this order (highest priority first):

1. CLI flags
2. Environment variables
3. `.librarian.yaml`
4. Built-in defaults

## `.librarian.yaml`

Place this file in your project root. All fields are optional; defaults are shown below.

```yaml
# Directory containing documentation to index
docs_dir: docs

# HelixDB connection
helix_host: http://localhost:6969

# Embedding configuration
embedding:
  provider: gemini       # Embedding provider (gemini uses text-embedding-004)
  model: ""              # Model name (provider-specific)
  api_key: ""            # API key (or set GEMINI_API_KEY env var)

# Chunking strategy
chunking:
  max_tokens: 512        # Maximum tokens per chunk before splitting
  min_tokens: 50         # Minimum tokens; smaller chunks are discarded
  overlap_lines: 3       # Lines from the previous chunk prepended to the next

# File extensions recognized as code references
code_file_patterns:
  - "*.go"
  - "*.ts"
  - "*.py"
  - "*.rs"
  - "*.java"
  - "*.rb"

# Glob patterns for files/directories to skip during indexing
exclude_patterns:
  - "node_modules/**"
  - ".git/**"
  - "vendor/**"
```

### Field Reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `docs_dir` | `string` | `"docs"` | Path to the documentation directory (relative to project root) |
| `helix_host` | `string` | `"http://localhost:6969"` | URL of the HelixDB instance |
| `embedding.provider` | `string` | `"gemini"` | Embedding provider. `"gemini"` uses Google's text-embedding-004 API |
| `embedding.model` | `string` | `""` | Model identifier (depends on provider) |
| `embedding.api_key` | `string` | `""` | API key for the embedding provider. Falls back to `GEMINI_API_KEY` env var |
| `chunking.max_tokens` | `int` | `512` | Maximum token count per chunk. Sections exceeding this are split at paragraph boundaries |
| `chunking.min_tokens` | `int` | `50` | Minimum token count. Chunks below this threshold are discarded |
| `chunking.overlap_lines` | `int` | `3` | Number of lines from the end of the previous chunk prepended to the next chunk for context continuity |
| `code_file_patterns` | `[]string` | `["*.go", "*.ts", "*.py", "*.rs", "*.java", "*.rb"]` | Glob patterns for file extensions recognized as code references |
| `exclude_patterns` | `[]string` | `["node_modules/**", ".git/**", "vendor/**"]` | Glob patterns for paths to exclude from indexing |

## Environment Variables

All configuration fields can be set via environment variables with the `LIBRARIAN_` prefix. Nested fields use underscores as separators.

| Variable | Config Field | Example |
|----------|-------------|---------|
| `LIBRARIAN_DOCS_DIR` | `docs_dir` | `LIBRARIAN_DOCS_DIR=documentation` |
| `LIBRARIAN_HELIX_HOST` | `helix_host` | `LIBRARIAN_HELIX_HOST=http://localhost:8080` |
| `LIBRARIAN_EMBEDDING_PROVIDER` | `embedding.provider` | `LIBRARIAN_EMBEDDING_PROVIDER=gemini` |
| `LIBRARIAN_EMBEDDING_MODEL` | `embedding.model` | `LIBRARIAN_EMBEDDING_MODEL=text-embedding-3-small` |
| `LIBRARIAN_EMBEDDING_API_KEY` | `embedding.api_key` | `LIBRARIAN_EMBEDDING_API_KEY=sk-...` |
| `GEMINI_API_KEY` | `embedding.api_key` (fallback) | `GEMINI_API_KEY=AIza...` |
| `LIBRARIAN_CHUNKING_MAX_TOKENS` | `chunking.max_tokens` | `LIBRARIAN_CHUNKING_MAX_TOKENS=1024` |
| `LIBRARIAN_CHUNKING_MIN_TOKENS` | `chunking.min_tokens` | `LIBRARIAN_CHUNKING_MIN_TOKENS=100` |
| `LIBRARIAN_CHUNKING_OVERLAP_LINES` | `chunking.overlap_lines` | `LIBRARIAN_CHUNKING_OVERLAP_LINES=5` |

Environment variables are bound through Viper's `AutomaticEnv()` with the prefix set in `cmd/root.go`:

```go
viper.SetEnvPrefix("LIBRARIAN")
viper.AutomaticEnv()
```

## CLI Global Flags

These flags are available on all commands:

| Flag | Description |
|------|-------------|
| `--config <path>` | Path to config file (default: `.librarian.yaml` in current directory) |
| `--helix-host <url>` | HelixDB host URL (overrides config file and env var) |

## Example Configurations

### Minimal (all defaults)

No config file needed. Librarian will look for markdown files in `./docs` and connect to HelixDB at `http://localhost:6969`.

### Custom docs directory

```yaml
docs_dir: documentation/wiki
```

### Larger chunks for long-form docs

```yaml
chunking:
  max_tokens: 1024
  min_tokens: 100
  overlap_lines: 5
```

### Additional code file patterns

```yaml
code_file_patterns:
  - "*.go"
  - "*.ts"
  - "*.py"
  - "*.rs"
  - "*.java"
  - "*.rb"
  - "*.swift"
  - "*.kt"
  - "*.scala"
```

### Monorepo with exclusions

```yaml
docs_dir: docs
exclude_patterns:
  - "node_modules/**"
  - ".git/**"
  - "vendor/**"
  - "archived/**"
  - "drafts/**"
```
