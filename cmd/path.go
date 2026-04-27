package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"librarian/internal/store"
)

var (
	pathMaxDepth int
	pathJSON     bool
)

var pathCmd = &cobra.Command{
	Use:   "path <from> <to>",
	Short: "Shortest directed path between two graph nodes",
	Long: `Finds the shortest directed path from <from> to <to> via BFS over graph_edges.
Each argument can be an exact node id or a substring of a label/source path.
If no path exists within --max-depth hops, prints "No path found".`,
	Args: cobra.ExactArgs(2),
	RunE: runPath,
}

func init() {
	pathCmd.Flags().IntVar(&pathMaxDepth, "max-depth", 6, "Maximum hops to search")
	pathCmd.Flags().BoolVar(&pathJSON, "json", false, "Output as JSON")
	rootCmd.AddCommand(pathCmd)
}

func runPath(cmd *cobra.Command, args []string) error {
	s, err := store.Open(cfg.DBPath, nil, 0)
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer s.Close()

	from, err := resolveNode(s, args[0])
	if err != nil {
		return fmt.Errorf("from: %w", err)
	}
	to, err := resolveNode(s, args[1])
	if err != nil {
		return fmt.Errorf("to: %w", err)
	}

	steps, err := s.ShortestPath(from.ID, to.ID, pathMaxDepth)
	if err != nil {
		return fmt.Errorf("shortest_path: %w", err)
	}

	if pathJSON {
		out := map[string]any{
			"from":  from,
			"to":    to,
			"steps": steps,
			"hops":  len(steps),
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	}

	fmt.Printf("From: %s\nTo:   %s\n\n", nodeDisplay(from), nodeDisplay(to))
	if len(steps) == 0 {
		fmt.Printf("No path found within %d hops.\n", pathMaxDepth)
		return nil
	}

	fmt.Printf("Path (%d hops):\n", len(steps))
	fmt.Printf("  %s\n", from.ID)
	for _, st := range steps {
		fmt.Printf("    --%s-->\n  %s\n", st.Kind, st.To)
	}
	return nil
}
