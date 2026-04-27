package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"librarian/internal/embedding"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults" // register all built-in handlers
	"librarian/internal/store"
	"librarian/internal/summarizer"
)

var (
	updateContent string
	updateReindex string
	updateJSON    bool
)

var updateCmd = &cobra.Command{
	Use:   "update <file-path>",
	Short: "Write or overwrite a doc and re-index it",
	Long: `Writes content to a file under the configured docs directory, then re-indexes it.
Content comes from --content or stdin. --reindex=file (default) re-indexes only the
updated file; --reindex=full re-indexes the entire docs directory.

Rejects paths that fall outside cfg.DocsDir to prevent accidental writes elsewhere.`,
	Args: cobra.ExactArgs(1),
	RunE: runUpdate,
}

func init() {
	updateCmd.Flags().StringVar(&updateContent, "content", "", "Content to write (default: read from stdin)")
	updateCmd.Flags().StringVar(&updateReindex, "reindex", "file", "Reindex scope: 'file' or 'full'")
	updateCmd.Flags().BoolVar(&updateJSON, "json", false, "Output as JSON")
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	filePath := args[0]

	if updateReindex != "file" && updateReindex != "full" {
		return fmt.Errorf("--reindex must be 'file' or 'full'")
	}

	content := updateContent
	if content == "" {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading stdin: %w", err)
		}
		content = string(b)
	}

	absDocsDir, err := filepath.Abs(cfg.DocsDir)
	if err != nil {
		return fmt.Errorf("resolving docs directory: %w", err)
	}
	absFilePath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolving file path: %w", err)
	}
	if !strings.HasPrefix(absFilePath, absDocsDir+string(filepath.Separator)) && absFilePath != absDocsDir {
		return fmt.Errorf("file path must be within the configured docs directory (%s)", cfg.DocsDir)
	}

	if err := os.MkdirAll(filepath.Dir(absFilePath), 0o755); err != nil {
		return fmt.Errorf("creating directories: %w", err)
	}
	if err := os.WriteFile(absFilePath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing file: %w", err)
	}

	embedder, err := embedding.NewEmbedder(cfg.Embedding)
	if err != nil {
		return fmt.Errorf("creating embedder: %w", err)
	}
	s, err := store.Open(cfg.DBPath, nil, 0)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	sum, err := summarizer.New(cfg.Summarization)
	if err != nil {
		return fmt.Errorf("creating summarizer: %w", err)
	}
	idx := indexer.New(s, cfg, embedder)
	idx.SetSummarizer(sum)
	var result *indexer.IndexResult
	if updateReindex == "full" {
		result, err = idx.IndexDirectory(cfg.DocsDir, true)
	} else {
		result, err = idx.IndexSingleFile(filePath, absFilePath, true)
	}
	if err != nil {
		return fmt.Errorf("re-indexing: %w", err)
	}

	if updateJSON {
		out := map[string]any{
			"file_path":         filePath,
			"reindex_scope":     updateReindex,
			"documents_indexed": result.DocumentsIndexed,
			"chunks_created":    result.ChunksCreated,
			"code_files_found":  result.CodeFilesFound,
			"skipped":           result.Skipped,
			"errors":            result.Errors,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("Updated %s\n\n", filePath)
	fmt.Printf("Re-indexed (%s):\n", updateReindex)
	fmt.Printf("  Documents: %d\n", result.DocumentsIndexed)
	fmt.Printf("  Chunks:    %d\n", result.ChunksCreated)
	fmt.Printf("  Code refs: %d\n", result.CodeFilesFound)
	if len(result.Errors) > 0 {
		fmt.Printf("  Errors:    %d\n", len(result.Errors))
		for _, e := range result.Errors {
			fmt.Printf("    - %s\n", e)
		}
	}
	return nil
}
