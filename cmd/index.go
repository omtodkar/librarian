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
	indexForce     bool
	indexDryRun    bool
	indexJSON      bool
	indexSkipDocs  bool
	indexSkipGraph bool
	indexWorkers   int
	indexQuiet     bool
	indexVerbose   bool
)

var indexCmd = &cobra.Command{
	Use:   "index [docs-dir]",
	Short: "Parse and index documentation and code graph",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runIndex,
}

func init() {
	indexCmd.Flags().BoolVar(&indexForce, "force", false, "Force re-index all documents, ignoring content hashes")
	indexCmd.Flags().BoolVar(&indexDryRun, "dry-run", false, "Show what would be indexed without making changes")
	indexCmd.Flags().BoolVar(&indexJSON, "json", false, "Output results as JSON")
	indexCmd.Flags().BoolVar(&indexSkipDocs, "skip-docs", false, "Skip the documentation indexing pass")
	indexCmd.Flags().BoolVar(&indexSkipGraph, "skip-graph", false, "Skip the code graph indexing pass")
	indexCmd.Flags().IntVar(&indexWorkers, "workers", -1, "Graph-pass worker count. -1 (default) = respect config/auto; 0 = auto; 1 = serial; N>1 = fixed pool")
	indexCmd.Flags().BoolVar(&indexQuiet, "quiet", false, "Force quiet progress mode (heartbeat every 100 files, no per-file output)")
	indexCmd.Flags().BoolVar(&indexVerbose, "verbose", false, "Force verbose progress mode (one line per file)")
	rootCmd.AddCommand(indexCmd)
}

func runIndex(cmd *cobra.Command, args []string) error {
	if indexSkipDocs && indexSkipGraph {
		return fmt.Errorf("--skip-docs and --skip-graph cannot both be set")
	}
	if indexSkipDocs && len(args) > 0 {
		return fmt.Errorf("[docs-dir] argument is incompatible with --skip-docs")
	}
	if indexQuiet && indexVerbose {
		return fmt.Errorf("--quiet and --verbose cannot both be set")
	}
	if indexWorkers < -1 {
		return fmt.Errorf("--workers must be -1 (respect config), 0 (auto), or a positive integer; got %d", indexWorkers)
	}

	// CLI flags override config for this run. --workers=-1 (default) leaves
	// the config value in place; anything >= 0 overrides. MaxWorkers stays
	// in cfg because it's a static pre-run decision; ProgressMode is set
	// via SetProgressOverride on the Indexer so per-run flags don't mutate
	// the shared *config.Config.
	if indexWorkers >= 0 {
		cfg.Graph.MaxWorkers = indexWorkers
	}

	docsDir := cfg.DocsDir
	if len(args) > 0 {
		docsDir = args[0]
	}

	// Guard against a blank docs_dir — filepath.Abs("") resolves to CWD,
	// which would silently feed every file in the working directory
	// (source code, binaries, everything) through the doc handlers. The
	// graph pass stays useful via cfg.ProjectRoot even if docs is unset,
	// so we only require docsDir when the docs pass is actually running.
	if !indexSkipDocs && docsDir == "" {
		return fmt.Errorf("docs_dir is not configured — set it in .librarian/config.yaml or pass `librarian index <docs-dir>` (use --skip-docs if you only want the graph pass)")
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

	// PDF handler holds a WebAssembly runtime (~5 MB) that's cheap to
	// reinit but wasteful to leave running after indexing completes.
	defer pdf.Shutdown()

	idx := indexer.New(s, cfg, embedder)

	// Per-run progress override from CLI flags. --json wins (we must keep
	// stdout clean for the JSON blob); --quiet / --verbose apply when
	// machine output isn't requested. Empty string leaves the Indexer
	// reading from cfg.Graph.ProgressMode.
	switch {
	case indexJSON:
		idx.SetProgressOverride("silent")
	case indexQuiet:
		idx.SetProgressOverride("quiet")
	case indexVerbose:
		idx.SetProgressOverride("verbose")
	}

	var (
		docsRes  *indexer.IndexResult
		graphRes *indexer.GraphResult
	)

	if !indexSkipDocs {
		if !indexJSON {
			fmt.Printf("Indexing documents from %s...\n", absDir)
		}
		docsRes, err = idx.IndexDirectory(docsDir, indexForce)
		if err != nil {
			return fmt.Errorf("docs pass: %w", err)
		}
	}

	if !indexSkipGraph && cfg.ProjectRoot != "" {
		if !indexJSON {
			fmt.Printf("Indexing code graph from %s...\n", cfg.ProjectRoot)
		}
		graphRes, err = idx.IndexProjectGraph(cfg.ProjectRoot, indexForce)
		if err != nil {
			return fmt.Errorf("graph pass: %w", err)
		}
	}

	// Orphan sweep on --force: a forced re-index is the user saying "I want
	// fresh state" — the moment to reap nodes left behind by prior schema
	// churn (e.g. lib-o8m's renamed Python import targets). Scoped to symbol
	// kind by default; matches `librarian gc` without flags. External nodes
	// (ext:<pkg>) are intentionally excluded — stale import edges are NOT
	// cleaned between the doc delete and re-projection, so a freshly re-
	// indexed ext: node usually still has an edge and wouldn't be flagged
	// orphan anyway. Users who want to reap external orphans should run
	// `librarian gc --kinds=external` explicitly. Incremental runs also
	// skip the sweep because they can briefly orphan a node between
	// DeleteSymbolsForFile and the subsequent re-projection.
	var orphanSwept []string
	if indexForce && !indexSkipGraph && graphRes != nil {
		orphanSwept, err = s.DeleteOrphanNodes([]string{store.NodeKindSymbol})
		if err != nil {
			return fmt.Errorf("orphan sweep: %w", err)
		}
	}

	if indexJSON {
		payload := map[string]any{
			"docs":  docsRes,
			"graph": graphRes,
		}
		if orphanSwept != nil {
			payload["orphans_swept"] = map[string]any{
				"count": len(orphanSwept),
				"ids":   orphanSwept,
			}
		}
		out, _ := json.MarshalIndent(payload, "", "  ")
		fmt.Println(string(out))
	} else {
		fmt.Printf("\nIndexing complete:\n")
		if docsRes != nil {
			fmt.Printf("  Documents indexed: %d\n", docsRes.DocumentsIndexed)
			fmt.Printf("  Chunks created:    %d\n", docsRes.ChunksCreated)
			fmt.Printf("  Code files found:  %d\n", docsRes.CodeFilesFound)
			fmt.Printf("  Skipped:           %d\n", docsRes.Skipped)
			if len(docsRes.Errors) > 0 {
				fmt.Printf("  Errors:            %d\n", len(docsRes.Errors))
				for _, e := range docsRes.Errors {
					fmt.Printf("    - %s\n", e)
				}
			}
		}
		if graphRes != nil {
			fmt.Printf("  Graph files:       %d (skipped %d", graphRes.FilesScanned, graphRes.FilesSkipped)
			if graphRes.FilesSkippedGenerated > 0 {
				fmt.Printf(", %d generated", graphRes.FilesSkippedGenerated)
			}
			if graphRes.FilesErrored > 0 {
				fmt.Printf(", %d errored", graphRes.FilesErrored)
			}
			fmt.Printf(")\n")
			fmt.Printf("  Symbols added:     %d\n", graphRes.SymbolsAdded)
			fmt.Printf("  Edges added:       %d\n", graphRes.EdgesAdded)
			if len(graphRes.Errors) > 0 {
				fmt.Printf("  Errors:            %d\n", len(graphRes.Errors))
				for _, e := range graphRes.Errors {
					fmt.Printf("    - %s\n", e)
				}
			}
		}
		if orphanSwept != nil {
			fmt.Printf("  Orphans swept:     %d (kind: %s)\n", len(orphanSwept), store.NodeKindSymbol)
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
