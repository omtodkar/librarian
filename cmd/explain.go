package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"librarian/internal/store"
)

var explainJSON bool

var explainCmd = &cobra.Command{
	Use:   "explain <node>",
	Short: "Summarize a graph node and its immediate connections",
	Long: `Prints the node's metadata along with grouped edge counts by kind, and
lists the neighbours most closely connected. Useful for a quick "what is this
thing and what touches it?" read.`,
	Args: cobra.ExactArgs(1),
	RunE: runExplain,
}

func init() {
	explainCmd.Flags().BoolVar(&explainJSON, "json", false, "Output as JSON")
	rootCmd.AddCommand(explainCmd)
}

func runExplain(cmd *cobra.Command, args []string) error {
	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	node, err := resolveNode(s, args[0])
	if err != nil {
		return err
	}

	edges, err := s.Neighbors(node.ID, "")
	if err != nil {
		return fmt.Errorf("neighbors: %w", err)
	}

	// Group by edge kind, preserving distinct other-node ids per kind.
	grouped := make(map[string][]string)
	for _, e := range edges {
		other := e.To
		if e.To == node.ID {
			other = e.From
		}
		grouped[e.Kind] = append(grouped[e.Kind], other)
	}

	if explainJSON {
		out := map[string]any{
			"node":             node,
			"edge_count":       len(edges),
			"neighbors_by_kind": grouped,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("# %s\n\n", nodeDisplay(node))
	fmt.Printf("Kind:         %s\n", node.Kind)
	if node.SourcePath != "" {
		fmt.Printf("Source path:  %s\n", node.SourcePath)
	}
	fmt.Printf("Edge count:   %d\n\n", len(edges))

	if len(grouped) == 0 {
		fmt.Println("No connections.")
		return nil
	}

	fmt.Println("## Connections by kind")
	for kind, others := range grouped {
		fmt.Printf("\n**%s** (%d):\n", kind, len(others))
		for _, other := range others {
			fmt.Printf("  - %s\n", other)
		}
	}
	return nil
}
