package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"librarian/internal/store"
)

var (
	neighborsDirection string
	neighborsJSON      bool
	neighborsEdgeKinds []string
)

var neighborsCmd = &cobra.Command{
	Use:   "neighbors <node>",
	Short: "Show immediate graph neighbors of a node",
	Long: `Prints nodes one edge away from the given node. Accepts an exact node id
(e.g. "doc:abc-123", "file:internal/auth/service.go") or any substring of a
label or source path; ambiguous matches are listed for disambiguation.

--edge-kind filters the result to the given edge kind(s). Repeat the flag to
accept multiple kinds (e.g. --edge-kind=inherits --edge-kind=contains). When
omitted, every incident edge is returned.`,
	Args: cobra.ExactArgs(1),
	RunE: runNeighbors,
}

func init() {
	neighborsCmd.Flags().StringVar(&neighborsDirection, "direction", "both", "Edge direction: 'out', 'in', or 'both'")
	neighborsCmd.Flags().BoolVar(&neighborsJSON, "json", false, "Output as JSON")
	neighborsCmd.Flags().StringSliceVar(&neighborsEdgeKinds, "edge-kind", nil, "Filter to edges of the given kind (repeatable): inherits, requires, part, contains, import, mentions, shared_code_ref, call")
	rootCmd.AddCommand(neighborsCmd)
}

func runNeighbors(cmd *cobra.Command, args []string) error {
	s, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	node, err := resolveNode(s, args[0])
	if err != nil {
		return err
	}

	dir := neighborsDirection
	if dir != "in" && dir != "out" && dir != "both" {
		return fmt.Errorf("--direction must be 'in', 'out', or 'both'")
	}
	if dir == "both" {
		dir = ""
	}

	edges, err := s.Neighbors(node.ID, dir, neighborsEdgeKinds...)
	if err != nil {
		return fmt.Errorf("neighbors: %w", err)
	}

	if neighborsJSON {
		out := map[string]any{
			"node":      node,
			"direction": neighborsDirection,
			"edges":     edges,
		}
		if len(neighborsEdgeKinds) > 0 {
			out["edge_kinds"] = neighborsEdgeKinds
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("Node: %s\n\n", nodeDisplay(node))
	if len(edges) == 0 {
		fmt.Println("No neighbors.")
		return nil
	}
	fmt.Printf("Neighbors (%d edges):\n", len(edges))
	for _, e := range edges {
		other := e.To
		arrow := "->"
		if e.To == node.ID {
			other = e.From
			arrow = "<-"
		}
		fmt.Printf("  %s %-18s %s\n", arrow, e.Kind, other)
	}
	return nil
}
