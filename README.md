# Librarian 📚

**Your project's knowledge base and code graph, in one SQLite file — for you, your CLI, and every AI assistant you use.**

[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Status: Alpha](https://img.shields.io/badge/status-alpha-orange)]()
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

<!-- Add a demo GIF here once available — show `librarian index` + `librarian search` + Claude Code picking up the /librarian skill. -->

---

## The Problem

Every AI coding assistant starts a session blind. It has never read your architecture doc, doesn't know which services share utilities, and hasn't seen the ADR that explains why `authenticateUser` does its weird token-rotation dance. So you paste. And paste again. And the assistant still misses the docs/code relationship a teammate would explain in 30 seconds.

## What Librarian Does

Librarian indexes your whole project — **markdown docs, source code, configs, Office documents, PDFs** — into a single SQLite database with two layers:

1. **Semantic knowledge base.** Vector + signal-aware search over your documentation. Queries like `"authentication flow"` or `"deprecated API usage"` return the right paragraphs, not just keyword matches.
2. **Typed code graph.** A navigable graph connecting documents → code files → symbols → imports. Ask "who references this module?" or "what sits between `AuthService` and `TokenStore`?" — get an answer in one command.

Every MCP-capable AI assistant (Claude Code, Cursor, Codex, Gemini CLI, OpenCode, GitHub Copilot, Aider) plugs in via one `librarian install` command.

## Why Librarian

| | |
|---|---|
| 🗂️ **One SQLite file** | No servers, no Docker, no external vector DB. `.librarian/librarian.db` ships with your repo. |
| 🧠 **Two-layer retrieval** | Vector search for prose, a typed graph for structure. Most tools only do one. |
| 🔌 **Seven assistants, one command** | `librarian install --all` wires CLAUDE.md, AGENTS.md, `.cursor/rules/`, `.github/copilot-instructions.md`, and friends in one shot. |
| 📄 **Nine file formats** | Markdown, Go/Python/Java/JS/TS/TSX via tree-sitter, YAML/JSON/TOML/XML/properties/env, DOCX/XLSX/PPTX, PDF. |
| ⚡ **Fast re-indexes** | SHA-256 hash per file → unchanged files skip `Parse` entirely. |
| 🔑 **Bring your own embedder** | Gemini by default; point at LM Studio / Ollama / any OpenAI-compatible endpoint to run fully local. |

---

## Quick Start

```sh
# 1. Install
go install librarian@latest                    # or: git clone … && go build -o librarian .

# 2. Set up in your project
cd your-project
export LIBRARIAN_EMBEDDING_API_KEY=<gemini-key>  # or point at a local embedder
librarian init                                   # creates .librarian/
librarian index                                  # walks docs/, builds the index
librarian install --all                          # wire every supported AI assistant
# or pick specific ones:
librarian install --platforms=claude,cursor,gemini

# To reverse everything:
# librarian uninstall --all              # unwire pointers, keep .librarian/
# librarian uninstall --full --yes       # also remove .librarian/

# 3. Use it
librarian search "authentication flow"
librarian context "how does indexing work"
librarian neighbors internal/auth/oauth.go
librarian report && open .librarian/out/graph.html
```

That's it. Your AI assistant now has `/librarian` available as a slash-skill and the MCP server is wired via the pointer files.

---

## Key Features

### Semantic search with metadata-aware re-ranking
Queries get a final score of `0.90 × vector_similarity + 0.10 × metadata_boost`, where the boost promotes chunks tagged with `**Warning:**`, `**Decision:**`, `@Deprecated`, and other actionable signals extracted at index time. A query for "auth" surfaces the ADR with the decision, not the neutral description.

### Typed code graph
Every indexed entity — document, code file, code symbol, config key — projects into a `graph_node` with a namespaced id (`doc:…`, `file:…`, `sym:…`, `key:…`). Typed edges (`mentions`, `shared_code_ref`, `imports`) connect them. CLI commands `neighbors`, `path`, `explain` walk the graph; `report` writes a topology snapshot (god nodes, communities, surprising connections) as `GRAPH_REPORT.md` + an interactive `graph.html`.

### Multi-format ingestion through one abstraction
A single `FileHandler` interface powers every format. Each handler converts source to markdown and delegates chunking — same signal extraction, same chunk shape, regardless of whether the input was a DOCX, a Python file, or a YAML config. Adding a new format is a 100-line subpackage.

### Incremental re-indexing
SHA-256 content hash per file; unchanged files skip parsing entirely. Editing one markdown file and re-running `librarian index` finishes in milliseconds.

### One-command assistant integration
`librarian install` writes idempotent pointer blocks into each assistant's convention file (`CLAUDE.md`, `AGENTS.md`, `CONVENTIONS.md`, `.github/copilot-instructions.md`, `.cursor/rules/librarian.mdc`, …). Pointer blocks use `<!-- librarian:start/end -->` markers so user content around them is preserved across reinstalls.

### Pure-Go PDF + Office parsers
DOCX/PPTX via `encoding/xml` over `archive/zip` (no AGPL dependencies); PDF via `go-pdfium` running in its WebAssembly mode (wazero — no CGo). XLSX uses the BSD-3 `excelize` library. The binary is ~35 MB and needs no system libraries.

---

## Supported File Formats

| Category | Formats |
|---|---|
| Documentation | `.md`, `.markdown` |
| Code (tree-sitter) | Go, Python, Java, JavaScript, TypeScript, TSX |
| Config | YAML, JSON, TOML, XML, `.properties`, `.env` |
| Office | DOCX, XLSX, PPTX |
| PDF | `.pdf` (with cascade: tagged-structure → bookmarks → font-size heuristic → per-page fallback) |

Full detail in [docs/handlers.md](docs/handlers.md).

## Supported AI Assistants

Wired by `librarian install`:

- **Claude Code** — CLAUDE.md pointer + SessionStart hook + `/librarian` skill
- **Codex** — AGENTS.md pointer + `.codex/hooks.json`
- **Cursor** — `.cursor/rules/librarian.mdc`
- **Gemini CLI** — GEMINI.md pointer
- **OpenCode** — AGENTS.md pointer (shared with Codex)
- **GitHub Copilot** — `.github/copilot-instructions.md`
- **Aider** — `CONVENTIONS.md` + post-install reminder to add to `.aider.conf.yml`

Any MCP-capable tool also works via `librarian mcp serve`.

---

## Documentation

Full docs live under [`docs/`](docs/):

- [Architecture](docs/architecture.md) — data model, pipeline, design decisions
- [CLI Reference](docs/cli.md) — every subcommand with flags + examples
- [Handlers](docs/handlers.md) — `FileHandler` abstraction, per-format behaviour
- [Indexing Pipeline](docs/indexing.md) — walk → parse → chunk → store
- [Storage Layer](docs/storage.md) — SQLite schema + graph spine + re-ranking
- [Configuration](docs/configuration.md) — `.librarian/config.yaml`, env vars
- [Embedding](docs/embedding.md) — providers + vector handling
- [MCP Tools](docs/mcp-tools.md) — the 5 tools exposed over stdio
- [Development Guide](docs/development.md) — build, test, extend

Or let Librarian index itself and ask it: `librarian context "how does X work"`.

## Contributing

Contributions welcome — bug reports, platform integrations, new file handlers, grammar improvements.

1. Pick an open issue from the GitHub issue tracker, or open one to propose your change.
2. Read [docs/development.md](docs/development.md) — project layout, test setup, handler extension points.
3. `make test` must pass; a new feature should come with regression tests.
4. Open a PR against `main`.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full flow.

## License

MIT. See [LICENSE](LICENSE).
