package indexer

import (
	"testing"

	"librarian/internal/store"
)

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
