package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"librarian/internal/embedding"
	helixclient "librarian/internal/helix"
	"librarian/internal/mcpserver"
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
	embedder, err := embedding.NewGeminiEmbedder(cfg.Embedding.APIKey)
	if err != nil {
		return fmt.Errorf("creating embedder: %w", err)
	}

	client := helixclient.NewClient(cfg.HelixHost)
	return mcpserver.Serve(client, cfg, embedder)
}
