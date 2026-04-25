package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"librarian/internal/store"
)

var (
	gcKinds  string
	gcDryRun bool
	gcJSON   bool
)

var gcCmd = &cobra.Command{
	Use:   "gc",
	Short: "Delete orphan graph nodes (no incoming or outgoing edges)",
	Long: `Delete orphan graph nodes — rows in graph_nodes whose id is referenced
neither as from_node nor to_node in any edge.

Useful after schema-changing updates (e.g. lib-o8m renamed Python relative
imports to absolute dotted form) that leave stale nodes behind. By default
only symbol-kind nodes are swept; pass --kinds to widen.

Safe to run alongside 'librarian index' — the sweep uses an immediate
transaction so a concurrent indexer write either commits first or blocks
briefly until the sweep completes.
`,
	RunE: runGC,
}

func init() {
	gcCmd.Flags().StringVar(&gcKinds, "kinds", store.NodeKindSymbol, "Comma-separated node kinds to sweep, or 'all' for every kind")
	gcCmd.Flags().BoolVar(&gcDryRun, "dry-run", false, "Show orphan ids without deleting")
	gcCmd.Flags().BoolVar(&gcJSON, "json", false, "Machine-readable output")
	rootCmd.AddCommand(gcCmd)
}

func runGC(cmd *cobra.Command, args []string) error {
	kinds, err := parseGCKinds(gcKinds)
	if err != nil {
		return err
	}

	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	var ids []string
	if gcDryRun {
		nodes, err := s.ListOrphanNodes(kinds)
		if err != nil {
			return fmt.Errorf("listing orphan nodes: %w", err)
		}
		ids = make([]string, len(nodes))
		for i, n := range nodes {
			ids[i] = n.ID
		}
	} else {
		ids, err = s.DeleteOrphanNodes(kinds)
		if err != nil {
			return fmt.Errorf("deleting orphan nodes: %w", err)
		}
	}

	if gcJSON {
		out, _ := json.MarshalIndent(map[string]any{
			"kinds":        kinds,
			"dry_run":      gcDryRun,
			"orphan_count": len(ids),
			"deleted":      ids,
		}, "", "  ")
		fmt.Println(string(out))
		return nil
	}

	printGCSummary(ids, kinds, gcDryRun)
	return nil
}

// parseGCKinds expands --kinds. "all" returns every built-in kind; a
// comma-separated list is validated against the known set so typos fail
// loudly rather than silently matching nothing.
func parseGCKinds(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "all" {
		return store.NodeKinds(), nil
	}
	valid := map[string]bool{}
	for _, k := range store.NodeKinds() {
		valid[k] = true
	}
	var out []string
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if !valid[k] {
			return nil, fmt.Errorf("unknown node kind %q; valid: %s (or 'all')", k, strings.Join(store.NodeKinds(), ", "))
		}
		out = append(out, k)
	}
	return out, nil
}

// printGCSummary renders the plain-text output for an orphan sweep. Caps the
// id listing at 20 entries so a 10k-orphan sweep doesn't flood the terminal;
// users who want every id should pass --json.
func printGCSummary(ids, kinds []string, dryRun bool) {
	verb := "Deleted"
	if dryRun {
		verb = "Would delete"
	}
	fmt.Printf("Orphan sweep (kinds: %s)", strings.Join(kinds, ", "))
	if dryRun {
		fmt.Printf(" — DRY RUN")
	}
	fmt.Println(":")
	fmt.Printf("  %s: %d nodes\n", verb, len(ids))
	const maxDisplay = 20
	for i, id := range ids {
		if i >= maxDisplay {
			fmt.Printf("    ... (%d more; use --json for the full list)\n", len(ids)-maxDisplay)
			break
		}
		fmt.Printf("    %s\n", id)
	}
}
