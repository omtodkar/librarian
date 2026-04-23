package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"librarian/internal/store"
)

var (
	listDocType string
	listJSON    bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List indexed documents",
	RunE:  runList,
}

func init() {
	listCmd.Flags().StringVar(&listDocType, "doc-type", "", "Filter by document type (e.g., 'guide', 'reference', 'architecture')")
	listCmd.Flags().BoolVar(&listJSON, "json", false, "Output as JSON")
	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	docs, err := s.ListDocuments()
	if err != nil {
		return fmt.Errorf("listing documents: %w", err)
	}

	if listDocType != "" {
		var filtered []store.Document
		for _, d := range docs {
			if d.DocType == listDocType {
				filtered = append(filtered, d)
			}
		}
		docs = filtered
	}

	if listJSON {
		b, _ := json.MarshalIndent(docs, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	if len(docs) == 0 {
		if listDocType != "" {
			fmt.Printf("No documents found with type %q.\n", listDocType)
		} else {
			fmt.Println("No documents indexed.")
		}
		return nil
	}

	fmt.Printf("Indexed Documents (%d):\n\n", len(docs))
	fmt.Printf("%-40s %-14s %-8s %s\n", "File", "Type", "Chunks", "Title")
	fmt.Printf("%-40s %-14s %-8s %s\n", "----", "----", "------", "-----")
	for _, d := range docs {
		fmt.Printf("%-40s %-14s %-8d %s\n", d.FilePath, d.DocType, d.ChunkCount, d.Title)
	}
	return nil
}
