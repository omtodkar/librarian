// Package report renders the analytics Report and raw graph into the three
// artifacts librarian writes to .librarian/out/:
//
//   - GRAPH_REPORT.md — human- and assistant-readable markdown summary
//   - graph.html      — truly self-contained SVG viz (no external deps)
//   - graph.json      — structured dump for programmatic consumers
//
// Nothing in this package touches the store directly — callers pass in
// (*analytics.Report, []store.Node, []store.Edge) so renderers are pure
// functions and trivially testable.
package report

import (
	"time"

	"librarian/internal/analytics"
	"librarian/internal/store"
)

// Input bundles the three things the renderers need. Pre-bundling keeps
// every RenderX signature identical and makes callers obvious.
type Input struct {
	Analytics *analytics.Report
	Nodes     []store.Node
	Edges     []store.Edge
	// GeneratedAt is the timestamp rendered in report headers. Passed in
	// explicitly (rather than using time.Now inside renderers) so tests can
	// produce deterministic output.
	GeneratedAt time.Time
}

// buildNodeToCommunity inverts the Communities list into a node-id -> id
// lookup. Shared between JSON and HTML renderers; returns an empty map
// when communities is nil.
func buildNodeToCommunity(communities []analytics.Community) map[string]int {
	out := make(map[string]int)
	for _, c := range communities {
		for _, id := range c.Nodes {
			out[id] = c.ID
		}
	}
	return out
}
