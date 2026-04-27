package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/store"
)

func registerExpandChunks(s *server.MCPServer, st *store.Store) {
	tool := mcp.NewTool("expand_chunks",
		mcp.WithDescription("Retrieve full content bodies for specific chunk IDs returned by search_docs. Use this after an initial search_docs call (which returns summaries by default) to read the full text of chunks that look relevant."),
		mcp.WithArray("ids",
			mcp.Required(),
			mcp.Description("List of chunk IDs to expand (from the **ID:** field in search_docs results)"),
			mcp.WithStringItems(),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ids, err := req.RequireStringSlice("ids")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		if len(ids) == 0 {
			return mcp.NewToolResultText("No IDs provided."), nil
		}

		chunks, err := st.GetChunksByIDs(ids)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("fetching chunks: %v", err)), nil
		}
		if len(chunks) == 0 {
			return mcp.NewToolResultText("No chunks found for the provided IDs."), nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Expanded %d chunk(s):\n\n", len(chunks)))
		for i, chunk := range chunks {
			sb.WriteString(fmt.Sprintf("### Chunk %d\n", i+1))
			sb.WriteString(formatChunkResult(chunk, true))
		}
		return mcp.NewToolResultText(sb.String()), nil
	})
}
