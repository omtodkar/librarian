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

## Non-Interactive Shell Commands

**ALWAYS use non-interactive flags** with file operations to avoid hanging on confirmation prompts. Shell commands like `cp`, `mv`, and `rm` may be aliased to include `-i` (interactive) mode on some systems, causing the agent to hang indefinitely waiting for y/n input.

Use these forms:

```bash
cp -f source dest           # NOT: cp source dest
mv -f source dest           # NOT: mv source dest
rm -f file                  # NOT: rm file
rm -rf directory            # NOT: rm -r directory
cp -rf source dest          # NOT: cp -r source dest
```

Other commands that may prompt:

- `scp` — use `-o BatchMode=yes`
- `ssh` — use `-o BatchMode=yes` to fail instead of prompting
- `apt-get` — use `-y`
- `brew` — use `HOMEBREW_NO_AUTO_UPDATE=1`

## Librarian MCP

This project has a librarian MCP server configured in `.mcp.json`. Use it to understand the codebase — it is the default codebase-exploration surface in this repo.

- `search_docs` — semantic search across indexed docs (start here for most questions)
- `get_context` — deep briefing with related code files and documents (use for architecture/design questions)
- `get_document` — read a full doc by file path
- `list_documents` — browse all indexed docs
- `update_docs` — write/overwrite a doc and re-index it

**Prefer these tools over Grep and over reading full files.** Order to try:

1. `get_context` — for "how does X work" / architecture / design-intent questions. Pulls related code files and docs in one call.
2. `search_docs` — for "where is X mentioned" / narrow lookups.
3. `get_document` — once you know the specific doc you want.
4. Only after the above: `Grep` for a specific literal symbol, or `Read` for a specific known path.

This applies to both direct work and review work. Workers doing code review should start with `get_context` on the changed subsystem before inspecting diffs; workers doing implementation should start with `get_context` on the area they're modifying. Falling straight through to `Grep`/`Read` without consulting librarian first is a workflow regression — the whole point of this repo is that the librarian index is richer than keyword search.

For the `Skill(librarian)` slash-skill and CLI-based exploration (`librarian search`, `librarian context`, `librarian neighbors`, `librarian path`), see the auto-installed `<!-- librarian:start -->` block at the bottom of this file or `.librarian/rules.md` for full guidance.

## Workspace & CLI

Every command runs against a project-local `.librarian/` workspace (config, ignore file, SQLite DB, generated reports). `cmd/root.go` walks up from the CWD to find it. `librarian init` bootstraps one; every other command requires one.

Primary CLI surface (`cmd/`):

- `init` / `index` / `update` — bootstrap, index, write-and-reindex
- `search` / `context` / `doc` / `list` / `status` — retrieval
- `neighbors` / `path` / `explain` / `report` — graph queries
- `install` / `uninstall` — write / reverse platform-integration pointers (CLAUDE.md, AGENTS.md, …)
- `mcp serve` — optional stdio MCP server (opt-in; top-level `mcp` is a subcommand group)

Every command supports `--json` for machine-readable output. See `docs/cli.md` for full flag reference.

## Architecture

Canonical reference is `docs/architecture.md` (and the focused docs alongside it: `indexing.md`, `handlers.md`, `storage.md`, `embedding.md`, `configuration.md`). Use the MCP `get_context` tool for architecture questions — it pulls from those docs plus the code.

Short version:

- **Dependency wiring**: `config.Load() → embedding.NewEmbedder() → store.Open() → indexer.New()`. Cobra + Viper wire a shared `*config.Config` in `cmd/root.go`.
- **Handler-based indexing** (`internal/indexer/`): one `FileHandler` interface (`handler.go`) covers every format. Per-format packages under `internal/indexer/handlers/<format>/` (markdown, code, config, office, pdf) register themselves at import time into a `Registry` (`registry.go`), keyed by extension. `internal/indexer/handlers/defaults/` blank-imports them all; `cmd/` and `mcpserver/` blank-import `defaults` to wire the full set.
- **Two-pass indexing**: `librarian index` runs both in one invocation (use `--skip-docs` / `--skip-graph` to iterate on one).
  - *Docs pass* (`IndexDirectory`, over `cfg.DocsDir`): walker → `registry.HandlerFor(ext)` → `Parse` → `Chunk` → embed → store (documents + chunks + vectors + code refs + graph nodes). A second pass (`buildGraphEdges`) adds `mentions` and `shared_code_ref` edges. This drives `search_docs` / `get_context`.
  - *Graph pass* (`IndexProjectGraph`, over `cfg.ProjectRoot`): walker (`WalkGraph` with `.gitignore`, monorepo-default, and generated-file banner filters) → `Parse` → projects each code-symbol Unit into `graph_nodes{kind=symbol}` with `contains` edges from the file node, and `import` / `call` / `inherits` / `requires` / `part` edges. `inherits` is the canonical kind for class-family parents across every grammar (Java `extends`/`implements`, Python bases, JS/TS class and interface heritage, Go interface embedding, Kotlin delegation heuristic, Swift per-flavor heuristic, Dart class heritage); flavor lives in `Edge.Metadata.relation` ∈ {`extends`, `implements`, `mixes`, `conforms`, `embeds`}. `requires` (Dart `mixin M on Base`) and `part` (Dart `part 'foo.dart'`) are distinct edge kinds — kept out of `inherits` / `import` so those common queries stay clean. `extends` / `implements` are retained as legacy `Kind` aliases in the graph-pass switches but aren't emitted by new code. A post-graph resolver (`buildImplementsRPCEdges` in `internal/indexer/implements_rpc.go`) then adds `implements_rpc` edges (symbol → symbol) from each language's generated-code method to its proto rpc declaration via naming conventions (protoc-gen-go/grpc-go, protoc-gen-dart, @bufbuild/protoc-gen-es); kept distinct from `inherits` because the relationship is codegen derivation, not subtype parenthood. When a `buf.gen.yaml` / `buf.gen.yml` is present, a preceding step (`buildBufManifest` in `internal/indexer/buf_manifest.go`, lib-4kb) harvests plugin out-dirs + per-proto `option *_package` values into a per-proto-file `buf_manifest` graph_node (`bufgen:<proto-path>`); the resolver then requires each candidate's `source_path` to live under the manifest's language-specific prefix, dropping lib-6wz's known false positives. Missing `buf.gen.yaml` or no prefix for a candidate's language → graceful fallback to name-only matching. No chunks or vectors — structural only. Optional per-file parallelism with adaptive worker count (`graph.max_workers`).
  - Both passes gate on SHA-256 content hash (`documents.content_hash` / `code_files.content_hash`) to skip unchanged files.
- **Shared chunking**: most handlers (including office/pdf after internal conversion to markdown) delegate chunking to `internal/indexer/chunker.go`'s section-aware splitter with paragraph fallback.
- **Store layer** (`internal/store/`): schema in `db/migrations.sql` embedded via `//go:embed`. `vec0` virtual table is created lazily on first chunk insert (dimensions come from the live embedding model). Search = vector KNN over-fetch (3× limit) + signal-weighted re-rank. Float64 embeddings → little-endian float32 bytes for sqlite-vec.
- **Embedding providers** (`internal/embedding/`): `Embedder` interface; Gemini + OpenAI-compatible implementations. Factory in `provider.go`.
- **MCP server** (`internal/mcpserver/`): stdio JSON-RPC via mcp-go; one file per tool, registered in `Serve()`. `get_context` is the most complex — it joins chunks, documents, code refs, and related docs.

## Adding new components

- **New file handler**: create `internal/indexer/handlers/<format>/`, implement the `FileHandler` interface (`handler.go`), call `indexer.RegisterDefault(...)` from a package `init()`, and add one blank-import line to `internal/indexer/handlers/defaults/defaults.go`. No other changes needed — walker, store, signals, and MCP are handler-agnostic. See `docs/handlers.md`.
- **New code grammar** (subset of the above for languages parsed via tree-sitter): the runtime is `github.com/tree-sitter/go-tree-sitter` (ABI 13–15). For the grammar's parser, prefer `go get github.com/tree-sitter/tree-sitter-<lang>/bindings/go` when an official binding exists (ABI 15 typically). Otherwise vendor under `internal/indexer/handlers/code/tree_sitter_<lang>/` following the existing Dart/Swift/TypeScript pattern — every vendor binding.go documents its source repo, commit SHA, and ABI version.
- **New embedding provider**: implement `Embedder` in `internal/embedding/`, add a case to `NewEmbedder()` in `provider.go`.
- **New MCP tool**: create a file in `internal/mcpserver/`, register in `Serve()` in `server.go`.

## Multi-worker review default

If this Claude Code session was spawned by the perles orchestrator and assigned a reviewer role (signaled by a task-thread message from `coordinator` of the form `Review of task <id>`, an `assign_task_review` assignment, or any review-phase instruction), the first action of the review MUST be to invoke `Skill(feature-dev)` and drive the review through its code-reviewer agent. This applies to every review dimension — correctness, tests, architecture, dead code, gap analysis, acceptance. Only call `report_review_verdict` after synthesizing the skill's output into the verdict. If `feature-dev` is unavailable on the worker, report that fact in the verdict rather than silently falling back to an ad-hoc review. This default applies whether or not the assignment message explicitly mentions the skill.

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

When ending a work session:

1. **File issues for remaining work** — create beads issues for anything that needs follow-up.
2. **Run quality gates** (if code changed) — tests, linters, build.
3. **Update issue status** — close finished work, update in-progress items.
4. **Commit** — stage and commit related changes with a descriptive message.
5. **Push if a remote is configured** — run `git remote -v`. If there's a remote, `git pull --rebase && git push` (and `bd dolt push` if beads has a remote too). This repo is currently local-only; skip the push steps when no remote is set.
6. **Hand off** — leave a short note on what's next.

Don't leave committed work stranded when a remote exists — push it. Don't invent a remote when none exists.
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
