package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"librarian/internal/store"
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
	s, err := store.Open(cfg.DBPath, nil, 0)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	docs, err := s.ListDocuments()
	if err != nil {
		return fmt.Errorf("listing documents: %w", err)
	}

	var totalChunks uint32
	for _, doc := range docs {
		totalChunks += doc.ChunkCount
	}

	if statusJSON {
		out, _ := json.MarshalIndent(map[string]any{
			"db_path":        cfg.DBPath,
			"document_count": len(docs),
			"chunk_count":    totalChunks,
			"documents":      docs,
		}, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("Librarian Index Status\n")
	fmt.Printf("  Database:  %s\n", cfg.DBPath)
	fmt.Printf("  Documents: %d\n", len(docs))
	fmt.Printf("  Chunks:    %d\n", totalChunks)

	if len(docs) > 0 {
		fmt.Printf("\nDocuments:\n")
		for _, doc := range docs {
			fmt.Printf("  %-40s %-14s %d chunks  %q\n", doc.FilePath, "("+doc.DocType+")", doc.ChunkCount, doc.Title)
		}
	}

	return nil
}
