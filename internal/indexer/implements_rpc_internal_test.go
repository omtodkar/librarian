package indexer

import (
	"os"
	"path/filepath"
	"testing"

	"librarian/internal/config"
	"librarian/internal/store"
)

// newImplementsRPCInternalIndexer opens a minimal Indexer+Store for internal
// implements_rpc tests. Lives in package indexer so unexported methods are
// reachable. Mirrors the external openImplementsRPCStore helper shape.
func newImplementsRPCInternalIndexer(t *testing.T) (*Indexer, *store.Store) {
	t.Helper()
	dir := t.TempDir()
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
		ProjectRoot: dir,
		Graph:       config.GraphConfig{MaxWorkers: 1},
	}
	return New(s, cfg, nil), s
}

// TestLowerFirst pins the PascalCase → lowerCamelCase helper that the
// implements_rpc resolver leans on for the Dart / TS derivations. Covers:
//
//   - empty input (no first rune to touch)
//   - single PascalCase ASCII (`L` → `l`)
//   - already-lowercase idempotency (`login`, `über` unchanged)
//   - multi-character PascalCase (`GetCurrentUser` → `getCurrentUser`)
//   - all-uppercase input — only the first rune lowercases; the tail is
//     preserved untouched. Regression guard for a future rewrite that
//     reaches for `strings.ToLower(s)` and would silently lowercase the
//     whole string (breaking acronym-based method names like `URL` →
//     `uRL` not `url`).
//   - multi-byte unicode initial rune (`Über` → `über`). Guards against a
//     naïve `s[0] |= 0x20` optimisation that would corrupt the two-byte
//     UTF-8 sequence.
//   - non-letter initial rune (digit, symbol) — unicode.IsUpper returns
//     false so the input passes through unchanged.
//
// Lives in package indexer (not indexer_test) so the unexported
// lowerFirst identifier is reachable without a test-only export shim.
func TestLowerFirst(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty", "", ""},
		{"already_lowercase_idempotent", "login", "login"},
		{"single_uppercase_letter", "L", "l"},
		{"pascal_ascii", "Login", "login"},
		{"pascal_multichar", "GetCurrentUser", "getCurrentUser"},
		{"all_caps_only_first_lowercased", "ABC", "aBC"},
		{"digits_after_initial_letter", "A1B2", "a1B2"},
		{"multibyte_initial_uppercase", "Über", "über"},
		{"multibyte_initial_lowercase_idempotent", "über", "über"},
		{"non_letter_leading_digit", "1stPlace", "1stPlace"},
		{"non_letter_leading_symbol", "_Login", "_Login"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lowerFirst(c.in); got != c.want {
				t.Errorf("lowerFirst(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestGraphTargetID_ImplementsRPCRoutesToSymbol is a direct unit verification
// that Reference.Kind="implements_rpc" is plumbed through the reference-to-node
// mapping. Sits in the same package as graphTargetID / graphNodeKindFromRef
// so we call the unexported helpers directly — the previous external-test
// variant indexed a whole proto+Go fixture just to make the assertion, which
// hid the actual invariant behind a lot of unrelated plumbing.
//
// Lives alongside TestGraphTargetID_InheritsRoutesToSymbol / the legacy-kind
// routing tests in inherits_test.go.
func TestGraphTargetID_ImplementsRPCRoutesToSymbol(t *testing.T) {
	ref := Reference{Kind: "implements_rpc", Target: "pkg.Svc.Method"}
	if got, want := graphTargetID(ref), store.SymbolNodeID("pkg.Svc.Method"); got != want {
		t.Errorf("graphTargetID(implements_rpc).target = %q, want %q", got, want)
	}
	if got, want := graphNodeKindFromRef(ref), store.NodeKindSymbol; got != want {
		t.Errorf("graphNodeKindFromRef(implements_rpc).kind = %q, want %q", got, want)
	}
}

// TestBuildImplementsRPCEdges_StoreErrorPopulatesErrors pins the error-return
// branch: when ListSymbolNodesWithMetadataContaining fails (e.g. closed DB),
// buildImplementsRPCEdges must append to result.Errors rather than swallowing
// the failure silently.
func TestBuildImplementsRPCEdges_StoreErrorPopulatesErrors(t *testing.T) {
	idx, s := newImplementsRPCInternalIndexer(t)
	// Close the store so every DB call returns "sql: database is closed".
	s.Close()

	result := &GraphResult{}
	idx.buildImplementsRPCEdges(result) // must not panic
	if len(result.Errors) == 0 {
		t.Error("expected result.Errors populated on store failure, got none")
	}
}

// TestBuildImplementsRPCEdges_DotLeadingSymSkippedGracefully pins the pkg==""
// defensive guard in linkRPCImplementations: a sym: node whose path starts
// with a dot (e.g. sym:.Svc.Method) produces an empty pkg segment after the
// dot-split, so the resolver skips it without emitting an edge or an error.
// The proto grammar maintains the non-empty-pkg invariant upstream; this test
// guards the graceful-skip path for any malformed row that bypasses it.
func TestBuildImplementsRPCEdges_DotLeadingSymSkippedGracefully(t *testing.T) {
	idx, s := newImplementsRPCInternalIndexer(t)

	// Seed a sym: node with a dot-leading path. The protoRPCMetadataMarker
	// substring in its Metadata causes ListSymbolNodesWithMetadataContaining
	// to return it, exercising the linkRPCImplementations code path.
	malformedID := store.SymbolNodeID(".Svc.Method") // "sym:.Svc.Method"
	if err := s.UpsertNode(store.Node{
		ID:       malformedID,
		Kind:     store.NodeKindSymbol,
		Label:    ".Svc.Method",
		Metadata: `{"input_type": "some.Request"}`,
	}); err != nil {
		t.Fatalf("seed malformed node: %v", err)
	}

	result := &GraphResult{}
	idx.buildImplementsRPCEdges(result) // must not panic, must skip the node
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors for dot-leading sym: node; got %v", result.Errors)
	}
	if result.EdgesAdded != 0 {
		t.Errorf("EdgesAdded = %d, want 0 (malformed node skipped by pkg==\"\" guard)", result.EdgesAdded)
	}
}
