package analytics

import (
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"time"

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
// timeout bounds the Modularize call; on timeout the function returns
// (nil, reason) and downstream renderers surface the reason instead of an
// empty communities section. A timeout of 0 or any negative value disables
// the bound (legacy unbounded behaviour). The negative case exists because
// time.Duration is signed; treating <= 0 uniformly avoids surprising users
// who inadvertently configure a negative duration.
//
// Note: this is Louvain, not Leiden. Leiden improves connectivity guarantees
// for the resulting communities but isn't available in gonum; for MVP
// Louvain is adequate and swapping in Leiden later is an internal detail
// of this function.
func detectCommunities(ctx *gonumGraph, nodes []store.Node, labels map[string]string, degree map[string]int, timeout time.Duration) ([]Community, string) {
	if len(nodes) == 0 {
		return nil, ""
	}
	reduced, timedOut := runModularize(ctx, timeout)
	if timedOut {
		// Modularize is not context-aware, so the goroutine spawned by
		// runModularize cannot be cancelled — it keeps running until it
		// converges and then exits. That is acceptable: it has no shared
		// mutable state besides ctx.g (which we no longer read here), so
		// the orphan dies on its own. The cost is one wasted CPU core
		// until convergence; the alternative ("forcibly stop Louvain")
		// would require panic-on-timeout hacks and is explicitly rejected
		// in lib-7gyz's design notes.
		reason := fmt.Sprintf("louvain exceeded timeout of %s on %d nodes / %d edges",
			timeout, ctx.g.Nodes().Len(), ctx.g.Edges().Len())
		slog.Warn("community detection skipped",
			"timeout", timeout,
			"nodes", ctx.g.Nodes().Len(),
			"edges", ctx.g.Edges().Len(),
		)
		return nil, reason
	}
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
	return comms, ""
}

// runModularize wraps community.Modularize with a deadline. Returns
// (result, false) on success and (nil, true) on timeout. A timeout of 0 or
// any negative value disables the bound (legacy unbounded behaviour).
//
// Concurrency note: community.Modularize is not context-aware, so on
// timeout the spawned goroutine is orphaned — it will keep running until
// it converges. The orphan has no shared mutable state with the caller
// (the channel is buffered, so the late send is non-blocking), so the
// only cost is CPU time until natural convergence. Do not "fix" this by
// panicking the goroutine; lib-7gyz's design explicitly rejects that.
func runModularize(ctx *gonumGraph, timeout time.Duration) (community.ReducedGraph, bool) {
	src := rand.NewPCG(42, 42)
	if timeout <= 0 {
		// Legacy unbounded path. Identical to the pre-lib-7gyz call site.
		return community.Modularize(ctx.g, 1.0, src), false
	}
	// Buffered channel so the goroutine's send never blocks even when we've
	// already returned via the timeout path — that's what keeps the orphan
	// from leaking past its own convergence.
	//
	// Orphan lifetime and concurrency note: the goroutine below cannot be
	// cancelled after a timeout because community.Modularize is not
	// context-aware. It runs until convergence and then exits silently.
	//
	// The post-commit hook installed by `librarian install` holds a
	// PID-based lockfile (internal/install/templates/hooks/git-post-commit.sh)
	// for the entire `index && report` pipeline. As a result, hook-driven
	// invocations bound concurrent orphans to one — the next commit waits
	// for the lockfile to clear before starting a new pipeline.
	// Interactive invocations (`librarian report` from a terminal, watch
	// loops, ad-hoc CI scripts) have no equivalent guard. A pathological
	// retry-loop on a 30+ minute Louvain graph could accumulate orphan
	// goroutines that share CPU. The data path is safe (each call builds
	// its own *simple.UndirectedGraph); the resource pressure is the only
	// concern. We accept this in the current usage profile rather than
	// adding process-wide semaphore complexity.
	done := make(chan community.ReducedGraph, 1)
	go func() {
		// Modularize panics are not expected in normal operation; let them
		// propagate (the program will crash with a useful stack instead of
		// hanging forever). If hardening is needed later, wrap with recover
		// and surface as a separate skip reason.
		done <- community.Modularize(ctx.g, 1.0, src)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r, false
	case <-timer.C:
		return nil, true
	}
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
