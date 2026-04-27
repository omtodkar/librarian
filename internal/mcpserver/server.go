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
		"0.2.0",
		server.WithToolCapabilities(false),
		server.WithRecovery(),
		server.WithInstructions("librarian 0.2.0. Provides semantic search across project documentation. Tools: search_docs (quick search), get_context (deep briefing + graph traversal), get_document (full file content), list_documents (enumerate index), update_docs (write + re-index), trace_rpc (gRPC end-to-end trace). Stable API: parameter names locked until v2 — see docs/mcp-tools.md for stability classifications."),
	)

	registerSearchDocs(srv, s, embedder, cfg.Search.HybridSearch)
	registerExpandChunks(srv, s)
	registerGetDocument(srv, s, cfg)
	registerGetContext(srv, s, embedder, cfg.Search.HybridSearch)
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
