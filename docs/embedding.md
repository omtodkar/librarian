---
title: Embedding
type: reference
description: How the embedding system works, covering the Embedder interface, the Gemini implementation, and vector dimensions.
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

### Error Handling

The embedder checks for:
- HTTP status codes other than 200
- API-level errors in the response JSON (`error.message`, `error.code`)
- Empty embedding arrays in the response

All errors are wrapped with context for debugging.

### Usage in the Pipeline

During indexing, each chunk's `EmbeddingText` (which includes the context header, content, and signal line) is passed to `Embed()`. The returned `[]float64` vector is then converted to `[]byte` of float32 values by the store layer before insertion into the sqlite-vec virtual table.

During search, the user's query string is passed to `Embed()` and the resulting vector is used for KNN search against stored chunk vectors.
