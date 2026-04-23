package report

import (
	"fmt"
	"html"
	"math"
	"sort"
	"strings"

	"librarian/internal/analytics"
)

// RenderHTML produces graph.html — a truly self-contained SVG visualisation
// with inline CSS + ~40 lines of JavaScript for hover tooltips and node
// highlight-on-click. No external deps; opens offline; works in any modern
// browser.
//
// Node positions are pre-computed in Go (see computeLayout); the SVG is
// static markup with a small script attached. Trade-off: no drag-to-
// rearrange, but the file stays small and portable. A richer interactive
// version using vendored vis-network can land as a follow-up if the static
// rendering proves inadequate.
func RenderHTML(in *Input) []byte {
	cfg := defaultLayoutConfig()
	pos := computeLayout(in.Nodes, in.Edges, cfg)

	// Shared helpers — community membership + degree — identical to what
	// the JSON renderer uses. Community IDs past the palette wrap around
	// in communityColor.
	nodeToCommunity := buildNodeToCommunity(in.Analytics.Communities)
	degree := analytics.BuildDegree(in.Edges)

	// Edges first so nodes render on top.
	var edgesSVG strings.Builder
	for _, e := range in.Edges {
		if e.From == e.To {
			continue
		}
		p1, ok1 := pos[e.From]
		p2, ok2 := pos[e.To]
		if !ok1 || !ok2 {
			continue
		}
		cls := "edge"
		fromComm, okF := nodeToCommunity[e.From]
		toComm, okT := nodeToCommunity[e.To]
		if okF && okT && fromComm != toComm {
			cls += " edge-cross"
		}
		fmt.Fprintf(&edgesSVG,
			`<line class=%q x1=%q y1=%q x2=%q y2=%q><title>%s → %s (%s)</title></line>`,
			cls,
			formatFloat(p1.X), formatFloat(p1.Y),
			formatFloat(p2.X), formatFloat(p2.Y),
			html.EscapeString(e.From), html.EscapeString(e.To), html.EscapeString(e.Kind),
		)
		edgesSVG.WriteByte('\n')
	}

	// Nodes, sorted by id for deterministic output.
	nodeOrder := make([]int, len(in.Nodes))
	for i := range nodeOrder {
		nodeOrder[i] = i
	}
	sort.Slice(nodeOrder, func(i, j int) bool {
		return in.Nodes[nodeOrder[i]].ID < in.Nodes[nodeOrder[j]].ID
	})

	var nodesSVG strings.Builder
	for _, i := range nodeOrder {
		n := in.Nodes[i]
		p, ok := pos[n.ID]
		if !ok {
			continue
		}
		label := n.Label
		if label == "" {
			label = n.ID
		}
		r := nodeRadius(degree[n.ID])
		comm := nodeToCommunity[n.ID]
		fmt.Fprintf(&nodesSVG,
			`<circle class="node" cx=%q cy=%q r=%q data-id=%q data-label=%q data-kind=%q data-degree=%q fill=%q><title>%s</title></circle>`,
			formatFloat(p.X), formatFloat(p.Y), formatFloat(r),
			html.EscapeString(n.ID), html.EscapeString(label), html.EscapeString(n.Kind),
			fmt.Sprintf("%d", degree[n.ID]),
			communityColor(comm),
			html.EscapeString(fmt.Sprintf("%s (%s, degree %d)", label, n.Kind, degree[n.ID])),
		)
		nodesSVG.WriteByte('\n')
	}

	generated := in.GeneratedAt.UTC().Format("2006-01-02 15:04 UTC")

	r := strings.NewReplacer(
		"{{WIDTH}}", fmt.Sprintf("%d", int(cfg.Width)),
		"{{HEIGHT}}", fmt.Sprintf("%d", int(cfg.Height)),
		"{{GENERATED}}", html.EscapeString(generated),
		"{{NODE_COUNT}}", fmt.Sprintf("%d", in.Analytics.TotalNodes),
		"{{EDGE_COUNT}}", fmt.Sprintf("%d", in.Analytics.TotalEdges),
		"{{COMMUNITY_COUNT}}", fmt.Sprintf("%d", len(in.Analytics.Communities)),
		"{{EDGES_SVG}}", edgesSVG.String(),
		"{{NODES_SVG}}", nodesSVG.String(),
	)
	return []byte(r.Replace(htmlTemplate))
}

// nodeRadius scales node size with degree. Capped to keep the viz readable
// when god nodes have orders-of-magnitude more edges than typical nodes.
func nodeRadius(degree int) float64 {
	const minR, maxR = 4.0, 18.0
	r := minR + math.Log1p(float64(degree))*2.5
	if r > maxR {
		return maxR
	}
	return r
}

// communityColor maps a community id to a CSS colour from a small
// qualitative palette. Wraps around past the palette length, so graphs
// with more than N communities reuse colours. Chosen to be distinguishable
// in both light and dark backgrounds.
func communityColor(id int) string {
	palette := []string{
		"#4e79a7", "#f28e2b", "#e15759", "#76b7b2", "#59a14f",
		"#edc948", "#b07aa1", "#ff9da7", "#9c755f", "#bab0ac",
	}
	if id < 0 {
		return "#999999"
	}
	return palette[id%len(palette)]
}

// formatFloat renders a float for SVG coordinates with 1-decimal precision.
// Shaves bytes vs fmt.Sprintf("%f") without losing visual quality.
func formatFloat(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", v), "0"), ".")
}

// htmlTemplate uses {{PLACEHOLDER}}-style markers substituted by
// strings.NewReplacer in RenderHTML. Simpler and safer than juggling
// positional fmt verbs across CSS/SVG/JS.
const htmlTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Librarian Graph</title>
<style>
  body {
    font: 14px/1.4 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    margin: 0; padding: 24px; background: #fafafa; color: #222;
  }
  header { max-width: {{WIDTH}}px; margin: 0 auto 16px; }
  header h1 { margin: 0 0 6px; font-size: 20px; }
  header p { margin: 0; color: #666; }
  .wrap { max-width: {{WIDTH}}px; margin: 0 auto; background: white; border: 1px solid #e0e0e0; border-radius: 6px; padding: 0; overflow: hidden; }
  svg { display: block; background: #fff; cursor: grab; }
  .edge { stroke: #cbd5e1; stroke-width: 1; stroke-opacity: 0.7; }
  .edge-cross { stroke: #f59e0b; stroke-width: 1.4; stroke-opacity: 0.9; }
  .node { stroke: rgba(0,0,0,0.3); stroke-width: 1; cursor: pointer; transition: stroke-width 0.1s; }
  .node:hover { stroke: #000; stroke-width: 2.5; }
  .node.selected { stroke: #000; stroke-width: 3; }
  #tooltip {
    position: fixed; background: #111; color: #fff; padding: 4px 8px;
    border-radius: 3px; font-size: 12px; pointer-events: none; display: none; z-index: 10;
  }
  #details {
    max-width: {{WIDTH}}px; margin: 12px auto 0; padding: 10px 14px; background: #fff;
    border: 1px solid #e0e0e0; border-radius: 6px; min-height: 28px; font-size: 13px; color: #444;
  }
  #details:empty::before { content: "Click a node for details."; color: #999; }
</style>
</head>
<body>
<header>
  <h1>Librarian Graph</h1>
  <p>Generated {{GENERATED}} · {{NODE_COUNT}} nodes · {{EDGE_COUNT}} edges · {{COMMUNITY_COUNT}} communities</p>
</header>
<div class="wrap">
<svg width="{{WIDTH}}" height="{{HEIGHT}}" viewBox="0 0 {{WIDTH}} {{HEIGHT}}">
<g class="edges">
{{EDGES_SVG}}</g>
<g class="nodes">
{{NODES_SVG}}</g>
</svg>
</div>
<div id="details"></div>
<div id="tooltip"></div>
<script>
(function(){
  const tip = document.getElementById('tooltip');
  const details = document.getElementById('details');
  let selected = null;

  document.querySelectorAll('.node').forEach(n => {
    n.addEventListener('mouseenter', e => {
      tip.textContent = n.dataset.label + ' (' + n.dataset.kind + ', degree ' + n.dataset.degree + ')';
      tip.style.display = 'block';
    });
    n.addEventListener('mousemove', e => {
      tip.style.left = (e.clientX + 12) + 'px';
      tip.style.top  = (e.clientY + 12) + 'px';
    });
    n.addEventListener('mouseleave', () => { tip.style.display = 'none'; });
    n.addEventListener('click', e => {
      if (selected) selected.classList.remove('selected');
      n.classList.add('selected');
      selected = n;
      details.textContent = n.dataset.id + ' — ' + n.dataset.label + ' · kind=' + n.dataset.kind + ' · degree=' + n.dataset.degree;
    });
  });
})();
</script>
</body>
</html>
`
