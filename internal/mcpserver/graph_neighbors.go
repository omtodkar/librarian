package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/store"
)

func registerGraphNeighbors(s *server.MCPServer, st *store.Store) {
	tool := mcp.NewTool("graph_neighbors",
		mcp.WithDescription("Return immediate graph neighbors of a node. Use node IDs from search_docs results (e.g. \"sym:com.acme.AuthService\", \"file:internal/auth/service.go\", \"doc:abc-123\")."),
		mcp.WithString("node_id",
			mcp.Required(),
			mcp.Description("Exact graph node ID"),
		),
		mcp.WithString("direction",
			mcp.Description("Edge direction: \"out\" (outgoing), \"in\" (incoming), or \"both\" (default)"),
			mcp.DefaultString("both"),
		),
		mcp.WithArray("edge_kinds",
			mcp.Description("Filter to specific edge kinds (e.g. [\"inherits\", \"contains\"]). Omit or pass [] to return all kinds."),
			mcp.WithStringItems(),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		nodeID, err := req.RequireString("node_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		direction := req.GetString("direction", "both")
		if direction != "in" && direction != "out" && direction != "both" {
			return mcp.NewToolResultError("direction must be \"in\", \"out\", or \"both\""), nil
		}
		dir := direction
		if dir == "both" {
			dir = ""
		}

		kinds := req.GetStringSlice("edge_kinds", nil)
		edges, err := st.Neighbors(nodeID, dir, kinds...)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("neighbors: %v", err)), nil
		}

		// Normalize nil to empty slice for consistent JSON output.
		if edges == nil {
			edges = []store.Edge{}
		}

		out := map[string]any{
			"node_id":   nodeID,
			"direction": direction,
			"edges":     edges,
		}
		if len(kinds) > 0 {
			out["edge_kinds"] = kinds
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	})
}
