package indexer

import (
	"strings"
	"testing"
)

// TestFQNShortName covers the last-segment extraction used to build the
// workspace short-name map.
func TestFQNShortName(t *testing.T) {
	cases := []struct {
		fqn  string
		want string
	}{
		{"com.example.Foo", "Foo"},
		{"Foo", "Foo"},
		{"", ""},
		{"utils.Bar", "Bar"},
		{"a.b.c.D", "D"},
		{"single", "single"},
	}
	for _, tc := range cases {
		got := fqnShortName(tc.fqn)
		if got != tc.want {
			t.Errorf("fqnShortName(%q) = %q, want %q", tc.fqn, got, tc.want)
		}
	}
}

// TestFQNPackage covers the package-prefix extraction used for Java same-package
// resolution.
func TestFQNPackage(t *testing.T) {
	cases := []struct {
		fqn  string
		want string
	}{
		{"com.example.Foo", "com.example"},
		{"Foo", ""},
		{"", ""},
		{"utils.Bar", "utils"},
		{"a.b.c.D", "a.b.c"},
	}
	for _, tc := range cases {
		got := fqnPackage(tc.fqn)
		if got != tc.want {
			t.Errorf("fqnPackage(%q) = %q, want %q", tc.fqn, got, tc.want)
		}
	}
}

// TestFQNIsRealSourcePath verifies that real file paths (with separators) are
// distinguished from placeholder FQNs and bare names.
func TestFQNIsRealSourcePath(t *testing.T) {
	cases := []struct {
		src  string
		want bool
	}{
		{"src/com/example/Foo.java", true},
		{"src/utils.ts", true},
		{"internal/auth/service.go", true},
		{"com.example.Foo", false},   // dotted FQN
		{"Bar", false},               // bare name
		{"utils.Bar", false},         // module-stem.Member
		{"", false},                  // empty (placeholder with no source)
		{`C:\project\foo.java`, true}, // Windows path
	}
	for _, tc := range cases {
		got := fqnIsRealSourcePath(tc.src)
		if got != tc.want {
			t.Errorf("fqnIsRealSourcePath(%q) = %v, want %v", tc.src, got, tc.want)
		}
	}
}

// TestFQNBuildResolvedMeta covers all branches of the metadata transformer:
// empty input, standard JSON with unresolved key, ambiguous flag, and
// unparseable JSON.
func TestFQNBuildResolvedMeta(t *testing.T) {
	t.Run("empty_non_ambiguous", func(t *testing.T) {
		got := fqnBuildResolvedMeta("", false)
		if got != "{}" {
			t.Errorf("got %q, want {}", got)
		}
	})

	t.Run("empty_ambiguous", func(t *testing.T) {
		got := fqnBuildResolvedMeta("", true)
		if got != `{"ambiguous_resolution":true}` {
			t.Errorf("got %q, want ambiguous_resolution:true", got)
		}
	})

	t.Run("removes_unresolved_key", func(t *testing.T) {
		meta := `{"relation":"extends","unresolved":true}`
		got := fqnBuildResolvedMeta(meta, false)
		if strings.Contains(got, "unresolved") {
			t.Errorf("got %q; expected 'unresolved' removed", got)
		}
		if !strings.Contains(got, `"relation":"extends"`) {
			t.Errorf("got %q; expected 'relation' preserved", got)
		}
	})

	t.Run("adds_ambiguous_resolution", func(t *testing.T) {
		meta := `{"relation":"extends","unresolved":true}`
		got := fqnBuildResolvedMeta(meta, true)
		if strings.Contains(got, "unresolved") {
			t.Errorf("got %q; expected 'unresolved' removed", got)
		}
		if !strings.Contains(got, `"ambiguous_resolution":true`) {
			t.Errorf("got %q; expected 'ambiguous_resolution':true present", got)
		}
	})

	t.Run("unparseable_non_ambiguous", func(t *testing.T) {
		got := fqnBuildResolvedMeta("not-json", false)
		if got != "{}" {
			t.Errorf("got %q, want {} for unparseable input", got)
		}
	})

	t.Run("unparseable_ambiguous", func(t *testing.T) {
		got := fqnBuildResolvedMeta("not-json", true)
		if got != `{"ambiguous_resolution":true}` {
			t.Errorf("got %q, want ambiguous marker for unparseable input", got)
		}
	})

	t.Run("preserves_relation_key_sorted", func(t *testing.T) {
		// Keys must be sorted: ambiguous_resolution < relation.
		meta := `{"relation":"implements","unresolved":true}`
		got := fqnBuildResolvedMeta(meta, true)
		wantPrefix := `{"ambiguous_resolution":true,"relation":"implements"}`
		if got != wantPrefix {
			t.Errorf("got %q, want %q (sorted keys)", got, wantPrefix)
		}
	})
}
