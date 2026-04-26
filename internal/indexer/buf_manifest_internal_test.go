package indexer

import (
	"testing"

	"librarian/internal/store"
)

// storeNodeStub is a minimal stand-in used by TestCandidateWithinCodegenTree —
// the check only reads node.SourcePath, so the rest of store.Node stays
// zero-valued. Keeps the test table above ergonomic while avoiding any
// real DB plumbing for pure-logic coverage.
type storeNodeStub struct{ SourcePath string }

func (s *storeNodeStub) asStoreNode() *store.Node { return &store.Node{SourcePath: s.SourcePath} }

// TestHasPathPrefix pins the path-under-prefix check the implements_rpc
// resolver leans on to decide whether a candidate's source file lives under
// a proto's codegen output tree. Covers:
//
//   - Exact match (prefix equals sourcePath)
//   - Match with trailing file under the prefix directory
//   - Directory-level match with / handling
//   - Partial-name collisions (gen/go/a is NOT under gen/go/auth)
//   - Trailing slash on prefix is tolerated
//   - Empty prefix / empty sourcePath degenerate to false (can't prove
//     containment, safer to fall back to name-only than silently accept)
func TestHasPathPrefix(t *testing.T) {
	cases := []struct {
		sourcePath, prefix string
		want               bool
	}{
		{"gen/go/authpb/auth.pb.go", "gen/go/authpb", true},
		{"gen/go/authpb", "gen/go/authpb", true},
		{"gen/go/authpb/", "gen/go/authpb", true},
		{"gen/go/authpb", "gen/go/authpb/", true},
		{"gen/go/authpb/nested/x.go", "gen/go/authpb", true},
		// Partial-name collisions must NOT match.
		{"gen/go/authpb_v2/x.go", "gen/go/authpb", false},
		{"gen/go/auth/x.go", "gen/go/authpb", false},
		// Unrelated paths.
		{"internal/customauth/client.go", "gen/go/authpb", false},
		// Degenerate empty inputs.
		{"", "gen/go", false},
		{"gen/go/x.go", "", false},
	}
	for _, c := range cases {
		if got := hasPathPrefix(c.sourcePath, c.prefix); got != c.want {
			t.Errorf("hasPathPrefix(%q, %q) = %v, want %v", c.sourcePath, c.prefix, got, c.want)
		}
	}
}

// TestGoPackageLastSegment pins the protoc-gen-go path-segment extractor the
// manifest builder uses when paths=source_relative is absent. Covers the
// three conventional go_package shapes plus the degenerate cases.
func TestGoPackageLastSegment(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"authpb", "authpb"},
		{"github.com/example/authpb", "authpb"},
		{"github.com/example/authpb;authpb", "authpb"},       // semicolon separates dir from go identifier
		{"github.com/example/authpb;pkgname", "authpb"},      // path wins over identifier
		{"github.com/example/authpb/", "authpb"},             // trailing slash tolerated
		{"github.com/example/authpb;authpb/extra", "authpb"}, // identifier with slash: only path (pre-`;`) counts
		{"example", "example"},
	}
	for _, c := range cases {
		if got := goPackageLastSegment(c.in); got != c.want {
			t.Errorf("goPackageLastSegment(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestJoinPrefix pins the slash-joined path builder used when assembling
// manifest prefixes. Keeps workspace-relative paths forward-slash-rooted
// on every host.
func TestJoinPrefix(t *testing.T) {
	cases := []struct {
		base, sub, want string
	}{
		{"gen/go", "authpb", "gen/go/authpb"},
		{"gen/go/", "authpb", "gen/go/authpb"},
		{"gen/go", "/authpb", "gen/go/authpb"},
		{"gen/go", "authpb/", "gen/go/authpb"},
		{"gen/go", "", "gen/go"},
		{"gen/go/", "", "gen/go"},
		{"gen/go", "a/b", "gen/go/a/b"},
	}
	for _, c := range cases {
		if got := joinPrefix(c.base, c.sub); got != c.want {
			t.Errorf("joinPrefix(%q, %q) = %q, want %q", c.base, c.sub, got, c.want)
		}
	}
}

// TestHasSourceRelativeOpt pins the opt-scan that drives Go prefix
// computation. Small but load-bearing: a regression (e.g. case-folding the
// match, missing "paths=source_relative") would silently mislabel every
// Go plugin's prefix.
func TestHasSourceRelativeOpt(t *testing.T) {
	cases := []struct {
		opts []string
		want bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"paths=source_relative"}, true},
		{[]string{" paths=source_relative "}, true},
		{[]string{"paths=import"}, false},
		{[]string{"other=opt", "paths=source_relative"}, true},
	}
	for _, c := range cases {
		if got := hasSourceRelativeOpt(c.opts); got != c.want {
			t.Errorf("hasSourceRelativeOpt(%+v) = %v, want %v", c.opts, got, c.want)
		}
	}
}

// TestBufManifest_LookupPrefix_Nil ensures LookupPrefix on a nil manifest
// returns (""; false) — the resolver depends on this to skip tightening
// wholesale when no buf.gen.yaml is present.
func TestBufManifest_LookupPrefix_Nil(t *testing.T) {
	var m *BufManifest
	if prefix, ok := m.LookupPrefix("api/auth.proto", "go"); ok || prefix != "" {
		t.Errorf("(nil).LookupPrefix = (%q, %v), want (\"\", false)", prefix, ok)
	}
}

// TestBufManifest_LookupPrefix_Hit / Miss cover the in-memory lookup.
func TestBufManifest_LookupPrefix(t *testing.T) {
	m := &BufManifest{Entries: map[string]*BufManifestEntry{
		"api/auth.proto": {
			ProtoPath:    "api/auth.proto",
			ProtoPackage: "auth",
			LangPrefixes: map[string]string{"go": "gen/go/authpb"},
		},
	}}

	if prefix, ok := m.LookupPrefix("api/auth.proto", "go"); !ok || prefix != "gen/go/authpb" {
		t.Errorf("hit: got (%q, %v), want (gen/go/authpb, true)", prefix, ok)
	}
	// Language without a prefix → miss.
	if prefix, ok := m.LookupPrefix("api/auth.proto", "dart"); ok || prefix != "" {
		t.Errorf("missing language: got (%q, %v), want (\"\", false)", prefix, ok)
	}
	// Proto file not in manifest → miss.
	if prefix, ok := m.LookupPrefix("api/other.proto", "go"); ok || prefix != "" {
		t.Errorf("missing proto: got (%q, %v), want (\"\", false)", prefix, ok)
	}
}

// TestAssembleManifestEntry pins prefix computation for the three supported
// languages with and without paths=source_relative. Uses the pure function so
// the test doesn't need a store; the integration tests in
// implements_rpc_test.go exercise end-to-end persistence + resolver use.
func TestAssembleManifestEntry(t *testing.T) {
	plugins := []bufGenPluginSerialized{
		{Language: "go", Out: "gen/go"},
		{Language: "dart", Out: "gen/dart"},
		{Language: "ts", Out: "gen/ts"},
	}

	t.Run("default_go_uses_go_package", func(t *testing.T) {
		entry := assembleManifestEntry("api/auth.proto", protoFileEntry{
			options: map[string]string{"go_package": "github.com/example/authpb"},
			pkg:     "auth",
		}, plugins)
		if entry.LangPrefixes["go"] != "gen/go/authpb" {
			t.Errorf("go prefix = %q, want gen/go/authpb", entry.LangPrefixes["go"])
		}
		if entry.LangPrefixes["dart"] != "gen/dart/api" {
			t.Errorf("dart prefix = %q, want gen/dart/api", entry.LangPrefixes["dart"])
		}
		if entry.LangPrefixes["ts"] != "gen/ts/api" {
			t.Errorf("ts prefix = %q, want gen/ts/api", entry.LangPrefixes["ts"])
		}
		if entry.ProtoPackage != "auth" {
			t.Errorf("proto_package = %q, want auth", entry.ProtoPackage)
		}
	})

	t.Run("source_relative_go_uses_proto_dir", func(t *testing.T) {
		pluginsSR := []bufGenPluginSerialized{
			{Language: "go", Out: "gen/go", Opt: []string{"paths=source_relative"}},
		}
		entry := assembleManifestEntry("api/auth.proto", protoFileEntry{
			options: map[string]string{"go_package": "github.com/example/authpb"},
		}, pluginsSR)
		// source_relative overrides go_package for the path convention.
		if entry.LangPrefixes["go"] != "gen/go/api" {
			t.Errorf("go prefix (source_relative) = %q, want gen/go/api", entry.LangPrefixes["go"])
		}
	})

	t.Run("go_without_go_package_drops", func(t *testing.T) {
		entry := assembleManifestEntry("api/auth.proto", protoFileEntry{options: nil}, plugins)
		if _, ok := entry.LangPrefixes["go"]; ok {
			t.Errorf("go prefix should be absent without go_package and without source_relative; got %q", entry.LangPrefixes["go"])
		}
		// Dart / TS still get prefixes because they don't depend on go_package.
		if entry.LangPrefixes["dart"] != "gen/dart/api" {
			t.Errorf("dart prefix = %q, want gen/dart/api", entry.LangPrefixes["dart"])
		}
	})

	t.Run("root_level_proto", func(t *testing.T) {
		// A .proto at the workspace root has no directory component; prefix
		// collapses to just <out>.
		entry := assembleManifestEntry("auth.proto", protoFileEntry{
			options: map[string]string{"go_package": "github.com/example/authpb"},
		}, plugins)
		if entry.LangPrefixes["go"] != "gen/go/authpb" {
			t.Errorf("go prefix = %q (go_package-based, unaffected by root-level proto)", entry.LangPrefixes["go"])
		}
		if entry.LangPrefixes["dart"] != "gen/dart" {
			t.Errorf("dart prefix = %q, want gen/dart (root-level collapses subdir)", entry.LangPrefixes["dart"])
		}
	})

	t.Run("plugin_without_language_is_skipped", func(t *testing.T) {
		pluginsWithJava := []bufGenPluginSerialized{
			{Language: "", Out: "gen/java"},
			{Language: "go", Out: "gen/go"},
		}
		entry := assembleManifestEntry("api/auth.proto", protoFileEntry{
			options: map[string]string{"go_package": "github.com/example/authpb"},
		}, pluginsWithJava)
		if _, ok := entry.LangPrefixes[""]; ok {
			t.Errorf("empty-language entry leaked: %+v", entry.LangPrefixes)
		}
		if entry.LangPrefixes["go"] != "gen/go/authpb" {
			t.Errorf("go prefix dropped when java plugin sat alongside; got %q", entry.LangPrefixes["go"])
		}
	})

	t.Run("same_language_duplicate_first_wins", func(t *testing.T) {
		// `go` + `go-grpc` both classify as Language="go" but commonly carry
		// different out-dirs in combined configs (protoc-gen-go-grpc ships
		// alongside protoc-gen-go). Manifest should yield exactly one go
		// prefix — the first plugin's, as the godoc in assembleManifestEntry
		// documents.
		dupGo := []bufGenPluginSerialized{
			{Language: "go", Out: "gen/go"},
			{Language: "go", Out: "gen/grpc"},
		}
		entry := assembleManifestEntry("api/auth.proto", protoFileEntry{
			options: map[string]string{"go_package": "github.com/example/authpb"},
		}, dupGo)
		if entry.LangPrefixes["go"] != "gen/go/authpb" {
			t.Errorf("first-go-wins violated: got %q, want gen/go/authpb", entry.LangPrefixes["go"])
		}
		if len(entry.LangPrefixes) != 1 {
			t.Errorf("unexpected multi-prefix result: %+v (want one go prefix)", entry.LangPrefixes)
		}
	})
}

// TestCandidateWithinCodegenTree pins each fallback branch of the tightening
// check — a matrix test protects against future conditions silently flipping
// which branch "no prefix known" takes.
func TestCandidateWithinCodegenTree(t *testing.T) {
	manifest := &BufManifest{Entries: map[string]*BufManifestEntry{
		"api/auth.proto": {
			LangPrefixes: map[string]string{"go": "gen/go/authpb"},
		},
	}}
	node := func(sp string) *storeNodeStub { return &storeNodeStub{SourcePath: sp} }

	cases := []struct {
		name          string
		manifest      *BufManifest
		rpcSourcePath string
		candidate     rpcCandidate
		candidateNode *storeNodeStub
		wantAccepted  bool
	}{
		{
			name:          "nil_manifest_accepts",
			manifest:      nil,
			rpcSourcePath: "api/auth.proto",
			candidate:     rpcCandidate{Language: "go"},
			candidateNode: node("anywhere/x.go"),
			wantAccepted:  true,
		},
		{
			name:          "no_entry_for_proto_accepts",
			manifest:      manifest,
			rpcSourcePath: "api/other.proto",
			candidate:     rpcCandidate{Language: "go"},
			candidateNode: node("gen/go/otherpb/x.go"),
			wantAccepted:  true,
		},
		{
			name:          "no_prefix_for_language_accepts",
			manifest:      manifest,
			rpcSourcePath: "api/auth.proto",
			candidate:     rpcCandidate{Language: "dart"}, // manifest has go only
			candidateNode: node("wherever/x.dart"),
			wantAccepted:  true,
		},
		{
			name:          "empty_source_path_accepts",
			manifest:      manifest,
			rpcSourcePath: "api/auth.proto",
			candidate:     rpcCandidate{Language: "go"},
			candidateNode: node(""),
			wantAccepted:  true,
		},
		{
			name:          "empty_rpc_source_accepts",
			manifest:      manifest,
			rpcSourcePath: "",
			candidate:     rpcCandidate{Language: "go"},
			candidateNode: node("gen/go/authpb/x.go"),
			wantAccepted:  true,
		},
		{
			name:          "in_prefix_accepts",
			manifest:      manifest,
			rpcSourcePath: "api/auth.proto",
			candidate:     rpcCandidate{Language: "go"},
			candidateNode: node("gen/go/authpb/x.go"),
			wantAccepted:  true,
		},
		{
			name:          "out_of_prefix_drops",
			manifest:      manifest,
			rpcSourcePath: "api/auth.proto",
			candidate:     rpcCandidate{Language: "go"},
			candidateNode: node("internal/customauth/x.go"),
			wantAccepted:  false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := candidateWithinCodegenTree(c.candidateNode.asStoreNode(), c.candidate, c.rpcSourcePath, c.manifest)
			if got != c.wantAccepted {
				t.Errorf("candidateWithinCodegenTree = %v, want %v", got, c.wantAccepted)
			}
		})
	}
}
