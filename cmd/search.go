package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"librarian/internal/embedding"
	helixclient "librarian/internal/helix"
)

var (
	searchLimit int
	searchJSON  bool
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
	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")

	embedder, err := embedding.NewGeminiEmbedder(cfg.Embedding.APIKey)
	if err != nil {
		return fmt.Errorf("creating embedder: %w", err)
	}

	vector, err := embedder.Embed(query)
	if err != nil {
		return fmt.Errorf("embedding query: %w", err)
	}

	client := helixclient.NewClient(cfg.HelixHost)

	chunks, err := client.SearchChunks(vector, searchLimit)
	if err != nil {
		return fmt.Errorf("searching: %w", err)
	}

	if searchJSON {
		out, _ := json.MarshalIndent(chunks, "", "  ")
		fmt.Println(string(out))
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
		fmt.Printf("Content:\n%s\n\n", truncate(chunk.Content, 500))
	}

	return nil
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
