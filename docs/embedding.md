---
title: Embedding
type: reference
description: The Embedder interface, the Gemini and OpenAI-compatible providers, and how embeddings flow through indexing and search.
---

# Embedding

The embedding layer (`internal/embedding/`) generates vector embeddings from text. Similar text produces similar vectors, which powers Librarian's semantic search — retrieval by meaning rather than keywords.

## Embedder interface

```go
type Embedder interface {
    Embed(text string) ([]float64, error)
    Model() string
}
```

`Embed` takes a string and returns a float64 vector — used by the indexer (embedding chunks) and by the MCP server + CLI (embedding search queries). `Model()` returns the resolved model identifier (after any default fallback) so the store layer can detect config-level model swaps that would otherwise corrupt the vec0 index.

## Provider factory

`embedding.NewEmbedder(cfg)` in `internal/embedding/provider.go` selects an implementation based on `cfg.Provider`:

| Provider | Implementation | Used for |
|---|---|---|
| `"gemini"` | `GeminiEmbedder` | Google's Gemini embedding API |
| `"openai"` | `OpenAIEmbedder` | Any OpenAI-compatible `/v1/embeddings` endpoint (LM Studio, Ollama, vLLM, OpenAI proper, …) |

Unknown providers return an error naming the supported options.

## GeminiEmbedder

`internal/embedding/gemini.go` — calls Google's Gemini embedding API.

### Configuration

`NewGeminiEmbedder(apiKey)` takes the key from:

1. `embedding.api_key` in `.librarian/config.yaml`
2. `GEMINI_API_KEY` environment variable
3. `LIBRARIAN_EMBEDDING_API_KEY` environment variable (via Viper's env binding)

If none resolves, initialisation fails with an error.

### API details

| Property | Value |
|---|---|
| Endpoint | `https://generativelanguage.googleapis.com/v1beta/models/{model}:embedContent` |
| Default model | `gemini-embedding-2` (used when `embedding.model` is empty) |
| Dimensions | 3072 for `gemini-embedding-2`; `gemini-embedding-001` is 3072; `text-embedding-004` is 768 (deprecated) |
| Input | Single string per request |
| Output | `[]float64` |
| Auth | API key as query parameter |

### Request shape

```json
{ "content": { "parts": [ { "text": "the text to embed" } ] } }
```

## OpenAIEmbedder

`internal/embedding/openai.go` — calls any OpenAI-compatible `/v1/embeddings` endpoint.

### Configuration

| Field | Required | Default | Notes |
|---|---|---|---|
| `embedding.base_url` | no | `http://localhost:1234/v1` | LM Studio's default; set to `http://localhost:11434/v1` for Ollama, your provider URL otherwise |
| `embedding.model` | **yes** | — | Model identifier the endpoint understands (e.g. `text-embedding-3-small`, `nomic-embed-text`) |
| `embedding.api_key` | no | — | Sent as `Authorization: Bearer` when set; local providers often don't need it |

### API details

| Property | Value |
|---|---|
| Endpoint | `{base_url}/embeddings` |
| Dimensions | Determined by the model (768, 1536, 3072, …) |
| Input | Single string per request |
| Output | `[]float64` |
| Auth | Bearer token (optional) |

### Request shape

```json
{ "model": "text-embedding-3-small", "input": "the text to embed" }
```

## Error handling

Both embedders surface:

- HTTP non-200 status codes (with status + body snippet)
- API-level errors in the response JSON
- Empty embedding arrays in the response

All errors wrap the underlying cause via `fmt.Errorf("%w")` so callers can `errors.Is` if needed.

## Vector dimensions

Dimensions are discovered at runtime: the first call to `AddChunk` creates the `doc_chunk_vectors` vec0 virtual table sized to whatever the embedder returned. There's no dimensions config field.

### Detecting model changes

The `(model, dimension)` pair used on first index is recorded in the `embedding_meta` table and checked on every subsequent `AddChunk`. If you change `embedding.model` or `embedding.provider` in `.librarian/config.yaml`, the next indexing run fails with:

```
embedding model/dimension mismatch: index was built with "text-embedding-004" (768-dim),
config now specifies "gemini-embedding-2" (3072-dim);
run 'librarian reindex --rebuild-vectors' to drop the vector table and re-embed every chunk
```

Recover with `librarian reindex --rebuild-vectors`, which drops `doc_chunk_vectors` + `embedding_meta` + `doc_chunks` and re-runs the docs indexing pass with the currently configured embedder. `documents` and `code_files` are preserved. The graph pass isn't re-run (it doesn't embed) — run `librarian index --skip-docs` after `reindex` if you also want to refresh the graph.

Known limitation: if two runs use the same model name against different OpenAI-compatible endpoints (e.g. LM Studio vs. Ollama serving different underlying weights under the same `model:` name), the mismatch can't be detected — the model identifier is all we have.

## Pipeline flow

**Indexing**: each chunk's `EmbeddingText` (context header + content + signal line — see [Indexing Pipeline](indexing.md#stage-4-chunk)) passes to `Embed()`. The returned `[]float64` is converted to float32 little-endian bytes at the store boundary and inserted into `doc_chunk_vectors` alongside the chunk metadata.

**Search**: `librarian search` / `search_docs` / `get_context` all embed the user's query string once, over-fetch `limit × 3` candidates from sqlite-vec, then re-rank with signal metadata. See [Storage Layer](storage.md#search-re-ranking).

## Cost considerations

One embedding request per chunk at index time. A small docs directory (~50 files, ~200 chunks) is trivially cheap on any provider. A large monorepo with code + docs + PDFs can run into hundreds of thousands of chunks — at that scale a local embedder (LM Studio, Ollama) becomes attractive for iteration speed and cost. Content-hash-based incremental indexing means only changed files re-embed on subsequent runs.
