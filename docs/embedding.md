---
title: Embedding
type: reference
description: How the embedding system works, covering the Embedder interface, the Gemini and OpenAI-compatible providers, and vector dimensions.
---

# Embedding

The embedding system (`internal/embedding/`) generates vector embeddings from text. These embeddings power Librarian's semantic search — similar text produces similar vectors, enabling retrieval by meaning rather than keywords.

## Embedder Interface

```go
type Embedder interface {
    Embed(text string) ([]float64, error)
}
```

The `Embedder` interface has a single method. It takes a text string and returns a float64 vector. This interface is used by the indexer (to embed chunks during indexing) and the MCP server (to embed search queries).

## Provider Factory

`embedding.NewEmbedder(cfg)` in `internal/embedding/provider.go` creates the appropriate embedder based on `cfg.Provider`:

| Provider | Embedder | Description |
|----------|----------|-------------|
| `"gemini"` | `GeminiEmbedder` | Google's Gemini embedding API |
| `"openai"` | `OpenAIEmbedder` | Any OpenAI-compatible API (LM Studio, Ollama, vLLM, etc.) |

Unknown providers return an error listing the supported options.

## GeminiEmbedder

The `GeminiEmbedder` in `internal/embedding/gemini.go` calls Google's Gemini embedding API.

### Configuration

`NewGeminiEmbedder(apiKey)` creates an embedder. The API key is resolved in this order:
1. The `apiKey` argument (from `embedding.api_key` in `.librarian.yaml`)
2. The `GEMINI_API_KEY` environment variable

If neither is set, initialization fails with an error.

### API Details

| Property | Value |
|----------|-------|
| Endpoint | `https://generativelanguage.googleapis.com/v1beta/models/gemini-embedding-001:embedContent` |
| Model | `gemini-embedding-001` |
| Dimensions | 3072 |
| Input | Single text string per request |
| Output | `[]float64` vector |
| Auth | API key as query parameter |

### Request Format

```json
{
  "content": {
    "parts": [{"text": "the text to embed"}]
  }
}
```

## OpenAIEmbedder

The `OpenAIEmbedder` in `internal/embedding/openai.go` calls any OpenAI-compatible `/v1/embeddings` endpoint. This works with LM Studio, Ollama, vLLM, and any other server that implements the OpenAI embeddings API.

### Configuration

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `embedding.base_url` | No | `http://localhost:1234/v1` | Base URL of the API (LM Studio's default) |
| `embedding.model` | Yes | — | Model identifier (e.g., `text-embedding-nomic-embed-text-v1.5`) |
| `embedding.api_key` | No | — | API key, sent as `Authorization: Bearer` header if provided |

### API Details

| Property | Value |
|----------|-------|
| Endpoint | `{base_url}/embeddings` |
| Dimensions | Determined by the model (e.g., 768 for nomic-embed-text) |
| Input | Single text string per request |
| Output | `[]float64` vector |
| Auth | Bearer token header (optional) |

### Request Format

```json
{
  "model": "text-embedding-nomic-embed-text-v1.5",
  "input": "the text to embed"
}
```

## Error Handling

Both embedders check for:
- HTTP status codes other than 200
- API-level errors in the response JSON
- Empty embedding arrays in the response

All errors are wrapped with context for debugging.

## Vector Dimensions

Vector dimensions are determined automatically by the embedding model. The sqlite-vec virtual table (`doc_chunk_vectors`) is created lazily on the first vector insert, sized to match the actual model output. There is no dimensions config field — switching models means re-indexing (delete the database and run `librarian index` again).

## Usage in the Pipeline

During indexing, each chunk's `EmbeddingText` (which includes the context header, content, and signal line) is passed to `Embed()`. The returned `[]float64` vector is then converted to `[]byte` of float32 values by the store layer before insertion into the sqlite-vec virtual table.

During search, the user's query string is passed to `Embed()` and the resulting vector is used for KNN search against stored chunk vectors.
