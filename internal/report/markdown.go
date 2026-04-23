package report

import (
	"fmt"
	"strings"

	"librarian/internal/analytics"
)

// RenderMarkdown produces GRAPH_REPORT.md content — a summary suitable for
// both humans scrolling the file and assistants reading it before doing
// a grep. The shape deliberately mirrors graphify's GRAPH_REPORT.md so
// assistant hooks that tell the LLM "read GRAPH_REPORT.md first" get
// familiar territory.
func RenderMarkdown(in *Input) []byte {
	var b strings.Builder
	r := in.Analytics

	fmt.Fprintf(&b, "# Graph Report\n\n")
	fmt.Fprintf(&b, "Generated: %s\n\n", in.GeneratedAt.UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&b, "- **Nodes:** %d\n", r.TotalNodes)
	fmt.Fprintf(&b, "- **Edges:** %d\n", r.TotalEdges)
	fmt.Fprintf(&b, "- **Communities:** %d\n\n", len(r.Communities))

	writeGodNodes(&b, r)
	writeCommunities(&b, r)
	writeSurprisingConnections(&b, r, in)
	writeSuggestedQuestions(&b, r)

	return []byte(b.String())
}

func writeGodNodes(b *strings.Builder, r *analytics.Report) {
	fmt.Fprintf(b, "## God Nodes\n\n")
	if len(r.GodNodes) == 0 {
		fmt.Fprintln(b, "No high-degree nodes yet — the graph is too sparse.")
		fmt.Fprintln(b)
		return
	}
	fmt.Fprintln(b, "Highest-connected nodes — everything routes through these.")
	fmt.Fprintln(b)
	fmt.Fprintln(b, "| Label | Kind | Degree | Community |")
	fmt.Fprintln(b, "|---|---|---:|---:|")
	for _, g := range r.GodNodes {
		comm := "—"
		if g.CommunityID != analytics.CommunityNone {
			comm = fmt.Sprintf("%d", g.CommunityID)
		}
		fmt.Fprintf(b, "| %s | %s | %d | %s |\n",
			escapeMarkdownTableCell(g.Label), g.Kind, g.Degree, comm)
	}
	fmt.Fprintln(b)
}

func writeCommunities(b *strings.Builder, r *analytics.Report) {
	fmt.Fprintf(b, "## Communities\n\n")
	if len(r.Communities) == 0 {
		fmt.Fprintln(b, "No communities detected.")
		fmt.Fprintln(b)
		return
	}
	for _, c := range r.Communities {
		fmt.Fprintf(b, "### %s (%d nodes)\n\n", c.Label, len(c.Nodes))
		// Show the first handful of members; a human can use `librarian
		// neighbors` or graph.json for the full list.
		const sampleSize = 8
		sample := c.Nodes
		truncated := false
		if len(sample) > sampleSize {
			sample = sample[:sampleSize]
			truncated = true
		}
		fmt.Fprintf(b, "Members: %s", strings.Join(sample, ", "))
		if truncated {
			fmt.Fprintf(b, " …and %d more", len(c.Nodes)-sampleSize)
		}
		fmt.Fprintln(b)
		fmt.Fprintln(b)
	}
}

func writeSurprisingConnections(b *strings.Builder, r *analytics.Report, in *Input) {
	fmt.Fprintf(b, "## Surprising Connections\n\n")
	if len(r.SurprisingConnections) == 0 {
		fmt.Fprintln(b, "No cross-community edges — nothing bridges the clusters.")
		fmt.Fprintln(b)
		return
	}
	fmt.Fprintln(b, "Cross-community edges — unusual because they link otherwise-separate clusters.")
	fmt.Fprintln(b)

	// Reuse analytics' helpers so the label / kind-count derivation
	// stays consistent with the internal analytics pass. Previously these
	// were reimplemented inline and drifted trivially over time.
	labels := analytics.BuildLabels(in.Nodes)
	kindCount := make(map[string]int, len(in.Edges))
	for _, e := range in.Edges {
		kindCount[e.Kind]++
	}

	for i, sc := range r.SurprisingConnections {
		fmt.Fprintf(b, "%d. %s\n", i+1, analytics.RenderSurprisingWhy(sc, labels, kindCount))
	}
	fmt.Fprintln(b)
}

func writeSuggestedQuestions(b *strings.Builder, r *analytics.Report) {
	fmt.Fprintf(b, "## Suggested Questions\n\n")
	if len(r.SuggestedQuestions) == 0 {
		fmt.Fprintln(b, "No automatic questions available for this graph shape.")
		fmt.Fprintln(b)
		return
	}
	fmt.Fprintln(b, "Questions the graph's structure is uniquely positioned to answer. Try them via `librarian query` or the /librarian skill:")
	fmt.Fprintln(b)
	for i, q := range r.SuggestedQuestions {
		fmt.Fprintf(b, "%d. %s\n", i+1, q.Text)
	}
	fmt.Fprintln(b)
}

// escapeMarkdownTableCell sanitises a value for use in a markdown table
// cell. Pipes are backslash-escaped so they don't terminate the cell; CR
// and LF are replaced with spaces because a newline inside a cell splits
// the table row across lines and breaks every downstream renderer
// (GitHub, VS Code preview, LLMs) from that row forward.
func escapeMarkdownTableCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "|", `\|`)
}
