package store

import (
	"reflect"
	"testing"
)

// TestDeleteOrphanNodes_OnlyTrueOrphansDeleted pins the topological
// predicate: a node with any incident edge (incoming OR outgoing) must
// survive; a node with zero edges in either direction must be deleted.
// Kind filter defaults to keep the blast radius tight.
func TestDeleteOrphanNodes_OnlyTrueOrphansDeleted(t *testing.T) {
	s := newTestStore(t)

	// Non-orphan: has an incoming edge.
	s.UpsertNode(Node{ID: "sym:kept_incoming", Kind: NodeKindSymbol})
	s.UpsertNode(Node{ID: "file:importer.py", Kind: NodeKindCodeFile})
	s.UpsertEdge(Edge{From: "file:importer.py", To: "sym:kept_incoming", Kind: "import"})

	// Non-orphan: has an outgoing edge.
	s.UpsertNode(Node{ID: "sym:kept_outgoing", Kind: NodeKindSymbol})
	s.UpsertNode(Node{ID: "sym:kept_outgoing_target", Kind: NodeKindSymbol})
	s.UpsertEdge(Edge{From: "sym:kept_outgoing", To: "sym:kept_outgoing_target", Kind: "call"})

	// Orphans (no edges in either direction).
	s.UpsertNode(Node{ID: "sym:.utils", Kind: NodeKindSymbol})
	s.UpsertNode(Node{ID: "sym:..pkg.Thing", Kind: NodeKindSymbol})

	// Doc orphan — kind filter should leave it alone when sweeping symbols only.
	s.UpsertNode(Node{ID: "doc:orphaned", Kind: NodeKindDocument})

	deleted, err := s.DeleteOrphanNodes([]string{NodeKindSymbol})
	if err != nil {
		t.Fatalf("DeleteOrphanNodes: %v", err)
	}
	want := []string{"sym:..pkg.Thing", "sym:.utils"} // alphabetical
	if !reflect.DeepEqual(deleted, want) {
		t.Errorf("deleted = %v, want %v", deleted, want)
	}

	for _, id := range []string{"sym:kept_incoming", "sym:kept_outgoing", "sym:kept_outgoing_target", "file:importer.py", "doc:orphaned"} {
		n, _ := s.GetNode(id)
		if n == nil {
			t.Errorf("non-orphan %q was deleted", id)
		}
	}
	for _, id := range want {
		n, _ := s.GetNode(id)
		if n != nil {
			t.Errorf("orphan %q still present", id)
		}
	}
}

func TestDeleteOrphanNodes_EmptyKindsSweepsAll(t *testing.T) {
	s := newTestStore(t)
	s.UpsertNode(Node{ID: "sym:orphan_sym", Kind: NodeKindSymbol})
	s.UpsertNode(Node{ID: "doc:orphan_doc", Kind: NodeKindDocument})
	s.UpsertNode(Node{ID: "file:orphan_file", Kind: NodeKindCodeFile})

	deleted, err := s.DeleteOrphanNodes(nil)
	if err != nil {
		t.Fatalf("DeleteOrphanNodes: %v", err)
	}
	want := []string{"doc:orphan_doc", "file:orphan_file", "sym:orphan_sym"}
	if !reflect.DeepEqual(deleted, want) {
		t.Errorf("deleted = %v, want %v", deleted, want)
	}
}

func TestDeleteOrphanNodes_NoOrphansReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	s.UpsertNode(Node{ID: "file:a", Kind: NodeKindCodeFile})
	s.UpsertNode(Node{ID: "sym:a", Kind: NodeKindSymbol})
	s.UpsertEdge(Edge{From: "file:a", To: "sym:a", Kind: EdgeKindContains})

	deleted, err := s.DeleteOrphanNodes([]string{NodeKindSymbol})
	if err != nil {
		t.Fatalf("DeleteOrphanNodes: %v", err)
	}
	if len(deleted) != 0 {
		t.Errorf("expected no deletions, got %v", deleted)
	}
}

// TestListOrphanNodes_MirrorsDelete ensures the preview (list) and the
// mutation (delete) operate on the same predicate — what the user sees in
// --dry-run is exactly what they'd get from a real run.
func TestListOrphanNodes_MirrorsDelete(t *testing.T) {
	s := newTestStore(t)
	s.UpsertNode(Node{ID: "sym:orphan1", Kind: NodeKindSymbol})
	s.UpsertNode(Node{ID: "sym:orphan2", Kind: NodeKindSymbol})
	s.UpsertNode(Node{ID: "sym:kept", Kind: NodeKindSymbol})
	s.UpsertNode(Node{ID: "file:x", Kind: NodeKindCodeFile})
	s.UpsertEdge(Edge{From: "file:x", To: "sym:kept", Kind: EdgeKindContains})

	listed, err := s.ListOrphanNodes([]string{NodeKindSymbol})
	if err != nil {
		t.Fatalf("ListOrphanNodes: %v", err)
	}
	var listedIDs []string
	for _, n := range listed {
		listedIDs = append(listedIDs, n.ID)
	}

	deleted, err := s.DeleteOrphanNodes([]string{NodeKindSymbol})
	if err != nil {
		t.Fatalf("DeleteOrphanNodes: %v", err)
	}
	if !reflect.DeepEqual(listedIDs, deleted) {
		t.Errorf("list / delete disagree: list=%v delete=%v", listedIDs, deleted)
	}
}

// TestDeleteOrphanNodes_PostLibO8mScenario constructs the exact topology the
// lib-o8m rename leaves behind: the new canonical node has healthy inbound
// edges while the retired dotted-form node is orphaned. The sweep must
// remove only the orphan and leave the live graph untouched.
func TestDeleteOrphanNodes_PostLibO8mScenario(t *testing.T) {
	s := newTestStore(t)
	s.UpsertNode(Node{ID: "file:mypkg/a.py", Kind: NodeKindCodeFile})
	s.UpsertNode(Node{ID: "file:mypkg/b.py", Kind: NodeKindCodeFile})
	s.UpsertNode(Node{ID: "sym:mypkg.utils", Kind: NodeKindSymbol})
	s.UpsertEdge(Edge{From: "file:mypkg/a.py", To: "sym:mypkg.utils", Kind: "import"})
	s.UpsertEdge(Edge{From: "file:mypkg/b.py", To: "sym:mypkg.utils", Kind: "import"})

	// Retired dotted form left behind by a prior indexer.
	s.UpsertNode(Node{ID: "sym:.utils", Kind: NodeKindSymbol})

	preview, err := s.ListOrphanNodes([]string{NodeKindSymbol})
	if err != nil {
		t.Fatalf("ListOrphanNodes: %v", err)
	}
	if len(preview) != 1 || preview[0].ID != "sym:.utils" {
		t.Errorf("preview = %+v, want exactly [sym:.utils]", preview)
	}

	deleted, err := s.DeleteOrphanNodes([]string{NodeKindSymbol})
	if err != nil {
		t.Fatalf("DeleteOrphanNodes: %v", err)
	}
	if !reflect.DeepEqual(deleted, []string{"sym:.utils"}) {
		t.Errorf("deleted = %v, want [sym:.utils]", deleted)
	}

	if n, _ := s.GetNode("sym:mypkg.utils"); n == nil {
		t.Error("canonical sym:mypkg.utils got swept")
	}
	// Both import edges should still be present.
	for _, from := range []string{"file:mypkg/a.py", "file:mypkg/b.py"} {
		out, _ := s.Neighbors(from, "out")
		found := false
		for _, e := range out {
			if e.To == "sym:mypkg.utils" {
				found = true
			}
		}
		if !found {
			t.Errorf("edge from %s to sym:mypkg.utils is missing", from)
		}
	}
}
