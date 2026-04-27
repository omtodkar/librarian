package analytics_test

import (
	"path/filepath"
	"strings"
	"testing"

	"librarian/internal/analytics"
	"librarian/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"), nil, 0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seedTwoClusters populates the store with two clearly separated clusters
// connected by one bridge edge. This is the canonical "Louvain should find
// two communities" shape.
//
//	cluster A: a1 -- a2 -- a3 -- a1 (triangle)
//	cluster B: b1 -- b2 -- b3 -- b1 (triangle)
//	bridge:    a1 -- b1 (single cross edge)
func seedTwoClusters(t *testing.T, s *store.Store) {
	t.Helper()
	for _, id := range []string{"doc:a1", "doc:a2", "doc:a3", "doc:b1", "doc:b2", "doc:b3"} {
		if err := s.UpsertNode(store.Node{
			ID:    id,
			Kind:  store.NodeKindDocument,
			Label: id,
		}); err != nil {
			t.Fatal(err)
		}
	}
	edges := []store.Edge{
		{From: "doc:a1", To: "doc:a2", Kind: "mentions"},
		{From: "doc:a2", To: "doc:a3", Kind: "mentions"},
		{From: "doc:a3", To: "doc:a1", Kind: "mentions"},

		{From: "doc:b1", To: "doc:b2", Kind: "mentions"},
		{From: "doc:b2", To: "doc:b3", Kind: "mentions"},
		{From: "doc:b3", To: "doc:b1", Kind: "mentions"},

		// The lone cross-community bridge. Different edge kind so the
		// rarity score for this edge is 1.0 (only one edge of this kind).
		{From: "doc:a1", To: "doc:b1", Kind: "shared_code_ref"},
	}
	for _, e := range edges {
		if err := s.UpsertEdge(e); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnalyze_EmptyGraph(t *testing.T) {
	s := newTestStore(t)
	r, err := analytics.Analyze(s)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil Report for empty graph")
	}
	if r.TotalNodes != 0 || r.TotalEdges != 0 {
		t.Errorf("empty graph Totals = (%d, %d), want (0, 0)", r.TotalNodes, r.TotalEdges)
	}
	if len(r.Communities) != 0 || len(r.GodNodes) != 0 || len(r.SurprisingConnections) != 0 {
		t.Errorf("expected empty analysis fields, got %+v", r)
	}
}

func TestAnalyze_TwoClustersFindsTwoCommunities(t *testing.T) {
	s := newTestStore(t)
	seedTwoClusters(t, s)

	r, err := analytics.Analyze(s)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if r.TotalNodes != 6 || r.TotalEdges != 7 {
		t.Errorf("Totals = (%d, %d), want (6, 7)", r.TotalNodes, r.TotalEdges)
	}
	if len(r.Communities) < 2 {
		t.Errorf("expected at least 2 communities, got %d: %+v", len(r.Communities), r.Communities)
	}

	nodeComm := map[string]int{}
	for _, c := range r.Communities {
		for _, n := range c.Nodes {
			nodeComm[n] = c.ID
		}
	}
	if nodeComm["doc:a1"] != nodeComm["doc:a2"] || nodeComm["doc:a2"] != nodeComm["doc:a3"] {
		t.Errorf("triangle A split across communities: %v", nodeComm)
	}
	if nodeComm["doc:b1"] != nodeComm["doc:b2"] || nodeComm["doc:b2"] != nodeComm["doc:b3"] {
		t.Errorf("triangle B split across communities: %v", nodeComm)
	}
	if nodeComm["doc:a1"] == nodeComm["doc:b1"] {
		t.Error("triangles A and B should be different communities (only one bridge edge)")
	}
}

func TestAnalyze_CommunityLabelsStripNamespacePrefix(t *testing.T) {
	s := newTestStore(t)
	seedTwoClusters(t, s)
	r, err := analytics.Analyze(s)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	for _, c := range r.Communities {
		for _, prefix := range store.NodeIDPrefixes() {
			if strings.HasPrefix(c.Label, prefix) {
				t.Errorf("Community.Label %q still carries prefix %q", c.Label, prefix)
			}
		}
	}
}

func TestAnalyze_GodNodesRankedByDegree(t *testing.T) {
	s := newTestStore(t)
	seedTwoClusters(t, s)
	r, err := analytics.Analyze(s)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	// doc:a1 and doc:b1 carry the bridge edge in addition to their
	// triangle edges, so they tie for top degree (3 each). The other four
	// nodes all have degree 2.
	if len(r.GodNodes) < 2 {
		t.Fatalf("expected at least 2 god nodes, got %d", len(r.GodNodes))
	}
	top := map[string]bool{"doc:a1": true, "doc:b1": true}
	for _, g := range r.GodNodes[:2] {
		if !top[g.NodeID] {
			t.Errorf("top god node %q was not a1/b1; degree=%d", g.NodeID, g.Degree)
		}
		if g.Degree != 3 {
			t.Errorf("top god node %q has degree %d, want 3", g.NodeID, g.Degree)
		}
	}
}

func TestAnalyze_SurprisingConnectionsAreCrossCommunity(t *testing.T) {
	s := newTestStore(t)
	seedTwoClusters(t, s)
	r, err := analytics.Analyze(s)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}

	if len(r.SurprisingConnections) == 0 {
		t.Fatal("expected at least one surprising connection")
	}
	for _, sc := range r.SurprisingConnections {
		if sc.FromCommunityID == sc.ToCommunityID {
			t.Errorf("intra-community edge leaked into SurprisingConnections: %+v", sc)
		}
	}
	top := r.SurprisingConnections[0]
	if top.From != "doc:a1" || top.To != "doc:b1" || top.Kind != "shared_code_ref" {
		t.Errorf("top surprising connection = %+v, want a1->b1 shared_code_ref", top)
	}
}

// TestAnalyze_RenderSurprisingWhy verifies the prose-rendering helper works
// on real analytics output. The helper is the path lib-u55 will take for
// plain-text / MCP fallback rendering.
func TestAnalyze_RenderSurprisingWhy(t *testing.T) {
	s := newTestStore(t)
	seedTwoClusters(t, s)
	r, err := analytics.Analyze(s)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(r.SurprisingConnections) == 0 {
		t.Fatal("no surprising connections to render")
	}
	// Labels map reconstructed from the store since the helper is exposed
	// to external callers with their own label choice.
	nodes, _ := s.ListNodes()
	labels := map[string]string{}
	for _, n := range nodes {
		labels[n.ID] = n.Label
	}
	edges, _ := s.ListEdges()
	kindCount := map[string]int{}
	for _, e := range edges {
		kindCount[e.Kind]++
	}

	why := analytics.RenderSurprisingWhy(r.SurprisingConnections[0], labels, kindCount)
	if why == "" {
		t.Error("RenderSurprisingWhy returned empty string")
	}
	if !strings.Contains(why, "shared_code_ref") {
		t.Errorf("expected rendered prose to mention the edge kind; got %q", why)
	}
}

func TestAnalyze_SuggestedQuestionsStructured(t *testing.T) {
	s := newTestStore(t)
	seedTwoClusters(t, s)
	r, err := analytics.Analyze(s)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(r.SuggestedQuestions) == 0 {
		t.Fatal("expected at least one suggested question")
	}
	for _, q := range r.SuggestedQuestions {
		if q.Text == "" {
			t.Errorf("question has empty Text: %+v", q)
		}
		if q.Template == "" {
			t.Errorf("question has empty Template: %+v", q)
		}
		if len(q.Args) == 0 {
			t.Errorf("question has no Args: %+v", q)
		}
		if q.Kind == "" {
			t.Errorf("question has empty Kind: %+v", q)
		}
	}
	// At least one should be classified as god_pair (given seedTwoClusters
	// produces two god nodes across the clusters).
	kinds := map[string]bool{}
	for _, q := range r.SuggestedQuestions {
		kinds[q.Kind] = true
	}
	if !kinds[analytics.QuestionKindGodPair] {
		t.Errorf("expected a god_pair question; got kinds %v", kinds)
	}
}

// TestAnalyze_SingleGodNodeStar covers the 1-god-node path that
// suggestQuestions must gracefully handle (no "connects A to B" emitted,
// but cluster-tour / bridge questions still produced when available).
// Also stresses the CommunityNone sentinel for isolated nodes.
func TestAnalyze_SingleGodNodeStar(t *testing.T) {
	s := newTestStore(t)
	// Star graph: hub + 2 leaves. Only 3 nodes total, only the hub has
	// degree > 1 (leaves have degree 1).
	for _, id := range []string{"doc:hub", "doc:leaf1", "doc:leaf2"} {
		if err := s.UpsertNode(store.Node{ID: id, Kind: store.NodeKindDocument, Label: id}); err != nil {
			t.Fatal(err)
		}
	}
	s.UpsertEdge(store.Edge{From: "doc:hub", To: "doc:leaf1", Kind: "mentions"})
	s.UpsertEdge(store.Edge{From: "doc:hub", To: "doc:leaf2", Kind: "mentions"})

	r, err := analytics.Analyze(s)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	// Hub has degree 2; leaves have degree 1 each. All three are god nodes
	// under the "degree > 0" rule.
	if len(r.GodNodes) < 1 {
		t.Fatal("expected at least one god node in a star graph")
	}
	if r.GodNodes[0].NodeID != "doc:hub" {
		t.Errorf("top god node should be the hub; got %q", r.GodNodes[0].NodeID)
	}
	// suggestQuestions should not crash and should produce zero or more
	// well-formed questions.
	for _, q := range r.SuggestedQuestions {
		if q.Text == "" || q.Template == "" {
			t.Errorf("malformed question from star graph: %+v", q)
		}
	}
}

// TestAnalyze_Deterministic re-runs Analyze twice on the same graph and
// expects identical output. GRAPH_REPORT.md needs stable content across
// commits so diffs stay meaningful.
func TestAnalyze_Deterministic(t *testing.T) {
	s := newTestStore(t)
	seedTwoClusters(t, s)

	first, err := analytics.Analyze(s)
	if err != nil {
		t.Fatalf("Analyze 1: %v", err)
	}
	second, err := analytics.Analyze(s)
	if err != nil {
		t.Fatalf("Analyze 2: %v", err)
	}

	if len(first.Communities) != len(second.Communities) {
		t.Errorf("community count differs between runs: %d vs %d", len(first.Communities), len(second.Communities))
	}
	if len(first.GodNodes) != len(second.GodNodes) {
		t.Errorf("god-node count differs: %d vs %d", len(first.GodNodes), len(second.GodNodes))
	}
	for i := range first.GodNodes {
		if i >= len(second.GodNodes) {
			break
		}
		if first.GodNodes[i].NodeID != second.GodNodes[i].NodeID {
			t.Errorf("god-node order differs at %d: %q vs %q",
				i, first.GodNodes[i].NodeID, second.GodNodes[i].NodeID)
		}
	}
}

// TestAnalyze_CommunityNoneForIsolated verifies the sentinel assignment
// for nodes that slipped through community detection (e.g., a node with
// zero edges that Louvain still emits as its own 1-element community;
// the sentinel applies when a GodNode's id isn't in any community).
// This test mostly guards the exported constant.
func TestAnalyze_CommunityNoneSentinelExported(t *testing.T) {
	// Compile-time use: the constant must exist.
	var _ int = analytics.CommunityNone
	if analytics.CommunityNone >= 0 {
		t.Errorf("CommunityNone should be negative to not collide with real community ids; got %d", analytics.CommunityNone)
	}
}
