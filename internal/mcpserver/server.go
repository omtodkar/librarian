package mcpserver

import (
	"fmt"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/config"
	"librarian/internal/embedding"
	"librarian/internal/store"
)

func Serve(s *store.Store, cfg *config.Config, embedder embedding.Embedder) error {
	srv := server.NewMCPServer(
		"librarian",
		"0.1.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
		server.WithInstructions("Librarian provides semantic search across project documentation. Use search_docs for quick searches, get_context for comprehensive briefings with related code files and documents, get_document to read full documents, list_documents to browse the index, update_docs to write and re-index documentation, and trace_rpc for end-to-end gRPC understanding (proto declaration + every language's implementation + input/output messages + sibling rpcs in one call)."),
	)

	registerSearchDocs(srv, s, embedder)
	registerGetDocument(srv, s, cfg)
	registerGetContext(srv, s, embedder)
	registerListDocuments(srv, s)
	registerUpdateDocs(srv, s, cfg, embedder)
	registerTraceRPC(srv, s, cfg)

	if err := server.ServeStdio(srv,
		server.WithErrorLogger(log.New(os.Stderr, "[librarian] ", log.LstdFlags)),
	); err != nil {
		return fmt.Errorf("MCP server error: %w", err)
	}

	return nil
}
