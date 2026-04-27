package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/embedding"
	"librarian/internal/store"
)

func registerSearchDocs(s *server.MCPServer, st *store.Store, embedder embedding.Embedder, hybridSearch bool) {
	tool := mcp.NewTool("search_docs",
		mcp.WithDescription("Semantic search across all indexed documentation. Returns relevant chunks with file paths and section context."),
		mcp.WithString("query",
			mcp.Required(),
			mcp.Description("Natural language search query"),
		),
		mcp.WithNumber("limit",
			mcp.Description("Maximum number of results to return"),
			mcp.DefaultNumber(5),
			mcp.Min(1),
			mcp.Max(20),
		),
		mcp.WithBoolean("include_refs",
			mcp.Description("Include referenced code files for each result"),
		),
		mcp.WithBoolean("include_body",
			mcp.Description("Return full chunk content instead of the 1-2 line summary. Default false — summaries reduce token cost; use expand_chunks or set include_body=true to retrieve full bodies."),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := req.GetInt("limit", 5)
		includeRefs := req.GetBool("include_refs", false)
		includeBody := req.GetBool("include_body", false)

		vector, err := embedder.Embed(query)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("embedding query: %v", err)), nil
		}

		var chunks []store.DocChunk
		if hybridSearch {
			chunks, err = st.HybridSearch(vector, query, limit)
		} else {
			chunks, err = st.SearchChunks(vector, limit)
		}
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}

		if len(chunks) == 0 {
			return mcp.NewToolResultText("No results found for query: " + query), nil
		}

		var refs map[string][]string
		if includeRefs {
			paths := make([]string, 0, len(chunks))
			for _, c := range chunks {
				paths = append(paths, c.FilePath)
			}
			refs, _ = st.GetReferencedPathsForDocPaths(paths)
		}

		var output string
		output = fmt.Sprintf("Found %d results for %q:\n\n", len(chunks), query)

		for i, chunk := range chunks {
			output += fmt.Sprintf("### Result %d\n", i+1)
			output += formatChunkResult(chunk, includeBody)

			if includeRefs {
				paths, ok := refs[chunk.FilePath]
				if !ok || len(paths) == 0 {
					continue
				}
				output += "**Refs:** " + strings.Join(paths, ", ") + "\n\n"
			}
		}

		return mcp.NewToolResultText(output), nil
	})
}

// formatChunkResult renders the per-chunk block for search_docs output.
// When includeBody is false and a summary exists, the summary is shown
// instead of the full content body. Callers that always want the full body
// (expand_chunks, get_context) pass includeBody=true.
func formatChunkResult(chunk store.DocChunk, includeBody bool) string {
	if !includeBody && chunk.Summary != "" {
		return fmt.Sprintf("**File:** %s\n**Section:** %s\n**ID:** %s\n**Summary:**\n%s\n\n",
			chunk.FilePath, chunk.SectionHeading, chunk.ID, chunk.Summary)
	}
	return fmt.Sprintf("**File:** %s\n**Section:** %s\n**ID:** %s\n**Content:**\n%s\n\n",
		chunk.FilePath, chunk.SectionHeading, chunk.ID, chunk.Content)
}
