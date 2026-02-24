---
title: Indexing Pipeline
type: reference
description: How the indexing pipeline works, from file walking through parsing, chunking, content processing (diagrams, tables, emphasis), code reference extraction, and incremental indexing.
---

# Indexing Pipeline

The indexing pipeline transforms markdown files into searchable vector chunks stored in SQLite with sqlite-vec. It runs when you execute `librarian index` or when the `update_docs` MCP tool triggers a re-index.

## Pipeline Overview

```
 docs/
 ├── auth.md
 ├── api.md
 └── security.md
      │
      ▼
 ┌─────────┐    ┌─────────┐    ┌─────────┐    ┌─────────────────┐
 │  Walk    │───>│  Parse  │───>│  Chunk  │───>│  Store          │
 │          │    │         │    │         │    │  + Gemini Embed   │
 │ Find .md │    │ AST +   │    │ Section │    │  + Code Refs     │
 │ files    │    │ front-  │    │ aware   │    │  + Related Docs  │
 │          │    │ matter  │    │ split   │    │  + Hash Check    │
 └─────────┘    └─────────┘    └─────────┘    └─────────────────┘
```

### Key files

- `internal/indexer/walker.go` - Stage 1: Walk
- `internal/indexer/parser.go` - Stage 2: Parse
- `internal/indexer/chunker.go` - Stage 3: Chunk
- `internal/indexer/diagrams.go` - Diagram detection and label extraction
- `internal/indexer/tables.go` - Table linearization for embeddings
- `internal/indexer/emphasis.go` - Bold text signal extraction
- `internal/indexer/references.go` - Code reference extraction
- `internal/indexer/indexer.go` - Stage 4: Store + orchestration

## Stage 1: Walk

The walker (`WalkDocs`) recursively traverses the docs directory using `filepath.Walk`, collecting all files with `.md` or `.markdown` extensions.

**Hard-coded directory exclusions:** `.git`, `node_modules`, `vendor`, `.librarian`

**Configurable exclusions:** The `exclude_patterns` config field supports glob patterns. Patterns containing `**` are expanded to match path segments containing the base pattern.

Each found file is returned as a `WalkResult` with both its relative path (for storage) and absolute path (for reading).

## Stage 2: Parse

The parser (`ParseMarkdown`) uses [Goldmark](https://github.com/yuin/goldmark) with the `goldmark-meta` extension to process each markdown file.

### Frontmatter Extraction

YAML frontmatter is extracted and mapped to document properties:

| Frontmatter Field | Document Property | Description |
|-------------------|-------------------|-------------|
| `title` | `Title` | Document title. Falls back to the first H1 heading if not set in frontmatter |
| `type` | `DocType` | Document type (e.g., `guide`, `reference`, `architecture`). Defaults to `"guide"` if not set |
| `description` | `Summary` | Document summary. Falls back to the first paragraph if not set in frontmatter |

All frontmatter fields are also stored as a JSON string in the `frontmatter` column of the `documents` table.

### AST Walk

The parser walks the Goldmark AST to build:

1. **Headings list** - All headings in the document, stored as a JSON array
2. **Section hierarchy** - A tree structure tracking heading nesting. For each heading, the parser maintains a stack: when a heading of level N is encountered, all headings at level >= N are popped, and the new heading is pushed. This produces a hierarchy like `["Architecture", "Data Model", "Node Types"]`
3. **Sections** - Each heading starts a new section. Content between headings (paragraphs, code blocks, lists, blockquotes) is accumulated into the current section

## Stage 3: Chunk

The chunker (`ChunkDocument`) converts parsed sections into chunks suitable for vector embedding.

### Section-Aware Splitting

The primary split boundary is the section (typically H2 headings). Each section becomes one chunk, unless it exceeds `max_tokens`.

### Paragraph Fallback

When a section exceeds `max_tokens`, it is split at paragraph boundaries (`\n\n`). The splitter accumulates paragraphs until adding the next paragraph would exceed the token limit, then starts a new chunk. This ensures chunks never break mid-paragraph.

### Token Estimation

Token count is estimated by counting words and dividing by 0.75:

```go
tokens := int(float64(words) / 0.75)
```

### Minimum Token Threshold

Chunks with fewer than `min_tokens` (default: 50) are discarded. This filters out sections that contain only a heading and a brief sentence.

### Context Header

Each chunk's embedding text is prefixed with a context header containing the document title and section hierarchy:

```
Document: Authentication Guide | Section: Authentication > Login Flow

The login flow uses OAuth 2.0 with PKCE...
```

This gives the embedding model additional context about where the content sits within the document structure.

### Overlap

After all chunks are generated, overlap is applied: the last `overlap_lines` (default: 3) lines of each chunk are prepended to the next chunk. This provides continuity across chunk boundaries during retrieval.

## Diagram Processing

The diagram processor (`ProcessDiagramBlock` in `internal/indexer/diagrams.go`) detects diagram code blocks and extracts human-readable labels for embedding. Raw diagram syntax (Mermaid arrows, PlantUML keywords, box-drawing characters) embeds poorly — the extracted labels capture the semantic content instead.

### Detection

Fenced code blocks are checked by language tag:

| Language Tag | Diagram Type |
|--------------|-------------|
| `mermaid` | Mermaid |
| `plantuml`, `puml` | PlantUML |
| `ascii`, `ascii-art` | ASCII art |
| `""`, `text`, `txt` | ASCII art (if heuristic passes) |

For unlabeled or plain-text code blocks, the `isASCIIDiagram` heuristic checks whether box-drawing characters (`│`, `├`, `┌`, `─`, `-->`, etc.) make up more than 30% of the content. This catches ASCII diagrams that aren't explicitly tagged.

### Label Extraction

Each diagram type has its own label extraction strategy:

**Mermaid** — Extracts from five regex patterns:
- Node labels: text inside `[]`, `()`, `{}` brackets
- Edge labels: text between `|pipes|`
- Participants/actors: `participant "Name"` or `participant Name`
- Titles: `title: ...`
- Subgraph names: `subgraph Name`

**PlantUML** — Extracts from four regex patterns:
- Titles: `title: ...`
- Participants: `participant`, `actor`, `database`, `entity`, `boundary`, `control`, `collections`
- Classes: `class`, `interface`, `enum`, `component`, `package`
- Arrow labels: `--> Target : label`

**ASCII art** — Extracts text from box patterns: `| text |` where text is 3+ alphabetic characters.

### Subtype Detection

Mermaid diagrams are classified by their opening keyword:

| Prefix | Subtype |
|--------|---------|
| `graph`, `flowchart` | flowchart |
| `sequenceDiagram` | sequence diagram |
| `classDiagram` | class diagram |
| `stateDiagram` | state diagram |
| `erDiagram` | ER diagram |
| `gantt` | gantt chart |
| `pie` | pie chart |

### Output

The extracted labels are formatted into a summary string that replaces the raw diagram code in the chunk's embedding text:

```
[Diagram: mermaid flowchart — Auth Service, User Database, validate credentials]
```

Labels are capped at 10 per diagram to avoid noise.

## Table Processing

The table processor (`internal/indexer/tables.go`) converts markdown and HTML tables into linearized natural-language text for better embedding quality. Tabular data in its raw form (pipes, dashes, HTML tags) doesn't embed well — linearization turns each row into a key-value sentence.

### Markdown Tables

`ProcessTableNode` walks a Goldmark `Table` AST node to extract headers from `TableHeader` cells and data from `TableRow` cells. Both are capped at 20 columns and 20 rows.

### HTML Tables

`ProcessHTMLTable` parses raw HTML using Go's `html` package, finding the first `<table>` element. It handles:
- `<thead>` / `<tbody>` structure
- `<th>` vs `<td>` cells
- Tables without explicit headers (uses first row or generates `Column 1`, `Column 2`, etc.)

HTML tables in markdown are detected by the `isHTMLTable` function, which checks if the content starts with `<table`.

### Linearization

Both table types are linearized into the same format:

```
[Table: 3 columns, 5 rows — Name, Type, Description]
Name: docs_dir, Type: string, Description: Path to documentation directory
Name: db_path, Type: string, Description: Path to the SQLite database file
```

The prefix line includes column count, row count, and header names for context. Each subsequent line joins header-value pairs with commas.

## Emphasis Signal Extraction

The emphasis processor (`internal/indexer/emphasis.go`) scans markdown AST nodes for bold text (`**bold**`) and classifies it into structured signals. These signals serve two purposes:

1. **Selective embedding augmentation** — A `SignalLine()` like `Signals: warning, deprecated` is appended to the chunk's embedding text, making the chunk findable by queries like "what's deprecated"
2. **Re-ranking metadata** — The signals are stored as JSON in the `signal_meta` column on `doc_chunks`, used by the search re-ranker to boost chunks containing warnings, decisions, and risk markers (see [Storage Layer](storage.md#search-re-ranking))

### Signal Classification

Bold text is classified into three categories:

**Inline Labels** — Bold text followed by a colon, mapped to canonical names:

| Variations | Canonical Label |
|-----------|----------------|
| `warn`, `warning`, `caution` | `warning` |
| `note`, `info`, `tip` | `note` |
| `decision` | `decision` |
| `important` | `important` |
| `input`, `output` | `input`, `output` |
| `example` | `example` |
| `todo`, `fixme` | `todo`, `fixme` |
| `default`, `prerequisite`, `requirement` | `default`, `prerequisite`, `requirement` |

Example: `**Warning:** This will delete all data` produces label `warning` with value `This will delete all data`.

**Risk Markers** — Standalone bold text (no colon) matching risk patterns:

| Variations | Canonical Marker |
|-----------|-----------------|
| `deprecated` | `deprecated` |
| `breaking`, `breaking change` | `breaking-change` |
| `unsafe` | `unsafe` |
| `experimental`, `unstable` | `experimental` |
| `do not run`, `do-not-run` | `do-not-run` |

**Emphasis Terms** — All bold text is also recorded as-is (normalized to lowercase) in the `emphasis_terms` array. This captures bold text that doesn't match labels or risk markers.

### EmphasisSignals Struct

```go
type EmphasisSignals struct {
    InlineLabels  []string          // canonical label names
    RiskMarkers   []string          // canonical risk marker names
    EmphasisTerms []string          // all bold text (normalized)
    LabelValues   map[string]string // label → value after colon
    HasWarning    bool              // shortcut flag
    HasDecision   bool              // shortcut flag
}
```

### Storage

Signals are serialized to JSON via `ToJSON()` and stored in the `signal_meta` column of `doc_chunks`. Only `InlineLabels` and `RiskMarkers` are included in the `SignalLine()` that augments the embedding text — general emphasis terms are stored but not embedded, to avoid noise.

## Code Reference Extraction

The reference extractor (`ExtractCodeReferences`) scans the raw markdown content for file path patterns using a regex:

```
(?:^|[\s`"'(])([a-zA-Z0-9_/.-]+\.(?:go|ts|tsx|js|jsx|py|rs|java|rb|c|cpp|h|hpp|cs|swift|kt|scala|sh|bash|zsh|yaml|yml|toml|json))(?:$|[\s`"')\]:,])
```

This matches file paths like `internal/auth/oauth.go` or `src/components/Login.tsx` that appear in documentation text, code blocks, or inline code.

### Filtering

Matched paths are filtered against the `code_file_patterns` config. Only files whose extension matches one of the configured patterns (e.g., `*.go`, `*.ts`) are kept.

### Language Detection

The file extension is mapped to a language name (e.g., `.go` -> `"go"`, `.ts`/`.tsx` -> `"typescript"`, `.py` -> `"python"`). This is stored on the `code_files` row.

### Storage

For each extracted reference:

1. Look up or create a `code_files` row in SQLite
2. Insert a `refs` row linking the document to the code file, with the source line as the `context` column

## Incremental Indexing

Each document's raw content is hashed with SHA-256:

```go
h := sha256.Sum256([]byte(content))
hash := fmt.Sprintf("%x", h)
```

On each index run, the hash is compared against the `content_hash` stored in the existing `documents` row:

- **Hash matches:** The document is skipped (counted as `Skipped` in the result)
- **Hash differs:** The existing document and all its chunks/edges are deleted, then the document is re-indexed from scratch
- **No existing document:** The document is indexed as new

The `--force` flag on `librarian index` bypasses the hash check and re-indexes all documents unconditionally.

## Related Document Building

After all files in a directory are indexed, `buildRelatedDocEdges` runs a second pass:

1. For each indexed document, query the `refs` table for its referenced code files
2. Build a reverse map: `code_file_path -> [document_paths...]`
3. For each code file referenced by 2+ documents, insert `related_docs` rows between all pairs of those documents with `relation_type: "shared_code_references"`

This means if `docs/auth.md` and `docs/api.md` both reference `internal/auth/oauth.go`, they will be linked by a `related_docs` row. The `get_context` MCP tool joins on this table to surface related documentation.

Duplicate entries are prevented by tracking linked pairs in a set keyed by `docPathA|docPathB`.

## Frontmatter Conventions

To get the most out of Librarian's indexing, use these frontmatter fields in your markdown files:

```yaml
---
title: Authentication Guide
type: guide
description: How authentication works in the application.
---
```

| Field | Effect |
|-------|--------|
| `title` | Used as the document title in search results and the context header prepended to chunk embeddings. Falls back to the first H1 heading |
| `type` | Stored as `doc_type` in the `documents` table. Used for filtering with `list_documents`. Defaults to `"guide"` |
| `description` | Stored as `summary` in the `documents` table. Falls back to the first paragraph of the document |

Additional frontmatter fields are preserved in the `frontmatter` JSON field but do not have special handling.
