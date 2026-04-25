package indexer

import (
	"encoding/json"
	"reflect"
	"testing"

	"librarian/internal/store"
)

// Tests for the shared helpers introduced by lib-wji.1 Phase 1: symbol-scoped
// edge sources, deterministic metadata serialisation, and target-node
// metadata promotion. Walker-level tests (extractParentRefs integration) land
// with the per-language grammar implementations in Phase 3+.

func TestRefEdgeSource_FileScopedFallback(t *testing.T) {
	ref := Reference{Kind: "import", Target: "java.util.List"}
	got := refEdgeSource(ref, "file:src/Foo.java")
	if got != "file:src/Foo.java" {
		t.Errorf("file-scoped ref should return defaultNodeID, got %q", got)
	}
}

func TestRefEdgeSource_SymbolScopedOverride(t *testing.T) {
	ref := Reference{
		Kind:   "inherits",
		Source: "com.example.Child",
		Target: "com.example.Base",
	}
	got := refEdgeSource(ref, "file:src/Child.java")
	if got != store.SymbolNodeID("com.example.Child") {
		t.Errorf("symbol-scoped ref should anchor at sym:<Source>, got %q", got)
	}
}

func TestRefMetadataJSON_EmptyReturnsEmptyString(t *testing.T) {
	// Empty metadata → empty string, which UpsertEdge/UpsertNode translate
	// to "{}". Keeps zero-value refs producing unchanged on-disk rows.
	for _, name := range []string{"nil", "empty"} {
		t.Run(name, func(t *testing.T) {
			ref := Reference{Kind: "import", Target: "foo"}
			if name == "empty" {
				ref.Metadata = map[string]any{}
			}
			if got := refMetadataJSON(ref); got != "" {
				t.Errorf("empty metadata should serialise to \"\", got %q", got)
			}
		})
	}
}

func TestRefMetadataJSON_DeterministicKeyOrder(t *testing.T) {
	// Non-deterministic key order would make INSERT OR REPLACE churn the
	// row bytes on every reindex even when the logical metadata is
	// unchanged. Run the serialiser repeatedly on the same input and
	// confirm byte-for-byte stability.
	ref := Reference{
		Kind: "inherits",
		Metadata: map[string]any{
			"relation":   "implements",
			"type_args":  []string{"K", "V"},
			"unresolved": true,
			"alias":      "Base",
			"node_kind":  "symbol",
		},
	}
	first := refMetadataJSON(ref)
	for i := 0; i < 50; i++ {
		if got := refMetadataJSON(ref); got != first {
			t.Fatalf("run %d produced different JSON than first run:\nfirst: %s\nnow:   %s", i, first, got)
		}
	}

	// Also confirm the JSON is valid and round-trips.
	var back map[string]any
	if err := json.Unmarshal([]byte(first), &back); err != nil {
		t.Fatalf("serialised metadata is not valid JSON: %v (%s)", err, first)
	}
	if back["relation"] != "implements" {
		t.Errorf("round-trip lost relation: %+v", back)
	}
}

func TestRefMetadataJSON_SortedKeys(t *testing.T) {
	// Explicit assertion on sort order so the contract is visible: keys
	// are emitted alphabetically, which is what makes the output
	// deterministic across runs regardless of Go's map iteration order.
	ref := Reference{
		Metadata: map[string]any{
			"zebra":  1,
			"apple":  2,
			"middle": 3,
		},
	}
	got := refMetadataJSON(ref)
	want := `{"apple":2,"middle":3,"zebra":1}`
	if got != want {
		t.Errorf("sorted-key serialisation = %q, want %q", got, want)
	}
}

func TestTargetNodeMetadataJSON_UnresolvedPromotes(t *testing.T) {
	ref := Reference{
		Kind:     "inherits",
		Target:   "Base",
		Metadata: map[string]any{"unresolved": true, "relation": "extends"},
	}
	got := targetNodeMetadataJSON(ref)
	// Only `unresolved=true` bubbles to the node — relation / type_args
	// are edge-level and stay on the edge. Hardcoded shape because there
	// should only ever be this one key on a target node today.
	want := `{"unresolved":true}`
	if got != want {
		t.Errorf("target-node metadata = %q, want %q", got, want)
	}
}

func TestTargetNodeMetadataJSON_ResolvedReturnsEmpty(t *testing.T) {
	// Resolved refs (no `unresolved` key, or unresolved=false) must not
	// produce a metadata payload for the target node — that would
	// otherwise overwrite a real node's metadata via
	// UpsertPlaceholderNode's nil-metadata handling (which translates
	// to "{}" and therefore would overwrite if we used UpsertNode; the
	// placeholder variant leaves existing rows alone regardless).
	cases := map[string]Reference{
		"no_metadata":        {Kind: "inherits", Target: "Base"},
		"unresolved_false":   {Kind: "inherits", Target: "Base", Metadata: map[string]any{"unresolved": false}},
		"unresolved_missing": {Kind: "inherits", Target: "Base", Metadata: map[string]any{"relation": "extends"}},
	}
	for name, ref := range cases {
		t.Run(name, func(t *testing.T) {
			if got := targetNodeMetadataJSON(ref); got != "" {
				t.Errorf("resolved ref target-node metadata = %q, want \"\"", got)
			}
		})
	}
}

func TestGraphTargetID_InheritsRoutesToSymbol(t *testing.T) {
	ref := Reference{Kind: "inherits", Target: "com.example.Base"}
	got := graphTargetID(ref)
	want := store.SymbolNodeID("com.example.Base")
	if got != want {
		t.Errorf("inherits targetID = %q, want %q", got, want)
	}
	if graphNodeKindFromRef(ref) != store.NodeKindSymbol {
		t.Errorf("inherits nodeKind = %q, want %q", graphNodeKindFromRef(ref), store.NodeKindSymbol)
	}
}

func TestGraphTargetID_LegacyExtendsImplementsStillResolve(t *testing.T) {
	// Legacy kinds are preserved as aliases so hand-authored fixtures and
	// any pre-lib-wji.1 on-disk data keep resolving to sym: nodes. Regression
	// guard against an over-eager cleanup that removes the aliases.
	for _, kind := range []string{"extends", "implements"} {
		ref := Reference{Kind: kind, Target: "Base"}
		if got := graphTargetID(ref); got != store.SymbolNodeID("Base") {
			t.Errorf("legacy %q targetID = %q, want %q", kind, got, store.SymbolNodeID("Base"))
		}
		if got := graphNodeKindFromRef(ref); got != store.NodeKindSymbol {
			t.Errorf("legacy %q nodeKind = %q, want %q", kind, got, store.NodeKindSymbol)
		}
	}
}

func TestReferenceMetadataKeys_DocConvention(t *testing.T) {
	// Smoke test that the conventional metadata keys we document on the
	// Reference godoc all round-trip through refMetadataJSON cleanly.
	// Catches accidental reliance on a specific type (e.g., if someone
	// changed type_args to []any and broke []string round-trips).
	ref := Reference{
		Metadata: map[string]any{
			"alias":                 "B",
			"static":                true,
			"node_kind":             "external",
			"relation":              "conforms",
			"type_args":             []string{"T"},
			"unresolved":            false,
			"unresolved_expression": true,
		},
	}
	raw := refMetadataJSON(ref)
	var back map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("JSON decode: %v (%s)", err, raw)
	}
	// JSON numbers come back as float64 and []string as []any — normalise
	// for the comparison.
	wantKeys := []string{"alias", "node_kind", "relation", "static", "type_args", "unresolved", "unresolved_expression"}
	gotKeys := make([]string, 0, len(back))
	for k := range back {
		gotKeys = append(gotKeys, k)
	}
	if len(gotKeys) != len(wantKeys) {
		t.Errorf("round-trip lost keys: got %v, want %v", gotKeys, wantKeys)
	}
	// Spot-check a couple of values.
	if back["alias"] != "B" || back["unresolved_expression"] != true {
		t.Errorf("value mismatch: %+v", back)
	}
	// type_args comes back as []any; ensure the string slice survived.
	args, ok := back["type_args"].([]any)
	if !ok || len(args) != 1 || args[0] != "T" {
		t.Errorf("type_args round-trip: got %#v", back["type_args"])
	}
	_ = reflect.TypeOf(back)
}

// TestIsSymbolKind pins the allow-list of Unit.Kind values that project
// into graph_nodes{kind=symbol}. Regression guard for the lib-wji.2
// latent bug where Kotlin's "object" and "property" Units were silently
// dropped from the graph because isSymbolKind didn't include them.
func TestIsSymbolKind(t *testing.T) {
	for _, k := range []string{
		// Cross-grammar core kinds.
		"function", "method", "constructor",
		"class", "interface", "enum", "record",
		"type", "field",
		// Kotlin-contributed (lib-wji.2).
		"object", "property",
	} {
		if !isSymbolKind(k) {
			t.Errorf("isSymbolKind(%q) = false, want true", k)
		}
	}
	// Non-code Unit kinds must stay out of the graph.
	for _, k := range []string{
		"section", "paragraph", "key-path", "page", "row", "table", "",
	} {
		if isSymbolKind(k) {
			t.Errorf("isSymbolKind(%q) = true, want false (non-code kind)", k)
		}
	}
}
