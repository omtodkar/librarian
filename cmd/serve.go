package cmd

import (
	"github.com/spf13/cobra"

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
	client := helixclient.NewClient(cfg.HelixHost)
	return mcpserver.Serve(client, cfg)
}
