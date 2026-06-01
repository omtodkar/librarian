# RFC: Multiple docs directories (`doc_dirs`)

- **Status**: Implemented (Phase 2, lib-avnk)
- **Date**: 2026-06-01 (Asia/Kolkata)
- **Author**: design captured with Claude Code
- **Motivating case**: `~/Workspace/Akaya` — a meta/container repo whose sub-repos
  (`proto`, `astro-engine`, `postgres`, `iac-terraform-gcp`, `k8s-deployments`)
  are gitignored siblings, each with its own `docs/` directory (and most with a
  `docs/DOCTRINE.md`). Today `librarian init` asks for a single `docs_dir`, so
  only one sub-repo's docs can feed the knowledge base.

## 1. Problem

`docs_dir` is a single string (`internal/config/config.go:10`, default `"docs"`
at `:213`). The whole docs pass is scoped to that one directory:

- `cmd/index.go:75` → `idx.IndexDirectory(docsDir, …)`
- `cmd/reindex.go:102`, `cmd/update.go:98`, `internal/mcpserver/update_docs.go:79`
  all call `IndexDirectory(cfg.DocsDir, true)`.

For a polyrepo workspace we want the knowledge base (`search_docs` /
`get_context`) to span every sub-repo's curated docs at once, while keeping the
curation boundary (we still don't want to index *all* files — just the docs
trees).

### The stated concern: path collisions

Several sub-repos each have `docs/DOCTRINE.md`. If two such files were stored
under the same `file_path`, the second would clobber the first — and the
`documents.file_path` column is **`UNIQUE NOT NULL`**
(`db/migrations/0001_initial_schema.sql`), so the second insert would actually
*fail* (`store.AddDocument` is a plain `INSERT`, no upsert).

**Good news — the existing path logic already namespaces by directory.**
`WalkDocs` (`internal/indexer/walker.go:275-328`) computes:

```go
relPath, _ := filepath.Rel(absDocsDir, path)        // path within the docs dir
FilePath: filepath.Join(docsDir, relPath)           // stored path = dir + rel
```

So if `doc_dirs` holds *distinct* entries, the stored paths are automatically
disambiguated:

| Sub-repo docs dir          | File           | Stored `file_path`                |
|----------------------------|----------------|-----------------------------------|
| `proto/docs`               | `DOCTRINE.md`  | `proto/docs/DOCTRINE.md`          |
| `astro-engine/docs`        | `DOCTRINE.md`  | `astro-engine/docs/DOCTRINE.md`   |
| `k8s-deployments/docs`     | `DOCTRINE.md`  | `k8s-deployments/docs/DOCTRINE.md`|

No collision, and the path stays human-meaningful (you can see which repo a doc
came from). **The core requirement is therefore: preserve `filepath.Join(dir,
rel)` per-directory, and forbid overlapping/duplicate entries.** This RFC also
recommends hardening the path to be *workspace-root-relative and
invocation-independent* (see §3.2).

## 2. Goals / non-goals

**Goals**
- `doc_dirs: [a, b, c]` config; index the union of all listed directories.
- Stored document paths are unique and stable across sub-repos.
- `librarian init` auto-detects per-sub-repo `docs/` dirs and confirms with the
  user (chosen UX).
- Backward compatible: existing `docs_dir: docs` configs keep working unchanged
  (chosen: alias it).

**Non-goals**
- The **graph pass** (`IndexProjectGraph` over `ProjectRoot`) is unchanged — it
  already walks the whole workspace root and is governed by `graph.*`. (Akaya
  runs `graph.enabled: false` anyway; sub-repos are gitignored.)
- No change to embedding, chunking, or retrieval ranking.

## 3. Design

### 3.1 Config

Add an array and keep the scalar as a deprecated alias.

```go
// internal/config/config.go
DocsDir  string   `mapstructure:"docs_dir"`   // DEPRECATED alias, kept for back-compat
DocDirs  []string `mapstructure:"doc_dirs"`   // new canonical field
```

Normalize in `Load()` (after `viper.Unmarshal`) into a single canonical list so
the rest of the codebase reads exactly one thing. Proposed helper:

```go
// ResolvedDocDirs returns the canonical, de-duplicated, cleaned list of docs
// directories. Precedence: doc_dirs if non-empty; else [docs_dir]; else
// ["docs"]. Entries are filepath.Clean'd; exact duplicates removed.
func (c *Config) ResolvedDocDirs() []string
```

Rules:
- `doc_dirs` non-empty → use it (ignore `docs_dir`, but warn if both set).
- `doc_dirs` empty, `docs_dir` set → `[docs_dir]`.
- both empty → `["docs"]` (preserve current default at `config.go:213`).
- Clean each entry; reject empty strings; de-dupe.
- **Reject overlapping entries** (one a prefix-path ancestor of another, e.g.
  `["docs", "docs/api"]`) — that's the only configuration that can produce a
  genuine `file_path` collision. Fail fast at config-load/index time with a
  clear error rather than hitting the UNIQUE constraint mid-index.
- **Reject entries outside `ProjectRoot`** (absolute siblings, `..`-escapes) —
  see §5. Keeps the ProjectRoot-relative path invariant (§3.2) total.

A single internal accessor means call sites stop reading `cfg.DocsDir` directly.

### 3.2 Path invariant — ProjectRoot-relative storage (DECIDED)

**Decision: adopt ProjectRoot-relative storage now.**

Today `docsDir` is resolved with `filepath.Abs(docsDir)` — i.e. **relative to
the process CWD** (`cmd/index.go:89`, `cmd/root.go:66` comment), while the
stored path uses the raw `docsDir` string. That's stable only if you always run
from the workspace root. For a polyrepo where people `cd` into sub-repos:

- Resolve each `doc_dir` **relative to `ProjectRoot`** (the `.librarian/` parent,
  already populated in `cmd/root.go`), not CWD.
- Store `file_path` as the **slash-normalized path relative to `ProjectRoot`**.

This makes `proto/docs/DOCTRINE.md` the stored key regardless of where the
command was invoked, and matches the graph pass's "workspace-relative"
convention used elsewhere (`handler.go:87`, buf manifest, and
`code_files` per `prune.go:35`). `get_document` / `doc` lookups
(`filepath.Abs(filePath)` to read, `GetDocumentByPath(filePath)` for metadata)
keep working as long as the stored key is the workspace-relative path the user
would naturally type.

Implementation notes:
- `WalkDocs` changes `FilePath: filepath.Join(docsDir, relPath)` →
  `filepath.Rel(ProjectRoot, absPath)`, slash-normalized. So `WalkDocs` needs
  the ProjectRoot (pass it in, or thread it via the Indexer).
- `prune.go:resolveAndGuard` already joins relative rows under `absRoot`
  (= ProjectRoot) and stats them, so it stays compatible with no change.
- **Fallback**: when `ProjectRoot` is empty (some test/standalone flows), fall
  back to the current CWD-relative `Join(docsDir, rel)` behavior so existing
  tests and workspace-less runs don't break.
- **No migration for typical users**: a single `docs_dir: docs` indexed from the
  workspace root already produces `docs/...`, which *equals* the ProjectRoot-
  relative path — stored keys are unchanged. Only unusual invocations (running
  from a subdir) change, for the better.

### 3.3 Indexer (IMPLEMENTED)

Implemented **option B** (smaller, lower-risk): kept `IndexDirectory(dir, force)` per-dir and added a thin orchestrator that loops the resolved dirs and aggregates `IndexResult` counts. The critical correctness point is pruning (see §3.4 below).

### 3.4 Pruning — already multi-dir-safe (verified)

Pruning is **stat-based, not walk-membership-based**, so multi-dir needs no special handling:

- Normal `librarian index` does **not** blanket-prune the docs tree. It only
  reconstitutes/cleans files surfaced via another reindexed file's
  affected-paths set. So indexing `proto/docs` will **not** delete `astro-engine/docs`
  rows — there is no "delete everything not in this walk" step.
- `--prune-missing` (`PruneMissingFiles`, opt-in) iterates **every** `documents`
  row, `os.Stat`s its stored path, and deletes only on `fs.ErrNotExist`.
  Because it keys off real on-disk existence rather than membership in any single dir's walk,
  it is **union-safe by construction** — a doc under `astro-engine/docs` survives
  a prune run triggered while reindexing `proto/docs`, as long as the file still exists.

The orchestrator simply loops `IndexDirectory` per dir; then `buildGraphEdges` runs a single
pass over the **union of all dirs' files** so cross-dir `shared_code_ref` edges materialize.
Prune semantics carry over unchanged.

The documented hazard: stored paths reflect the config **at original index time**.
If `doc_dirs` changes, old-path rows linger until a full reindex followed by `--prune-missing`.
This is a **migration note** (§4), not new prune logic.

### 3.5 `librarian init` — auto-detect + confirm (chosen UX)

`cmd/init.go` currently writes a static `defaultConfigYAML` with `docs_dir: docs`.
New flow:

1. From `ProjectRoot`, scan immediate children one level deep for a `docs/`
   directory (and the root `docs/` itself). For Akaya this yields
   `proto/docs`, `astro-engine/docs`, `postgres/docs`, `iac-terraform-gcp/docs`,
   `k8s-deployments/docs`.
2. Present the discovered list and let the user confirm/edit (de-select, add).
3. Write a `doc_dirs:` array into the generated config. If exactly one is found
   (or none), fall back to a single-entry list / `docs` default so simple repos
   stay simple.
4. Non-interactive / `--json` / piped runs: write the auto-detected list without
   prompting (and support an explicit `--doc-dir` repeatable flag override).

Update the post-init hint (`init.go` "Edit .librarian/config.yaml (docs_dir, …)")
to mention `doc_dirs`.

### 3.6 Write-side: `update`, `update_docs`, `capture_session`, FAQ

- **Containment check** (`cmd/update.go:69`, `internal/mcpserver/update_docs.go:60`):
  today it rejects paths outside the single `docs_dir`. Change to accept a path
  under **any** resolved `doc_dir` (loop the list).
- **Reindex after write**: switch the `IndexDirectory(cfg.DocsDir, true)` calls
  to the orchestrator (or, as an optimization, reindex only the dir that
  contains the written file).
- **`capture_session`** (`capture_session.go:106`) and **FAQ default**
  (`faq.go:59`, `<DocsDir>/faqs`) currently build paths under the single dir.
  Define a **primary docs dir** = `ResolvedDocDirs()[0]` as the default write
  target for generated content.
- **`capture_session` target override (DECIDED — option B)**: add an optional
  `docs_dir` argument to the `capture_session` MCP tool. When omitted, write
  under the primary dir (`ResolvedDocDirs()[0]`) — preserving today's behavior
  for single-dir workspaces. When provided, it must resolve to one of the
  configured `doc_dirs` (validated against the resolved list; reject otherwise
  with a clear error listing the valid dirs) so a session note can be filed into
  the right sub-repo (e.g. `astro-engine/docs`). The category subdir
  (`sessions/`, `decisions/`) and filename logic are unchanged — only the base
  dir is selectable. The captured file's stored `file_path` is then the usual
  ProjectRoot-relative key (e.g. `astro-engine/docs/sessions/2026-…-foo.md`).
- FAQ keeps its single default base (`<primary>/faqs`) for v1; no override
  surfaced there yet — revisit only if multi-repo FAQ generation is requested.

### 3.7 Lookups (read-side) — mostly free

`get_document`, `doc`, `list_documents` look up by exact `file_path` string and
display the stored path. They need no logic change; users just now type/see the
sub-repo-prefixed path (`proto/docs/DOCTRINE.md`). Worth a docs note so callers
know paths are workspace-relative.

## 4. Backward compatibility & migration

- **Compat**: `docs_dir: docs` → normalized to `["docs"]`; stored paths
  identical to today (`Join("docs", rel)`), so existing single-dir indexes need
  no reindex.
- **Migration to multi-dir**: switching Akaya from `docs_dir: docs` (a dir that
  may not even exist at root) to `doc_dirs: [proto/docs, …]` changes the set of
  stored paths → requires a **full reindex** (`librarian reindex`). The stale
  old-path rows are then cleaned up by the stat-based
  `librarian index --prune-missing` sweep (§3.4), which deletes rows whose stored
  path no longer exists on disk. Call this out in the changelog/docs.
- If §3.2 (ProjectRoot-relative) is adopted, single-dir users whose stored paths
  were CWD-relative-but-run-from-root are unaffected (same string); only unusual
  invocations change.

## 5. Edge cases

- Duplicate entries → de-dupe.
- Overlapping/nested entries → reject (collision source).
- A `doc_dir` that doesn't exist → warn + skip (don't fail the whole run; common
  while sub-repos are cloned incrementally).
- Entries outside `ProjectRoot` → **rejected in v1 (DECIDED)**. `doc_dirs` must
  resolve to a path within `ProjectRoot`; a `..`-escaping or absolute-sibling
  entry fails config validation with a clear error. This keeps the
  ProjectRoot-relative path invariant (§3.2) total — every stored `file_path` is
  a clean workspace-relative key. (Akaya's sub-repos are all inside the
  workspace, so this costs nothing today. Revisit if external docs trees are
  ever needed.)
- Symlinked docs dirs → resolve consistently (match current `filepath.Walk`
  behavior; document it).

## 6. File-by-file change list

| File | Change |
|------|--------|
| `internal/config/config.go` | Add `DocDirs []string`; keep `DocsDir` alias; add `ResolvedDocDirs()`; normalize + validate (overlap/dupe) in `Load()` |
| `internal/config/config_test.go` | Tests: alias precedence, default, dedupe, overlap rejection |
| `internal/indexer/indexer.go` | Orchestrator looping dirs + aggregate `IndexResult` counts |
| `internal/indexer/walker.go` | Per-dir walk unchanged; (opt §3.2) store ProjectRoot-relative path |
| `internal/indexer/prune.go` | No change needed — stat-based prune is already union-safe (§3.4) |
| `cmd/index.go` | Use resolved list; positional arg = index just that one dir |
| `cmd/reindex.go` | Iterate resolved list; single prune |
| `cmd/update.go` | Containment check against any doc_dir; reindex via orchestrator |
| `cmd/init.go` | Auto-detect `docs/` dirs + confirm; emit `doc_dirs:` array; `--doc-dir` flag |
| `internal/mcpserver/update_docs.go` | Containment against any doc_dir |
| `internal/mcpserver/capture_session.go` | Default write under primary doc_dir (`[0]`); add optional `docs_dir` arg validated against the configured list (§3.6). Optional → backward-compatible with the locked tool API |
| `docs/mcp-tools.md` | Document the new optional `capture_session` `docs_dir` param |
| `internal/faq/faq.go` | Default FAQDir under primary doc_dir |
| `docs/configuration.md`, `docs/cli.md`, `docs/indexing.md` | Document `doc_dirs`, path convention, migration note |
| `cmd/init.go` config template / `docs/` examples | `doc_dirs` example block |

## 7. Test plan

- Config: `doc_dirs` parsing, `docs_dir` alias fallthrough, default, dedupe,
  overlap rejection.
- Indexer integration: two sibling dirs each containing `DOCTRINE.md` →
  both indexed, distinct `file_path`, both retrievable, no UNIQUE error.
- **Prune (regression guard, §3.4)**: index dirs A+B; delete a file in A; run
  `index --prune-missing` → only A's stale doc removed, B untouched.
- init auto-detect: temp workspace with N sub-repo `docs/` dirs → generated
  config lists them; single/zero → falls back cleanly.
- Write path: `update_docs` accepts a file under a non-first doc_dir.

## 8. Rollout (completed — Phase 2, lib-avnk)

1. ✅ Config field + `ResolvedDocDirs()` + validation (overlap/dupe) + tests.
2. ✅ Indexer orchestrator looping dirs + aggregate result + integration test
   (two sibling `DOCTRINE.md` → distinct paths, no UNIQUE error).
3. ✅ Wired `index` / `reindex` / `update` / `update_docs` to the orchestrator.
4. ✅ `init` auto-detect + confirm + `--doc-dir` flag.
5. ✅ Write-side: primary-dir default for `capture_session`/FAQ, optional
   `capture_session` `docs_dir` override (§3.6), + any-dir containment checks
   for `update`/`update_docs`.
6. ✅ Docs + changelog + migration note.
7. ✅ §3.2 ProjectRoot-relative path hardening (decided and implemented in Phase 2, lib-avnk.2).

## 9. Decisions & open questions

**Decided:**
- **Q1 — Path convention**: adopt **ProjectRoot-relative storage** now (§3.2).
- **Q3 — Out-of-workspace dirs**: **restrict `doc_dirs` to within `ProjectRoot`**
  for v1; reject external/`..`-escaping entries at config validation (§5).

- **Q2 — generated-doc write target** (`capture_session`, FAQ): **option B
  (DECIDED)** — default to the primary dir `ResolvedDocDirs()[0]`, plus an
  optional `docs_dir` arg on `capture_session` validated against the configured
  list (§3.6). FAQ stays single-default for v1.

**Open:** none — all design questions resolved; ready to break into an epic.
