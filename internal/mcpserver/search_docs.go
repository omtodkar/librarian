package mcpserver

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/embedding"
	helixclient "librarian/internal/helix"
)

func registerSearchDocs(s *server.MCPServer, client *helixclient.Client, embedder embedding.Embedder) {
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
		mcp.WithReadOnlyHintAnnotation(true),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := req.GetInt("limit", 5)

		vector, err := embedder.Embed(query)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("embedding query: %v", err)), nil
		}

		chunks, err := client.SearchChunks(vector, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("search failed: %v", err)), nil
		}

		if len(chunks) == 0 {
			return mcp.NewToolResultText("No results found for query: " + query), nil
		}

		var output string
		output = fmt.Sprintf("Found %d results for %q:\n\n", len(chunks), query)

		for i, chunk := range chunks {
			output += fmt.Sprintf("### Result %d\n", i+1)
			output += fmt.Sprintf("**File:** %s\n", chunk.FilePath)
			output += fmt.Sprintf("**Section:** %s\n", chunk.SectionHeading)
			output += fmt.Sprintf("**Content:**\n%s\n\n", chunk.Content)
		}

		return mcp.NewToolResultText(output), nil
	})
}
