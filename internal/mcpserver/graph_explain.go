package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/store"
)

func registerGraphExplain(s *server.MCPServer, st *store.Store) {
	tool := mcp.NewTool("graph_explain",
		mcp.WithDescription("Summarize a graph node and its immediate connections, grouped by edge kind. Useful for a quick \"what is this thing and what touches it?\" read."),
		mcp.WithString("node_id",
			mcp.Required(),
			mcp.Description("Exact graph node ID to explain"),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		nodeID, err := req.RequireString("node_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Exact ID lookup (vs CLI's fuzzy resolveNode): in MCP sessions node IDs
		// are machine-surfaced via search_docs, so substring matching adds no value.
		node, err := st.GetNode(nodeID)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("get_node: %v", err)), nil
		}
		if node == nil {
			return mcp.NewToolResultError(fmt.Sprintf("node not found: %s", nodeID)), nil
		}

		edges, err := st.Neighbors(nodeID, "")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("neighbors: %v", err)), nil
		}

		grouped := make(map[string][]string)
		for _, e := range edges {
			other := e.To
			if e.To == nodeID {
				other = e.From
			}
			grouped[e.Kind] = append(grouped[e.Kind], other)
		}

		out := map[string]any{
			"node":              node,
			"edge_count":        len(edges),
			"neighbors_by_kind": grouped,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return mcp.NewToolResultText(string(b)), nil
	})
}
