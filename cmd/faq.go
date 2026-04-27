package cmd

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"librarian/internal/faq"
)

var (
	faqRegenerate  bool
	faqGitCommits  int
	faqThreshold   float64
	faqOutputJSON  bool
)

var faqCmd = &cobra.Command{
	Use:   "faq",
	Short: "Extract FAQs from git history and bd issues",
	Long: `Scans git commit history and bd closed issues for question-shaped content,
clusters near-duplicates by embedding similarity, and writes each cluster as a
searchable markdown FAQ file under docs/faqs/.

Must supply --regenerate to run (batch-only; no auto-refresh).`,
	RunE: runFAQ,
}

func init() {
	faqCmd.Flags().BoolVar(&faqRegenerate, "regenerate", false, "Regenerate FAQ files from history (required)")
	faqCmd.Flags().IntVar(&faqGitCommits, "git-commits", 100, "Number of recent git commits to scan")
	faqCmd.Flags().Float64Var(&faqThreshold, "threshold", 0.85, "Cosine similarity clustering threshold (0–1)")
	faqCmd.Flags().BoolVar(&faqOutputJSON, "json", false, "Output results as JSON")
	rootCmd.AddCommand(faqCmd)
}

func runFAQ(cmd *cobra.Command, args []string) error {
	if !faqRegenerate {
		return fmt.Errorf("pass --regenerate to run FAQ extraction")
	}

	if !faqOutputJSON {
		fmt.Println("Scanning git history and bd issues for question-shaped content...")
	}

	result, err := faq.Run(faq.RunConfig{
		GitCommits: faqGitCommits,
		Threshold:  faqThreshold,
		Cfg:        cfg,
	})
	if err != nil {
		return err
	}

	// Record the run timestamp so future sessions know when FAQs were last
	// regenerated. Non-fatal: bd may not be installed in every environment.
	ts := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	_ = exec.Command("bd", "remember", "--key", "faq-last-run",
		fmt.Sprintf("Last FAQ extraction run: %s — %d git, %d issue sources → %d clusters → %d files",
			ts, result.GitSources, result.IssueSources, result.Clusters, len(result.FilesWritten))).Run()

	if faqOutputJSON {
		out, _ := json.MarshalIndent(map[string]any{
			"git_sources":   result.GitSources,
			"issue_sources": result.IssueSources,
			"clusters":      result.Clusters,
			"files_written": result.FilesWritten,
			"chunks_created": result.ChunksCreated,
		}, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("\nFAQ extraction complete:\n")
	fmt.Printf("  Git sources:   %d\n", result.GitSources)
	fmt.Printf("  Issue sources: %d\n", result.IssueSources)
	fmt.Printf("  Clusters:      %d\n", result.Clusters)
	fmt.Printf("  Files written: %d\n", len(result.FilesWritten))
	fmt.Printf("  Chunks indexed: %d\n", result.ChunksCreated)
	for _, f := range result.FilesWritten {
		fmt.Printf("    %s\n", f)
	}
	return nil
}
