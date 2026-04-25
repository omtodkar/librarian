---
title: Configuration
type: reference
description: Complete reference for .librarian/config.yaml, environment variables, and CLI flags.
---

# Configuration

Librarian is configured through three layers, resolved highest-priority first:

1. **CLI flags** (e.g. `--db-path`, `--config`)
2. **Environment variables** (`LIBRARIAN_*` + a few legacy names)
3. **`.librarian/config.yaml`** inside the workspace
4. **Built-in defaults**

## Workspace discovery

Most commands walk up from the current directory looking for a `.librarian/` folder. `.librarian/config.yaml` inside is the canonical config file.

`librarian init` creates the workspace with default templates. Outside a workspace, only `init` and `install` work; other commands error out.

## `.librarian/config.yaml`

`librarian init` writes this template:

```yaml
# librarian workspace config — team-wide, safe to commit.
# API keys belong in environment variables (LIBRARIAN_EMBEDDING_API_KEY), not here.

docs_dir: docs

embedding:
  provider: gemini         # gemini | openai (or any OpenAI-compatible endpoint)
  model: gemini-embedding-2   # 3072-dim multimodal; older text-embedding-004 is deprecated
  # base_url: for OpenAI-compatible endpoints (e.g. http://localhost:1234/v1)
  # batch_size: 100           # chunks per embedding API call (Gemini cap 100, OpenAI cap 2048)

chunking:
  max_tokens: 512
  min_tokens: 50
  overlap_lines: 3

office:
  # Per-sheet cell caps for .xlsx — prevents a spreadsheets-as-database
  # file from ballooning the index. Truncation is noted inline in the
  # generated markdown.
  xlsx_max_rows: 100
  xlsx_max_cols: 50
  # Include PowerPoint speaker notes as "### Notes" sections per slide.
  include_speaker_notes: true

pdf:
  # Cap on pages indexed per PDF. 0 = unlimited.
  # Large books produce proportional chunks, which can dominate
  # the index if left unbounded.
  max_pages: 0

code_file_patterns:
  - "*.go"
  - "*.ts"
  - "*.tsx"
  - "*.py"
  - "*.rs"
  - "*.java"
  - "*.rb"

exclude_patterns:
  - "node_modules/**"
  - ".git/**"
  - "vendor/**"
  - ".librarian/**"
```

### Top-level fields

| Field | Type | Default | Description |
|---|---|---|---|
| `docs_dir` | string | `docs` | Directory with documentation to index (relative to workspace root) |
| `db_path` | string | `.librarian/librarian.db` | SQLite database path |
| `code_file_patterns` | []string | see above | Glob patterns for file extensions recognised as code references in markdown |
| `exclude_patterns` | []string | see above | Glob patterns skipped during the walk; stack on top of the hard-coded exclusions (`.git/`, `node_modules/`, `vendor/`, `.librarian/`) and `.librarian/ignore` |

### `embedding`

| Field | Required | Default | Description |
|---|---|---|---|
| `provider` | yes | `gemini` | `"gemini"` or `"openai"` |
| `model` | conditionally | — | Required for `openai`; optional for `gemini` (has a built-in default) |
| `api_key` | no | — | Falls back to `LIBRARIAN_EMBEDDING_API_KEY` / `GEMINI_API_KEY` env vars for Gemini |
| `base_url` | no | `http://localhost:1234/v1` | Only read for `openai`; set to your provider's endpoint |
| `batch_size` | no | `100` | Max chunks per `EmbedBatch` API call at index time. `0` resolves to `100`. Silently clamped to the provider's documented hard max (Gemini 100, OpenAI 2048). Does not affect the single-query search path. |

See [Embedding](embedding.md) for provider-specific detail.

### `chunking`

| Field | Default | Description |
|---|---|---|
| `max_tokens` | 512 | Sections exceeding this split at paragraph boundaries |
| `min_tokens` | 50 | Chunks below this are dropped (filters heading-only sections) |
| `overlap_lines` | 3 | Lines from the previous chunk prepended to the next for retrieval continuity |

Applies uniformly to every handler — markdown, code, config, Office, PDF all go through the shared `ChunkSections` splitter.

### `office`

| Field | Default | Description |
|---|---|---|
| `xlsx_max_rows` | 100 | Cap per sheet; truncation is noted inline in the generated markdown |
| `xlsx_max_cols` | 50 | Column cap per row |
| `include_speaker_notes` | true | Append PPTX notes as `### Notes` sections per slide |

### `pdf`

| Field | Default | Description |
|---|---|---|
| `max_pages` | 0 | 0 = unlimited. Bounds large books from dominating the index |

### `graph`

Controls the code-graph pass — the second of two passes `librarian index` runs. The docs pass (controlled by `docs_dir` and `exclude_patterns`) produces chunks + vectors for the knowledge base; the graph pass walks the **workspace root** and projects code symbols into `graph_nodes`. See [Indexing Pipeline](indexing.md#graph-pass--walking-the-project-root).

| Field | Default | Description |
|---|---|---|
| `honor_gitignore` | `true` | Skip files matched by any `.gitignore` in the project (root + nested, git's layered semantics). Each sub-project's own `.gitignore` covers its build outputs for free — turn off only if you want to index files git would ignore |
| `detect_generated` | `true` | Skip files whose first ~1 KiB contains a canonical generated-file banner (see below). Content-based detection, so hand-written files are never flagged by extension alone |
| `exclude_patterns` | `[]` | Filepath-match globs stacked on top of the built-in defaults (see below). Patterns matching a directory prune the whole subtree |
| `roots` | `[]` | Restrict the graph walk to these subdirectories (relative to the workspace root). Empty = walk everything. Useful for monorepos when only a slice of the repo is relevant to the current task |
| `max_workers` | `0` | Goroutines the graph pass uses. `0` (auto) samples the first ~10 files serially to measure per-file wall time, then scales workers to the cost profile (fast files → 2 workers, medium → 4, slow → full pool of `min(GOMAXPROCS, 8)`). `1` forces serial (useful for determinism/debugging); `N>1` uses a fixed pool. Also overridable per-run via `--workers N` on `librarian index` |
| `progress_mode` | `""` (auto) | Force a progress display mode. `""` auto-selects (verbose if ≤500 files, bar on TTY above, quiet on non-TTY above). Valid values: `verbose`, `bar`, `quiet`, `silent` (no output — used automatically by `--json`). Also overridable per-run via `--verbose` / `--quiet` on `librarian index` |

**Built-in default excludes** (always applied on top of `exclude_patterns`):

- Heavyweight / plumbing: `.git/`, `.librarian/` (hard — cannot be overridden), `node_modules/`, `vendor/`, `target/`, `build/`, `dist/`, `out/`, `coverage/`
- Python: `__pycache__/`, `.venv/`, `venv/`, `*.egg-info/`
- JS/TS frameworks: `.next/`, `.nuxt/`, `.svelte-kit/`
- Monorepo build-tool caches: `.turbo/`, `.nx/`, `.yarn/`, `.cache/`, `.parcel-cache/`
- Bazel workspace symlinks: `bazel-*` (`bazel-bin`, `bazel-out`, `bazel-testlogs`, …)
- Dart/Flutter: `.dart_tool/`
- IDE metadata: `.idea/`, `.vscode/`

Directory-name matching fires at any depth — a nested `apps/web/node_modules` is pruned the same way as a root-level `node_modules`.

**Generated-file banners detected** (first ~1 KiB, all regex-anchored):

- `// Code generated ... DO NOT EDIT.` — Go toolchain (protoc-gen-go, stringer, mockgen, gqlgen, …)
- `// @generated` / `/* @generated` — Meta / Facebook (Buck, signedsource, graphql-codegen, Relay)
- `# Generated by ... DO NOT EDIT.` — Python/Ruby codegen (grpc-tools, sqlc)
- `<!-- Generated by ... -->` — HTML/XML/SVG codegen

A detected file is skipped entirely — no chunks, no symbols, no `code_file` row. If a previously-indexed file acquires a banner, its stale symbols and `code_file` node are cleaned up automatically on the next run.

```yaml
graph:
  honor_gitignore: true
  detect_generated: true
  exclude_patterns:
    - "generated/**"
  roots:
    - services/auth
    - libs/core
  max_workers: 0  # auto
```

### Ignore file

`.librarian/ignore` (gitignore-style, one pattern per line) stacks on top of `exclude_patterns`. `librarian init` seeds it with common patterns:

```
node_modules/
vendor/
.git/
dist/
build/
```

## Environment variables

All config fields bind to environment variables with the `LIBRARIAN_` prefix. Nested fields use underscores.

| Variable | Config field |
|---|---|
| `LIBRARIAN_DOCS_DIR` | `docs_dir` |
| `LIBRARIAN_DB_PATH` | `db_path` |
| `LIBRARIAN_EMBEDDING_PROVIDER` | `embedding.provider` |
| `LIBRARIAN_EMBEDDING_MODEL` | `embedding.model` |
| `LIBRARIAN_EMBEDDING_API_KEY` | `embedding.api_key` |
| `LIBRARIAN_EMBEDDING_BASE_URL` | `embedding.base_url` |
| `LIBRARIAN_CHUNKING_MAX_TOKENS` | `chunking.max_tokens` |
| `LIBRARIAN_CHUNKING_MIN_TOKENS` | `chunking.min_tokens` |
| `LIBRARIAN_CHUNKING_OVERLAP_LINES` | `chunking.overlap_lines` |
| `LIBRARIAN_OFFICE_XLSX_MAX_ROWS` | `office.xlsx_max_rows` |
| `LIBRARIAN_OFFICE_XLSX_MAX_COLS` | `office.xlsx_max_cols` |
| `LIBRARIAN_OFFICE_INCLUDE_SPEAKER_NOTES` | `office.include_speaker_notes` |
| `LIBRARIAN_PDF_MAX_PAGES` | `pdf.max_pages` |
| `GEMINI_API_KEY` | `embedding.api_key` (legacy fallback) |

Binding is done through Viper's `AutomaticEnv()` in `cmd/root.go`:

```go
viper.SetEnvPrefix("LIBRARIAN")
viper.AutomaticEnv()
```

## Global CLI flags

Available on every subcommand:

| Flag | Description |
|---|---|
| `--config <path>` | Explicit config file path (overrides workspace discovery) |
| `--db-path <path>` | Override the SQLite database path |

Per-subcommand flags are documented in [CLI Reference](cli.md).

## Example configs

### Minimal

No config file needed beyond what `librarian init` writes. Point `LIBRARIAN_EMBEDDING_API_KEY` at a Gemini key and `librarian index` works.

### Larger chunks for long-form docs

```yaml
chunking:
  max_tokens: 1024
  min_tokens: 100
  overlap_lines: 5
```

### LM Studio (local embeddings, no API key)

```yaml
embedding:
  provider: openai
  base_url: http://localhost:1234/v1
  model: text-embedding-nomic-embed-text-v1.5
```

### Ollama

```yaml
embedding:
  provider: openai
  base_url: http://localhost:11434/v1
  model: nomic-embed-text
```

### Bounded XLSX + PDF caps for a monorepo

```yaml
office:
  xlsx_max_rows: 50         # smaller per-sheet samples
  xlsx_max_cols: 20
pdf:
  max_pages: 200            # skip past page 200 of long reports
```

### Extra code extensions

```yaml
code_file_patterns:
  - "*.go"
  - "*.py"
  - "*.ts"
  - "*.tsx"
  - "*.rs"
  - "*.swift"
  - "*.kt"
  - "*.scala"
```

### Archived / drafts excluded

```yaml
exclude_patterns:
  - "node_modules/**"
  - ".git/**"
  - "vendor/**"
  - ".librarian/**"
  - "archived/**"
  - "drafts/**"
```
