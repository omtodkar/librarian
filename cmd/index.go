package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"librarian/internal/embedding"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/markdown" // register markdown handler
	"librarian/internal/store"
)

var (
	indexForce  bool
	indexDryRun bool
	indexJSON   bool
)

var indexCmd = &cobra.Command{
	Use:   "index [docs-dir]",
	Short: "Parse and index documentation",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runIndex,
}

func init() {
	indexCmd.Flags().BoolVar(&indexForce, "force", false, "Force re-index all documents, ignoring content hashes")
	indexCmd.Flags().BoolVar(&indexDryRun, "dry-run", false, "Show what would be indexed without making changes")
	indexCmd.Flags().BoolVar(&indexJSON, "json", false, "Output results as JSON")
	rootCmd.AddCommand(indexCmd)
}

func runIndex(cmd *cobra.Command, args []string) error {
	docsDir := cfg.DocsDir
	if len(args) > 0 {
		docsDir = args[0]
	}

	absDir, err := filepath.Abs(docsDir)
	if err != nil {
		return fmt.Errorf("resolving docs directory: %w", err)
	}

	if indexDryRun {
		return runDryIndex(docsDir, absDir)
	}

	embedder, err := embedding.NewEmbedder(cfg.Embedding)
	if err != nil {
		return fmt.Errorf("creating embedder: %w", err)
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	idx := indexer.New(s, cfg, embedder)

	if !indexJSON {
		fmt.Printf("Indexing documents from %s...\n", absDir)
	}

	result, err := idx.IndexDirectory(docsDir, indexForce)
	if err != nil {
		return fmt.Errorf("indexing: %w", err)
	}

	if indexJSON {
		out, _ := json.MarshalIndent(result, "", "  ")
		fmt.Println(string(out))
	} else {
		fmt.Printf("\nIndexing complete:\n")
		fmt.Printf("  Documents indexed: %d\n", result.DocumentsIndexed)
		fmt.Printf("  Chunks created:    %d\n", result.ChunksCreated)
		fmt.Printf("  Code files found:  %d\n", result.CodeFilesFound)
		fmt.Printf("  Skipped:           %d\n", result.Skipped)
		if len(result.Errors) > 0 {
			fmt.Printf("  Errors:            %d\n", len(result.Errors))
			for _, e := range result.Errors {
				fmt.Printf("    - %s\n", e)
			}
		}
	}

	return nil
}

func runDryIndex(docsDir, absDir string) error {
	files, err := indexer.WalkDocs(docsDir, cfg.ExcludePatterns, indexer.DefaultRegistry())
	if err != nil {
		return fmt.Errorf("walking docs directory: %w", err)
	}

	if indexJSON {
		out, _ := json.MarshalIndent(map[string]any{
			"docs_dir":    absDir,
			"files_found": len(files),
			"files":       files,
		}, "", "  ")
		fmt.Println(string(out))
	} else {
		fmt.Printf("Dry run - would index %d files from %s:\n", len(files), absDir)
		for _, f := range files {
			fmt.Printf("  %s\n", f.FilePath)
		}
	}

	return nil
}
