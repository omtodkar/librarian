package report

import (
	"math"
	"math/rand/v2"

	"librarian/internal/store"
)

// position is a 2D point used for node placement in the SVG.
type position struct{ X, Y float64 }

// layoutConfig sets the viewport and physics knobs. Kept tiny and
// deterministic — the goal is a "good-enough" static render, not a
// publication-quality graph drawing.
type layoutConfig struct {
	Width       float64
	Height      float64
	Iterations  int
	Seed        uint64
	EdgeLen     float64 // preferred edge length (pixels)
	RepelScale  float64 // multiplier on the repulsion term
}

func defaultLayoutConfig() layoutConfig {
	return layoutConfig{
		Width:      1200,
		Height:     800,
		Iterations: 120,
		Seed:       42,
		EdgeLen:    80,
		RepelScale: 1.0,
	}
}

// computeLayout returns a position per node computed via a simple
// Fruchterman-Reingold force-directed algorithm. The seed is fixed so the
// same graph renders identically across runs — GRAPH_REPORT.md and
// graph.html are both committed to git by teams, and identical outputs on
// identical inputs keeps diffs meaningful.
//
// This is intentionally a MVP implementation: O(N²) per iteration for
// repulsion is fine for the tens-to-low-thousands of nodes librarian
// corpora produce. Spatial hashing / Barnes-Hut can come later if needed.
func computeLayout(nodes []store.Node, edges []store.Edge, cfg layoutConfig) map[string]position {
	if len(nodes) == 0 {
		return map[string]position{}
	}
	rng := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed))
	pos := make(map[string]position, len(nodes))
	for _, n := range nodes {
		pos[n.ID] = position{
			X: rng.Float64() * cfg.Width,
			Y: rng.Float64() * cfg.Height,
		}
	}

	// Build an adjacency map for efficient attractive-force updates.
	neighbours := make(map[string][]string, len(nodes))
	for _, e := range edges {
		if e.From == e.To {
			continue
		}
		neighbours[e.From] = append(neighbours[e.From], e.To)
		neighbours[e.To] = append(neighbours[e.To], e.From)
	}

	k := cfg.EdgeLen
	kSquared := k * k
	temperature := cfg.Width / 10
	cooling := temperature / float64(cfg.Iterations)

	// Stable iteration order for determinism (map iteration is random).
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	// nodes are passed in a stable order (ListNodes returns SQL rowid
	// order), which is already deterministic; but copy regardless so
	// nothing in this function mutates the caller's slice.

	for iter := 0; iter < cfg.Iterations; iter++ {
		disp := make(map[string]position, len(nodes))

		// Repulsion — every pair pushes apart.
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				a, b := ids[i], ids[j]
				dx := pos[a].X - pos[b].X
				dy := pos[a].Y - pos[b].Y
				distSq := dx*dx + dy*dy + 0.01 // +eps so we never /0
				dist := math.Sqrt(distSq)
				force := kSquared / distSq * cfg.RepelScale
				ax := dx / dist * force
				ay := dy / dist * force
				da, db := disp[a], disp[b]
				da.X += ax
				da.Y += ay
				db.X -= ax
				db.Y -= ay
				disp[a], disp[b] = da, db
			}
		}

		// Attraction — connected nodes pull together. Iterate via the
		// sorted ids slice rather than ranging `neighbours` directly: Go
		// map iteration is randomised per-process, and floating-point
		// addition isn't associative, so ranging the map would produce
		// different disp values across processes — breaking byte-
		// identical graph.html output between `go test` invocations.
		seen := make(map[string]bool, len(edges))
		for _, from := range ids {
			for _, to := range neighbours[from] {
				key := from + "|" + to
				rev := to + "|" + from
				if seen[key] || seen[rev] {
					continue
				}
				seen[key] = true
				dx := pos[from].X - pos[to].X
				dy := pos[from].Y - pos[to].Y
				dist := math.Sqrt(dx*dx+dy*dy) + 0.01
				force := dist * dist / k
				ax := dx / dist * force
				ay := dy / dist * force
				df, dt := disp[from], disp[to]
				df.X -= ax
				df.Y -= ay
				dt.X += ax
				dt.Y += ay
				disp[from], disp[to] = df, dt
			}
		}

		// Apply displacement, capped by temperature; keep inside the viewport.
		for _, id := range ids {
			d := disp[id]
			mag := math.Sqrt(d.X*d.X+d.Y*d.Y) + 0.01
			scale := math.Min(mag, temperature) / mag
			p := pos[id]
			p.X += d.X * scale
			p.Y += d.Y * scale
			if p.X < 0 {
				p.X = 0
			} else if p.X > cfg.Width {
				p.X = cfg.Width
			}
			if p.Y < 0 {
				p.Y = 0
			} else if p.Y > cfg.Height {
				p.Y = cfg.Height
			}
			pos[id] = p
		}

		temperature -= cooling
		if temperature < 1 {
			temperature = 1
		}
	}
	return pos
}
