package mcpserver

import (
	"fmt"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/config"
	"librarian/internal/embedding"
	helixclient "librarian/internal/helix"
)

func Serve(client *helixclient.Client, cfg *config.Config, embedder embedding.Embedder) error {
	s := server.NewMCPServer(
		"librarian",
		"0.1.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
		server.WithInstructions("Librarian provides semantic search across project documentation. Use search_docs for quick searches, get_context for comprehensive briefings with related code files and documents, get_document to read full documents, list_documents to browse the index, and update_docs to write and re-index documentation."),
	)

	registerSearchDocs(s, client, embedder)
	registerGetDocument(s, client, cfg)
	registerGetContext(s, client, embedder)
	registerListDocuments(s, client)
	registerUpdateDocs(s, client, cfg, embedder)

	if err := server.ServeStdio(s,
		server.WithErrorLogger(log.New(os.Stderr, "[librarian] ", log.LstdFlags)),
	); err != nil {
		return fmt.Errorf("MCP server error: %w", err)
	}

	return nil
}
