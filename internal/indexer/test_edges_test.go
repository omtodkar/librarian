package indexer_test

import (
	"os"
	"path/filepath"
	"testing"

	"librarian/internal/config"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults"
	"librarian/internal/store"
)

// openTestEdgesStore builds an Indexer + Store pair for a test workspace
// rooted at dir with a given TestEdges config.
func openTestEdgesStore(t *testing.T, dir string, testEdges config.TestEdgesConfig) (*indexer.Indexer, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(dir, ".librarian", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cfg := &config.Config{
		DocsDir:     filepath.Join(dir, "docs"),
		DBPath:      dbPath,
		ProjectRoot: dir,
		Chunking:    config.ChunkingConfig{MaxTokens: 512, MinTokens: 1},
		Graph: config.GraphConfig{
			HonorGitignore:  false,
			DetectGenerated: true,
			MaxWorkers:      1,
			TestEdges:       testEdges,
		},
	}
	return indexer.New(s, cfg, fakeEmbedder{dim: 4}), s
}

// edgesOfKind returns all graph edges with the given Kind.
func edgesOfKind(t *testing.T, s *store.Store, kind string) []store.Edge {
	t.Helper()
	all, err := s.ListEdges()
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	var out []store.Edge
	for _, e := range all {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// TestBuildTestEdges_Integration seeds a workspace with a Go test file and
// its SUT, runs IndexProjectGraph with TestEdges.Enabled=true, and asserts
// exactly one tests edge is emitted with the expected metadata.
func TestBuildTestEdges_Integration(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"pkg/auth.go": `package pkg

func Authenticate(user string) bool { return user != "" }
`,
		"pkg/auth_test.go": `package pkg

import "testing"

func TestAuthenticate(t *testing.T) {
	if !Authenticate("admin") {
		t.Error("expected true")
	}
}
`,
	})

	idx, s := openTestEdgesStore(t, dir, config.TestEdgesConfig{Enabled: true})
	result, err := idx.IndexProjectGraph(dir, false)
	if err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("unexpected graph errors: %v", result.Errors)
	}

	edges := edgesOfKind(t, s, store.EdgeKindTests)
	if len(edges) != 1 {
		t.Fatalf("want 1 tests edge, got %d: %+v", len(edges), edges)
	}

	e := edges[0]
	wantFrom := store.CodeFileNodeID("pkg/auth_test.go")
	wantTo := store.CodeFileNodeID("pkg/auth.go")
	if e.From != wantFrom {
		t.Errorf("edge.From = %q, want %q", e.From, wantFrom)
	}
	if e.To != wantTo {
		t.Errorf("edge.To = %q, want %q", e.To, wantTo)
	}
	if e.Metadata != `{"heuristic":"path-convention"}` {
		t.Errorf("edge.Metadata = %q, want %q", e.Metadata, `{"heuristic":"path-convention"}`)
	}
	if result.EdgesAdded < 1 {
		t.Errorf("result.EdgesAdded = %d, want >= 1", result.EdgesAdded)
	}
}

// TestBuildTestEdges_DisabledByConfig verifies that setting
// cfg.Graph.TestEdges.Enabled=false suppresses all tests edges even when
// test files and their subjects are present in the project.
func TestBuildTestEdges_DisabledByConfig(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"pkg/auth.go": `package pkg

func Authenticate(user string) bool { return user != "" }
`,
		"pkg/auth_test.go": `package pkg

import "testing"

func TestAuthenticate(t *testing.T) {}
`,
	})

	idx, s := openTestEdgesStore(t, dir, config.TestEdgesConfig{Enabled: false})
	if _, err := idx.IndexProjectGraph(dir, false); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	edges := edgesOfKind(t, s, store.EdgeKindTests)
	if len(edges) != 0 {
		t.Errorf("want 0 tests edges with Enabled=false, got %d: %+v", len(edges), edges)
	}
}
