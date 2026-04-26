---
title: Upgrading
type: guide
description: How to upgrade Librarian across versions — what changes automatically, what requires user action, and how to recover from embedding model swaps.
---

# Upgrading

## How upgrades work

Librarian stores its index in a single SQLite file at `.librarian/librarian.db`. Every schema change is delivered as a numbered [goose](https://github.com/pressly/goose) migration under `db/migrations/`. On every `store.Open` call (any command that opens the database), Librarian runs `goose.Up` against the embedded migrations — pending migrations apply automatically, and already-applied ones are a no-op. No manual `migrate` step is needed.

The vector table (`doc_chunk_vectors`) is the exception: it's created lazily on the first vector insert because its column width is determined by the embedding model's output dimensions, not a compile-time constant. It is not managed by goose.

What you *may* need to do after upgrading depends on whether the release changes the embedding model or configuration:

| Upgrade type | What happens automatically | What you must do |
|---|---|---|
| New release, same embedder | Schema migrations apply on next run | Nothing |
| New release, embedder changed | Schema migrations apply | Run `librarian reindex --rebuild-vectors` |
| Switching embedding provider | n/a (user-initiated) | Run `librarian reindex --rebuild-vectors` |
| Switching embedding model name (same provider) | n/a (user-initiated) | Run `librarian reindex --rebuild-vectors` |
| Changing model weights behind the same model name | Nothing — can't be detected | Run `librarian reindex --rebuild-vectors` manually |

---

## Upgrading to 0.2.0

**Schema changes:** The initial goose-managed baseline (`0001_initial_schema.sql`) establishes all seven core tables. If you were running a pre-goose build of Librarian (before migration tracking was added), your database lacks the `goose_db_version` table. `store.Open` detects this and refuses to open it:

```
pre-goose database detected: delete .librarian/librarian.db and run 'librarian index' to rebuild
```

**Action required if you see this error:**

```sh
rm .librarian/librarian.db
librarian index          # rebuilds from the current schema
```

If you had the same embedding model configured before and after the rebuild, this is safe and non-destructive to your documentation source files.

**If you also changed your embedding model** while upgrading, run `librarian reindex --rebuild-vectors` instead of a plain `librarian index` after the database is rebuilt — see [Switching embedding models](#switching-embedding-models) below.

---

## Switching embedding models

### Switching providers (e.g. Gemini → Infinity)

Gemini's default model (`gemini-embedding-2`) produces 3072-dimensional vectors. A local Infinity server with `nomic-embed-text` produces 768-dimensional vectors. The dimensions are incompatible — the vec0 virtual table is sized at creation and cannot be altered.

When you change `embedding.provider` or `embedding.model` in `.librarian/config.yaml` and run `librarian index`, the index refuses with:

```
embedding model/dimension mismatch: index was built with "gemini-embedding-2" (3072-dim),
config now specifies "nomic-embed-text" (768-dim);
run 'librarian reindex --rebuild-vectors' to drop the vector table and re-embed every chunk
```

**Recovery:**

```sh
# Update .librarian/config.yaml with the new provider/model, then:
librarian reindex --rebuild-vectors
```

This drops `doc_chunk_vectors`, `doc_chunks`, and `embedding_meta`, then re-embeds every document with the currently configured model. `documents`, `code_files`, and the graph are preserved. Afterwards, optionally refresh the graph (the graph pass doesn't embed and is unaffected by model changes):

```sh
librarian index --skip-docs    # optional: refresh graph only
```

Note: `librarian reindex --rebuild-vectors` re-embeds every chunk, which costs API credits on paid providers. Content-hash-based incremental indexing does not apply to reindex — every chunk is re-embedded regardless.

See [Embedding → Detecting model changes](embedding.md#detecting-model-changes) for the full detection mechanism.

### Switching between local models with the same name

**Known limitation:** if two `librarian index` runs use the same `embedding.model` name against different model weights — for example, an Ollama server that was updated in place while `nomic-embed-text` kept its name — the mismatch cannot be detected. The model identifier is the only signal Librarian has.

If you knowingly swapped the underlying weights (model upgrade, re-pull from a different registry), run `librarian reindex --rebuild-vectors` manually after the swap to avoid stale vectors silently remaining in the index:

```sh
librarian reindex --rebuild-vectors
```

---

## Detecting model changes

The `embedding_meta` table records two rows, `model` and `dimension`, written on the first ever vector insert. Every subsequent `AddChunk` checks the current embedder's model identifier and dimension against these rows. A mismatch produces the error shown above. This guard makes silent corruption impossible for the cases it can detect — a config-level swap (provider change, model rename) is caught on the next indexing run before any mismatched vectors reach the database.

The full mechanics are in [Embedding → Detecting model changes](embedding.md#detecting-model-changes).

---

## Version history

| Version | Schema | Embedder changes | User action |
|---|---|---|---|
| 0.1.x | Pre-goose (no migration tracker) | — | Delete DB and re-index when upgrading to 0.2+ |
| 0.2.0 | `0001_initial_schema.sql` (goose baseline) | None from project side | Re-index from scratch if coming from 0.1.x; otherwise none |
