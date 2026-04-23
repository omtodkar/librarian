package analytics

import (
	"math/rand/v2"
	"sort"

	"gonum.org/v1/gonum/graph/community"
	"gonum.org/v1/gonum/graph/simple"

	"librarian/internal/store"
)

// gonumGraph holds the store-id <-> gonum-id mapping built alongside a
// simple.UndirectedGraph.
type gonumGraph struct {
	g      *simple.UndirectedGraph
	idOf   map[string]int64 // store node id -> gonum node id
	nameOf map[int64]string // reverse
}

// buildGonumGraph converts the store's nodes + edges into an undirected
// gonum graph suitable for Louvain. Edge direction is collapsed because
// Louvain operates on undirected modularity; librarian's "shared_code_ref"
// and "mentions" edges are semantically symmetric enough that ignoring
// direction produces sensible communities.
//
// Self-loops and duplicate edges (same endpoints) are deduplicated since
// gonum's simple graph allows at most one edge per node pair. Degree
// tallying happens elsewhere (buildDegree in analytics.go) so this
// function is purely about building the Louvain input graph.
func buildGonumGraph(nodes []store.Node, edges []store.Edge) *gonumGraph {
	ctx := &gonumGraph{
		g:      simple.NewUndirectedGraph(),
		idOf:   make(map[string]int64, len(nodes)),
		nameOf: make(map[int64]string, len(nodes)),
	}
	var next int64
	for _, n := range nodes {
		id := next
		next++
		ctx.g.AddNode(simple.Node(id))
		ctx.idOf[n.ID] = id
		ctx.nameOf[id] = n.ID
	}
	for _, e := range edges {
		from, ok := ctx.idOf[e.From]
		if !ok {
			continue
		}
		to, ok := ctx.idOf[e.To]
		if !ok {
			continue
		}
		if from == to {
			continue
		}
		if ctx.g.Edge(from, to) == nil {
			ctx.g.SetEdge(simple.Edge{F: ctx.g.Node(from), T: ctx.g.Node(to)})
		}
	}
	return ctx
}

// detectCommunities runs gonum's Louvain modularity-optimisation pass and
// returns the resulting partition. Uses the shared label / degree maps so
// community labels agree with god-node rankings.
//
// The seed is fixed so the same graph produces the same communities across
// runs — analytics output feeds GRAPH_REPORT.md, which needs to be stable
// for a given corpus.
//
// Note: this is Louvain, not Leiden. Leiden improves connectivity guarantees
// for the resulting communities but isn't available in gonum; for MVP
// Louvain is adequate and swapping in Leiden later is an internal detail
// of this function.
func detectCommunities(ctx *gonumGraph, nodes []store.Node, labels map[string]string, degree map[string]int) []Community {
	if len(nodes) == 0 {
		return nil
	}
	src := rand.NewPCG(42, 42)
	reduced := community.Modularize(ctx.g, 1.0, src)
	structure := reduced.Communities()

	comms := make([]Community, 0, len(structure))
	for i, members := range structure {
		if len(members) == 0 {
			continue
		}
		ids := make([]string, 0, len(members))
		for _, n := range members {
			if name, ok := ctx.nameOf[n.ID()]; ok {
				ids = append(ids, name)
			}
		}
		if len(ids) == 0 {
			continue
		}
		sort.Strings(ids)
		comms = append(comms, Community{
			ID:    i,
			Nodes: ids,
			Label: pickCommunityLabel(ids, labels, degree),
		})
	}
	return comms
}

// pickCommunityLabel returns the display label for a community — the label
// of its most-connected member, with the namespaced prefix stripped so
// downstream consumers don't see "doc:…" / "file:…" headings. Ties broken
// by node id for determinism.
func pickCommunityLabel(ids []string, labels map[string]string, degree map[string]int) string {
	best, bestDegree := ids[0], degree[ids[0]]
	for _, id := range ids[1:] {
		d := degree[id]
		if d > bestDegree || (d == bestDegree && id < best) {
			best = id
			bestDegree = d
		}
	}
	label := labels[best]
	if label == "" {
		label = best
	}
	return displayID(label)
}
