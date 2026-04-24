---
title: MCP Tools
type: reference
description: The 5 tools exposed by `librarian mcp serve` — parameters, behaviour, and example outputs.
---

# MCP Tools

Librarian's MCP server is **opt-in**. The primary UX is the CLI (`librarian search`, `context`, etc.) plus platform pointers written by `librarian install`. MCP is there for assistants that prefer structured tool calls over shelling out to the CLI.

## Setup

Start the server on stdio:

```sh
librarian mcp serve
```

(Earlier versions used `librarian serve`; `librarian install` handles older and newer shape transparently via the pointer files it writes.)

Configure your assistant to launch it. `librarian install` writes most of this for you — otherwise:

### Claude Code — `.mcp.json`

```json
{
  "mcpServers": {
    "librarian": {
      "command": "librarian",
      "args": ["mcp", "serve"]
    }
  }
}
```

### Cursor — `.cursor/mcp.json`

```json
{
  "mcpServers": {
    "librarian": {
      "command": "librarian",
      "args": ["mcp", "serve"]
    }
  }
}
```

## Tools

The server registers **5 tools** in `Serve()` at `internal/mcpserver/server.go`.

### `search_docs`

Semantic vector search. Returns the top chunks ranked by `0.90 × vector_similarity + 0.10 × metadata_boost`.

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | string | yes | — | Natural-language search query |
| `limit` | number | no | 5 | 1–20 |

Behaviour: embeds `query` via the configured embedder, over-fetches `limit × 3` candidates from sqlite-vec, re-ranks with signal metadata (warnings, decisions, risk markers, code annotations), returns the top `limit`. Read-only.

**Example output:**

```
Found 3 results for "authentication flow":

### Result 1
**File:** docs/auth.md
**Section:** Login Flow
**Content:**
The login flow uses OAuth 2.0 with PKCE. The client redirects to...

### Result 2
**File:** internal/auth/oauth.go
**Section:** AuthService.Login
**Content:**
// Login exchanges an authorization code for access + refresh tokens.
func (s *AuthService) Login(ctx context.Context, code string) (*Session, error) {
    ...

### Result 3
**File:** docs/security.md
**Section:** Token Management
**Content:**
Access tokens expire after 1 hour. Refresh tokens are rotated on each use...
```

---

### `get_document`

Reads a document's full content from disk and enriches it with database metadata (title, type, chunk count).

| Parameter | Type | Required | Description |
|---|---|---|---|
| `file_path` | string | yes | File path relative to workspace root |

Resolves to an absolute path, reads the file, looks up the document row. If not indexed, the raw content is still returned. Read-only.

---

### `get_context`

The heavy retrieval tool: comprehensive briefing combining vector search with graph walks. Equivalent to `librarian context`. Use it for "how does X work" or architecture questions.

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `query` | string | yes | — | Topic to brief |
| `limit` | number | no | 5 | 1–10 initial search results |

Five-step flow:

1. **Vector search** — matches top chunks for `query`.
2. **Primary sources** — emits the matched chunks with path + section context.
3. **Source documents** — unique parent docs of those chunks, with type + title.
4. **Referenced code files** — joins through `refs` to find code files those docs mention, annotated with language.
5. **Related documentation** — joins through `graph_edges{kind="shared_code_ref"}` to surface other docs that reference the same code.

Read-only.

**Example output:**

```
=== BRIEFING: "authentication flow" ===

## Primary Sources (direct matches):

### docs/auth.md > Login Flow
The login flow uses OAuth 2.0 with PKCE. The client redirects to
the identity provider, which returns an authorization code...

### internal/auth/oauth.go > AuthService.Login
// Login exchanges an authorization code for access + refresh tokens.
func (s *AuthService) Login(ctx context.Context, code string) (*Session, error) { … }

## Source Documents:
- docs/auth.md (guide) — "Authentication Guide"
- internal/auth/oauth.go (go)

## Referenced Code Files:
- internal/auth/oauth.go (go)
- internal/middleware/auth.go (go)
- internal/auth/tokens.go (go)

## Related Documentation:
- docs/api.md — "API Reference"
- docs/security.md — "Security Policy"
```

---

### `list_documents`

Lists every indexed document. Optional type filter.

| Parameter | Type | Required | Description |
|---|---|---|---|
| `doc_type` | string | no | Filter (e.g. `guide`, `reference`, `architecture`, `docx`, `pdf`, `go`, `python`, …) |

Behaviour: queries `documents`, optionally filtered. Read-only.

Since every indexed file becomes a document, this also surfaces source code and Office / PDF documents — filter by their format (`go`, `python`, `docx`, `pdf`, `xlsx`, `pptx`) to narrow down.

---

### `update_docs`

Writes a documentation file and re-indexes it. Not read-only.

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `file_path` | string | yes | — | Path relative to workspace root |
| `content` | string | yes | — | Full markdown body to write |
| `reindex` | string | no | `"file"` | `"file"` to re-index only this file, `"full"` for the whole docs directory |

Flow:

1. Validates the resolved path is within `docs_dir` — writes outside the docs directory are rejected.
2. Creates parent directories as needed.
3. Writes `content` to the file.
4. Re-indexes (`reindex="file"`) a single file or (`reindex="full"`) the whole docs directory with `force=true`.

Callers that need to write source code or files outside `docs_dir` should use a conventional filesystem tool; `update_docs` is scoped to documentation.

---

## Instructions string

The server advertises a short usage string to connecting assistants:

> Librarian provides semantic search across project documentation. Use search_docs for quick searches, get_context for comprehensive briefings with related code files and documents, get_document to read full documents, list_documents to browse the index, and update_docs to write and re-index documentation.

This appears in tool listings on assistants that surface instructions.

## CLI equivalents

Most MCP tools have a direct CLI mirror:

| MCP tool | CLI |
|---|---|
| `search_docs` | `librarian search <query>` |
| `get_context` | `librarian context <query>` |
| `get_document` | `librarian doc <path>` |
| `list_documents` | `librarian list [--doc-type=…]` |
| `update_docs` | `librarian update <path> --content=…` |

The CLI also exposes graph-specific commands (`neighbors`, `path`, `explain`, `report`) that aren't yet surfaced as MCP tools — see [CLI Reference](cli.md).
