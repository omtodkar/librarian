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
//  1. Exact node id (e.g. "doc:abc-123", "file:internal/auth/service.go") —
//     an exact hit short-circuits everything else.
//  2. All automatic prefix-expansions ("doc:<input>", "file:<input>", etc.)
//     combined into a candidate set.
//  3. If still empty, substring match against id, label, and source_path.
//
// Step 2 collects *all* prefix hits so an input like "auth" that matches both
// "doc:auth" and "file:auth" surfaces the ambiguity instead of silently picking
// whichever prefix the loop hits first.
func resolveNode(s *store.Store, query string) (*store.Node, error) {
	if n, err := s.GetNode(query); err == nil && n != nil {
		return n, nil
	}

	var candidates []store.Node
	seen := make(map[string]bool)
	for _, prefix := range store.NodeIDPrefixes() {
		if n, err := s.GetNode(prefix + query); err == nil && n != nil && !seen[n.ID] {
			candidates = append(candidates, *n)
			seen[n.ID] = true
		}
	}

	if len(candidates) == 0 {
		found, err := s.FindNodes(query, 10)
		if err != nil {
			return nil, fmt.Errorf("find_nodes: %w", err)
		}
		candidates = found
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
