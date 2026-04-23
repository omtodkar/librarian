package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"librarian/internal/embedding"
	"librarian/internal/mcpserver"
	"librarian/internal/store"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP server commands",
	Long: `Commands for running Librarian's optional MCP server.

The primary Librarian UX is skill + CLI (see 'librarian search', 'librarian context',
etc.). The MCP server is opt-in for callers that prefer structured tool access —
programmatic integrations, custom tooling, or assistants that route via MCP.`,
}

var mcpServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start MCP stdio server",
	RunE:  runMCPServe,
}

func init() {
	mcpCmd.AddCommand(mcpServeCmd)
	rootCmd.AddCommand(mcpCmd)
}

func runMCPServe(cmd *cobra.Command, args []string) error {
	embedder, err := embedding.NewEmbedder(cfg.Embedding)
	if err != nil {
		return fmt.Errorf("creating embedder: %w", err)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	return mcpserver.Serve(s, cfg, embedder)
}
