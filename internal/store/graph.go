package store

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// likeEscaper backslash-escapes LIKE wildcard characters in a user-supplied
// query so substring matches don't degrade when the query literally contains
// "%" or "_". The SQL statement must pair this with `ESCAPE '\'`.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// Graph edge kinds used by the current indexer. Additional kinds will land as
// new handlers extract richer structural information (imports, calls, etc.).
const (
	EdgeKindMentions      = "mentions"        // document → code_file
	EdgeKindSharedCodeRef = "shared_code_ref" // document → document (both mention same code file)
	EdgeKindContains      = "contains"        // code_file → symbol (graph pass projects each parsed Unit into a symbol node tied to its file via this edge)
	EdgeKindInherits      = "inherits"        // symbol → symbol (class/interface/protocol parent; Edge.Metadata carries a "relation" of extends/implements/mixes/conforms/embeds)
	EdgeKindRequires      = "requires"        // symbol → symbol (Dart `mixin M on Base` — use-site constraint, not an inheritance parent; kept distinct so "all parents of X" queries stay clean)
	EdgeKindPart          = "part"            // code_file → code_file (Dart `part 'foo.dart'` / `part of 'bar.dart'` file-join; a single Dart library lives across multiple files)
	EdgeKindImplementsRPC = "implements_rpc"  // symbol → symbol (generated-code method → proto rpc declaration; codegen derivation, not inheritance — kept distinct so "all parents of X" queries stay clean)
	EdgeKindCallRPC       = "call_rpc"        // symbol → symbol (call site in hand-written code → proto rpc declaration; runtime invocation, not codegen derivation — kept distinct from implements_rpc so derivation and invocation queries stay clean)
)

// Graph node kinds. Additional kinds will land as new handlers emit richer
// structural information.
const (
	NodeKindDocument    = "document"
	NodeKindCodeFile    = "code_file"
	NodeKindSymbol      = "symbol"       // tree-sitter method/class/function nodes
	NodeKindConfigKey   = "config_key"   // YAML/TOML/properties key paths
	NodeKindExternal    = "external"     // external packages (npm, crates.io, PyPI) referenced but not in-project
	NodeKindBufManifest = "buf_manifest" // one per proto file: per-language codegen path prefixes (lib-4kb), consumed by the implements_rpc resolver
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

// ExternalPackageNodeID returns the stable graph node id for a reference to an
// external package (e.g. "ext:lodash", "ext:@scope/pkg"). Used for bare JS/TS
// module specifiers that don't map to an in-project file — distinguishing
// them from in-project symbols keeps the sym: namespace clean.
func ExternalPackageNodeID(spec string) string { return "ext:" + spec }

// BufManifestNodeID returns the stable graph node id for a per-proto-file
// buf codegen manifest (lib-4kb). Keyed by the proto file's workspace-relative
// path so each .proto has its own manifest node — a layout that lets the
// implements_rpc resolver GetNode(BufManifestNodeID(protoPath)) directly when
// tightening candidate matches.
func BufManifestNodeID(protoPath string) string { return "bufgen:" + protoPath }

// NodeIDPrefixes returns the namespaced id prefixes used by the built-in node
// id constructors. Callers that resolve user input against all known node
// kinds (e.g. the CLI's resolveNode) iterate this list rather than hardcoding
// a parallel copy.
func NodeIDPrefixes() []string {
	return []string{"doc:", "file:", "sym:", "key:", "ext:", "bufgen:"}
}

// NodeKinds returns every built-in node kind. Used by the orphan sweep
// command to expand --kinds=all without hardcoding a parallel copy.
func NodeKinds() []string {
	return []string{NodeKindDocument, NodeKindCodeFile, NodeKindSymbol, NodeKindConfigKey, NodeKindExternal, NodeKindBufManifest}
}

// UpsertNode inserts or updates a graph node. Idempotent — safe to call on
// re-index. Uses ON CONFLICT DO UPDATE rather than INSERT OR REPLACE because
// the latter deletes the conflicting row and re-inserts, which triggers the
// ON DELETE CASCADE on graph_edges.from_node/to_node and wipes every edge
// incident to this node — even when the only difference is updated metadata.
// That's catastrophic for cross-file shared targets: file A upserts sym:X,
// adds edge A→X; file B upserts sym:X and vapourises A's edge. See lib-o8m's
// Python integration test for a concrete reproduction. ON CONFLICT DO UPDATE
// preserves the row identity and leaves FK-dependent edges untouched.
func (s *Store) UpsertNode(n Node) error {
	if n.Metadata == "" {
		n.Metadata = "{}"
	}
	_, err := s.db.Exec(`
		INSERT INTO graph_nodes (id, kind, label, source_path, metadata)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			kind        = excluded.kind,
			label       = excluded.label,
			source_path = excluded.source_path,
			metadata    = excluded.metadata`,
		n.ID, n.Kind, n.Label, nullString(n.SourcePath), n.Metadata)
	if err != nil {
		return fmt.Errorf("upsert_node: %w", err)
	}
	return nil
}

// UpsertPlaceholderNode inserts a node row IF AND ONLY IF no row with the
// same id already exists. Unlike UpsertNode (which rewrites label / source_path /
// metadata on conflict via ON CONFLICT DO UPDATE), a conflict here is a no-op —
// the existing row's data is preserved in full.
//
// Intended for the graph pass's reference-projection loop, where an outbound
// Reference.Target may or may not correspond to a real, already-indexed symbol:
//
//   - If the target is known (its own file's symbol-projection pass already ran
//     UpsertNode with kind=symbol and source_path=<file>), this call leaves the
//     real node's metadata intact. Avoids the "unresolved=true flag silently
//     poisons a resolved symbol's node" ordering bug: a Reference.Metadata with
//     unresolved=true would otherwise overwrite the real node's clean metadata
//     whenever the referencing file is walked after the defining file.
//   - If the target is unknown, the placeholder row is written with whatever
//     label / source_path / metadata the caller supplies, and a later
//     symbol-projection pass on the defining file can upgrade it via UpsertNode.
//
// The ON CONFLICT DO NOTHING form is SQLite-native (≥ 3.24) and keeps the row's
// identity stable — FK-dependent graph_edges remain intact, same guarantee
// UpsertNode's godoc describes.
func (s *Store) UpsertPlaceholderNode(n Node) error {
	if n.Metadata == "" {
		n.Metadata = "{}"
	}
	_, err := s.db.Exec(`
		INSERT INTO graph_nodes (id, kind, label, source_path, metadata)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		n.ID, n.Kind, n.Label, nullString(n.SourcePath), n.Metadata)
	if err != nil {
		return fmt.Errorf("upsert_placeholder_node: %w", err)
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

// ListSymbolNodesWithMetadataContaining returns every graph_node with
// kind='symbol' whose metadata column contains the given literal substring.
// The match is a SQL LIKE anchored with %…% and the substring's LIKE
// wildcards (`%`, `_`, `\`) are backslash-escaped, so callers pass a plain
// literal and get substring-match semantics without surprises.
//
// Narrowly useful for cross-language resolvers that need to re-find a
// structural-kind subset of symbols after indexing (e.g. lib-6wz's
// buildImplementsRPCEdges walks proto rpc nodes by matching the
// `"input_type":` substring — the proto grammar is the sole emitter of
// that key on symbol metadata). Cheaper than ListNodes + in-Go filter
// because the LIKE predicate pushes the scan into SQLite.
//
// Thin wrapper over ListNodesByKindWithMetadataContaining kept for
// backwards compatibility with existing callers.
func (s *Store) ListSymbolNodesWithMetadataContaining(substring string) ([]Node, error) {
	return s.ListNodesByKindWithMetadataContaining(NodeKindSymbol, substring)
}

// ListNodesByKindWithMetadataContaining is the kind-parameterised form of the
// symbol-only helper above. Used by the buf manifest builder (lib-4kb) to
// pull every code_file node whose metadata stashes either a buf.gen.yaml
// plugin list or a proto file's `option *_package` map — two different
// substring markers ("buf_gen" / "options") against the same kind filter.
func (s *Store) ListNodesByKindWithMetadataContaining(kind, substring string) ([]Node, error) {
	like := "%" + likeEscaper.Replace(substring) + "%"
	rows, err := s.db.Query(`
		SELECT id, kind, label, source_path, metadata
		FROM graph_nodes
		WHERE kind = ? AND metadata LIKE ? ESCAPE '\'`,
		kind, like)
	if err != nil {
		return nil, fmt.Errorf("list_nodes_by_kind_with_metadata: %w", err)
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		var sp sql.NullString
		if err := rows.Scan(&n.ID, &n.Kind, &n.Label, &sp, &n.Metadata); err != nil {
			return nil, fmt.Errorf("list_nodes_by_kind_with_metadata scan: %w", err)
		}
		if sp.Valid {
			n.SourcePath = sp.String
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListNodesByKind returns every graph_node row with the given kind. Companion
// to ListNodesByKindWithMetadataContaining for callers that need all nodes of
// a kind regardless of metadata content.
func (s *Store) ListNodesByKind(kind string) ([]Node, error) {
	rows, err := s.db.Query(
		`SELECT id, kind, label, source_path, metadata FROM graph_nodes WHERE kind = ?`, kind)
	if err != nil {
		return nil, fmt.Errorf("list_nodes_by_kind: %w", err)
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		var sp sql.NullString
		if err := rows.Scan(&n.ID, &n.Kind, &n.Label, &sp, &n.Metadata); err != nil {
			return nil, fmt.Errorf("list_nodes_by_kind scan: %w", err)
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
// kinds: optional variadic edge-kind filter. Zero kinds = no filter (returns every
// edge, backwards-compat with the original two-arg call site). Multiple kinds use
// SQL `kind IN (...)`. Empty strings in the filter are ignored.
func (s *Store) Neighbors(nodeID, direction string, kinds ...string) ([]Edge, error) {
	var base string
	var args []any
	switch direction {
	case "out":
		base = `SELECT from_node, to_node, kind, weight, metadata FROM graph_edges WHERE from_node = ?`
		args = []any{nodeID}
	case "in":
		base = `SELECT from_node, to_node, kind, weight, metadata FROM graph_edges WHERE to_node = ?`
		args = []any{nodeID}
	default:
		base = `SELECT from_node, to_node, kind, weight, metadata FROM graph_edges WHERE from_node = ? OR to_node = ?`
		args = []any{nodeID, nodeID}
	}

	// Strip empty strings so callers can pass a flag value unchecked (e.g.,
	// `Neighbors(id, dir, "")` — a pattern the CLI ends up doing when its
	// flag slice is empty). Empty kinds in the IN list would match no rows,
	// which is worse than being ignored.
	var filtered []string
	for _, k := range kinds {
		if k != "" {
			filtered = append(filtered, k)
		}
	}

	query := base
	if len(filtered) > 0 {
		var b strings.Builder
		b.WriteString(query)
		b.WriteString(" AND kind IN (")
		for i, k := range filtered {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("?")
			args = append(args, k)
		}
		b.WriteString(")")
		query = b.String()
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

// DeleteSymbolsForFile removes every symbol-kind graph_node whose source_path
// matches filePath, plus (via the FK cascade on graph_edges) their incident
// edges. Called by the graph pass when a file's content_hash changes — stale
// symbol nodes from the previous parse get wiped before the new parse
// reprojects fresh ones, so renamed/removed symbols don't linger.
//
// Scoped to kind='symbol' so the code_file node and its outgoing "mentions"
// / "shared_code_ref" edges (populated by the docs pass) are preserved.
func (s *Store) DeleteSymbolsForFile(filePath string) error {
	_, err := s.db.Exec(
		`DELETE FROM graph_nodes WHERE source_path = ? AND kind = ?`,
		filePath, NodeKindSymbol,
	)
	if err != nil {
		return fmt.Errorf("delete_symbols_for_file: %w", err)
	}
	return nil
}

// AffectedSourcePathsForFile returns the distinct source_paths of graph nodes
// that share an edge with any symbol node belonging to filePath. Used by the
// graph pass to identify files that hold cross-file edges pointing into
// filePath's symbols; those files are force-reindexed after filePath is
// reindexed so any edges lost via DeleteSymbolsForFile's FK cascade are
// reconstructed.
//
// Both edge directions are checked (from_node and to_node) so the result
// covers edges originating at filePath's symbols (e.g. a child class in
// fileA calling a method on fileB's symbol) as well as edges terminating at
// them (e.g. a child class in fileB inheriting from fileA's base class).
//
// filePath itself, nodes with a NULL/empty source_path (placeholder nodes),
// and nodes with the same source_path as filePath are excluded so callers
// only receive genuinely OTHER files to reconstitute.
func (s *Store) AffectedSourcePathsForFile(filePath string) ([]string, error) {
	rows, err := s.db.Query(`
SELECT DISTINCT gn.source_path
FROM graph_nodes gn
WHERE gn.id IN (
  SELECT ge.from_node
  FROM graph_edges ge
  WHERE ge.to_node IN (
    SELECT id FROM graph_nodes WHERE source_path = ? AND kind = ?
  )
  UNION
  SELECT ge.to_node
  FROM graph_edges ge
  WHERE ge.from_node IN (
    SELECT id FROM graph_nodes WHERE source_path = ? AND kind = ?
  )
)
AND gn.source_path IS NOT NULL
AND gn.source_path != ''
AND gn.source_path != ?
`, filePath, NodeKindSymbol, filePath, NodeKindSymbol, filePath)
	if err != nil {
		return nil, fmt.Errorf("affected_source_paths_for_file: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sp string
		if err := rows.Scan(&sp); err != nil {
			return nil, fmt.Errorf("affected_source_paths_for_file scan: %w", err)
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}

// ListOrphanNodes returns every graph_node whose kind is in the given set and
// which has neither incoming nor outgoing edges. Pass nil/empty kinds to
// scan across every kind (matches `librarian gc --kinds=all`). Results are
// ordered by id for deterministic output.
//
// "Orphan" here means graph-topological: zero incident edges. Not "stale on
// disk" — DeleteSymbolsForFile / DeleteGeneratedFile handle file-backed
// staleness. Orphans accumulate when Reference.Target shapes change across
// schema evolutions (lib-o8m renamed sym:.utils → sym:mypkg.utils, leaving
// old nodes unreachable).
func (s *Store) ListOrphanNodes(kinds []string) ([]Node, error) {
	query, args := orphanNodeQuery(kinds, "SELECT id, kind, label, source_path, metadata FROM graph_nodes")
	query += " ORDER BY id"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list_orphan_nodes: %w", err)
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var n Node
		var sp sql.NullString
		if err := rows.Scan(&n.ID, &n.Kind, &n.Label, &sp, &n.Metadata); err != nil {
			return nil, fmt.Errorf("list_orphan_nodes scan: %w", err)
		}
		if sp.Valid {
			n.SourcePath = sp.String
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// DeleteOrphanNodes removes every graph_node whose kind is in the given set
// and which has neither incoming nor outgoing edges. Uses a single
// DELETE ... RETURNING id statement so identify + delete happen atomically
// under SQLite's row-level mutex — the returned ids list is exactly what was
// deleted, regardless of concurrent writers. (A SELECT-then-DELETE split
// would need an explicit BEGIN IMMEDIATE transaction plus careful ordering
// to match the same guarantee.)
//
// RETURNING requires SQLite ≥ 3.35 (2021); mattn/go-sqlite3 bundles a
// modern SQLite, so this is available across every platform we ship to.
//
// Returns the deleted ids in alphabetical order for stable CLI output. Pass
// nil/empty kinds to sweep every kind.
func (s *Store) DeleteOrphanNodes(kinds []string) ([]string, error) {
	query, args := orphanNodeQuery(kinds, "DELETE FROM graph_nodes")
	query += " RETURNING id"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("delete_orphan_nodes: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("delete_orphan_nodes scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("delete_orphan_nodes rows: %w", err)
	}
	sort.Strings(ids)
	return ids, nil
}

// orphanNodeQuery builds the shared predicate: kind filter plus "no incident
// edges" via two NOT EXISTS clauses (indexed on graph_edges.from_node /
// to_node). leader is the full SQL prefix up to (and not including) the
// shared WHERE clause — ListOrphanNodes supplies a full SELECT, while
// DeleteOrphanNodes supplies a DELETE. orderBy is appended for reads;
// DELETE statements can't carry ORDER BY in SQLite so callers that run a
// DELETE with RETURNING sort the returned ids in Go.
func orphanNodeQuery(kinds []string, leader string) (string, []any) {
	var b strings.Builder
	b.WriteString(leader)
	b.WriteString(" WHERE NOT EXISTS (SELECT 1 FROM graph_edges WHERE from_node = graph_nodes.id) AND NOT EXISTS (SELECT 1 FROM graph_edges WHERE to_node = graph_nodes.id)")
	var args []any
	if len(kinds) > 0 {
		b.WriteString(" AND kind IN (")
		for i, k := range kinds {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString("?")
			args = append(args, k)
		}
		b.WriteString(")")
	}
	return b.String(), args
}

// DeleteGeneratedFile atomically removes every artefact the graph pass
// produced for filePath: symbol nodes + their edges (via cascade), the
// code_file graph node + its edges (via cascade), and the code_files row.
// Used when a previously-indexed file acquires a generator banner — the
// three deletes must land together so a crash between them doesn't leave
// orphaned symbol nodes or a stale code_file row pointing at a file the
// graph no longer owns. Name mirrors sibling delete methods
// (DeleteNode / DeleteSymbolsForFile / DeleteCodeFile).
func (s *Store) DeleteGeneratedFile(filePath string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("delete_generated_file begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(
		`DELETE FROM graph_nodes WHERE source_path = ? AND kind = ?`,
		filePath, NodeKindSymbol,
	); err != nil {
		return fmt.Errorf("delete_generated_file symbols: %w", err)
	}
	if _, err := tx.Exec(
		`DELETE FROM graph_nodes WHERE id = ?`, CodeFileNodeID(filePath),
	); err != nil {
		return fmt.Errorf("delete_generated_file code_file node: %w", err)
	}
	if _, err := tx.Exec(
		`DELETE FROM code_files WHERE file_path = ?`, filePath,
	); err != nil {
		return fmt.Errorf("delete_generated_file code_files row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete_generated_file commit: %w", err)
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
