package cmd

import (
	"fmt"
	"os"

	"librarian/internal/store"
)

// resolveNode takes a user-provided identifier (exact node id, doc path, label,
// or substring) and returns the matching node. If multiple candidates match,
// it prints them to stderr and returns an error asking for disambiguation.
//
// Resolution order:
//  1. Exact node id (e.g. "doc:abc-123", "file:internal/auth/service.go").
//  2. Common prefixes tried automatically: "doc:<input>", "file:<input>".
//  3. Substring match against id, label, and source_path (FindNodes).
func resolveNode(s *store.Store, query string) (*store.Node, error) {
	if n, err := s.GetNode(query); err == nil && n != nil {
		return n, nil
	}
	for _, prefix := range []string{"doc:", "file:"} {
		if n, err := s.GetNode(prefix + query); err == nil && n != nil {
			return n, nil
		}
	}

	candidates, err := s.FindNodes(query, 10)
	if err != nil {
		return nil, fmt.Errorf("find_nodes: %w", err)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no node matching %q", query)
	}
	if len(candidates) > 1 {
		fmt.Fprintf(os.Stderr, "Multiple matches for %q:\n", query)
		for _, c := range candidates {
			label := c.Label
			if label == "" {
				label = c.ID
			}
			fmt.Fprintf(os.Stderr, "  %-8s %s  %s\n", c.Kind, c.ID, label)
		}
		return nil, fmt.Errorf("ambiguous match; pass one of the ids above")
	}
	return &candidates[0], nil
}

// nodeDisplay returns a compact string for a node suitable for terminal output.
func nodeDisplay(n *store.Node) string {
	label := n.Label
	if label == "" {
		label = n.ID
	}
	return fmt.Sprintf("%s [%s]", label, n.ID)
}
