package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/store"
)

func registerGraphPath(s *server.MCPServer, st *store.Store) {
	tool := mcp.NewTool("graph_path",
		mcp.WithDescription("Find the shortest directed path between two graph nodes via BFS. Returns empty steps if no path exists within max_depth hops."),
		// from_id/to_id chosen over bare from/to for clarity; locked as canonical by schema_stability_test.
		mcp.WithString("from_id",
			mcp.Required(),
			mcp.Description("Starting graph node ID"),
		),
		mcp.WithString("to_id",
			mcp.Required(),
			mcp.Description("Destination graph node ID"),
		),
		mcp.WithNumber("max_depth",
			mcp.Description("Maximum hops to search (default 6)"),
			mcp.DefaultNumber(6),
			mcp.Min(1),
			mcp.Max(20),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fromID, err := req.RequireString("from_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		toID, err := req.RequireString("to_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		maxDepth := req.GetInt("max_depth", 6)

		steps, err := st.ShortestPath(fromID, toID, maxDepth)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("shortest_path: %v", err)), nil
		}

		// Normalize nil to empty slice for consistent JSON output.
		if steps == nil {
			steps = []store.PathStep{}
		}

		out := map[string]any{
			"from_id":   fromID,
			"to_id":     toID,
			"max_depth": maxDepth,
			"hops":      len(steps),
			"steps":     steps, // "steps" chosen over spec's "path" for precision; locked by schema_stability_test
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	})
}
