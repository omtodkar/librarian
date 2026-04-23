package report

import (
	"encoding/json"

	"librarian/internal/analytics"
)

// SchemaVersion is the version tag stamped into every graph.json. Bump
// when the on-disk shape changes in a way consumers need to notice.
// Downstream tooling (lib-652 install commands, lib-e49 platform
// adapters, external graph viewers) can branch on it.
const SchemaVersion = 1

// jsonDoc is the on-disk shape of graph.json. It carries raw nodes + edges
// (so consumers can re-render the graph without re-querying the store)
// plus an analytics overlay whose communities.nodes embed {id, label,
// kind} so consumers don't need to cross-reference the top-level nodes
// array to resolve community membership.
type jsonDoc struct {
	SchemaVersion int           `json:"schema_version"`
	GeneratedAt   string        `json:"generated_at"`
	Totals        jsonTotals    `json:"totals"`
	Nodes         []jsonNode    `json:"nodes"`
	Edges         []jsonEdge    `json:"edges"`
	Analytics     jsonAnalytics `json:"analytics"`
}

type jsonTotals struct {
	Nodes       int `json:"nodes"`
	Edges       int `json:"edges"`
	Communities int `json:"communities"`
}

type jsonNode struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	SourcePath  string `json:"source_path,omitempty"`
	CommunityID int    `json:"community_id"`
	Degree      int    `json:"degree"`
}

type jsonEdge struct {
	From   string  `json:"from"`
	To     string  `json:"to"`
	Kind   string  `json:"kind"`
	Weight float64 `json:"weight"`
	// CrossCommunity flags edges whose endpoints are in different
	// communities — a quick cue for visualisers to highlight them.
	CrossCommunity bool `json:"cross_community"`
}

// jsonCommunity wraps analytics.Community but replaces the bare node-id
// list with embedded {id, label, kind} records. Consumers that want
// per-member display data no longer have to cross-reference the top-level
// nodes slice.
type jsonCommunity struct {
	ID    int                 `json:"id"`
	Label string              `json:"label"`
	Nodes []jsonCommunityNode `json:"nodes"`
}

type jsonCommunityNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}

type jsonAnalytics struct {
	Communities           []jsonCommunity                  `json:"communities"`
	GodNodes              []analytics.GodNode              `json:"god_nodes"`
	SurprisingConnections []analytics.SurprisingConnection `json:"surprising_connections"`
	SuggestedQuestions    []analytics.SuggestedQuestion    `json:"suggested_questions"`
}

// RenderJSON produces graph.json content. Fully deterministic given
// in.GeneratedAt, in.Nodes/Edges, and the analytics report.
func RenderJSON(in *Input) []byte {
	r := in.Analytics

	nodeToCommunity := buildNodeToCommunity(r.Communities)
	degree := analytics.BuildDegree(in.Edges)
	labels := analytics.BuildLabels(in.Nodes)
	kinds := make(map[string]string, len(in.Nodes))
	for _, n := range in.Nodes {
		kinds[n.ID] = n.Kind
	}

	doc := jsonDoc{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   in.GeneratedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Totals: jsonTotals{
			Nodes:       r.TotalNodes,
			Edges:       r.TotalEdges,
			Communities: len(r.Communities),
		},
		Nodes: make([]jsonNode, 0, len(in.Nodes)),
		Edges: make([]jsonEdge, 0, len(in.Edges)),
		Analytics: jsonAnalytics{
			Communities:           enrichCommunities(r.Communities, labels, kinds),
			GodNodes:              r.GodNodes,
			SurprisingConnections: r.SurprisingConnections,
			SuggestedQuestions:    r.SuggestedQuestions,
		},
	}
	for _, n := range in.Nodes {
		comm := analytics.CommunityNone
		if c, ok := nodeToCommunity[n.ID]; ok {
			comm = c
		}
		doc.Nodes = append(doc.Nodes, jsonNode{
			ID:          n.ID,
			Kind:        n.Kind,
			Label:       n.Label,
			SourcePath:  n.SourcePath,
			CommunityID: comm,
			Degree:      degree[n.ID],
		})
	}
	for _, e := range in.Edges {
		fromComm, okF := nodeToCommunity[e.From]
		toComm, okT := nodeToCommunity[e.To]
		doc.Edges = append(doc.Edges, jsonEdge{
			From:           e.From,
			To:             e.To,
			Kind:           e.Kind,
			Weight:         e.Weight,
			CrossCommunity: okF && okT && fromComm != toComm,
		})
	}

	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		// Unreachable — every field is a concrete type.
		panic(err)
	}
	return append(b, '\n')
}

// enrichCommunities converts analytics.Community{Nodes []string} into
// jsonCommunity{Nodes []jsonCommunityNode} so downstream JSON readers can
// render community members without having to look up labels and kinds in
// the top-level nodes array.
func enrichCommunities(comms []analytics.Community, labels, kinds map[string]string) []jsonCommunity {
	out := make([]jsonCommunity, 0, len(comms))
	for _, c := range comms {
		nodes := make([]jsonCommunityNode, 0, len(c.Nodes))
		for _, id := range c.Nodes {
			nodes = append(nodes, jsonCommunityNode{
				ID:    id,
				Label: labels[id],
				Kind:  kinds[id],
			})
		}
		out = append(out, jsonCommunity{
			ID:    c.ID,
			Label: c.Label,
			Nodes: nodes,
		})
	}
	return out
}
