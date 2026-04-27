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
	contextLimit  int
	contextBudget int
	contextJSON   bool
)

var contextCmd = &cobra.Command{
	Use:   "context <query>",
	Short: "Comprehensive briefing: search + related docs + referenced code",
	Long: `Runs a semantic search for the query, then expands with source documents, referenced
code files (grouped as files, directories, patterns), and related documents reached via
shared code references. Use 'search' for a lighter query; use 'context' to orient.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runContext,
}

func init() {
	contextCmd.Flags().IntVar(&contextLimit, "limit", 5, "Maximum primary-source chunks (1-10)")
	contextCmd.Flags().IntVar(&contextBudget, "budget", 0, "Token budget: stop including chunks once cumulative tokens would exceed this value (0 = disabled)")
	contextCmd.Flags().BoolVar(&contextJSON, "json", false, "Output as JSON")
	rootCmd.AddCommand(contextCmd)
}

func runContext(cmd *cobra.Command, args []string) error {
	query := strings.Join(args, " ")

	if contextLimit < 1 {
		contextLimit = 1
	}
	if contextLimit > 10 {
		contextLimit = 10
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

	vector, err := embedder.Embed(query)
	if err != nil {
		return fmt.Errorf("embedding query: %w", err)
	}

	var chunks []store.DocChunk
	if cfg.Search.HybridSearch {
		chunks, err = s.HybridSearch(vector, query, contextLimit)
	} else {
		chunks, err = s.SearchChunks(vector, contextLimit)
	}
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	chunks = store.ApplyTokenBudget(chunks, contextBudget)

	// Collect unique source documents.
	seenDocs := make(map[string]bool)
	var sourceDocs []store.Document
	var docIDs []string
	for _, chunk := range chunks {
		if seenDocs[chunk.FilePath] {
			continue
		}
		seenDocs[chunk.FilePath] = true
		doc, err := s.GetDocumentByPath(chunk.FilePath)
		if err != nil {
			continue
		}
		sourceDocs = append(sourceDocs, *doc)
		docIDs = append(docIDs, doc.ID)
	}

	// Referenced code files, grouped by ref type.
	var files, dirs, patterns []store.CodeFile
	seenCodeFiles := make(map[string]bool)
	for _, docID := range docIDs {
		refs, err := s.GetReferencedCodeFiles(docID)
		if err != nil {
			continue
		}
		for _, cf := range refs {
			if seenCodeFiles[cf.FilePath] {
				continue
			}
			seenCodeFiles[cf.FilePath] = true
			switch cf.RefType {
			case "directory":
				dirs = append(dirs, cf)
			case "pattern":
				patterns = append(patterns, cf)
			default:
				files = append(files, cf)
			}
		}
	}

	// Related documents reached via shared code references.
	var related []store.Document
	seenRelated := make(map[string]bool)
	for _, docID := range docIDs {
		rels, err := s.GetRelatedDocuments(docID)
		if err != nil {
			continue
		}
		for _, r := range rels {
			if seenRelated[r.FilePath] || seenDocs[r.FilePath] {
				continue
			}
			seenRelated[r.FilePath] = true
			related = append(related, r)
		}
	}

	if contextJSON {
		out := map[string]any{
			"query":           query,
			"primary_chunks":  chunks,
			"source_docs":     sourceDocs,
			"code_files":      files,
			"code_dirs":       dirs,
			"code_patterns":   patterns,
			"related_docs":    related,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	// Markdown-style TTY output, mirroring the MCP get_context briefing shape.
	fmt.Printf("=== BRIEFING: %q ===\n\n", query)

	fmt.Println("## Primary Sources (direct matches):")
	fmt.Println()
	if len(chunks) == 0 {
		fmt.Println("No direct matches found.")
		fmt.Println()
	} else {
		for _, c := range chunks {
			fmt.Printf("### %s > %s\n", c.FilePath, c.SectionHeading)
			fmt.Println(c.Content)
			fmt.Println()
		}
	}

	fmt.Println("## Source Documents:")
	for _, d := range sourceDocs {
		fmt.Printf("- %s (%s) - %q\n", d.FilePath, d.DocType, d.Title)
	}
	fmt.Println()

	fmt.Println("## Referenced Code:")
	if len(seenCodeFiles) == 0 {
		fmt.Println("None found.")
	} else {
		if len(files) > 0 {
			fmt.Print("**Files:** ")
			for i, f := range files {
				if i > 0 {
					fmt.Print(", ")
				}
				fmt.Printf("%s (%s)", f.FilePath, f.Language)
			}
			fmt.Println()
		}
		if len(dirs) > 0 {
			fmt.Print("**Directories:** ")
			for i, d := range dirs {
				if i > 0 {
					fmt.Print(", ")
				}
				fmt.Print(d.FilePath)
			}
			fmt.Println()
		}
		if len(patterns) > 0 {
			fmt.Print("**Patterns:** ")
			for i, p := range patterns {
				if i > 0 {
					fmt.Print(", ")
				}
				fmt.Print(p.FilePath)
			}
			fmt.Println()
		}
	}
	fmt.Println()

	fmt.Println("## Related Documentation:")
	if len(related) == 0 {
		fmt.Println("None found.")
	} else {
		for _, r := range related {
			fmt.Printf("- %s - %q\n", r.FilePath, r.Title)
		}
	}

	return nil
}
