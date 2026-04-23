package store

import (
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestGraph_UpsertAndGetNode(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertNode(Node{ID: "doc:a", Kind: NodeKindDocument, Label: "Alpha", SourcePath: "docs/a.md"}); err != nil {
		t.Fatalf("UpsertNode: %v", err)
	}

	got, err := s.GetNode("doc:a")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got == nil {
		t.Fatal("expected node, got nil")
	}
	if got.Label != "Alpha" || got.SourcePath != "docs/a.md" || got.Kind != NodeKindDocument {
		t.Errorf("unexpected node: %+v", got)
	}

	missing, err := s.GetNode("doc:missing")
	if err != nil {
		t.Fatalf("GetNode missing: %v", err)
	}
	if missing != nil {
		t.Errorf("expected nil for missing node, got %+v", missing)
	}
}

func TestGraph_UpsertEdgeIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	s.UpsertNode(Node{ID: "doc:a", Kind: NodeKindDocument})
	s.UpsertNode(Node{ID: "doc:b", Kind: NodeKindDocument})

	e := Edge{From: "doc:a", To: "doc:b", Kind: EdgeKindSharedCodeRef}
	for i := 0; i < 3; i++ {
		if err := s.UpsertEdge(e); err != nil {
			t.Fatalf("UpsertEdge attempt %d: %v", i, err)
		}
	}

	out, err := s.Neighbors("doc:a", "out")
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("expected 1 edge after 3 upserts, got %d", len(out))
	}
}

func TestGraph_NeighborsDirections(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"doc:a", "doc:b", "file:x.go"} {
		kind := NodeKindDocument
		if id == "file:x.go" {
			kind = NodeKindCodeFile
		}
		s.UpsertNode(Node{ID: id, Kind: kind})
	}
	s.UpsertEdge(Edge{From: "doc:a", To: "file:x.go", Kind: EdgeKindMentions})
	s.UpsertEdge(Edge{From: "doc:b", To: "file:x.go", Kind: EdgeKindMentions})
	s.UpsertEdge(Edge{From: "doc:a", To: "doc:b", Kind: EdgeKindSharedCodeRef})

	out, _ := s.Neighbors("doc:a", "out")
	if len(out) != 2 {
		t.Errorf("outgoing from doc:a = %d, want 2", len(out))
	}

	in, _ := s.Neighbors("file:x.go", "in")
	if len(in) != 2 {
		t.Errorf("incoming to file:x.go = %d, want 2", len(in))
	}

	both, _ := s.Neighbors("doc:a", "")
	if len(both) != 2 {
		t.Errorf("both directions from doc:a = %d, want 2", len(both))
	}
}

func TestGraph_ShortestPath(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"doc:a", "doc:b", "doc:c", "doc:d"} {
		s.UpsertNode(Node{ID: id, Kind: NodeKindDocument})
	}
	// a -> b -> c; and a -> d (detour). Shortest a->c is a->b->c.
	s.UpsertEdge(Edge{From: "doc:a", To: "doc:b", Kind: "next"})
	s.UpsertEdge(Edge{From: "doc:b", To: "doc:c", Kind: "next"})
	s.UpsertEdge(Edge{From: "doc:a", To: "doc:d", Kind: "next"})

	path, err := s.ShortestPath("doc:a", "doc:c", 0)
	if err != nil {
		t.Fatalf("ShortestPath: %v", err)
	}
	if len(path) != 2 {
		t.Fatalf("path length = %d, want 2: %+v", len(path), path)
	}
	if path[0].From != "doc:a" || path[0].To != "doc:b" {
		t.Errorf("step 0 = %+v, want doc:a->doc:b", path[0])
	}
	if path[1].From != "doc:b" || path[1].To != "doc:c" {
		t.Errorf("step 1 = %+v, want doc:b->doc:c", path[1])
	}
}

func TestGraph_ShortestPath_NoPath(t *testing.T) {
	s := newTestStore(t)
	s.UpsertNode(Node{ID: "doc:a", Kind: NodeKindDocument})
	s.UpsertNode(Node{ID: "doc:b", Kind: NodeKindDocument})

	path, err := s.ShortestPath("doc:a", "doc:b", 6)
	if err != nil {
		t.Fatalf("ShortestPath: %v", err)
	}
	if path != nil {
		t.Errorf("expected nil path when disconnected, got %+v", path)
	}
}

func TestGraph_FindNodes(t *testing.T) {
	s := newTestStore(t)
	s.UpsertNode(Node{ID: "doc:auth", Kind: NodeKindDocument, Label: "Authentication", SourcePath: "docs/auth.md"})
	s.UpsertNode(Node{ID: "doc:user", Kind: NodeKindDocument, Label: "Users", SourcePath: "docs/user.md"})

	match, _ := s.FindNodes("uth", 10)
	if len(match) < 1 {
		t.Error("expected at least one substring match")
	}

	exact, _ := s.FindNodes("doc:user", 10)
	if len(exact) == 0 || exact[0].ID != "doc:user" {
		t.Errorf("exact id lookup failed: %+v", exact)
	}
}
