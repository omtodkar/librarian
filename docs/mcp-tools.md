---
title: MCP Tools
type: reference
description: Reference for all 5 MCP tools exposed by librarian serve, with parameters, behavior, and example outputs.
---

# MCP Tools

Librarian exposes 5 tools via the Model Context Protocol (MCP). AI coding tools connect to Librarian over stdio and call these tools to search, retrieve, and update project documentation.

## Setup

Start the MCP server:

```sh
librarian serve
```

This launches an MCP server on stdio. Configure your AI coding tool to connect to it:

### Claude Code

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "librarian": {
      "command": "librarian",
      "args": ["serve"]
    }
  }
}
```

### Cursor

Add to `.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "librarian": {
      "command": "librarian",
      "args": ["serve"]
    }
  }
}
```

## Tool Reference

### `search_docs`

Semantic vector search across all indexed documentation chunks. Returns the most relevant chunks with their file paths and section context.

**Parameters:**

| Name | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `query` | `string` | yes | - | Natural language search query |
| `limit` | `number` | no | `5` | Maximum results to return (1-20) |

**Behavior:** Calls `SearchChunks` which performs a vector similarity search against chunk embeddings in the `doc_chunk_vectors` table via sqlite-vec. Candidates are over-fetched (3x the requested limit) and then re-ranked using a weighted formula:

```
finalScore = 0.90 * vectorSimilarity + 0.10 * metadataBoost
```

The metadata boost promotes chunks containing emphasis signals — warnings (+0.3), decisions (+0.3), important labels (+0.3), risk markers like deprecated or breaking-change (+0.2), and other inline labels (+0.1). This surfaces actionable content like warnings and deprecation notices without overriding semantic relevance. See [Storage Layer](storage.md#search-re-ranking) for full details.

**Annotations:** Read-only.

**Example output:**

```
Found 3 results for "authentication flow":

### Result 1
**File:** docs/auth.md
**Section:** Login Flow
**Content:**
The login flow uses OAuth 2.0 with PKCE. The client redirects to...

### Result 2
**File:** docs/api.md
**Section:** Authentication
**Content:**
All API endpoints require a Bearer token in the Authorization header...

### Result 3
**File:** docs/security.md
**Section:** Token Management
**Content:**
Access tokens expire after 1 hour. Refresh tokens are rotated on each use...
```

---

### `get_document`

Reads the full content of a document from disk and enriches it with metadata from the database (title, type, chunk count).

**Parameters:**

| Name | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `file_path` | `string` | yes | - | File path of the document (e.g., `docs/auth.md`) |

**Behavior:** Resolves the file path to an absolute path, reads the file from disk, and looks up the document in the database for metadata. If the document is not indexed, the raw file content is still returned.

**Annotations:** Read-only.

**Example output:**

```
# Authentication Guide
**Type:** guide | **Chunks:** 4

---
title: Authentication Guide
type: guide
description: How authentication works in the application.
---

## Login Flow

The login flow uses OAuth 2.0 with PKCE...
```

---

### `get_context`

Comprehensive intelligence briefing that combines semantic search with relational joins. This is the most powerful tool - use it when you need to deeply understand a topic.

**Parameters:**

| Name | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `query` | `string` | yes | - | Natural language query for the topic you want context on |
| `limit` | `number` | no | `5` | Number of initial search results (1-10) |

**Behavior:** Executes a 5-step intelligence gathering flow:

1. **Semantic search** - Vector search for relevant chunks matching the query
2. **Primary sources** - Display the matched chunks with file path and section context
3. **Source documents** - Collect the unique parent documents of matched chunks, showing their type and title
4. **Referenced code files** - Join through the `refs` table from source documents to find code files they mention, with language annotations
5. **Related documentation** - Join through the `related_docs` table to surface other documents that share code references with the source documents

**Annotations:** Read-only.

**Example output:**

```
=== BRIEFING: "authentication flow" ===

## Primary Sources (direct matches):

### docs/auth.md > Login Flow
The login flow uses OAuth 2.0 with PKCE. The client redirects to
the identity provider, which returns an authorization code...

### docs/auth.md > Token Refresh
Access tokens expire after 1 hour. The client uses the refresh
token to obtain a new access token without user interaction...

## Source Documents:
- docs/auth.md (guide) - "Authentication Guide"

## Referenced Code Files:
- internal/auth/oauth.go (go)
- internal/middleware/auth.go (go)
- internal/auth/tokens.go (go)

## Related Documentation:
- docs/api.md - "API Reference"
- docs/security.md - "Security Policy"
```

---

### `list_documents`

Lists all indexed documents with metadata in a tabular format. Optionally filters by document type.

**Parameters:**

| Name | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `doc_type` | `string` | no | - | Filter by document type (e.g., `guide`, `reference`, `architecture`) |

**Behavior:** Queries the `documents` table for all rows. If `doc_type` is provided, filters results to only include documents of that type.

**Annotations:** Read-only.

**Example output:**

```
Indexed Documents (4):

File                                     Type           Chunks   Title
----                                     ----           ------   -----
docs/architecture.md                     architecture   3        Architecture
docs/configuration.md                    reference      2        Configuration
docs/mcp-tools.md                        reference      5        MCP Tools
docs/indexing.md                         reference      4        Indexing Pipeline
```

---

### `update_docs`

Writes or updates a documentation file on disk and re-indexes it. Enforces a path safety constraint: the file path must be within the configured `docs_dir`.

**Parameters:**

| Name | Type | Required | Default | Description |
|------|------|----------|---------|-------------|
| `file_path` | `string` | yes | - | File path relative to project root (e.g., `docs/auth.md`) |
| `content` | `string` | yes | - | Full markdown content to write to the file |
| `reindex` | `string` | no | `"file"` | Reindex scope: `"file"` to re-index only this file, or `"full"` to re-index the entire docs directory |

**Behavior:**

1. Validates that the resolved absolute path is within the configured `docs_dir`
2. Creates parent directories if they don't exist
3. Writes the content to the file
4. Re-indexes either the single file or the full docs directory (with `force=true`, bypassing content hash checks)

**Annotations:** Not read-only (writes to disk).

**Example output:**

```
Updated docs/auth.md

Re-indexed (file):
  Documents: 1
  Chunks:    4
  Code refs: 3
```
