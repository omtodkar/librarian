---
title: Indexing Pipeline
type: reference
description: How the indexing pipeline walks the filesystem, dispatches files to format-specific handlers, and stores parsed units, chunks, signals, and graph edges in SQLite.
---

# Indexing Pipeline

`librarian index` turns a project's documentation + source into a searchable SQLite index. The pipeline is format-agnostic: the walker doesn't know what a `.pdf` or `.py` file is — it dispatches to a handler registered for each extension (see [Handlers](handlers.md)).

## Pipeline overview

```
   filesystem
       │
       ▼
 1. Walk ────── find files, apply exclude patterns + gitignore-style .librarian/ignore
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
       │        + graph_nodes + graph_edges
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

## Incremental indexing

Every file's raw content is hashed with SHA-256 before Parse. The stored hash on the `documents` row is compared:

| State | Action |
|---|---|
| Hash matches | Skip (counted as `Skipped`) |
| Hash differs | Delete existing doc + chunks + edges, re-index |
| No existing doc | Index as new |

`librarian index --force` bypasses the hash check and re-indexes everything.

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
