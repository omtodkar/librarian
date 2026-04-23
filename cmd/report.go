package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"librarian/internal/analytics"
	"librarian/internal/report"
	"librarian/internal/store"
	"librarian/internal/workspace"
)

var (
	reportJSON   bool
	reportDryRun bool
)

var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Write GRAPH_REPORT.md, graph.html, and graph.json to .librarian/out/",
	Long: `Runs graph analytics over the indexed content and writes three artifacts
into the workspace's out/ directory:

  GRAPH_REPORT.md — god nodes, communities, surprising connections,
                    suggested questions (the file assistants should read
                    before grepping).
  graph.html      — self-contained SVG visualisation; opens offline.
  graph.json      — structured dump for programmatic consumers.

Expects a workspace to already exist (run 'librarian init' first) and the
index to be populated ('librarian index').`,
	RunE: runReport,
}

func init() {
	reportCmd.Flags().BoolVar(&reportJSON, "json", false, "Print a JSON summary instead of the default text summary")
	reportCmd.Flags().BoolVar(&reportDryRun, "dry-run", false, "Analyse and render but don't write files to disk")
	rootCmd.AddCommand(reportCmd)
}

func runReport(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}
	ws, err := workspace.Find(cwd)
	if err != nil {
		return fmt.Errorf("workspace: %w (did you run 'librarian init'?)", err)
	}

	// Use the workspace's DB path rather than cfg.DBPath — cfg's default is
	// a CWD-relative ".librarian/librarian.db" which is wrong whenever the
	// user runs `librarian report` from a subdirectory of the workspace.
	s, err := store.Open(ws.DBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	// Single Analyze call — no separate ListNodes/ListEdges round trip,
	// eliminating both the extra SQL and the WAL-snapshot consistency
	// hazard of reading the graph twice.
	result, err := analytics.Analyze(s)
	if err != nil {
		return fmt.Errorf("analysing: %w", err)
	}

	in := &report.Input{
		Analytics:   result.Report,
		Nodes:       result.Nodes,
		Edges:       result.Edges,
		GeneratedAt: time.Now(),
	}

	outputs := []struct {
		name string
		path string
		data []byte
	}{
		{"GRAPH_REPORT.md", filepath.Join(ws.OutDir(), "GRAPH_REPORT.md"), report.RenderMarkdown(in)},
		{"graph.json", filepath.Join(ws.OutDir(), "graph.json"), report.RenderJSON(in)},
		{"graph.html", filepath.Join(ws.OutDir(), "graph.html"), report.RenderHTML(in)},
	}

	if reportDryRun {
		// Print sizes only — surface what would be written without
		// touching the filesystem. Useful for CI / install scripts.
		if reportJSON {
			entries := make([]map[string]any, len(outputs))
			for i, o := range outputs {
				entries[i] = map[string]any{
					"name":  o.name,
					"path":  o.path,
					"bytes": len(o.data),
				}
			}
			out, _ := json.MarshalIndent(map[string]any{
				"dry_run":     true,
				"output_dir":  ws.OutDir(),
				"outputs":     entries,
				"nodes":       result.TotalNodes,
				"edges":       result.TotalEdges,
				"communities": len(result.Communities),
			}, "", "  ")
			fmt.Println(string(out))
			return nil
		}
		fmt.Printf("Dry run — would write %d files under %s:\n", len(outputs), ws.OutDir())
		for _, o := range outputs {
			fmt.Printf("  %-18s  %d bytes\n", o.name, len(o.data))
		}
		fmt.Printf("\nGraph: %d nodes, %d edges, %d communities\n",
			result.TotalNodes, result.TotalEdges, len(result.Communities))
		return nil
	}

	if err := os.MkdirAll(ws.OutDir(), 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}
	written := make([]string, 0, len(outputs))
	for _, o := range outputs {
		if err := os.WriteFile(o.path, o.data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", o.path, err)
		}
		rel, _ := filepath.Rel(cwd, o.path)
		if rel == "" {
			rel = o.path
		}
		written = append(written, rel)
	}

	if reportJSON {
		out, _ := json.MarshalIndent(map[string]any{
			"output_dir":  ws.OutDir(),
			"files":       written,
			"nodes":       result.TotalNodes,
			"edges":       result.TotalEdges,
			"communities": len(result.Communities),
		}, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	fmt.Printf("Graph report written to %s\n", ws.OutDir())
	for _, path := range written {
		fmt.Printf("  %s\n", path)
	}
	fmt.Printf("\nGraph: %d nodes, %d edges, %d communities\n",
		result.TotalNodes, result.TotalEdges, len(result.Communities))
	return nil
}
