package analytics

import (
	"fmt"
	"sort"
	"strings"

	"librarian/internal/store"
)

// Question kinds — exposed so downstream consumers can filter (e.g., lib-652
// might include only bridge questions in its skill file).
const (
	QuestionKindGodPair     = "god_pair"
	QuestionKindBridge      = "bridge"
	QuestionKindClusterTour = "cluster_tour"
)

// suggestQuestions produces a small set of probe queries the graph's
// structure is positioned to answer. Each question has Template + Args
// preserved alongside pre-rendered Text so html / json renderers can
// link the arguments to the nodes they reference.
//
// Question shapes, in priority order:
//  1. "What connects <god-A> to <god-B>?" — top two god nodes, preferring
//     a cross-community pair when one is available.
//  2. "Why does <X> reference <Y> via a <kind> edge?" — top surprising
//     connection, since those are by definition counterintuitive.
//  3. "What else lives in the <community-label> cluster?" — top two
//     largest communities.
func suggestQuestions(r *Report, labels map[string]string) []SuggestedQuestion {
	var out []SuggestedQuestion

	if len(r.GodNodes) >= 2 {
		a, b := r.GodNodes[0], r.GodNodes[1]
		// Prefer a pair from different communities if we have one; the
		// cross-cluster path is more interesting than an intra-cluster one.
		if a.CommunityID == b.CommunityID && len(r.GodNodes) >= 3 {
			for _, cand := range r.GodNodes[2:] {
				if cand.CommunityID != a.CommunityID {
					b = cand
					break
				}
			}
		}
		template := "What connects %s to %s?"
		args := []string{a.Label, b.Label}
		out = append(out, SuggestedQuestion{
			Text:     fmt.Sprintf(template, args[0], args[1]),
			Template: template,
			Args:     args,
			Kind:     QuestionKindGodPair,
		})
	}

	if len(r.SurprisingConnections) > 0 {
		s := r.SurprisingConnections[0]
		from := labelOrDisplayID(s.From, labels)
		to := labelOrDisplayID(s.To, labels)
		template := "Why does %s reference %s via a %q edge?"
		args := []string{from, to, s.Kind}
		out = append(out, SuggestedQuestion{
			Text:     fmt.Sprintf(template, args[0], args[1], args[2]),
			Template: template,
			Args:     args,
			Kind:     QuestionKindBridge,
		})
	}

	// Communities are ordered by detection index, not size; pick the two
	// largest for the cluster-tour questions.
	if len(r.Communities) > 0 {
		for _, c := range pickLargestCommunities(r.Communities, 2) {
			template := "What else lives in the %q cluster (%d nodes)?"
			args := []string{c.Label, fmt.Sprintf("%d", len(c.Nodes))}
			out = append(out, SuggestedQuestion{
				Text:     fmt.Sprintf(template, args[0], len(c.Nodes)),
				Template: template,
				Args:     args,
				Kind:     QuestionKindClusterTour,
			})
		}
	}

	return out
}

// displayID strips a store-known namespaced prefix (doc: / file: / sym: /
// key:) so the tail reads like a friendly identifier. Falls back to the
// input unchanged when no prefix matches. Uses store.NodeIDPrefixes as the
// single source of truth — new node kinds pick up the stripping automatically.
func displayID(id string) string {
	for _, prefix := range store.NodeIDPrefixes() {
		if strings.HasPrefix(id, prefix) && len(id) > len(prefix) {
			return id[len(prefix):]
		}
	}
	return id
}

// labelOrDisplayID returns the prepared label for an id, falling back to
// the prefix-stripped id when no label was recorded.
func labelOrDisplayID(id string, labels map[string]string) string {
	if label := labels[id]; label != "" {
		return displayID(label)
	}
	return displayID(id)
}

// pickLargestCommunities returns up to n communities sorted by size
// descending. Returns a new slice; does not mutate the caller's.
func pickLargestCommunities(comms []Community, n int) []Community {
	if n <= 0 || len(comms) == 0 {
		return nil
	}
	cp := make([]Community, len(comms))
	copy(cp, comms)
	sort.Slice(cp, func(i, j int) bool {
		return len(cp[i].Nodes) > len(cp[j].Nodes)
	})
	if len(cp) > n {
		cp = cp[:n]
	}
	return cp
}
