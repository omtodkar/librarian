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

// TestGraph_ShortestPath_SpecialCharsInNodeIDs guards against a bug in the
// earlier CTE-based ShortestPath: node IDs containing `|`, `,`, `>`, `_`, or
// `%` would either be mis-parsed when splitting the edge trail, or match too
// many rows under the NOT LIKE cycle guard. The application-level BFS is
// byte-exact and immune to either.
func TestGraph_ShortestPath_SpecialCharsInNodeIDs(t *testing.T) {
	s := newTestStore(t)
	weird := []string{"file:a|b.go", "file:a,b.go", "file:a>b.go", "file:a_b%c.go"}
	for _, id := range weird {
		s.UpsertNode(Node{ID: id, Kind: NodeKindCodeFile, Label: id})
	}
	for i := 0; i < len(weird)-1; i++ {
		s.UpsertEdge(Edge{From: weird[i], To: weird[i+1], Kind: "next"})
	}

	path, err := s.ShortestPath(weird[0], weird[3], 0)
	if err != nil {
		t.Fatalf("ShortestPath: %v", err)
	}
	if len(path) != 3 {
		t.Fatalf("path length = %d, want 3: %+v", len(path), path)
	}
	for i, step := range path {
		if step.From != weird[i] || step.To != weird[i+1] {
			t.Errorf("step %d = %+v, want %s->%s", i, step, weird[i], weird[i+1])
		}
	}
}

// TestGraph_ShortestPath_SelfLoop documents that ShortestPath returns (nil,
// nil) when source and destination are the same node. Callers can treat this
// as "the empty path" rather than "no path exists".
func TestGraph_ShortestPath_SelfLoop(t *testing.T) {
	s := newTestStore(t)
	s.UpsertNode(Node{ID: "doc:a", Kind: NodeKindDocument})

	path, err := s.ShortestPath("doc:a", "doc:a", 0)
	if err != nil {
		t.Fatalf("ShortestPath: %v", err)
	}
	if path != nil {
		t.Errorf("expected nil path for fromID == toID, got %+v", path)
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

func TestGraph_UpsertPlaceholderNode_DoesNotOverwrite(t *testing.T) {
	// Regression test for the ordering bug described in
	// UpsertPlaceholderNode's godoc: a later UpsertPlaceholderNode call for
	// an already-indexed real symbol must not overwrite the real row with
	// placeholder values (including metadata={"unresolved":true}).
	s := newTestStore(t)
	real := Node{
		ID:         "sym:com.example.Base",
		Kind:       NodeKindSymbol,
		Label:      "Base",
		SourcePath: "src/Base.java",
		Metadata:   "{}",
	}
	if err := s.UpsertNode(real); err != nil {
		t.Fatalf("UpsertNode (real): %v", err)
	}

	placeholder := Node{
		ID:         "sym:com.example.Base",
		Kind:       NodeKindSymbol,
		Label:      "Base", // raw target name, not the qualified label
		SourcePath: "Base",
		Metadata:   `{"unresolved":true}`,
	}
	if err := s.UpsertPlaceholderNode(placeholder); err != nil {
		t.Fatalf("UpsertPlaceholderNode: %v", err)
	}

	got, err := s.GetNode("sym:com.example.Base")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got == nil {
		t.Fatal("expected node, got nil")
	}
	if got.SourcePath != "src/Base.java" {
		t.Errorf("SourcePath overwritten: got %q, want %q (placeholder must not clobber real row)", got.SourcePath, "src/Base.java")
	}
	if got.Metadata != "{}" {
		t.Errorf("Metadata overwritten: got %q, want %q (placeholder must not poison real node with unresolved=true)", got.Metadata, "{}")
	}
}

func TestGraph_UpsertPlaceholderNode_InsertsWhenAbsent(t *testing.T) {
	s := newTestStore(t)
	if err := s.UpsertPlaceholderNode(Node{
		ID:         "sym:unresolved.Parent",
		Kind:       NodeKindSymbol,
		Label:      "Parent",
		SourcePath: "Parent",
		Metadata:   `{"unresolved":true}`,
	}); err != nil {
		t.Fatalf("UpsertPlaceholderNode: %v", err)
	}

	got, err := s.GetNode("sym:unresolved.Parent")
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if got == nil {
		t.Fatal("expected node after placeholder insert")
	}
	if got.Metadata != `{"unresolved":true}` {
		t.Errorf("placeholder metadata = %q, want %q", got.Metadata, `{"unresolved":true}`)
	}
}

func TestGraph_Neighbors_KindFilter(t *testing.T) {
	s := newTestStore(t)
	for _, id := range []string{"sym:Child", "sym:Base", "sym:Iface"} {
		s.UpsertNode(Node{ID: id, Kind: NodeKindSymbol})
	}
	s.UpsertEdge(Edge{From: "sym:Child", To: "sym:Base", Kind: EdgeKindInherits, Metadata: `{"relation":"extends"}`})
	s.UpsertEdge(Edge{From: "sym:Child", To: "sym:Iface", Kind: EdgeKindInherits, Metadata: `{"relation":"implements"}`})
	s.UpsertEdge(Edge{From: "sym:Child", To: "sym:Base", Kind: "call"})

	// No filter: all three edges.
	all, err := s.Neighbors("sym:Child", "out")
	if err != nil {
		t.Fatalf("Neighbors (no filter): %v", err)
	}
	if len(all) != 3 {
		t.Errorf("no-filter edges = %d, want 3", len(all))
	}

	// Single kind filter.
	only, err := s.Neighbors("sym:Child", "out", EdgeKindInherits)
	if err != nil {
		t.Fatalf("Neighbors (inherits): %v", err)
	}
	if len(only) != 2 {
		t.Errorf("inherits-only edges = %d, want 2", len(only))
	}
	for _, e := range only {
		if e.Kind != EdgeKindInherits {
			t.Errorf("unexpected kind in filtered result: %q", e.Kind)
		}
	}

	// Multi-kind filter.
	multi, err := s.Neighbors("sym:Child", "out", EdgeKindInherits, "call")
	if err != nil {
		t.Fatalf("Neighbors (inherits+call): %v", err)
	}
	if len(multi) != 3 {
		t.Errorf("multi-kind edges = %d, want 3", len(multi))
	}

	// Empty-string kinds are stripped (CLI-flag-safe).
	empty, err := s.Neighbors("sym:Child", "out", "")
	if err != nil {
		t.Fatalf("Neighbors (empty kind): %v", err)
	}
	if len(empty) != 3 {
		t.Errorf("empty-kind filter edges = %d, want 3 (empty strings should be stripped, not used as a filter)", len(empty))
	}

	// Unknown kind returns nothing without error.
	none, err := s.Neighbors("sym:Child", "out", "unknown_kind")
	if err != nil {
		t.Fatalf("Neighbors (unknown): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("unknown-kind filter edges = %d, want 0", len(none))
	}

	// Dart-introduced kinds round-trip through the filter as raw
	// strings — no special handling needed in the store layer. Guards
	// against a future consumer treating these kinds specially.
	s.UpsertNode(Node{ID: "sym:Mixin", Kind: NodeKindSymbol})
	s.UpsertNode(Node{ID: "file:main.dart", Kind: NodeKindCodeFile})
	s.UpsertNode(Node{ID: "file:other.dart", Kind: NodeKindCodeFile})
	s.UpsertEdge(Edge{From: "sym:Mixin", To: "sym:Base", Kind: EdgeKindRequires})
	s.UpsertEdge(Edge{From: "file:main.dart", To: "file:other.dart", Kind: EdgeKindPart})

	req, err := s.Neighbors("sym:Mixin", "out", EdgeKindRequires)
	if err != nil {
		t.Fatalf("Neighbors (requires): %v", err)
	}
	if len(req) != 1 || req[0].Kind != "requires" {
		t.Errorf("requires filter = %+v, want single requires edge", req)
	}

	parts, err := s.Neighbors("file:main.dart", "out", EdgeKindPart)
	if err != nil {
		t.Fatalf("Neighbors (part): %v", err)
	}
	if len(parts) != 1 || parts[0].Kind != "part" {
		t.Errorf("part filter = %+v, want single part edge", parts)
	}
}
