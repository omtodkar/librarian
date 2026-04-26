package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestIsBufGenFile pins the filename matcher — required for the graph-pass
// metadata forwarder to distinguish a buf codegen config from any other YAML
// file whose content might happen to contain a "plugins:" key (kustomize,
// GitHub Actions reusable workflows).
func TestIsBufGenFile(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"buf.gen.yaml", true},
		{"buf.gen.yml", true},
		{"proto/buf.gen.yaml", true},
		{"deep/nested/buf.gen.yml", true},
		{"buf.yaml", false},       // module config, not codegen config
		{"buf.work.yaml", false},  // workspace config
		{"buf.lock", false},       // dep lock
		{"something.yaml", false}, // random yaml
		{"Buf.gen.yaml", false},   // case-sensitive (matches upstream buf CLI semantics)
		{"buf.gen.yaml.bak", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsBufGenFile(c.path); got != c.want {
			t.Errorf("IsBufGenFile(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

// TestLanguageFromPluginIdentity pins classification for every shape the YAML
// handler hands us. Coverage: bare names (v1 `name:`), remote specs
// (`remote: buf.build/...`), local binaries (`local: protoc-gen-*`),
// language-agnostic plugins (validate, breaking) → empty string, unknown
// suffix → empty string.
func TestLanguageFromPluginIdentity(t *testing.T) { //nolint:revive // Lives in same package — calls unexported languageFromPluginIdentity.
	cases := []struct {
		identity string
		want     string
	}{
		{"", ""},
		// v1 bare names
		{"go", "go"},
		{"go-grpc", "go"},
		{"dart", "dart"},
		{"es", "ts"},
		{"connect-es", "ts"},
		// v2 remote specs
		{"buf.build/protocolbuffers/go", "go"},
		{"buf.build/grpc/go", "go"},
		{"buf.build/bufbuild/es", "ts"},
		{"buf.build/connectrpc/es", "ts"},
		{"custom-org/protocolbuffers/go", "go"}, // suffix-based, so third-party orgs classify
		// v2 local binaries
		{"protoc-gen-go", "go"},
		{"protoc-gen-go-grpc", "go"},
		{"protoc-gen-dart", "dart"},
		{"protoc-gen-es", "ts"},
		{"protoc-gen-connect-es", "ts"},
		// Unclassified — must return "" so the manifest builder skips
		// rather than miscategorising.
		{"buf.build/bufbuild/validate", ""},
		{"java", ""},
		{"python", ""},
		{"protoc-gen-java", ""},
		{"buf.build/grpc/python", ""},
	}
	for _, c := range cases {
		if got := languageFromPluginIdentity(c.identity); got != c.want {
			t.Errorf("languageFromPluginIdentity(%q) = %q, want %q", c.identity, got, c.want)
		}
	}
}

// TestLanguageFromPluginIdentity_LocalConnectShapes pins the explicit
// connect-go / connect-dart / query arms added by lib-4g2.1. These are
// distinct from the existing connect-es arm and from the generic go/dart
// prefix rules — they must classify correctly whether the identity arrives
// as a bare name, as a local binary (after protoc-gen- stripping), or via
// a remote buf.build path.
func TestLanguageFromPluginIdentity_LocalConnectShapes(t *testing.T) {
	cases := []struct {
		identity string
		want     string
	}{
		// Local binary shapes (after protoc-gen- prefix stripping).
		{"protoc-gen-connect-go", "go"},
		{"protoc-gen-connect-dart", "dart"},
		{"protoc-gen-connect-es", "ts"},  // existing arm, regression guard
		// Bare names (v1 `name:` field).
		{"connect-go", "go"},
		{"connect-dart", "dart"},
		{"connect-es", "ts"},  // existing arm, regression guard
		// Remote buf.build paths.
		{"buf.build/connectrpc/go", "go"},
		{"buf.build/connectrpc/dart", "dart"},
		// Connect-Query (TypeScript hooks generator). The remote buf.build
		// form has last segment "query"; local binary form "protoc-gen-query"
		// strips to "query". Both classify as ts via the exact-match arm.
		// See the comment in languageFromPluginIdentity for the trade-off.
		{"query", "ts"},
		{"protoc-gen-query", "ts"},
		{"buf.build/connectrpc/query", "ts"},
	}
	for _, c := range cases {
		if got := languageFromPluginIdentity(c.identity); got != c.want {
			t.Errorf("languageFromPluginIdentity(%q) = %q, want %q", c.identity, got, c.want)
		}
	}
}

// TestParseBufGenPlugins_V1 covers buf v1 shape: top-level `plugins:` list
// with `name:` identities and optional `opt:` (scalar or list).
func TestParseBufGenPlugins_V1(t *testing.T) {
	src := `version: v1
plugins:
  - name: go
    out: gen/go
    opt:
      - paths=source_relative
  - name: dart
    out: gen/dart
  - name: validate
    out: gen/validate
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plugins := ParseBufGenPlugins(&root)
	if len(plugins) != 3 {
		t.Fatalf("plugins count = %d, want 3; got %+v", len(plugins), plugins)
	}
	if plugins[0].Language != "go" || plugins[0].Out != "gen/go" {
		t.Errorf("plugin[0] = %+v", plugins[0])
	}
	if len(plugins[0].Opt) != 1 || plugins[0].Opt[0] != "paths=source_relative" {
		t.Errorf("plugin[0].Opt = %+v", plugins[0].Opt)
	}
	if plugins[1].Language != "dart" || plugins[1].Out != "gen/dart" {
		t.Errorf("plugin[1] = %+v", plugins[1])
	}
	// "validate" → Language="" (resolver doesn't derive names for it), but
	// the plugin is still returned so MCP tools can inspect out-dirs.
	if plugins[2].Language != "" || plugins[2].Out != "gen/validate" {
		t.Errorf("plugin[2] = %+v", plugins[2])
	}
}

// TestParseBufGenPlugins_V2 covers buf v2 shape: `remote:` / `local:`
// identities, opt as scalar.
func TestParseBufGenPlugins_V2(t *testing.T) {
	src := `version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: gen/go
    opt: paths=source_relative
  - local: protoc-gen-dart
    out: gen/dart
  - local: [protoc-gen-es]
    out: gen/ts
    opt:
      - target=ts
      - import_extension=none
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plugins := ParseBufGenPlugins(&root)
	if len(plugins) != 3 {
		t.Fatalf("plugins count = %d, want 3: %+v", len(plugins), plugins)
	}
	if plugins[0].Language != "go" || plugins[0].Out != "gen/go" {
		t.Errorf("plugin[0] = %+v", plugins[0])
	}
	if len(plugins[0].Opt) != 1 || plugins[0].Opt[0] != "paths=source_relative" {
		t.Errorf("plugin[0].Opt = %+v (expected scalar flattened to single-entry slice)", plugins[0].Opt)
	}
	if plugins[1].Language != "dart" || plugins[1].Out != "gen/dart" {
		t.Errorf("plugin[1] = %+v", plugins[1])
	}
	// local as a sequence — first element wins.
	if plugins[2].Language != "ts" || plugins[2].Out != "gen/ts" {
		t.Errorf("plugin[2] = %+v", plugins[2])
	}
	if len(plugins[2].Opt) != 2 {
		t.Errorf("plugin[2].Opt = %+v (expected 2 entries)", plugins[2].Opt)
	}
}

// TestParseBufGenPlugins_LocalMultiElementSequence pins bufGenLocalIdentity's
// rule that the first element of a `local:` argv-style list wins. Buf accepts
// the sequence form when users pass flags to their local binary
// (`local: [protoc-gen-go, --experimental_allow_proto3_optional]`); only the
// executable name should reach the language classifier.
func TestParseBufGenPlugins_LocalMultiElementSequence(t *testing.T) {
	src := `version: v2
plugins:
  - local: [protoc-gen-go, --foo=bar, --baz]
    out: gen/go
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plugins := ParseBufGenPlugins(&root)
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin; got %+v", plugins)
	}
	// Classification took only the first element (protoc-gen-go → go);
	// trailing flags never reached languageFromPluginIdentity.
	if plugins[0].Language != "go" {
		t.Errorf("plugins[0].Language = %q, want go (trailing flags should be ignored)", plugins[0].Language)
	}
	if plugins[0].Out != "gen/go" {
		t.Errorf("plugins[0].Out = %q, want gen/go", plugins[0].Out)
	}
}

// TestParseBufGenPlugins_SkipEmpty verifies plugins without an out-dir are
// dropped — they can't contribute to a codegen path prefix, and persisting
// them would just clutter the manifest.
func TestParseBufGenPlugins_SkipEmpty(t *testing.T) {
	src := `version: v1
plugins:
  - name: go
    out: gen/go
  - name: java
    # no out — must be dropped
  - out: orphan
`
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	plugins := ParseBufGenPlugins(&root)
	if len(plugins) != 2 {
		t.Fatalf("plugins count = %d, want 2 (java dropped, orphan kept because out present): %+v", len(plugins), plugins)
	}
	// First plugin (go) classifies; second ("out: orphan" with no identity)
	// has empty Language but keeps its out-dir so the manifest builder still
	// sees it (and simply ignores it because Language is empty).
	if plugins[0].Language != "go" || plugins[0].Out != "gen/go" {
		t.Errorf("plugins[0] = %+v", plugins[0])
	}
	if plugins[1].Language != "" || plugins[1].Out != "orphan" {
		t.Errorf("plugins[1] = %+v", plugins[1])
	}
}

// TestParseBufGenPlugins_Malformed pins graceful handling of inputs that
// aren't shaped like a buf.gen.yaml — missing plugins, plugins-as-mapping,
// nil root. No crash; no spurious entries.
func TestParseBufGenPlugins_Malformed(t *testing.T) {
	cases := []struct {
		name, src string
	}{
		{"empty", ""},
		{"no_plugins_key", "version: v1\n"},
		{"plugins_not_sequence", "plugins: {name: go, out: gen/go}\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var root yaml.Node
			_ = yaml.Unmarshal([]byte(c.src), &root) // errors on some inputs; ok
			plugins := ParseBufGenPlugins(&root)
			if len(plugins) != 0 {
				t.Errorf("expected 0 plugins from %q; got %+v", c.src, plugins)
			}
		})
	}

	t.Run("nil_root", func(t *testing.T) {
		if plugins := ParseBufGenPlugins(nil); plugins != nil {
			t.Errorf("nil root: expected nil, got %+v", plugins)
		}
	})

	// Unicode / non-ASCII values in `name`, `remote`, or `out` must not
	// panic the parser or poison the manifest. languageFromPluginIdentity
	// returns "" for unknown classifications; the plugin keeps its out-dir
	// and a later language-resolver lookup treats it as "no prefix known"
	// and falls back to name-only matching for that rpc.
	t.Run("unicode_identity_and_out", func(t *testing.T) {
		src := `version: v1
plugins:
  - name: "日本語"
    out: "gen/生成"
  - remote: "buf.build/emoji/🚀"
    out: "gen/🚀"
`
		var root yaml.Node
		if err := yaml.Unmarshal([]byte(src), &root); err != nil {
			t.Fatalf("unmarshal unicode: %v", err)
		}
		plugins := ParseBufGenPlugins(&root)
		if len(plugins) != 2 {
			t.Fatalf("expected 2 plugins; got %d: %+v", len(plugins), plugins)
		}
		for i, p := range plugins {
			if p.Language != "" {
				t.Errorf("plugins[%d].Language = %q, want \"\" (unknown classification)", i, p.Language)
			}
			if p.Out == "" {
				t.Errorf("plugins[%d].Out = \"\" — unicode out-dir was dropped", i)
			}
		}
	})
}

// TestYAMLHandler_BufGenAttachedOnBufGenFile pins the integration boundary:
// parsing a buf.gen.yaml through the YAML handler attaches a structured
// "buf_gen" entry to ParsedDoc.Metadata, which is what the graph pass
// forwards to the code_file node. A non-buf YAML file must NOT carry
// "buf_gen" even if its content happens to have a plugins: key.
func TestYAMLHandler_BufGenAttachedOnBufGenFile(t *testing.T) {
	h := NewYAML()
	src := []byte(`version: v1
plugins:
  - name: go
    out: gen/go
`)
	doc, err := h.Parse("buf.gen.yaml", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plugins, ok := doc.Metadata["buf_gen"].([]BufGenPlugin)
	if !ok {
		t.Fatalf("Metadata[buf_gen] missing or wrong type: %T / %+v", doc.Metadata["buf_gen"], doc.Metadata["buf_gen"])
	}
	if len(plugins) != 1 || plugins[0].Language != "go" || plugins[0].Out != "gen/go" {
		t.Errorf("plugins = %+v", plugins)
	}

	// Non-buf yaml with the same content must NOT attach buf_gen.
	docOther, err := h.Parse("some/other/config.yaml", src)
	if err != nil {
		t.Fatalf("Parse other: %v", err)
	}
	if _, ok := docOther.Metadata["buf_gen"]; ok {
		t.Errorf("buf_gen leaked into non-buf yaml: %+v", docOther.Metadata)
	}
}
