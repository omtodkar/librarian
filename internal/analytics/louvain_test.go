package analytics_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"librarian/internal/analytics"
	"librarian/internal/store"
)

// TestAnalyze_HappyPathWithDefaultTimeout exercises the canonical post-commit
// configuration: a small graph and the production 60s default. Communities
// must be returned and CommunitiesSkippedReason must stay empty — the bound
// is generous enough that no real-world workload should trip it on a 6-node
// graph.
func TestAnalyze_HappyPathWithDefaultTimeout(t *testing.T) {
	s := newTestStore(t)
	seedTwoClusters(t, s)

	r, err := analytics.Analyze(s, analytics.Options{
		CommunityTimeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if r.CommunitiesSkippedReason != "" {
		t.Errorf("expected empty CommunitiesSkippedReason on tiny graph, got %q", r.CommunitiesSkippedReason)
	}
	if len(r.Communities) < 2 {
		t.Errorf("expected at least 2 communities, got %d", len(r.Communities))
	}
}

// TestAnalyze_DisabledTimeoutRunsToCompletion confirms the legacy unbounded
// path still works: opts.CommunityTimeout == 0 must NOT short-circuit the
// Louvain pass; communities should be returned exactly as before lib-7gyz.
func TestAnalyze_DisabledTimeoutRunsToCompletion(t *testing.T) {
	s := newTestStore(t)
	seedTwoClusters(t, s)

	r, err := analytics.Analyze(s, analytics.Options{
		CommunityTimeout: 0, // explicit "disabled"
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if r.CommunitiesSkippedReason != "" {
		t.Errorf("expected empty CommunitiesSkippedReason with disabled timeout, got %q", r.CommunitiesSkippedReason)
	}
	if len(r.Communities) < 2 {
		t.Errorf("expected at least 2 communities, got %d", len(r.Communities))
	}
}

// TestAnalyze_TimeoutSkipsCommunities builds a graph large enough that
// gonum's Louvain pass takes meaningfully more than 1ns of wall time, sets
// the timeout to 1ns, and asserts the Communities section is skipped while
// the rest of the analytics pass keeps working (god nodes are the
// load-bearing analytic per lib-7gyz).
//
// We use a 1ns timeout (not 1ms) for two reasons:
//  1. It triggers reliably even when the timer fires before the goroutine
//     gets scheduled — the channel-vs-timer race is decided at the timer
//     side every single time.
//  2. It removes the flaky band where small graphs sometimes finish before
//     the timeout. Even an N=1 Modularize call doesn't return in 1ns.
//
// The graph is a 200-node ring with shortcut edges — small enough to keep
// the test fast but still requires the goroutine to actually start
// Modularize, which is all we need to assert the timeout fires.
func TestAnalyze_TimeoutSkipsCommunities(t *testing.T) {
	s := newTestStore(t)

	const n = 200
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("doc:n%03d", i)
		if err := s.UpsertNode(store.Node{
			ID:    id,
			Kind:  store.NodeKindDocument,
			Label: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Ring topology so every node has degree >= 2 and Louvain has real work.
	for i := 0; i < n; i++ {
		from := fmt.Sprintf("doc:n%03d", i)
		to := fmt.Sprintf("doc:n%03d", (i+1)%n)
		if err := s.UpsertEdge(store.Edge{From: from, To: to, Kind: "mentions"}); err != nil {
			t.Fatal(err)
		}
	}
	// Sprinkle long-range edges so the modularity pass has multiple
	// non-trivial communities to consider.
	for i := 0; i < n; i += 7 {
		from := fmt.Sprintf("doc:n%03d", i)
		to := fmt.Sprintf("doc:n%03d", (i+50)%n)
		if err := s.UpsertEdge(store.Edge{From: from, To: to, Kind: "shared_code_ref"}); err != nil {
			t.Fatal(err)
		}
	}

	r, err := analytics.Analyze(s, analytics.Options{
		CommunityTimeout: 1 * time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(r.Communities) != 0 {
		t.Errorf("expected empty Communities on timeout, got %d", len(r.Communities))
	}
	if r.CommunitiesSkippedReason == "" {
		t.Fatal("expected CommunitiesSkippedReason to be set on timeout")
	}
	if !strings.Contains(r.CommunitiesSkippedReason, "louvain exceeded timeout") {
		t.Errorf("expected reason to mention the louvain timeout; got %q", r.CommunitiesSkippedReason)
	}
	if !strings.Contains(r.CommunitiesSkippedReason, "1ns") {
		t.Errorf("expected reason to include the configured timeout; got %q", r.CommunitiesSkippedReason)
	}

	// God-node analytics must still be populated — the entire point of
	// skipping communities cleanly is that the load-bearing section of the
	// report keeps working.
	if len(r.GodNodes) == 0 {
		t.Error("expected god nodes to still be computed when communities timed out")
	}
	// Totals must still reflect the input graph.
	if r.TotalNodes != n {
		t.Errorf("TotalNodes = %d, want %d", r.TotalNodes, n)
	}
}
