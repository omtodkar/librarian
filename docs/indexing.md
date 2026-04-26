---
title: Indexing Pipeline
type: reference
description: How the indexing pipeline walks the filesystem, dispatches files to format-specific handlers, and stores parsed units, chunks, signals, and graph edges in SQLite.
---

# Indexing Pipeline

`librarian index` turns a project's documentation + source into a searchable SQLite index. It runs **two passes** inside one invocation, each scoped to a different root:

- **Docs pass** over `docs_dir` — produces chunks + vectors for the knowledge base (`search_docs`, `get_context`). Unchanged from earlier versions.
- **Graph pass** over the project root — parses every source file the walker hasn't excluded, projects code symbols into `graph_nodes`, and emits `contains` / `import` / `call` / `inherits` / `requires` / `part` edges. The `inherits` edge covers every class-family parent relationship — Java `extends`/`implements`, Python class bases, JS/TS class and interface heritage, Go interface embedding, Kotlin delegation_specifier heuristic, Swift per-flavor heuristic (class: first=extends + rest=conforms; struct/enum/extension: all=conforms; protocol: all=extends), Dart class heritage (extends + implements + with, all three relations in a single class declaration) — with the flavor carried in `Edge.Metadata.relation` (`extends`, `implements`, `mixes`, `conforms`, `embeds`). `requires` is Dart-specific for `mixin M on Base` constraints, kept distinct from `inherits` because the `on` clause is a use-site type bound, not a parent. `part` is Dart-specific for `part 'foo.dart'` / `part of 'bar.dart'` file-join directives (code_file → code_file). No chunks or vectors — structural only.

The pipeline is format-agnostic: the walker doesn't know what a `.pdf` or `.py` file is — it dispatches to a handler registered for each extension (see [Handlers](handlers.md)).

## Docs pass — pipeline overview

```
   filesystem
       │
       ▼
 1. Walk ────── find files under docs_dir, apply exclude_patterns
       │
       ▼
 2. Dispatch ── registry.HandlerFor(ext)  → FileHandler implementation
       │
       ▼
 3. Parse ──── handler.Parse(path, bytes) → ParsedDoc (units + signals + refs)
       │
       ▼
 4. Chunk ──── handler.Chunk(doc, opts)   → []Chunk (embedding text + signal meta)
       │
       ▼
 5. Embed ──── embedder.Embed(chunk.EmbeddingText) per chunk  →  []float64
       │
       ▼
 6. Store ──── documents + doc_chunks + doc_chunk_vectors + code_files + refs
       │        + graph_nodes + graph_edges (mentions)
       ▼
 7. Link ───── buildRelatedDocEdges — second pass, docs sharing code refs get edges
```

Key files:

- `internal/indexer/walker.go` — stage 1
- `internal/indexer/registry.go` — stage 2 dispatch
- `internal/indexer/handlers/*/*.go` — stages 3–4, per format
- `internal/indexer/chunker.go` — shared `ChunkSections`, used by every handler
- `internal/indexer/indexer.go` — stages 5–7, orchestration
- `internal/indexer/signals.go`, `emphasis.go` — signal extraction (shared)

## Stage 1: Walk

`WalkDocs(dir, excludePatterns, registry)` recursively traverses the workspace using `filepath.Walk`. A file is indexed if:

1. Its extension is registered with a `FileHandler` (i.e. the registry knows what to do with it)
2. Its path doesn't match any `exclude_patterns` glob (config) or `.librarian/ignore` entry
3. It doesn't fall under the hard-coded ignore list: `.git/`, `node_modules/`, `vendor/`, `.librarian/`

Each matched file yields a `WalkResult` with the relative path (stored) + absolute path (for reading).

## Stage 2: Dispatch

The registry (`internal/indexer/registry.go`) is an extension → handler map. `DefaultRegistry().HandlerFor(path)` returns the handler whose `Extensions()` claims this file's extension. Unknown extensions are skipped silently.

Handlers register themselves at package init time; blank-importing `internal/indexer/handlers/defaults` wires every built-in handler. `cmd/root.go` re-registers format-specific handlers after `config.Load()` so user-configured options (XLSX row caps, PDF page caps, etc.) flow through.

## Stage 3: Parse

Each handler turns raw bytes into a `ParsedDoc`:

```go
type ParsedDoc struct {
    Path       string
    Format     string          // "markdown", "go", "docx", "pdf", …
    Title      string
    DocType    string
    Summary    string
    Headings   []string
    Frontmatter map[string]any
    Units      []Unit          // tree of sections/symbols/keys
    Signals    []Signal        // doc-level signals
    Refs       []Reference     // outbound refs: imports, code files, doc links
    Metadata   map[string]any  // format-specific extras
    RawContent string
}
```

See [Handlers](handlers.md) for the per-format shape. Key behaviours:

- **Markdown** uses Goldmark + goldmark-meta. Extracts frontmatter, builds heading hierarchy, processes diagrams and tables (see below), extracts emphasis signals and code references.
- **Code grammars** use tree-sitter. Each grammar defines symbol kinds, container kinds, comment node types, docstring extraction. The shared walker handles preceding-comment buffering, container descent (class bodies), and rationale signal extraction.
- **Config handlers** flatten trees into dotted key paths; leading comments become the key's docstring.
- **Office + PDF handlers** convert to markdown first, then delegate `Parse` to the markdown handler, then stamp `Format` / `DocType` on the result.

### Markdown-specific processing

These three transformations run inside the markdown handler (and therefore also run on Office + PDF content, since those convert to markdown first):

**Diagrams** (`diagrams.go`) — Mermaid, PlantUML, ASCII diagrams get their raw syntax replaced with extracted labels. Raw arrow/pipe syntax embeds poorly; labels capture the semantic content. Example:

```
[Diagram: mermaid flowchart — Auth Service, User Database, validate credentials]
```

Labels are capped at 10 per diagram. ASCII diagrams are detected heuristically (>30% box-drawing characters).

**Tables** (`tables.go`) — Markdown GFM tables and HTML tables are linearised into key-value sentences. Raw pipe syntax embeds poorly; linearised text lets a chunk stay findable by a query that mentions a column value:

```
[Table: 3 columns, 5 rows — Name, Type, Description]
Name: docs_dir, Type: string, Description: Path to documentation directory
```

Both tables are capped at 20 rows × 20 columns.

**Emphasis signals** (`emphasis.go`) — Bold text is classified into three categories: **inline labels** (`**Warning:**`, `**Decision:**`), **risk markers** (standalone `**deprecated**`, `**breaking**`), and **emphasis terms** (all bold). Inline labels and risk markers are appended to the chunk's embedding text as a signal line and stored on `signal_meta` JSON for the re-ranker. Emphasis terms are recorded but not embedded, to keep noise out.

### Rationale signals (every format)

`ExtractRationaleSignals` scans comments for TODO / FIXME / HACK / XXX / NOTE patterns. Every comment-bearing handler (code grammars + markdown) runs this. Signals land on `Unit.Signals` and the chunk's `signal_meta`.

### Code annotations (Java + TS)

Grammars that opt in via the `annotationExtractor` interface emit `Signal{Kind: "annotation", Value: "Deprecated"}` for each `@Annotation` on a symbol. Search boosts these alongside rationale signals.

## Stage 4: Chunk

Every handler's `Chunk` method ultimately calls `indexer.ChunkSections`:

```go
func ChunkSections(docTitle, rawContent string, inputs []SectionInput, opts ChunkOpts) []Chunk
```

One `SectionInput` per H2 section / code symbol / config key block. Each is a candidate for one chunk. The splitter:

1. **Token count** via words / 0.75 ≈ tokens.
2. **Section fits** (`≤ max_tokens`) → one chunk.
3. **Section too big** → split at paragraph boundaries (`\n\n`), accumulate paragraphs until the next one would overflow, then start a new chunk. Never splits mid-paragraph.
4. **Too small** (`< min_tokens`) → drop; filters single-heading-only sections.

Each chunk's `EmbeddingText` is prefixed with a **context header**:

```
Document: Authentication Guide | Section: Authentication > Login Flow
Signals: warning, decision

The login flow uses OAuth 2.0 with PKCE…
```

The context header gives the embedding model structural clues the raw text lacks; the signal line captures metadata that should influence vector distance. **Overlap** lines from the previous chunk prepend to the next for retrieval continuity.

## Stage 5: Embed

Each chunk's `EmbeddingText` is sent to the configured provider (`embedder.Embed`). Returned `[]float64` is converted to little-endian `[]byte` of float32 at the store boundary — sqlite-vec's expected format. See [Embedding](embedding.md).

## Stage 6: Store

Per document:

1. `AddDocument` → `documents` row (or replaces existing when content hash differs)
2. `AddChunk` per chunk → `doc_chunks` + `doc_chunk_vectors`
3. `AddCodeFile` + `AddReference` for each extracted code-file mention
4. `UpsertNode` for every unit that projects into a `graph_node` (symbol, section, key-path)
5. `UpsertEdge` for every inbound `mentions` / `imports` / `calls` edge

See [Storage Layer](storage.md) for CRUD detail.

## Stage 7: Link related docs

After every file in the run has been indexed, `buildRelatedDocEdges` computes doc-to-doc edges:

1. Build reverse map `code_file_path → [doc_paths]` from `refs`.
2. For each code file referenced by 2+ documents, add a `graph_edge{kind: "shared_code_ref"}` between every pair of those documents.

Duplicates are avoided by tracking pairs in a set keyed by the two doc paths.

The `get_context` MCP tool and the `librarian context` CLI command walk this layer to surface "here's what else in the docs references the same code".

## Graph pass — walking the project root

The graph pass is the second half of `librarian index`. It walks the **workspace root** (not `docs_dir`) and parses every source file it finds, projecting code-symbol `Unit`s into `graph_nodes{kind=symbol}`. Unlike the docs pass it produces no chunks and no vectors — the graph stays structural so `search_docs` stays scoped to curated prose.

### What the graph pass emits

| Node kind | ID convention | Source |
|---|---|---|
| `code_file` | `file:<relative-path>` | Every code file visited |
| `symbol` | `sym:<fqn>` | Each parsed `Unit` whose Kind is a symbol (function, method, class, interface, enum, record, type, field, constructor) |

| Edge kind | From | To | Meaning |
|---|---|---|---|
| `contains` | `file:<path>` | `sym:<fqn>` | Symbol lives in this file |
| `import` | `file:<path>` | `sym:<target>` / `file:<target>` / `ext:<pkg>` | File imports the target (resolver sets `Metadata["node_kind"]` to pick the namespace) |
| `inherits` | `sym:<child>` | `sym:<parent>` | Class/interface/protocol parent relationship. Source is the child **symbol** (not the file) — `Reference.Source` is populated by the grammar's `inheritanceExtractor`. `Edge.Metadata.relation` ∈ {`extends`, `implements`, `mixes`, `conforms`, `embeds`}. `extends` / `implements` Kind values remain backward-compat aliases in the graph-pass switches but are not emitted by new code. |
| `requires` | `sym:<mixin>` | `sym:<constraint>` | Dart `mixin M on Base` use-site constraint — a type bound, not an inheritance parent. Kept distinct from `inherits` so "all parents of X" queries stay clean. |
| `part` | `file:<library>` | `file:<part-file>` | Dart `part 'foo.dart'` / `part of 'bar.dart'` file-join directive. A single Dart library lives across multiple files. |
| `call` | `file:<path>` | `sym:<target>` | Reserved; no grammar emits `call` Refs today |

### Walker filtering

`WalkGraph` composes these filters in order:

1. **Hard excludes** — `.git/`, `.librarian/`. Never overridable.
2. **Built-in default directory excludes** — `node_modules/`, `vendor/`, `target/`, `build/`, `dist/`, `out/`, `__pycache__/`, `.venv/`, `venv/`, `.next/`, `.nuxt/`, `.svelte-kit/`, `.dart_tool/`, `.idea/`, `.vscode/`, `coverage/`, `.turbo/`, `.nx/`, `.yarn/`, `.cache/`, `.parcel-cache/`, `*.egg-info/`, `bazel-*/`. Directory-name matching fires at any depth, so nested `apps/web/node_modules` is pruned the same way as root-level `node_modules`.
3. **User `graph.exclude_patterns`** — filepath.Match globs stacked on top of defaults.
4. **`.gitignore`** (root + nested, layered git semantics) when `graph.honor_gitignore: true` (default).
5. **`SkipFormats`** — handlers named `markdown`, `docx`, `xlsx`, `pptx`, `pdf` are skipped so files already covered by the docs pass aren't double-indexed.

`graph.roots` restricts the walk to declared subdirectories of the project root. Empty (default) walks the whole workspace.

After the walker passes a file to `indexGraphFile`, one more filter runs:

6. **Generated-file banner detection** (when `graph.detect_generated: true`, default) — the first ~1 KiB of each file is scanned against a conservative allowlist of canonical banners (`// Code generated ... DO NOT EDIT.`, `// @generated`, `# Generated by ... DO NOT EDIT.`, `<!-- Generated by ... -->`). A match skips the file and cleans up any symbols/code_file row from a previous run when the file wasn't yet marked generated. Matching is content-based, not extension-based — a hand-written `foo_pb.go` without the banner is never flagged.

### Progress feedback

The graph pass auto-selects one of three progress modes based on file count and whether stdout is a terminal:

- **Verbose** (≤500 files): per-file line `  [N/M] path OK/ERROR` — today's behaviour, preserved for small projects.
- **Bar** (>500 files, TTY): single-line progress bar rewritten in place via `\r` — `  [N/M] pct% — path`. One line of output regardless of project size.
- **Quiet** (>500 files, non-TTY): heartbeat line every 100 files — keeps CI logs readable without looking stuck.

Overrides: `--quiet` and `--verbose` on `librarian index` force the corresponding mode regardless of file count or TTY. `graph.progress_mode` in `.librarian/config.yaml` provides the same override persistently (useful for CI pipelines that always want quiet output).

### Parallelism

The graph pass can run across multiple goroutines since tree-sitter parsing is CPU-bound and per-file safe. `graph.max_workers` controls the pool size:

- `0` (default): **adaptive**. Below 20 files, stay serial. Otherwise, the first ~10 files are processed serially to measure per-file wall time; the remaining files run through a worker pool sized to the measurement:
  - avg < 2 ms → 2 workers (overhead-bound; extra goroutines don't pay back)
  - 2–10 ms → 4 workers
  - ≥ 10 ms → full pool of `min(GOMAXPROCS, 8)`
- `1`: force serial. Useful for deterministic output or debugging a parse error.
- `N>1`: fixed pool of size N.

The per-run CLI override `--workers N` on `librarian index` takes the same values (`-1` means "respect config"). The sample files are real indexing work counted toward results, so adaptive mode wastes no computation.

Writes to SQLite serialise via the connection mutex, so the upper-cap of 8 is where extra workers start queuing on the DB lock without gaining throughput. Symbol and edge counts produced by parallel or adaptive runs are identical to serial runs (idempotent `UpsertNode` / `UpsertEdge` + per-worker-local counters merged at the end).

### Cross-file edge reconstitution

`graph_edges` has `ON DELETE CASCADE` on `graph_nodes.id`. When a file is reindexed, `DeleteSymbolsForFile` removes its stale symbol nodes and the cascade drops all incident edges — including edges from *other unchanged files* that pointed at the reindexed file's symbols. Because unchanged files are hash-skipped and never reindexed, those cross-file edges would be permanently lost.

The graph pass handles this via a **serial reconstitution post-pass** that runs after all parallel workers complete:

1. **Collection phase** (parallel): each worker calls `AffectedSourcePathsForFile` before the symbol deletion and records the `source_path` values of nodes with edges into the file's symbols into a shared `sync.Map`.
2. **Reconstitution phase** (serial, after `wg.Wait()`): each collected path is force-reindexed via the core per-file logic (`indexGraphFileDirect`). This replays the file's edges against the freshly projected symbols of the upstream file.

The reconstitution phase is serial to avoid racing on the same downstream file from multiple reconstitution goroutines. It uses `indexGraphFileDirect` (not the collection-aware `indexGraphFile`) so the post-pass does not itself trigger another reconstitution round.

See [Configuration](configuration.md#graph) for the full config schema.

### Incremental graph indexing

Each code file's content is SHA-256 hashed and compared to `code_files.content_hash`:

| State | Action |
|---|---|
| Hash matches | Skip (counted as `FilesSkipped`) |
| Hash differs | `DeleteSymbolsForFile` wipes stale symbol nodes, re-parse and re-project |
| No existing row | Index as new |

Stale symbol wipe is scoped to `kind='symbol'` nodes whose `source_path` matches — the file's `code_file` node and its incoming `mentions` / `shared_code_ref` edges from docs survive. So renaming a function in a code file is safe: the old symbol node + `contains` edge go, the new one takes its place, and doc-to-file edges stay intact.

## Incremental indexing

Every file's raw content is hashed with SHA-256 before Parse. The stored hash on the `documents` row (docs pass) or `code_files` row (graph pass) is compared:

| State | Action |
|---|---|
| Hash matches | Skip (counted as `Skipped` / `FilesSkipped`) |
| Hash differs | Delete existing row and dependents, re-index |
| No existing row | Index as new |

`librarian index --force` bypasses the hash check in both passes. `--skip-docs` or `--skip-graph` run only one pass — useful while iterating on a specific area.

## Frontmatter conventions (markdown)

Markdown files get the most out of librarian when they include YAML frontmatter:

```yaml
---
title: Authentication Guide
type: guide
description: How authentication works in the application.
---
```

| Field | Effect |
|---|---|
| `title` | Used as document title + context header. Falls back to the first H1 |
| `type` | Stored as `doc_type` — filterable via `list_documents` / `librarian list --doc-type=guide`. Defaults to `"guide"` |
| `description` | Stored as `summary`. Falls back to the first paragraph |

Other frontmatter fields are preserved in the `frontmatter` JSON column but aren't otherwise interpreted.
