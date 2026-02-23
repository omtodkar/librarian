---
title: Indexing Pipeline
type: reference
description: How the indexing pipeline works, from file walking through parsing, chunking, code reference extraction, and incremental indexing.
---

# Indexing Pipeline

The indexing pipeline transforms markdown files into searchable vector chunks and a connected graph in HelixDB. It runs when you execute `librarian index` or when the `update_docs` MCP tool triggers a re-index.

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
 │          │    │         │    │         │    │  + Code Refs     │
 │ Find .md │    │ AST +   │    │ Section │    │  + Related Docs  │
 │ files    │    │ front-  │    │ aware   │    │  + Hash Check    │
 │          │    │ matter  │    │ split   │    │                  │
 └─────────┘    └─────────┘    └─────────┘    └─────────────────┘
```

### Key files

- `internal/indexer/walker.go` - Stage 1: Walk
- `internal/indexer/parser.go` - Stage 2: Parse
- `internal/indexer/chunker.go` - Stage 3: Chunk
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

All frontmatter fields are also stored as a JSON string in the `frontmatter` field of the Document node.

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

## Code Reference Extraction

The reference extractor (`ExtractCodeReferences`) scans the raw markdown content for file path patterns using a regex:

```
(?:^|[\s`"'(])([a-zA-Z0-9_/.-]+\.(?:go|ts|tsx|js|jsx|py|rs|java|rb|c|cpp|h|hpp|cs|swift|kt|scala|sh|bash|zsh|yaml|yml|toml|json))(?:$|[\s`"')\]:,])
```

This matches file paths like `internal/auth/oauth.go` or `src/components/Login.tsx` that appear in documentation text, code blocks, or inline code.

### Filtering

Matched paths are filtered against the `code_file_patterns` config. Only files whose extension matches one of the configured patterns (e.g., `*.go`, `*.ts`) are kept.

### Language Detection

The file extension is mapped to a language name (e.g., `.go` -> `"go"`, `.ts`/`.tsx` -> `"typescript"`, `.py` -> `"python"`). This is stored on the `CodeFile` node.

### Storage

For each extracted reference:

1. Look up or create a `CodeFile` node in HelixDB
2. Create a `References` edge from the `Document` to the `CodeFile`, with the source line as the `context` property

## Incremental Indexing

Each document's raw content is hashed with SHA-256:

```go
h := sha256.Sum256([]byte(content))
hash := fmt.Sprintf("%x", h)
```

On each index run, the hash is compared against the `content_hash` stored on the existing `Document` node in HelixDB:

- **Hash matches:** The document is skipped (counted as `Skipped` in the result)
- **Hash differs:** The existing document and all its chunks/edges are deleted, then the document is re-indexed from scratch
- **No existing document:** The document is indexed as new

The `--force` flag on `librarian index` bypasses the hash check and re-indexes all documents unconditionally.

## RelatedDoc Edge Building

After all files in a directory are indexed, `buildRelatedDocEdges` runs a second pass:

1. For each indexed document, query HelixDB for its referenced code files
2. Build a reverse map: `code_file_path -> [document_paths...]`
3. For each code file referenced by 2+ documents, create `RelatedDoc` edges between all pairs of those documents with `relation_type: "shared_code_references"`

This means if `docs/auth.md` and `docs/api.md` both reference `internal/auth/oauth.go`, they will be linked by a `RelatedDoc` edge. The `get_context` MCP tool traverses these edges to surface related documentation.

Duplicate edges are prevented by tracking linked pairs in a set keyed by `docPathA|docPathB`.

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
| `type` | Stored as `doc_type` on the Document node. Used for filtering with `list_documents`. Defaults to `"guide"` |
| `description` | Stored as `summary` on the Document node. Falls back to the first paragraph of the document |

Additional frontmatter fields are preserved in the `frontmatter` JSON field but do not have special handling.
