package analytics

import (
	"sort"

	"librarian/internal/store"
)

// computeSurprisingConnections finds edges that bridge different
// communities and ranks them by a simple composite score:
//   - only cross-community edges qualify (same-community edges are the
//     communities' raison d'être, not "surprising")
//   - rare edge kinds score higher (1 / kind-frequency) — a lone "import"
//     across communities is more informative than one of a hundred
//     "mentions" edges
//   - edges between small communities score higher than edges into hub
//     communities — but the smaller-community factor is floored at 2 so a
//     single orphan node doesn't saturate the top of the list after a
//     rename that leaves it isolated.
//
// Self-loops are filtered (same-community by definition). At most `top`
// connections are returned, sorted descending by score. The struct has no
// Why field — downstream callers that want prose invoke
// RenderSurprisingWhy with the labels + kind-count maps (edgeKindCounts).
func computeSurprisingConnections(edges []store.Edge, nodeToCommunity map[string]int, top int) []SurprisingConnection {
	if len(edges) == 0 {
		return nil
	}
	if top <= 0 {
		top = 10
	}

	kindCount := edgeKindCounts(edges)
	commSize := make(map[int]int)
	for _, c := range nodeToCommunity {
		commSize[c]++
	}

	var out []SurprisingConnection
	for _, e := range edges {
		if e.From == e.To {
			continue // self-loops never bridge communities
		}
		fromComm, ok1 := nodeToCommunity[e.From]
		toComm, ok2 := nodeToCommunity[e.To]
		if !ok1 || !ok2 || fromComm == toComm {
			continue
		}
		rarity := 1.0 / float64(kindCount[e.Kind])
		smaller := commSize[fromComm]
		if commSize[toComm] < smaller {
			smaller = commSize[toComm]
		}
		// Floor at 2 so a singleton community (size 1) doesn't monopolise
		// the top scores: any edge touching a size-1 community would
		// otherwise have score = rarity * 1.0 = max.
		if smaller < 2 {
			smaller = 2
		}
		score := rarity * (1.0 / float64(smaller))

		out = append(out, SurprisingConnection{
			From:            e.From,
			To:              e.To,
			Kind:            e.Kind,
			FromCommunityID: fromComm,
			ToCommunityID:   toComm,
			Score:           score,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].Kind < out[j].Kind
	})

	if len(out) > top {
		out = out[:top]
	}
	return out
}
