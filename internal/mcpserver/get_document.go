package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/config"
	helixclient "librarian/internal/helix"
)

func registerGetDocument(s *server.MCPServer, client *helixclient.Client, cfg *config.Config) {
	tool := mcp.NewTool("get_document",
		mcp.WithDescription("Get the full content of a document by its file path."),
		mcp.WithString("file_path",
			mcp.Required(),
			mcp.Description("File path of the document (e.g., 'docs/auth.md')"),
		),
		mcp.WithReadOnlyHintAnnotation(true),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		filePath, err := req.RequireString("file_path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		absPath, err := filepath.Abs(filePath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid path: %v", err)), nil
		}

		content, err := os.ReadFile(absPath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to read file: %v", err)), nil
		}

		// Get metadata from HelixDB
		doc, err := client.GetDocumentByPath(filePath)
		if err != nil {
			return mcp.NewToolResultText(fmt.Sprintf("# %s\n\n%s", filePath, string(content))), nil
		}

		output := fmt.Sprintf("# %s\n", doc.Title)
		output += fmt.Sprintf("**Type:** %s | **Chunks:** %d\n\n", doc.DocType, doc.ChunkCount)
		output += string(content)

		return mcp.NewToolResultText(output), nil
	})
}
