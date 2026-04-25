package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"librarian/internal/embedding"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults" // register all built-in handlers
	"librarian/internal/indexer/handlers/pdf"
	"librarian/internal/store"
)

var (
	reindexRebuildVectors bool
	reindexJSON           bool
)

var reindexCmd = &cobra.Command{
	Use:   "reindex",
	Short: "Drop vector state and re-embed every document (for recovering from embedding model changes)",
	Long: `Drops doc_chunk_vectors, doc_chunks, and embedding_meta, then re-runs the
docs indexing pass with force=true so every chunk gets re-embedded with the
currently configured model. Existing 'documents' and 'code_files' rows are
preserved — only the chunk-level and vector-level state is rebuilt.

This is the supported recovery path when 'librarian index' refuses to run
with "embedding model/dimension mismatch" after you change embedding.model
or embedding.provider in .librarian/config.yaml.

Note that this re-embeds every chunk, which is costly on paid providers. It
does NOT run the code-graph pass — run 'librarian index --skip-docs' after
reindex if the graph also needs a refresh (the graph pass doesn't embed and
is unaffected by model changes).

The --rebuild-vectors flag is required today. It exists so future reindex
modes (graph-only, selective file) have room to add their own flags without
changing this command's default behaviour.`,
	Example: `  librarian reindex --rebuild-vectors
  librarian reindex --rebuild-vectors --json`,
	RunE: runReindex,
}

func init() {
	reindexCmd.Flags().BoolVar(&reindexRebuildVectors, "rebuild-vectors", false, "Drop the vector table and re-embed every chunk (required today)")
	reindexCmd.Flags().BoolVar(&reindexJSON, "json", false, "Output results as JSON")
	rootCmd.AddCommand(reindexCmd)
}

func runReindex(cmd *cobra.Command, args []string) error {
	if !reindexRebuildVectors {
		return fmt.Errorf("specify --rebuild-vectors to confirm a full vector rebuild (this will re-embed every chunk)")
	}

	if cfg.DocsDir == "" {
		return fmt.Errorf("docs_dir is not configured — set it in .librarian/config.yaml before running reindex")
	}

	absDir, err := filepath.Abs(cfg.DocsDir)
	if err != nil {
		return fmt.Errorf("resolving docs directory: %w", err)
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

	// Same PDF runtime shutdown hygiene as cmd/index.go.
	defer pdf.Shutdown()

	if !reindexJSON {
		fmt.Printf("Dropping vector state (doc_chunk_vectors, doc_chunks, embedding_meta)...\n")
	}
	if err := s.ClearVectorState(); err != nil {
		return fmt.Errorf("clearing vector state: %w", err)
	}

	idx := indexer.New(s, cfg, embedder)
	if reindexJSON {
		idx.SetProgressOverride("silent")
	}

	if !reindexJSON {
		fmt.Printf("Re-embedding documents from %s with %s...\n", absDir, embedder.Model())
	}
	docsRes, err := idx.IndexDirectory(cfg.DocsDir, true)
	if err != nil {
		return fmt.Errorf("docs pass: %w", err)
	}

	if reindexJSON {
		out, _ := json.MarshalIndent(map[string]any{
			"model": embedder.Model(),
			"docs":  docsRes,
		}, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("\nReindex complete:\n")
	fmt.Printf("  Model:             %s\n", embedder.Model())
	fmt.Printf("  Documents indexed: %d\n", docsRes.DocumentsIndexed)
	fmt.Printf("  Chunks created:    %d\n", docsRes.ChunksCreated)
	if len(docsRes.Errors) > 0 {
		fmt.Printf("  Errors:            %d\n", len(docsRes.Errors))
		for _, e := range docsRes.Errors {
			fmt.Printf("    - %s\n", e)
		}
	}
	fmt.Printf("\nRun 'librarian index --skip-docs' separately if the code graph also needs refreshing.\n")
	return nil
}
