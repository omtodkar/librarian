package indexer_test

import (
	"encoding/json"
	"testing"

	"librarian/internal/store"
)

// TestCallEdges_GoIntegration verifies the end-to-end pipeline for Go call
// edges: a 2-file project where pkg/util.go has A() calling B() in the same
// file (same-file resolved), and main.go calling A() (cross-file unresolved).
//
// Asserts:
//   - call edge sym:pkg.A → sym:pkg.B exists with confidence="resolved"
//   - call edge sym:main.main → (unresolved "A") exists with confidence="unresolved"
func TestCallEdges_GoIntegration(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		// pkg/util.go — A calls B (same file, same package → resolved).
		"pkg/util.go": `package pkg

func A() {
	B()
}

func B() {}
`,
		// main.go — main calls A (cross-file/package → unresolved for main).
		"main.go": `package main

import "example/pkg"

func main() {
	pkg.A()
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// --- Assertion 1: pkg.A → pkg.B is a resolved call edge ---
	callerA := store.SymbolNodeID("pkg.A")
	edges, err := s.Neighbors(callerA, "out", store.EdgeKindCall)
	if err != nil {
		t.Fatalf("Neighbors(pkg.A, out, call): %v", err)
	}

	targetB := store.SymbolNodeID("pkg.B")
	var edgeAB *store.Edge
	for i := range edges {
		if edges[i].To == targetB {
			edgeAB = &edges[i]
			break
		}
	}
	if edgeAB == nil {
		t.Fatalf("expected call edge pkg.A → pkg.B; got edges: %+v", edges)
	}
	if edgeAB.Kind != store.EdgeKindCall {
		t.Errorf("edge kind = %q, want %q", edgeAB.Kind, store.EdgeKindCall)
	}
	var metaAB map[string]any
	if err := json.Unmarshal([]byte(edgeAB.Metadata), &metaAB); err != nil {
		t.Fatalf("unmarshal metadata: %v (%s)", err, edgeAB.Metadata)
	}
	if metaAB["confidence"] != "resolved" {
		t.Errorf("pkg.A → pkg.B: confidence = %v, want resolved", metaAB["confidence"])
	}

	// --- Assertion 2: main.main → unresolved "A" edge ---
	callerMain := store.SymbolNodeID("main.main")
	mainEdges, err := s.Neighbors(callerMain, "out", store.EdgeKindCall)
	if err != nil {
		t.Fatalf("Neighbors(main.main, out, call): %v", err)
	}

	// The callee "A" is a selector_expression field — for `pkg.A()` the
	// callee identifier extracted by goCalleeIdent is "A".
	targetA := store.SymbolNodeID("A")
	var edgeMainA *store.Edge
	for i := range mainEdges {
		if mainEdges[i].To == targetA {
			edgeMainA = &mainEdges[i]
			break
		}
	}
	if edgeMainA == nil {
		t.Fatalf("expected call edge main.main → sym:A (unresolved); edges: %+v", mainEdges)
	}
	if len(mainEdges) != 1 {
		t.Errorf("main.main: expected exactly 1 call edge; got %d: %+v", len(mainEdges), mainEdges)
	}
	var metaMain map[string]any
	if err := json.Unmarshal([]byte(edgeMainA.Metadata), &metaMain); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	if metaMain["confidence"] != "unresolved" {
		t.Errorf("main.main → A: confidence = %v, want unresolved", metaMain["confidence"])
	}
}
