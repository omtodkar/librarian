---
title: MCP Tools
type: reference
description: The 6 tools exposed by `librarian mcp serve` — parameters, stability classifications, behaviour, and example outputs.
---

# MCP Tools

Librarian's MCP server is **opt-in**. The primary UX is the CLI (`librarian search`, `context`, etc.) plus platform pointers written by `librarian install`. MCP is there for assistants that prefer structured tool calls over shelling out to the CLI.

## API Stability Policy

Each parameter in this document carries one of three stability labels:

| Label | Meaning |
|---|---|
| **STABLE** | Name and semantics are locked until a v2 major release. Downstream consumers may depend on this field unconditionally. |
| **EXPERIMENTAL** | May change with a deprecation notice in the CHANGELOG before the next minor release. |
| **INTERNAL** | Implementation detail — may change without notice between any two releases. |

The server currently advertises version **0.2.0** in both the `initialize` response and its instructions string. Clients that gate on version-string presence can use the `librarian <ver>.` prefix in the instructions to detect which stability tier applies.

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

The `command` field resolves against `PATH` by default. If the binary is only built in-repo (e.g. `go build -o librarian .`), use an absolute path instead — `/path/to/your/project/librarian`. When the server needs environment variables that aren't already exported in your shell (typically `LIBRARIAN_EMBEDDING_API_KEY` for local embedders), pass them in an `env` block:

```json
{
  "mcpServers": {
    "librarian": {
      "command": "/path/to/project/librarian",
      "args": ["mcp", "serve"],
      "env": {
        "LIBRARIAN_EMBEDDING_API_KEY": "local"
      }
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

The server registers **6 tools** in `Serve()` at `internal/mcpserver/server.go`.

---

### `search_docs`

Semantic vector search. Returns the top chunks ranked by `0.90 × vector_similarity + 0.10 × metadata_boost`.

**Inputs:**

| Parameter | Type | Required | Default | Stability | Description |
|---|---|---|---|---|---|
| `query` | string | yes | — | **STABLE** | Natural-language search query |
| `limit` | number | no | 5 | **STABLE** | Result count; min 1, max 20 |
| `include_refs` | boolean | no | false | **STABLE** | Append referenced code file paths for each result |

**Output:** Plain text. A header line followed by one `### Result N` block per chunk. Each block contains `**File:**`, `**Section:**`, and `**Content:**` fields. When `include_refs=true`, a `**Refs:**` line is appended to blocks that have associated code references.

**Stability notes:**
- The `### Result N` / `**File:**` / `**Section:**` / `**Content:**` heading literals are **STABLE** — parsers may rely on them.
- Internal scoring weights (`0.90 × vector`, `0.10 × metadata`) are **INTERNAL** and may be tuned without notice.

Read-only.

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

### `get_context`

The heavy retrieval tool: a comprehensive briefing combining vector search with graph walks. Equivalent to `librarian context`. Use it for "how does X work" or architecture questions.

**Inputs:**

| Parameter | Type | Required | Default | Stability | Description |
|---|---|---|---|---|---|
| `query` | string | yes | — | **STABLE** | Topic to brief |
| `limit` | number | no | 5 | **STABLE** | Initial search results; min 1, max 10 |

**Output:** Structured markdown with five sections (headings are **STABLE**):

1. `=== BRIEFING: "<query>" ===` — wrapper header
2. `## Primary Sources (direct matches):` — matched chunks with `### filepath > section` headers
3. `## Source Documents:` — unique parent documents of matched chunks
4. `## Referenced Code:` — code files referenced by those documents, grouped as `**Files:**`, `**Directories:**`, `**Patterns:**`
5. `## Related Documentation:` — documents linked to the same code via graph edges

**Stability notes:**
- All five section heading literals are **STABLE**.
- Content within each section (paths, titles, chunk text) reflects the current index state and is **STABLE per-version** — it changes as documents are updated, not as a result of tool API changes.
- The `### filepath > section` format within Primary Sources is **STABLE**.

Read-only.

**Example output:**

```
=== BRIEFING: "authentication flow" ===

## Primary Sources (direct matches):

### docs/auth.md > Login Flow
The login flow uses OAuth 2.0 with PKCE. The client redirects to
the identity provider, which returns an authorization code...

## Source Documents:
- docs/auth.md (guide) — "Authentication Guide"

## Referenced Code:
**Files:** internal/auth/oauth.go (go), internal/middleware/auth.go (go)

## Related Documentation:
- docs/api.md — "API Reference"
- docs/security.md — "Security Policy"
```

---

### `get_document`

Reads a document's full content from disk and enriches it with database metadata (title, type, chunk count).

**Inputs:**

| Parameter | Type | Required | Default | Stability | Description |
|---|---|---|---|---|---|
| `file_path` | string | yes | — | **STABLE** | File path relative to workspace root (e.g. `docs/auth.md`) |

**Output:** Plain text. When the document is indexed: a `# title` heading, a `**Type:** … | **Chunks:** …` metadata line, then the full file content. When not indexed: `# file_path` heading followed by raw content.

**Stability notes:**
- `# <title>` and `**Type:**` / `**Chunks:**` output structure is **STABLE** for indexed documents.
- Unindexed fallback format is **EXPERIMENTAL** — prefer indexing documents before fetching.

Resolves `file_path` to an absolute path and reads from disk. If not indexed, raw content is still returned. Read-only.

---

### `list_documents`

Lists every indexed document with metadata. Optional type filter.

**Inputs:**

| Parameter | Type | Required | Default | Stability | Description |
|---|---|---|---|---|---|
| `doc_type` | string | no | `""` | **STABLE** | Filter by type (e.g. `guide`, `reference`, `architecture`, `go`, `python`, `docx`, `pdf`); omit to list all |

**Output:** Plain text. A `Indexed Documents (N):` header followed by a fixed-column table with columns `File`, `Type`, `Chunks`, `Title`. When the index is empty (or no documents match the filter), a single explanatory message is returned.

**Stability notes:**
- The four column names (`File`, `Type`, `Chunks`, `Title`) are **STABLE**.
- Column widths and alignment are **INTERNAL** — do not parse via fixed-offset slicing.

Read-only.

---

### `update_docs`

Writes a documentation file and re-indexes it.

**Inputs:**

| Parameter | Type | Required | Default | Stability | Description |
|---|---|---|---|---|---|
| `file_path` | string | yes | — | **STABLE** | Path relative to workspace root (e.g. `docs/auth.md`) |
| `content` | string | yes | — | **STABLE** | Full markdown body to write |
| `reindex` | string | no | `"file"` | **STABLE** | `"file"` — re-index only this file; `"full"` — re-index entire docs directory |

**Output:** Plain text summary. First line: `Updated <file_path>`. Then a `Re-indexed (<scope>):` block with `Documents:`, `Chunks:`, `Code refs:` counts, and an `Errors:` count + list if any indexing errors occurred.

**Stability notes:**
- `Updated <path>` first-line format is **STABLE**.
- The `Documents:` / `Chunks:` / `Code refs:` label names are **STABLE**.
- `Errors:` section only appears when errors > 0; its presence is **STABLE**, individual error message text is **INTERNAL**.

Safety: validates that the resolved absolute path is inside the configured `docs_dir` — writes outside that directory are rejected. Not read-only.

Flow:

1. Validates the resolved path is within `docs_dir`.
2. Creates parent directories as needed.
3. Writes `content` to the file.
4. Re-indexes: `"file"` scope re-indexes only this file; `"full"` re-indexes the whole docs directory with `force=true`.

Callers that need to write source code or files outside `docs_dir` should use a conventional filesystem tool; `update_docs` is scoped to documentation.

---

### `trace_rpc`

End-to-end trace of a gRPC RPC in one call: proto declaration, every language's generated-code implementation (via `implements_rpc` graph edges), input/output message fields (recursively resolved to depth 3), sibling RPCs on the same service, and a BFS caller walk up to depth 3.

**Inputs:**

| Parameter | Type | Required | Default | Stability | Description |
|---|---|---|---|---|---|
| `rpc` | string | yes | — | **STABLE** | RPC identifier — see accepted forms below |
| `format` | string | no | `"markdown"` | **STABLE** | Output format: `"markdown"` (human-readable) or `"json"` (structured) |

Accepted forms for `rpc`:
- Full symbol ID: `sym:auth.v1.AuthService.Login`
- Dotted path: `auth.v1.AuthService.Login`
- Service+method suffix: `AuthService.Login`
- File+method: `api/auth.proto:Login`

**Output — markdown format:** Human-readable sections covering definition, implementations, callers, input/output messages, and related RPCs. Section heading literals are **EXPERIMENTAL** — they may be restructured in a minor release with CHANGELOG notice.

**Output — JSON format:** Structured object. Top-level field names are **STABLE**:

| Field | Stability | Description |
|---|---|---|
| `input` | **STABLE** | The raw RPC identifier passed by the caller |
| `definition` | **STABLE** | Proto declaration surface (service, method, package, etc.) |
| `implementations` | **STABLE** | Generated-code bindings via `implements_rpc` edges |
| `callers` | **STABLE** | BFS caller walk result |
| `callers_note` | **STABLE** | Explanatory note when no callers are found |
| `input_message` | **STABLE** | Resolved input message type with fields |
| `output_message` | **STABLE** | Resolved output message type with fields |
| `related_rpcs` | **STABLE** | Sibling RPCs on the same proto service |
| `warnings` | **STABLE** | Best-effort warnings (dangling edges, I/O failures) |

Nested fields within `definition`, `implementations`, `input_message`, `output_message`, and `related_rpcs` objects are **EXPERIMENTAL** — their names and presence may change with deprecation notice.

Read-only. Requires a populated graph index (`librarian index` must have run with proto files present).

---

## Instructions string

The server advertises a usage string to connecting assistants:

> librarian 0.2.0. Provides semantic search across project documentation. Tools: search_docs (quick search), get_context (deep briefing + graph traversal), get_document (full file content), list_documents (enumerate index), update_docs (write + re-index), trace_rpc (gRPC end-to-end trace). Stable API: parameter names locked until v2 — see docs/mcp-tools.md for stability classifications.

The `librarian <version>.` prefix at the start of the instructions string is intentional — clients that want to gate on API version can detect it with a prefix match.

---

## CLI equivalents

Most MCP tools have a direct CLI mirror:

| MCP tool | CLI |
|---|---|
| `search_docs` | `librarian search <query>` |
| `get_context` | `librarian context <query>` |
| `get_document` | `librarian doc <path>` |
| `list_documents` | `librarian list [--doc-type=…]` |
| `update_docs` | `librarian update <path> --content=…` |
| `trace_rpc` | `librarian explain <rpc-id>` (partial; CLI doesn't expose message fields) |

The CLI also exposes graph-specific commands (`neighbors`, `path`, `report`) that aren't yet surfaced as MCP tools — see [CLI Reference](cli.md).
