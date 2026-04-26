# Librarian 📚

**Your project's knowledge base and code graph, in one SQLite file — for you, your CLI, and every AI assistant you use.**

[![CI](https://github.com/OWNER/REPO/actions/workflows/ci.yml/badge.svg)](https://github.com/OWNER/REPO/actions/workflows/ci.yml)
[![Go Version](https://img.shields.io/badge/go-1.26%2B-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-green.svg)](LICENSE)
[![Status: Alpha](https://img.shields.io/badge/status-alpha-orange)](CHANGELOG.md)
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

## The Core Idea: retrieve, don't read

A typical codebase is 100k–10M+ tokens. A Claude context window is 200k. Dumping everything in is either impossible or drowns the model in irrelevant noise. Librarian turns the codebase into a **searchable, graph-linked index** so the AI pulls only the chunks it needs for each question — not whole files, not whole directories.

Every indexed file is split into section-aware chunks (~512 tokens each), embedded, and stored alongside a typed graph of `imports` / `contains` / `mentions` / `extends` / `implements` edges. The AI queries by intent (`"how does auth token rotation work?"`) and gets the top-ranked chunks that actually answer the question.

## How Librarian Helps AI Assistants

Once `librarian install` wires up your tools, the assistant gains six MCP tools — each tuned for a different phase of the read-think-write loop:

| Tool | Use when | Typical tokens |
|---|---|---|
| `search_docs` | "Where's X?" / "how does Y work?" | ~2.5k |
| `get_context` | "What's related to this file/symbol?" | ~5–8k |
| `get_document` | You actually need the full doc | full file |
| `list_documents` | Enumerate without content | tiny |
| `update_docs` | Distill findings back into the KB | write, not read |
| `trace_rpc` | End-to-end gRPC trace (proto + all impls) | ~3–6k |

**Token efficiency compounds across four mechanisms:**

- **Chunking** — retrieval unit is a 512-token chunk, not a 5000-token file. A `search_docs` top-5 is ~2.5k tokens; the equivalent "dump the docs folder" is often 50k+. **~20× compression** on the common case.
- **Signal-weighted re-ranking** — chunks tagged with `**Decision:**`, `@Deprecated`, `TODO`, or Javadoc signals get boosted. The ADR explaining *why* auth works this way surfaces before the neutral prose describing *what* it does — AI finds intent first and stops reading when the answer appears.
- **Typed graph** — `"who imports `auth.Service.validate`?"` is an incoming-edge lookup on a single node, not a grep across the codebase. `"what sits between `AuthService` and `TokenStore`?"` is a shortest-path query. Answers in hops, not file reads.
- **`update_docs` feedback loop** — the AI can write a distilled summary back to `docs/` where the next session finds it via one `search_docs` call. Tokens spent once, saved forever.

**A concrete before/after.**

Debugging "why does auth fail on rotated tokens?" *without* Librarian: AI grep-reads `internal/auth/*.go`, skims `docs/auth.md`, inspects callers, chases tests — **~30k tokens** of raw source before it can reason.

*With* Librarian:
```
search_docs "token rotation"              → top 5 chunks, ~2.5k tokens
get_context sym:auth.Service.Validate     → related files + callers, ~6k tokens
```
**~8.5k tokens of precise context instead of 30k of raw text** — more room for reasoning, tool use, and multi-turn iteration. Skipping generated files, vendored deps, and unchanged-hash files happens automatically.

**Plays well with Claude's prompt cache.** Smaller, deterministically-ordered contexts hit the 5-minute TTL cache more often — repeated queries in a session reuse the cached prefix instead of paying full prefix-evaluation cost.

## Why Librarian

| | |
|---|---|
| 🗂️ **One SQLite file** | No servers, no Docker, no external vector DB. `.librarian/librarian.db` ships with your repo. |
| 🧠 **Two-layer retrieval** | Vector search for prose, a typed graph for structure. Most tools only do one. |
| 🔌 **Seven assistants, one command** | `librarian install --all` wires CLAUDE.md, AGENTS.md, `.cursor/rules/`, `.github/copilot-instructions.md`, and friends in one shot. |
| 📄 **Nine file formats** | Markdown, Go/Python/Java/JS/TS/TSX via tree-sitter, YAML/JSON/TOML/XML/properties/env, DOCX/XLSX/PPTX, PDF. |
| ⚡ **Fast re-indexes** | SHA-256 hash per file → unchanged files skip `Parse` entirely. |
| 🔑 **Bring your own embedder** | Gemini by default; point at LM Studio, Ollama, or run fully local via the bundled `make infinity-*` tooling (Qwen3-Embedding + gte-reranker on MPS/CUDA). |

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
| Code (tree-sitter) | Go, Python, Java, JavaScript, TypeScript, TSX, Kotlin, Swift, Dart |
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
- [MCP Tools](docs/mcp-tools.md) — the 6 tools exposed over stdio, with API stability classifications
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
