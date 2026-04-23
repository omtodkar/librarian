package analytics

import (
	"sort"

	"librarian/internal/store"
)

// computeGodNodes ranks nodes by their entry in the shared degree map.
// Ties broken by node id for stable output. At most `top` god nodes are
// returned. Nodes with zero degree are omitted — they're not connected to
// anything, so calling them "gods" is misleading.
//
// Centrality is deliberately simple: it's cheap, correlates well with
// "everything connects through this" intuition, and doesn't require
// eigenvector / PageRank iteration that adds noise on small graphs.
func computeGodNodes(
	nodes []store.Node,
	degree map[string]int,
	labels map[string]string,
	kinds map[string]string,
	nodeToCommunity map[string]int,
	top int,
) []GodNode {
	if len(nodes) == 0 {
		return nil
	}
	if top <= 0 {
		top = 10
	}

	godNodes := make([]GodNode, 0, len(nodes))
	for _, n := range nodes {
		d := degree[n.ID]
		if d == 0 {
			continue
		}
		comm, ok := nodeToCommunity[n.ID]
		if !ok {
			comm = CommunityNone
		}
		godNodes = append(godNodes, GodNode{
			NodeID:      n.ID,
			Label:       labels[n.ID],
			Kind:        kinds[n.ID],
			Degree:      d,
			CommunityID: comm,
		})
	}

	sort.Slice(godNodes, func(i, j int) bool {
		if godNodes[i].Degree != godNodes[j].Degree {
			return godNodes[i].Degree > godNodes[j].Degree
		}
		return godNodes[i].NodeID < godNodes[j].NodeID
	})

	if len(godNodes) > top {
		godNodes = godNodes[:top]
	}
	return godNodes
}
