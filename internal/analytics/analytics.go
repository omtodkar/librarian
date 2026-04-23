// Package analytics runs topology-based analyses over the graph store and
// produces a Report describing communities, god nodes, surprising
// cross-community connections, and suggested questions the graph is
// uniquely positioned to answer.
//
// The analyses are deliberately embedding-free: they operate only on
// graph_nodes + graph_edges produced by the indexer pipeline. Report is a
// pure-data DTO consumed by the outputs layer (GRAPH_REPORT.md, graph.html,
// graph.json — see bd lib-u55) and by the skill + install commands (lib-652).
//
// Degree semantics used throughout: each store edge contributes +1 to each
// endpoint's degree, with self-loops counted once per endpoint-pair (not
// doubled). Duplicate typed edges between the same pair are each counted,
// reflecting "more typed relationships = more connected" even though Louvain
// collapses duplicates when detecting communities.
package analytics

import (
	"fmt"

	"librarian/internal/store"
)

// CommunityNone is the sentinel CommunityID assigned to a god node that
// isn't part of any detected community (e.g., an isolated singleton).
// Exported so downstream consumers (lib-u55, lib-652) don't reinvent the
// check.
const CommunityNone = -1

// Report is the full analytics output for a single graph snapshot.
type Report struct {
	// Communities partition the graph into clusters by modularity-optimising
	// community detection. Each community is labelled with its
	// most-connected member (prefix-stripped for display).
	Communities []Community

	// GodNodes are the highest-connected nodes overall, ranked by degree.
	GodNodes []GodNode

	// SurprisingConnections are edges that bridge different communities.
	// Cross-community edges are more informative than intra-community ones
	// because they reveal hidden structure.
	SurprisingConnections []SurprisingConnection

	// SuggestedQuestions are structured natural-language probes the graph
	// can answer. Text is pre-rendered for plain-text fallback; Template +
	// Args keep the structure so html/json renderers can link the
	// arguments to nodes.
	SuggestedQuestions []SuggestedQuestion

	// TotalNodes / TotalEdges snapshot the input size for the header of
	// any downstream report.
	TotalNodes int
	TotalEdges int
}

// Community is a cluster of graph nodes produced by community detection.
type Community struct {
	ID    int      // stable index within this Report (not persisted)
	Label string   // human-friendly name (namespaced prefix stripped)
	Nodes []string // node IDs (namespaced, e.g. "doc:…", "file:…")
}

// GodNode is a high-centrality node singled out because "everything connects
// through it".
type GodNode struct {
	NodeID      string
	Label       string
	Kind        string
	Degree      int // in + out (see package doc for semantics)
	CommunityID int // index into Report.Communities; CommunityNone if isolated
}

// SurprisingConnection is a cross-community edge. Raw fields only — callers
// that want prose build it with RenderSurprisingWhy from what they render.
type SurprisingConnection struct {
	From            string
	To              string
	Kind            string
	FromCommunityID int
	ToCommunityID   int
	Score           float64 // higher = more surprising
}

// SuggestedQuestion is a template-generated probe query. Text is the
// pre-rendered form for plain display; Template + Args keep structure so
// html/json consumers can link arguments to the nodes they reference.
type SuggestedQuestion struct {
	Text     string
	Template string   // e.g. "What connects %s to %s?"
	Args     []string // e.g. ["Auth", "Session"]
	Kind     string   // "god_pair" | "bridge" | "cluster_tour"
}

// Result is the full output of Analyze: the Report plus the raw nodes +
// edges the analytics pass queried. Embedding *Report means callers that
// only care about the analyses (`result.Communities`, `result.GodNodes`)
// keep working without a refactor; callers that also need to render the
// raw graph (cmd/report.go, lib-652 install commands) read Nodes / Edges
// directly instead of re-querying the store and risking a WAL-snapshot
// mismatch.
type Result struct {
	*Report
	Nodes []store.Node
	Edges []store.Edge
}

// Analyze runs every analysis pass against the current graph store and
// returns a Result. Returns a Result with an empty-but-non-nil Report for
// an empty graph so downstream code can render "nothing to show" without
// nil-checking.
func Analyze(s *store.Store) (*Result, error) {
	nodes, err := s.ListNodes()
	if err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	edges, err := s.ListEdges()
	if err != nil {
		return nil, fmt.Errorf("list edges: %w", err)
	}

	report := &Report{
		TotalNodes: len(nodes),
		TotalEdges: len(edges),
	}
	result := &Result{Report: report, Nodes: nodes, Edges: edges}
	if len(nodes) == 0 {
		return result, nil
	}

	// One pass to build shared state — every downstream computation reads
	// these maps. Consolidated here to avoid the earlier drift where
	// buildGraph and computeGodNodes each recomputed degree with subtly
	// different semantics (self-loop handling, duplicate edges).
	labels := BuildLabels(nodes)
	kinds := buildKinds(nodes)
	degree := BuildDegree(edges)

	g := buildGonumGraph(nodes, edges)
	report.Communities = detectCommunities(g, nodes, labels, degree)

	nodeToCommunity := make(map[string]int, len(nodes))
	for _, c := range report.Communities {
		for _, id := range c.Nodes {
			nodeToCommunity[id] = c.ID
		}
	}

	report.GodNodes = computeGodNodes(nodes, degree, labels, kinds, nodeToCommunity, 10)
	report.SurprisingConnections = computeSurprisingConnections(edges, nodeToCommunity, 10)
	report.SuggestedQuestions = suggestQuestions(report, labels)

	return result, nil
}

// BuildLabels maps node id -> display label. Returns the stored Label when
// present, else the bare id. Exported so the report package shares the
// single source of truth for label derivation.
func BuildLabels(nodes []store.Node) map[string]string {
	out := make(map[string]string, len(nodes))
	for _, n := range nodes {
		if n.Label != "" {
			out[n.ID] = n.Label
		} else {
			out[n.ID] = n.ID
		}
	}
	return out
}

// buildKinds maps node id -> NodeKind. Unexported — no external caller
// currently needs it; export if the list grows.
func buildKinds(nodes []store.Node) map[string]string {
	out := make(map[string]string, len(nodes))
	for _, n := range nodes {
		out[n.ID] = n.Kind
	}
	return out
}

// BuildDegree counts edge incidences per node. Self-loops contribute +1 to
// the single endpoint (not +2); duplicate typed edges each contribute. See
// package doc for the rationale. Exported so the report package doesn't
// have to reimplement this and drift from the documented semantics.
func BuildDegree(edges []store.Edge) map[string]int {
	out := make(map[string]int)
	for _, e := range edges {
		out[e.From]++
		if e.From != e.To {
			out[e.To]++
		}
	}
	return out
}

// RenderSurprisingWhy formats a SurprisingConnection as human-readable prose
// for plain-text consumers (CLI, MCP tool output, fallback). HTML/JSON
// renderers should build their own presentation from the struct's fields
// instead of re-parsing this string. kindCount is the map from edge Kind
// to global occurrence count (callable via edgeKindCounts below).
func RenderSurprisingWhy(sc SurprisingConnection, labels map[string]string, kindCount map[string]int) string {
	from := labels[sc.From]
	if from == "" {
		from = sc.From
	}
	to := labels[sc.To]
	if to == "" {
		to = sc.To
	}
	return fmt.Sprintf(
		"%s -> %s is a %q edge crossing communities %d and %d (edge kind appears %d times total)",
		from, to, sc.Kind, sc.FromCommunityID, sc.ToCommunityID, kindCount[sc.Kind],
	)
}

// edgeKindCounts builds the kind → count map. Exported indirectly via
// RenderSurprisingWhy which callers may need to call with the map from
// analytics' own internal computation.
func edgeKindCounts(edges []store.Edge) map[string]int {
	out := make(map[string]int)
	for _, e := range edges {
		out[e.Kind]++
	}
	return out
}
