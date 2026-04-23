package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"librarian/internal/store"
)

var docJSON bool

var docCmd = &cobra.Command{
	Use:   "doc <file-path>",
	Short: "Print a document's full content with index metadata",
	Args:  cobra.ExactArgs(1),
	RunE:  runDoc,
}

func init() {
	docCmd.Flags().BoolVar(&docJSON, "json", false, "Output as JSON")
	rootCmd.AddCommand(docCmd)
}

func runDoc(cmd *cobra.Command, args []string) error {
	filePath := args[0]

	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	content, err := os.ReadFile(absPath)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	// Best-effort index metadata lookup; a missing entry is not an error.
	doc, _ := s.GetDocumentByPath(filePath)

	if docJSON {
		out := map[string]any{
			"file_path": filePath,
			"content":   string(content),
		}
		if doc != nil {
			out["title"] = doc.Title
			out["doc_type"] = doc.DocType
			out["chunk_count"] = doc.ChunkCount
			out["indexed"] = true
		} else {
			out["indexed"] = false
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	if doc != nil {
		fmt.Printf("# %s\n", doc.Title)
		fmt.Printf("**Type:** %s | **Chunks:** %d\n\n", doc.DocType, doc.ChunkCount)
	} else {
		fmt.Printf("# %s\n\n", filePath)
	}
	fmt.Print(string(content))
	return nil
}
