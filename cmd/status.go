package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	helixclient "librarian/internal/helix"
)

var statusJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show index statistics",
	RunE:  runStatus,
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "Output as JSON")
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	client := helixclient.NewClient(cfg.HelixHost)

	docs, err := client.ListDocuments()
	if err != nil {
		return fmt.Errorf("listing documents: %w", err)
	}

	var totalChunks uint32
	for _, doc := range docs {
		totalChunks += doc.ChunkCount
	}

	if statusJSON {
		out, _ := json.MarshalIndent(map[string]any{
			"helix_host":     cfg.HelixHost,
			"document_count": len(docs),
			"chunk_count":    totalChunks,
			"documents":      docs,
		}, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("Librarian Index Status\n")
	fmt.Printf("  Documents: %d\n", len(docs))
	fmt.Printf("  Chunks:    %d\n", totalChunks)
	fmt.Printf("  HelixDB:   %s\n", cfg.HelixHost)

	if len(docs) > 0 {
		fmt.Printf("\nDocuments:\n")
		for _, doc := range docs {
			fmt.Printf("  %-40s %-14s %d chunks  %q\n", doc.FilePath, "("+doc.DocType+")", doc.ChunkCount, doc.Title)
		}
	}

	return nil
}
