package mcpserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/config"
	"librarian/internal/embedding"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/markdown" // register markdown handler for single-file re-index
	"librarian/internal/store"
)

func registerUpdateDocs(s *server.MCPServer, st *store.Store, cfg *config.Config, embedder embedding.Embedder) {
	tool := mcp.NewTool("update_docs",
		mcp.WithDescription("Write or update a documentation file and re-index it. Only allows writes within the configured docs directory."),
		mcp.WithString("file_path",
			mcp.Required(),
			mcp.Description("File path relative to project root (e.g., 'docs/auth.md')"),
		),
		mcp.WithString("content",
			mcp.Required(),
			mcp.Description("Full markdown content to write to the file"),
		),
		mcp.WithString("reindex",
			mcp.Description("Reindex scope: 'file' (default) to re-index only this file, or 'full' to re-index entire docs directory"),
			mcp.DefaultString("file"),
			mcp.Enum("file", "full"),
		),
	)

	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		filePath, err := req.RequireString("file_path")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		content, err := req.RequireString("content")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		reindexScope := req.GetString("reindex", "file")

		// Safety: ensure path is within docs directory
		absDocsDir, err := filepath.Abs(cfg.DocsDir)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to resolve docs directory: %v", err)), nil
		}

		absFilePath, err := filepath.Abs(filePath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("invalid file path: %v", err)), nil
		}

		if !strings.HasPrefix(absFilePath, absDocsDir+string(filepath.Separator)) && absFilePath != absDocsDir {
			return mcp.NewToolResultError("file path must be within the configured docs directory (" + cfg.DocsDir + ")"), nil
		}

		// Create parent directories if needed
		if err := os.MkdirAll(filepath.Dir(absFilePath), 0755); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to create directories: %v", err)), nil
		}

		// Write the file
		if err := os.WriteFile(absFilePath, []byte(content), 0644); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to write file: %v", err)), nil
		}

		// Re-index
		idx := indexer.New(st, cfg, embedder)
		var result *indexer.IndexResult

		if reindexScope == "full" {
			result, err = idx.IndexDirectory(cfg.DocsDir, true)
		} else {
			result, err = idx.IndexSingleFile(filePath, absFilePath, true)
		}
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("re-indexing failed: %v", err)), nil
		}

		output := fmt.Sprintf("Updated %s\n\n", filePath)
		output += fmt.Sprintf("Re-indexed (%s):\n", reindexScope)
		output += fmt.Sprintf("  Documents: %d\n", result.DocumentsIndexed)
		output += fmt.Sprintf("  Chunks:    %d\n", result.ChunksCreated)
		output += fmt.Sprintf("  Code refs: %d\n", result.CodeFilesFound)
		if len(result.Errors) > 0 {
			output += fmt.Sprintf("  Errors:    %d\n", len(result.Errors))
			for _, e := range result.Errors {
				output += fmt.Sprintf("    - %s\n", e)
			}
		}

		return mcp.NewToolResultText(output), nil
	})
}
