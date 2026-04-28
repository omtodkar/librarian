package indexer_test

import (
	"encoding/json"
	"testing"

	"librarian/internal/store"
)

// edgesFromTo returns all graph_edges from fromID to toID.
func edgesFromTo(t *testing.T, s *store.Store, fromID, toID string) []store.Edge {
	t.Helper()
	all, err := s.ListEdges()
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	var out []store.Edge
	for _, e := range all {
		if e.From == fromID && e.To == toID {
			out = append(out, e)
		}
	}
	return out
}

// inheritsEdgesFrom returns all inherits edges from fromID.
func inheritsEdgesFrom(t *testing.T, s *store.Store, fromID string) []store.Edge {
	t.Helper()
	all, err := s.ListEdges()
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	var out []store.Edge
	for _, e := range all {
		if e.From == fromID && e.Kind == store.EdgeKindInherits {
			out = append(out, e)
		}
	}
	return out
}

// edgeMetaHasKey reports whether an edge's metadata JSON contains the given
// top-level key.
func edgeMetaHasKey(t *testing.T, e store.Edge, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(e.Metadata), &m); err != nil {
		t.Fatalf("unmarshal edge metadata: %v; metadata: %s", err, e.Metadata)
	}
	_, ok := m[key]
	return ok
}

// TestFQNResolution_JavaSamePackage verifies that a bare parent name in a Java
// file is resolved to a same-package sibling when no import covers it.
//
// Setup: two Java files in package "com.example" — Base.java defines Base,
// Child.java defines Child extends Base. No import statement.
// Expected: after IndexProjectGraph the unresolved edge Child→sym:Base is
// replaced by Child→sym:com.example.Base with no "unresolved" key.
func TestFQNResolution_JavaSamePackage(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"src/com/example/Base.java": `package com.example;

public class Base {
    public void baseMethod() {}
}
`,
		"src/com/example/Child.java": `package com.example;

public class Child extends Base {
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	childID := store.SymbolNodeID("com.example.Child")
	baseID := store.SymbolNodeID("com.example.Base")

	// The resolved edge must exist.
	resolved := edgesFromTo(t, s, childID, baseID)
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved edge Child→Base, got %d", len(resolved))
	}
	if edgeMetaHasKey(t, resolved[0], "unresolved") {
		t.Errorf("resolved edge has 'unresolved' key; metadata: %s", resolved[0].Metadata)
	}

	// The placeholder edge sym:Child→sym:Base (bare) must be gone.
	placeholder := edgesFromTo(t, s, childID, store.SymbolNodeID("Base"))
	if len(placeholder) != 0 {
		t.Errorf("expected placeholder edge deleted, got %d remaining", len(placeholder))
	}
}

// TestFQNResolution_WorkspaceWide verifies that a bare parent name is resolved
// via workspace-wide short-name lookup when the parent lives in a different
// package (same-package probe finds nothing, workspace lookup succeeds).
func TestFQNResolution_WorkspaceWide(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"src/com/lib/Base.java": `package com.lib;

public class Base {}
`,
		"src/com/app/Child.java": `package com.app;

public class Child extends Base {
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	childID := store.SymbolNodeID("com.app.Child")
	baseID := store.SymbolNodeID("com.lib.Base")

	resolved := edgesFromTo(t, s, childID, baseID)
	if len(resolved) != 1 {
		t.Fatalf("expected 1 resolved edge, got %d; childID=%s baseID=%s", len(resolved), childID, baseID)
	}
	if edgeMetaHasKey(t, resolved[0], "unresolved") {
		t.Errorf("resolved edge still has 'unresolved' key; metadata: %s", resolved[0].Metadata)
	}

	// Bare placeholder edge must be gone.
	placeholder := edgesFromTo(t, s, childID, store.SymbolNodeID("Base"))
	if len(placeholder) != 0 {
		t.Errorf("expected placeholder edge deleted, got %d remaining", len(placeholder))
	}
}

// TestFQNResolution_Ambiguous verifies that when a bare name matches multiple
// real symbols in the workspace, one edge per candidate is emitted and each
// carries the "ambiguous_resolution":true marker.
func TestFQNResolution_Ambiguous(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"src/com/a/Base.java": `package com.a;
public class Base {}
`,
		"src/com/b/Base.java": `package com.b;
public class Base {}
`,
		"src/com/c/Child.java": `package com.c;
public class Child extends Base {
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	childID := store.SymbolNodeID("com.c.Child")

	// Must have exactly 2 inherits edges from Child, one per Base candidate.
	all := inheritsEdgesFrom(t, s, childID)
	if len(all) != 2 {
		t.Fatalf("expected 2 ambiguous inherits edges, got %d; edges: %v", len(all), all)
	}

	for _, e := range all {
		if !edgeMetaHasKey(t, e, "ambiguous_resolution") {
			t.Errorf("edge %s→%s missing 'ambiguous_resolution' key; metadata: %s", e.From, e.To, e.Metadata)
		}
		if edgeMetaHasKey(t, e, "unresolved") {
			t.Errorf("edge %s→%s still has 'unresolved' key; metadata: %s", e.From, e.To, e.Metadata)
		}
	}

	// Placeholder edge to sym:Base (bare) must be gone.
	placeholder := edgesFromTo(t, s, childID, store.SymbolNodeID("Base"))
	if len(placeholder) != 0 {
		t.Errorf("expected placeholder edge deleted, got %d remaining", len(placeholder))
	}
}

// TestFQNResolution_NoMatch verifies that when no workspace symbol matches the
// bare name, the placeholder edge is left unchanged.
func TestFQNResolution_NoMatch(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"src/com/app/Child.java": `package com.app;

// Base is not defined anywhere in this workspace.
public class Child extends Base {
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	childID := store.SymbolNodeID("com.app.Child")

	// Placeholder edge should still be present with unresolved=true.
	edges := inheritsEdgesFrom(t, s, childID)
	if len(edges) != 1 {
		t.Fatalf("expected 1 placeholder edge, got %d", len(edges))
	}
	if edges[0].To != store.SymbolNodeID("Base") {
		t.Errorf("expected placeholder target sym:Base, got %s", edges[0].To)
	}
	if !edgeMetaHasKey(t, edges[0], "unresolved") {
		t.Errorf("expected placeholder edge to retain 'unresolved' key; metadata: %s", edges[0].Metadata)
	}
}

// TestFQNResolution_TSReexport verifies that a TS inheritance where the parent
// was resolved to a module-stem proxy (utils.Bar) by jsLocalNamedBindings,
// but the real symbol is in another file (other.Bar), is fixed by the resolver.
//
// Setup:
//   - other.ts: export class Bar {}          → real symbol other.Bar
//   - utils.ts: export { Bar } from './other'  → no sym for utils.Bar defined
//   - child.ts: import { Bar } from './utils'; class Child extends Bar {}
//
// Per-file resolution maps Bar → utils.Bar (module-stem). utils.Bar is not a
// real symbol, so the resolver detects a placeholder and looks up Bar → other.Bar.
func TestFQNResolution_TSReexport(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"src/other.ts": `export class Bar {
    method() {}
}
`,
		// utils.ts only re-exports Bar — no class definition here.
		"src/utils.ts": `export { Bar } from './other';
`,
		"src/child.ts": `import { Bar } from './utils';

export class Child extends Bar {
    childMethod() {}
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	childID := store.SymbolNodeID("child.Child")
	realBarID := store.SymbolNodeID("other.Bar")

	// The resolved edge must point to the real symbol.
	resolved := edgesFromTo(t, s, childID, realBarID)
	if len(resolved) != 1 {
		all := inheritsEdgesFrom(t, s, childID)
		var targets []string
		for _, e := range all {
			targets = append(targets, e.To)
		}
		t.Fatalf("expected 1 edge Child→other.Bar, got %d; all inherits from Child: %v", len(resolved), targets)
	}

	// The stale proxy edge sym:child.Child→sym:utils.Bar must be gone.
	proxy := edgesFromTo(t, s, childID, store.SymbolNodeID("utils.Bar"))
	if len(proxy) != 0 {
		t.Errorf("expected stale proxy edge deleted, got %d remaining", len(proxy))
	}
}

// TestFQNResolution_Idempotent verifies that running IndexProjectGraph twice
// produces the same result without duplicate edges.
func TestFQNResolution_Idempotent(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"src/com/example/Base.java": `package com.example;
public class Base {}
`,
		"src/com/example/Child.java": `package com.example;
public class Child extends Base {}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)

	for i := 0; i < 2; i++ {
		if _, err := idx.IndexProjectGraph(dir, true); err != nil {
			t.Fatalf("IndexProjectGraph run %d: %v", i+1, err)
		}
	}

	childID := store.SymbolNodeID("com.example.Child")
	baseID := store.SymbolNodeID("com.example.Base")

	// After two runs there must still be exactly one resolved edge.
	resolved := edgesFromTo(t, s, childID, baseID)
	if len(resolved) != 1 {
		t.Errorf("expected 1 resolved edge after re-index, got %d", len(resolved))
	}

	// Placeholder edge must be gone.
	placeholder := edgesFromTo(t, s, childID, store.SymbolNodeID("Base"))
	if len(placeholder) != 0 {
		t.Errorf("expected placeholder edge cleaned up after re-index, got %d", len(placeholder))
	}
}
