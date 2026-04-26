# Security Policy

## Supported Versions

Only the **latest release** receives security fixes. If you are running an older version, please upgrade before reporting.

| Version | Supported |
|---------|-----------|
| latest  | ✅        |
| older   | ❌        |

## Reporting a Vulnerability

**Do not open a public GitHub issue for security vulnerabilities.**

Use [GitHub Private Vulnerability Reporting (PVR)](../../security/advisories/new) to report a vulnerability privately. PVR requires the maintainer to enable it in repo settings (Settings → Code security → Private vulnerability reporting — if the link above doesn't work, that step hasn't been done yet; in that case email the address in the git commit history or open a GitHub Discussion marked "Security").

A maintainer will acknowledge your report within **5 business days** and aim to provide a confirmed fix or remediation plan within **90 days** of the initial report. If the timeline can't be met due to complexity, we will communicate that clearly and keep you updated.

We follow [coordinated disclosure](https://cheatsheetseries.owasp.org/cheatsheets/Vulnerability_Disclosure_Cheat_Sheet.html): we ask that you give us the 90-day window before public disclosure. We will credit you in the release notes unless you prefer otherwise.

## Scope — What Qualifies as a Vulnerability

A security vulnerability is a flaw that allows an attacker to violate confidentiality, integrity, or availability in a way that the user would not have authorized.

**In scope:**

- Remote or local code execution via any Librarian command or MCP tool invocation
- Exfiltration of API keys, credentials, or source code to an attacker-controlled endpoint
- Path traversal or arbitrary file read/write outside the workspace
- Denial of service caused by crafted input files that consume unbounded memory/CPU
- Privilege escalation through the MCP stdio server
- Injection attacks (command injection, SQL injection) via file names or document content

**Out of scope (treat as a regular bug):**

- Issues that require the attacker to already have write access to the `.librarian/config.yaml` or the SQLite database
- Issues in third-party embedding providers (Gemini, OpenAI-compatible servers) — report those upstream
- Theoretical/low-severity issues with no practical exploit path
- Missing hardening that is documented as a known trade-off

## Threat Model

Librarian runs locally and touches several sensitive surfaces:

**MCP stdio server** (`librarian mcp serve`) — AI assistants (Claude, Cursor, etc.) launch this process and send it JSON-RPC over stdin/stdout. A bug in argument parsing, tool dispatch, or path resolution could let a malicious prompt cause the server to read or write files outside the intended workspace, or execute arbitrary shell commands. Trust boundary: the MCP client is trusted; document content piped through the server is not.

**Embedding API calls** — Every `index` and `search` operation sends text chunks to an external embedding endpoint (Gemini or a user-configured OpenAI-compatible base URL). A misconfigured or attacker-supplied `base_url` in `.librarian/config.yaml` could cause source-code fragments to be exfiltrated to an unexpected server. `LIBRARIAN_EMBEDDING_API_KEY` is also in scope — it must not be logged or stored outside the config.

**SQLite workspace database** (`.librarian/librarian.db`) — The database is written inside the project directory and grows with the indexed corpus. A maliciously crafted project (e.g. symlinks pointing outside the workspace, file names containing SQL metacharacters) could cause unexpected behavior. Extremely large files could cause the indexer to exhaust disk or memory.

**File-handler chain** — Librarian parses PDFs (via pdfcpu), Office documents (DOCX/XLSX/PPTX via ZIP extraction), and markdown/code via tree-sitter grammars. All of these parsers execute on user-supplied files. Vulnerabilities in upstream parsers (out-of-bounds reads, zip-bomb decompression, malformed AST handling) are within Librarian's attack surface even if the root cause is upstream. Pin or audit upstream parser versions when vulnerabilities are disclosed.

## Responsible Disclosure

See [CONTRIBUTING.md → Responsible Reporting](CONTRIBUTING.md#responsible-reporting) for contributor guidance on how to handle suspected vulnerabilities found while working on the codebase.
