package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Graph edge kinds used by the current indexer. Additional kinds will land as
// new handlers extract richer structural information (imports, calls, etc.).
const (
	EdgeKindMentions       = "mentions"         // document → code_file
	EdgeKindSharedCodeRef  = "shared_code_ref"  // document → document (both mention same code file)
)

// Graph node kinds.
const (
	NodeKindDocument = "document"
	NodeKindCodeFile = "code_file"
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
// Used by CLI commands to accept friendly names.
func (s *Store) FindNodes(query string, limit int) ([]Node, error) {
	if limit <= 0 {
		limit = 10
	}
	like := "%" + query + "%"
	rows, err := s.db.Query(`
		SELECT id, kind, label, source_path, metadata
		FROM graph_nodes
		WHERE id = ? OR label LIKE ? OR source_path LIKE ?
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

// ShortestPath finds the shortest directed path from → to via BFS in SQL
// (recursive CTE). maxDepth caps the search; zero or negative values default to 6.
// Returns nil if no path exists.
//
// The recursive CTE encodes the traversal trail as a delimited string so we can
// skip visited nodes and reconstruct the edge sequence from a single column at
// the end — cheaper than joining back to graph_edges per-step.
func (s *Store) ShortestPath(fromID, toID string, maxDepth int) ([]PathStep, error) {
	if maxDepth <= 0 {
		maxDepth = 6
	}
	const q = `
		WITH RECURSIVE paths AS (
			SELECT from_node, to_node, 1 AS depth,
				   '>' || from_node || '>' || to_node || '>' AS trail,
				   from_node || '|' || to_node || '|' || kind || '|' || weight AS edges
			FROM graph_edges
			WHERE from_node = ?

			UNION ALL

			SELECT p.from_node, e.to_node, p.depth + 1,
				   p.trail || e.to_node || '>',
				   p.edges || ',' || e.from_node || '|' || e.to_node || '|' || e.kind || '|' || e.weight
			FROM paths p
			JOIN graph_edges e ON e.from_node = p.to_node
			WHERE p.depth < ?
			AND p.trail NOT LIKE '%>' || e.to_node || '>%'
		)
		SELECT edges FROM paths
		WHERE to_node = ?
		ORDER BY depth ASC
		LIMIT 1`
	var edges string
	err := s.db.QueryRow(q, fromID, maxDepth, toID).Scan(&edges)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("shortest_path: %w", err)
	}

	var steps []PathStep
	for _, e := range strings.Split(edges, ",") {
		parts := strings.Split(e, "|")
		if len(parts) < 4 {
			continue
		}
		w, _ := strconv.ParseFloat(parts[3], 64)
		steps = append(steps, PathStep{
			From:   parts[0],
			To:     parts[1],
			Kind:   parts[2],
			Weight: w,
		})
	}
	return steps, nil
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
