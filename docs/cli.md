---
title: CLI Reference
type: reference
description: Every librarian subcommand with its flags, behavior, and typical use.
---

# CLI Reference

Librarian's CLI is organised around a project-local **workspace** at `.librarian/` (see [Configuration](configuration.md)). Every command assumes it was invoked inside or below a workspace root.

| Command | What it does |
|---|---|
| `init` | Create a `.librarian/` workspace in the current directory |
| `index` | Walk docs + code, parse, chunk, embed, store |
| `search` | Semantic search across indexed content |
| `context` | Search + related docs + referenced code (one-shot briefing) |
| `doc` | Print a document's full content with index metadata |
| `list` | List indexed documents, optionally filtered by type |
| `status` | Show index statistics |
| `update` | Write or overwrite a doc and re-index it |
| `neighbors` | Show a graph node's immediate connections |
| `path` | Shortest directed path between two graph nodes |
| `explain` | Summarise a graph node and its connections |
| `report` | Write `GRAPH_REPORT.md`, `graph.html`, `graph.json` to `.librarian/out/` |
| `install` | Write assistant-platform integration pointers |
| `mcp serve` | Start the MCP stdio server |

## Workspace discovery

Most commands walk up from the current directory looking for a `.librarian/` folder. Outside a workspace, `index`, `search`, etc. error out; `init` is the only command that bootstraps a new workspace.

## Global flags

Available on every subcommand:

| Flag | Description |
|---|---|
| `-h`, `--help` | Print usage for the command and exit |
| `--config <path>` | Explicit path to the config file (overrides workspace discovery) |
| `--db-path <path>` | Override the SQLite database path |

Run `librarian <command> --help` (or just `librarian --help`) for Cobra-generated usage that lists every flag the command accepts.

## Setup commands

### `librarian init`

Creates `.librarian/` in the current directory with default templates: `config.yaml`, `ignore`, `.gitignore`, and empty `out/` + `hooks/` subdirs. Opens the SQLite database. Safe to run over an existing workspace — it only writes files that are missing.

```sh
librarian init
```

After init, edit `.librarian/config.yaml`, set `LIBRARIAN_EMBEDDING_API_KEY` in your environment, then run `librarian index`.

### `librarian install [--all|--platforms=...]`

Writes thin pointer files that make assistant platforms (Aider, Claude Code, Codex, Cursor, Gemini CLI, GitHub Copilot, OpenCode) discover Librarian automatically. Pointer files live at the project root (`CLAUDE.md`, `AGENTS.md`, `CONVENTIONS.md`, etc.) wrapped in `<!-- librarian:start/end -->` markers so reinstalls are idempotent and user content is preserved.

```sh
librarian install                        # interactive
librarian install --all --no-git-hook    # every platform, no prompt, no post-commit hook
librarian install --platforms=claude,cursor
librarian install --dry-run
```

| Flag | Default | Description |
|---|---|---|
| `--all` | `false` | Install for every supported platform without prompting |
| `--platforms <keys>` | — | Comma-separated subset: `aider`, `claude`, `codex`, `copilot`, `cursor`, `gemini`, `opencode` |
| `--no-git-hook` | `false` | Skip writing `.git/hooks/post-commit` |
| `--dry-run` | `false` | Print planned writes without touching disk |

JSON hook entries in `.claude/settings.json` / `.codex/hooks.json` are merged by command string so other hooks aren't clobbered.

## Indexing commands

### `librarian index [docs-dir]`

Runs the pipeline over the configured docs directory (or the given argument). Produces documents + chunks + graph nodes + graph edges in SQLite.

```sh
librarian index                   # default: cfg.docs_dir
librarian index docs/             # explicit directory
librarian index --force           # ignore content hashes, re-index everything
librarian index --dry-run         # show files that would be indexed
librarian index --json            # machine-readable summary
```

| Flag | Default | Description |
|---|---|---|
| `--force` | `false` | Ignore content hashes; re-index every file |
| `--dry-run` | `false` | List files that would be indexed; no writes |
| `--json` | `false` | Emit the run summary as JSON |

Incremental: each document's SHA-256 content hash is compared against the stored hash; unchanged files are skipped.

### `librarian update <path> --content='...' [--reindex=file|full]`

Writes content to a file (under `docs_dir`) and re-indexes it. Convenient for programmatic doc edits from an assistant.

| Flag | Default | Description |
|---|---|---|
| `--content <text>` | read from stdin | Content to write. When omitted, stdin is read |
| `--reindex <scope>` | `file` | `file` to re-index only this file; `full` to re-index the whole `docs_dir` |
| `--json` | `false` | Emit the write + re-index summary as JSON |

## Retrieval commands

### `librarian search <query> [--limit=N]`

Vector similarity search against chunk embeddings. Over-fetches `limit * 3` candidates from sqlite-vec, re-ranks with metadata signals (warnings, decisions, risk markers boost ordering), returns top `limit`.

```sh
librarian search "authentication flow"
librarian search "rate limiting" --limit=10
librarian search "TODO" --json
librarian search "AuthService" --include-refs   # each result lists the code files it references
```

| Flag | Default | Description |
|---|---|---|
| `--limit <n>` | `5` | Maximum results to return |
| `--json` | `false` | Emit results as JSON |
| `--include-refs` | `false` | Append referenced code files to each result (joins through `refs`) |

Pretty markdown output on TTY, plain markdown when piped.

### `librarian context <query> [--limit=N]`

The heavy retrieval tool. Does everything `search` does, then joins outward through the graph: primary chunk matches → their source documents → referenced code files → related documents (docs sharing code refs). Equivalent to the `get_context` MCP tool.

```sh
librarian context "authentication flow"
```

Use this for "how does X work" or architecture questions — it returns a curated briefing instead of a raw list.

| Flag | Default | Description |
|---|---|---|
| `--limit <n>` | `5` | Initial search results (1–10); graph joins fan out from these |
| `--json` | `false` | Emit the briefing as JSON |

### `librarian doc <path>`

Print a document's full content with metadata header (title, type, chunk count). Path is relative to the workspace root or an indexed file path.

| Flag | Default | Description |
|---|---|---|
| `--json` | `false` | Emit content + metadata as JSON |

### `librarian list [--doc-type=<type>]`

List indexed documents as a table (path, type, chunks, title). Filter by `doc_type` with `--doc-type=guide`, `--doc-type=reference`, etc.

| Flag | Default | Description |
|---|---|---|
| `--doc-type <type>` | — | Filter to a single `doc_type` value (e.g. `guide`, `reference`, `architecture`, `go`, `pdf`) |
| `--json` | `false` | Emit the listing as JSON |

### `librarian status`

Print index statistics: document count, chunk count, code-file count, graph node/edge counts, database size.

| Flag | Default | Description |
|---|---|---|
| `--json` | `false` | Emit stats as JSON |

## Graph commands

The graph layer connects documents, code files, and code symbols into one typed graph (see [Storage Layer](storage.md#graph-layer)). These commands walk it.

### `librarian neighbors <node> [--direction=in|out|both]`

Show all edges incident to `<node>`. `<node>` is a namespaced id (`doc:<uuid>`, `file:<path>`, `sym:<fqn>`) or a fuzzy name — Librarian resolves friendly inputs to concrete node ids.

| Flag | Default | Description |
|---|---|---|
| `--direction <dir>` | `both` | Restrict to `in` (incoming), `out` (outgoing), or `both` |
| `--json` | `false` | Emit edges as JSON |

### `librarian path <from> <to> [--max-depth=N]`

Shortest directed path via BFS. Shows the edges hopped through.

| Flag | Default | Description |
|---|---|---|
| `--max-depth <n>` | `6` | Abandon the search past this many hops |
| `--json` | `false` | Emit the path as JSON |

### `librarian explain <node>`

Summarise a node: its labels, kind, surrounding edges, and a short natural-language description pulled from metadata.

| Flag | Default | Description |
|---|---|---|
| `--json` | `false` | Emit the summary as JSON |

### `librarian report`

Compute community structure + centrality over the entire graph and write three files under `.librarian/out/`:

| File | Purpose |
|---|---|
| `GRAPH_REPORT.md` | God nodes (highest-connected), communities (clusters), cross-cluster edges — the topology snapshot |
| `graph.html` | Force-directed visualization in a single HTML file |
| `graph.json` | Raw nodes + edges for external tooling |

| Flag | Default | Description |
|---|---|---|
| `--dry-run` | `false` | Analyse + render in memory; don't write the files to disk |
| `--json` | `false` | Print a JSON summary instead of the default text summary |

## MCP server

### `librarian mcp serve`

Start the MCP stdio server. Registers 5 tools (`search_docs`, `get_document`, `get_context`, `list_documents`, `update_docs`) — see [MCP Tools](mcp-tools.md).

```sh
librarian mcp serve
```

Intended for platforms that consume MCP (Claude Code, Cursor, Codex, etc.). The subcommand layout (not a bare `librarian serve`) leaves room for future MCP-related tooling under the `mcp` group.

## Output formats

CLI commands emit pretty-printed markdown on a TTY, plain markdown when piped to another process, and JSON when `--json` is passed. This lets the same command drive human use, shell pipelines, and programmatic callers.
