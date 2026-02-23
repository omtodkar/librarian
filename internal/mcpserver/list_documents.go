package mcpserver

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	helixclient "librarian/internal/helix"
)

func registerListDocuments(s *server.MCPServer, client *helixclient.Client) {
	tool := mcp.NewTool("list_documents",
		mcp.WithDescription("List all indexed documents with metadata. Optionally filter by document type."),
		mcp.WithString("doc_type",
			mcp.Description("Filter by document type (e.g., 'guide', 'reference', 'architecture')"),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		docType := req.GetString("doc_type", "")

		docs, err := client.ListDocuments()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to list documents: %v", err)), nil
		}

		// Filter by type if specified
		if docType != "" {
			var filtered []helixclient.Document
			for _, doc := range docs {
				if doc.DocType == docType {
					filtered = append(filtered, doc)
				}
			}
			docs = filtered
		}

		if len(docs) == 0 {
			msg := "No documents indexed."
			if docType != "" {
				msg = fmt.Sprintf("No documents found with type %q.", docType)
			}
			return mcp.NewToolResultText(msg), nil
		}

		output := fmt.Sprintf("Indexed Documents (%d):\n\n", len(docs))
		output += fmt.Sprintf("%-40s %-14s %-8s %s\n", "File", "Type", "Chunks", "Title")
		output += fmt.Sprintf("%-40s %-14s %-8s %s\n", "----", "----", "------", "-----")
		for _, doc := range docs {
			output += fmt.Sprintf("%-40s %-14s %-8d %s\n", doc.FilePath, doc.DocType, doc.ChunkCount, doc.Title)
		}

		return mcp.NewToolResultText(output), nil
	})
}
