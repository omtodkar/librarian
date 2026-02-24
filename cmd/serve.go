package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"librarian/internal/embedding"
	"librarian/internal/mcpserver"
	"librarian/internal/store"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start MCP stdio server",
	RunE:  runServe,
}

func init() {
	rootCmd.AddCommand(serveCmd)
}

func runServe(cmd *cobra.Command, args []string) error {
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
