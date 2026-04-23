package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// likeEscaper backslash-escapes LIKE wildcard characters in a user-supplied
// query so substring matches don't degrade when the query literally contains
// "%" or "_". The SQL statement must pair this with `ESCAPE '\'`.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Graph edge kinds used by the current indexer. Additional kinds will land as
// new handlers extract richer structural information (imports, calls, etc.).
const (
	EdgeKindMentions       = "mentions"         // document → code_file
	EdgeKindSharedCodeRef  = "shared_code_ref"  // document → document (both mention same code file)
)

// Graph node kinds. Additional kinds will land as new handlers emit richer
// structural information.
const (
	NodeKindDocument  = "document"
	NodeKindCodeFile  = "code_file"
	NodeKindSymbol    = "symbol"     // tree-sitter method/class/function nodes
	NodeKindConfigKey = "config_key" // YAML/TOML/properties key paths
)

// Node is a row in graph_nodes.
type Node struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	Label      string `json:"label"`
	SourcePath string `json:"source_path,omitempty"`
	Metadata   string `json:"metadata"` // JSON blob
}

// Edge is a row in graph_edges.
type Edge struct {
	From     string  `json:"from"`
	To       string  `json:"to"`
	Kind     string  `json:"kind"`
	Weight   float64 `json:"weight"`
	Metadata string  `json:"metadata"`
}

// PathStep is one hop in a shortest-path result.
type PathStep struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Kind   string  `json:"kind"`
	Weight float64 `json:"weight"`
}

// DocNodeID returns the stable graph node id for a document.
func DocNodeID(docID string) string { return "doc:" + docID }

// CodeFileNodeID returns the stable graph node id for a code file (keyed by path).
func CodeFileNodeID(filePath string) string { return "file:" + filePath }

// SymbolNodeID returns the stable graph node id for a code symbol (fully
// qualified name, e.g. "com.acme.AuthService.validate").
func SymbolNodeID(target string) string { return "sym:" + target }

// ConfigKeyNodeID returns the stable graph node id for a config key (dotted
// path, e.g. "spring.datasource.url").
func ConfigKeyNodeID(target string) string { return "key:" + target }

// NodeIDPrefixes returns the namespaced id prefixes used by the built-in node
// id constructors. Callers that resolve user input against all known node
// kinds (e.g. the CLI's resolveNode) iterate this list rather than hardcoding
// a parallel copy.
func NodeIDPrefixes() []string {
	return []string{"doc:", "file:", "sym:", "key:"}
}

// UpsertNode inserts or replaces a graph node. Idempotent — safe to call on re-index.
func (s *Store) UpsertNode(n Node) error {
	if n.Metadata == "" {
		n.Metadata = "{}"
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO graph_nodes (id, kind, label, source_path, metadata)
		VALUES (?, ?, ?, ?, ?)`,
		n.ID, n.Kind, n.Label, nullString(n.SourcePath), n.Metadata)
	if err != nil {
		return fmt.Errorf("upsert_node: %w", err)
	}
	return nil
}

// UpsertEdge inserts or replaces an edge. The UNIQUE(from_node, to_node, kind)
// constraint makes repeated calls idempotent.
func (s *Store) UpsertEdge(e Edge) error {
	if e.Metadata == "" {
		e.Metadata = "{}"
	}
	if e.Weight == 0 {
		e.Weight = 1.0
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO graph_edges (from_node, to_node, kind, weight, metadata)
		VALUES (?, ?, ?, ?, ?)`,
		e.From, e.To, e.Kind, e.Weight, e.Metadata)
	if err != nil {
		return fmt.Errorf("upsert_edge: %w", err)
	}
	return nil
}

// GetNode fetches a node by id; returns (nil, nil) if not found.
func (s *Store) GetNode(id string) (*Node, error) {
	var n Node
	var sp sql.NullString
	err := s.db.QueryRow(
		`SELECT id, kind, label, source_path, metadata FROM graph_nodes WHERE id = ?`,
		id,
	).Scan(&n.ID, &n.Kind, &n.Label, &sp, &n.Metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get_node: %w", err)
	}
	if sp.Valid {
		n.SourcePath = sp.String
	}
	return &n, nil
}

// FindNodes returns nodes matching a substring against id, label, or source_path.
// Used by CLI commands to accept friendly names. Wildcard characters (%, _, \)
// in the query are backslash-escaped so a query like "test_helpers" matches
// literally instead of treating "_" as "any single char".
func (s *Store) FindNodes(query string, limit int) ([]Node, error) {
	if limit <= 0 {
		limit = 10
	}
	like := "%" + likeEscaper.Replace(query) + "%"
	rows, err := s.db.Query(`
		SELECT id, kind, label, source_path, metadata
		FROM graph_nodes
		WHERE id = ? OR label LIKE ? ESCAPE '\' OR source_path LIKE ? ESCAPE '\'
		ORDER BY CASE WHEN id = ? THEN 0 ELSE 1 END, label
		LIMIT ?`, query, like, like, query, limit)
	if err != nil {
		return nil, fmt.Errorf("find_nodes: %w", err)
	}
	defer rows.Close()

	var out []Node
	for rows.Next() {
		var n Node
		var sp sql.NullString
		if err := rows.Scan(&n.ID, &n.Kind, &n.Label, &sp, &n.Metadata); err != nil {
			return nil, fmt.Errorf("find_nodes scan: %w", err)
		}
		if sp.Valid {
			n.SourcePath = sp.String
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListNodes returns every graph_node row. Used by graph-wide analytics
// (community detection, centrality) that need the full topology in memory.
func (s *Store) ListNodes() ([]Node, error) {
	rows, err := s.db.Query(`SELECT id, kind, label, source_path, metadata FROM graph_nodes`)
	if err != nil {
		return nil, fmt.Errorf("list_nodes: %w", err)
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		var sp sql.NullString
		if err := rows.Scan(&n.ID, &n.Kind, &n.Label, &sp, &n.Metadata); err != nil {
			return nil, fmt.Errorf("list_nodes scan: %w", err)
		}
		if sp.Valid {
			n.SourcePath = sp.String
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListEdges returns every graph_edges row. Companion to ListNodes for
// graph-wide analytics.
func (s *Store) ListEdges() ([]Edge, error) {
	rows, err := s.db.Query(`SELECT from_node, to_node, kind, weight, metadata FROM graph_edges`)
	if err != nil {
		return nil, fmt.Errorf("list_edges: %w", err)
	}
	defer rows.Close()
	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.From, &e.To, &e.Kind, &e.Weight, &e.Metadata); err != nil {
			return nil, fmt.Errorf("list_edges scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Neighbors returns edges incident to nodeID.
// direction: "out" (outgoing only), "in" (incoming only), "" or "both" (both directions).
func (s *Store) Neighbors(nodeID, direction string) ([]Edge, error) {
	var query string
	var args []any
	switch direction {
	case "out":
		query = `SELECT from_node, to_node, kind, weight, metadata FROM graph_edges WHERE from_node = ?`
		args = []any{nodeID}
	case "in":
		query = `SELECT from_node, to_node, kind, weight, metadata FROM graph_edges WHERE to_node = ?`
		args = []any{nodeID}
	default:
		query = `SELECT from_node, to_node, kind, weight, metadata FROM graph_edges WHERE from_node = ? OR to_node = ?`
		args = []any{nodeID, nodeID}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("neighbors: %w", err)
	}
	defer rows.Close()

	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.From, &e.To, &e.Kind, &e.Weight, &e.Metadata); err != nil {
			return nil, fmt.Errorf("neighbors scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ShortestPath finds the shortest directed path from → to via breadth-first
// search over graph_edges. maxDepth caps the search; zero or negative values
// default to 6. Returns nil if no path exists.
//
// Implemented as application-level BFS rather than a recursive CTE so that
// node IDs containing SQL-LIKE special characters (`%`, `_`) or the string
// delimiters a CTE would need (`|`, `,`, `>`) don't corrupt traversal. For
// typical project graphs (< 50k edges) the extra round-trips are negligible.
func (s *Store) ShortestPath(fromID, toID string, maxDepth int) ([]PathStep, error) {
	if maxDepth <= 0 {
		maxDepth = 6
	}
	if fromID == toID {
		return nil, nil
	}

	// visited records, for every reached node, the edge that got us there.
	// Reconstructing the path is then a walk back from toID to fromID.
	visited := map[string]Edge{fromID: {}}
	frontier := []string{fromID}

	for depth := 0; depth < maxDepth && len(frontier) > 0; depth++ {
		var next []string
		for _, nodeID := range frontier {
			edges, err := s.outgoingEdges(nodeID)
			if err != nil {
				return nil, fmt.Errorf("shortest_path bfs: %w", err)
			}
			for _, e := range edges {
				if _, seen := visited[e.To]; seen {
					continue
				}
				visited[e.To] = e
				if e.To == toID {
					return reconstructPath(visited, fromID, toID), nil
				}
				next = append(next, e.To)
			}
		}
		frontier = next
	}
	return nil, nil
}

// outgoingEdges returns outgoing edges for a node. Kept separate from
// Neighbors so ShortestPath always walks in the directed "out" sense without
// bouncing on incoming edges.
func (s *Store) outgoingEdges(nodeID string) ([]Edge, error) {
	rows, err := s.db.Query(
		`SELECT from_node, to_node, kind, weight, metadata FROM graph_edges WHERE from_node = ?`,
		nodeID)
	if err != nil {
		return nil, fmt.Errorf("outgoing_edges: %w", err)
	}
	defer rows.Close()

	var out []Edge
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.From, &e.To, &e.Kind, &e.Weight, &e.Metadata); err != nil {
			return nil, fmt.Errorf("outgoing_edges scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// reconstructPath walks the visited-edge map backwards from toID to fromID,
// returning the edges in forward order.
func reconstructPath(visited map[string]Edge, fromID, toID string) []PathStep {
	var steps []PathStep
	for cur := toID; cur != fromID; {
		e, ok := visited[cur]
		if !ok {
			// Should not happen if BFS reached toID, but guard defensively
			// rather than loop forever on a corrupted visited map.
			return nil
		}
		steps = append([]PathStep{{From: e.From, To: e.To, Kind: e.Kind, Weight: e.Weight}}, steps...)
		cur = e.From
	}
	return steps
}

// DeleteNode removes a node and (via FK cascade) its incident edges.
func (s *Store) DeleteNode(id string) error {
	_, err := s.db.Exec(`DELETE FROM graph_nodes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete_node: %w", err)
	}
	return nil
}

// nullString converts empty string to SQL NULL for nullable columns.
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
