package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"librarian/internal/embedding"
	"librarian/internal/store"
)

var (
	searchLimit       int
	searchJSON        bool
	searchIncludeRefs bool
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search indexed documentation",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runSearch,
}

func init() {
	searchCmd.Flags().IntVar(&searchLimit, "limit", 5, "Maximum number of results")
	searchCmd.Flags().BoolVar(&searchJSON, "json", false, "Output results as JSON")
	searchCmd.Flags().BoolVar(&searchIncludeRefs, "include-refs", false, "Include referenced code files for each result")
	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")

	embedder, err := embedding.NewEmbedder(cfg.Embedding)
	if err != nil {
		return fmt.Errorf("creating embedder: %w", err)
	}

	vector, err := embedder.Embed(query)
	if err != nil {
		return fmt.Errorf("embedding query: %w", err)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	chunks, err := s.SearchChunks(vector, searchLimit)
	if err != nil {
		return fmt.Errorf("searching: %w", err)
	}

	// Collect referenced code files per chunk file path when requested.
	var refs map[string][]string
	if searchIncludeRefs {
		refs = make(map[string][]string)
		for _, chunk := range chunks {
			if _, already := refs[chunk.FilePath]; already {
				continue
			}
			doc, err := s.GetDocumentByPath(chunk.FilePath)
			if err != nil || doc == nil {
				continue
			}
			codeFiles, err := s.GetReferencedCodeFiles(doc.ID)
			if err != nil {
				continue
			}
			paths := make([]string, 0, len(codeFiles))
			for _, cf := range codeFiles {
				paths = append(paths, cf.FilePath)
			}
			refs[chunk.FilePath] = paths
		}
	}

	if searchJSON {
		if searchIncludeRefs {
			out, _ := json.MarshalIndent(map[string]any{
				"chunks": chunks,
				"refs":   refs,
			}, "", "  ")
			fmt.Println(string(out))
		} else {
			out, _ := json.MarshalIndent(chunks, "", "  ")
			fmt.Println(string(out))
		}
		return nil
	}

	if len(chunks) == 0 {
		fmt.Println("No results found.")
		return nil
	}

	fmt.Printf("Found %d results for %q:\n\n", len(chunks), query)

	for i, chunk := range chunks {
		fmt.Printf("--- Result %d ---\n", i+1)
		fmt.Printf("File:    %s\n", chunk.FilePath)
		fmt.Printf("Section: %s\n", chunk.SectionHeading)
		fmt.Printf("Content:\n%s\n", truncate(chunk.Content, 500))
		if searchIncludeRefs {
			if paths, ok := refs[chunk.FilePath]; ok && len(paths) > 0 {
				fmt.Printf("Refs:    %s\n", strings.Join(paths, ", "))
			}
		}
		fmt.Println()
	}

	return nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
