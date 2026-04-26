package config

import (
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// BufGenPlugin summarises one entry in a `buf.gen.yaml` `plugins` list —
// everything the implements_rpc resolver (lib-4kb) needs to compute where
// generated code for a given proto file lands under this plugin's output tree.
//
// Language normalises the raw yaml identity (v1 `name:`, v2 `remote:` /
// `local:`) to one of {"go", "dart", "ts"} — the set the implements_rpc
// resolver derives names for today. Unknown plugins (buf.build/bufbuild/validate,
// language outputs we don't yet link) get Language="" and are skipped by the
// resolver rather than tripping an error. The raw identity is intentionally
// not persisted — the resolver only needs classification + out-dir + opts.
//
// Opt carries `opt:` entries verbatim (e.g. "paths=source_relative"); the
// manifest builder parses only the subset it needs (source_relative today).
type BufGenPlugin struct {
	Language string   `json:"language,omitempty"`
	Out      string   `json:"out"`
	Opt      []string `json:"opt,omitempty"`
}

// IsBufGenFile reports whether path names a Buf codegen config file. Exported
// so the YAML handler's graph-pass metadata stashing can detect it without
// duplicating the string match.
//
// buf supports both `.yaml` and `.yml`; per Buf's conventions the file lives
// at the repo root, but users do nest them (e.g. `proto/buf.gen.yaml`), so we
// match on base name alone. Nested configs still give the manifest builder
// correct-enough prefixes for the resolver's tightening — worst case a stray
// config file names outdirs that don't match any generated tree and the
// resolver silently falls through to name-only matching for those packages.
func IsBufGenFile(path string) bool {
	base := filepath.Base(path)
	return base == "buf.gen.yaml" || base == "buf.gen.yml"
}

// ParseBufGenPlugins walks the `plugins:` sequence at the root of a decoded
// buf.gen.yaml document and returns one BufGenPlugin per entry. Missing or
// malformed `plugins` yields nil (not an error) — a buf.gen.yaml without a
// plugins key is meaningless but shouldn't crash the graph pass.
//
// Fields consumed:
//
//   - identity: v1 `name:` (e.g. "go"), v2 `remote:` (e.g.
//     "buf.build/protocolbuffers/go") or `local:` (e.g. "protoc-gen-dart"
//     — string or single-element sequence).
//   - out: workspace-relative output directory (required for the resolver;
//     an entry with empty out is skipped).
//   - opt: single string or sequence of strings — flattened into []string.
//
// Unknown keys on the plugin mapping are ignored; future buf schema
// additions don't regress this parser.
func ParseBufGenPlugins(root *yaml.Node) []BufGenPlugin {
	if root == nil {
		return nil
	}
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return nil
	}
	var pluginsNode *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i]
		if key.Value == "plugins" {
			pluginsNode = root.Content[i+1]
			break
		}
	}
	if pluginsNode == nil || pluginsNode.Kind != yaml.SequenceNode {
		return nil
	}
	var out []BufGenPlugin
	for _, item := range pluginsNode.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}
		plugin := bufGenPluginFromNode(item)
		if plugin.Out == "" {
			// Plugin with no out-dir can't be used for path matching; the
			// resolver would fall back to name-only for any proto it would
			// have applied to, so dropping it here keeps the persisted
			// manifest tidy.
			continue
		}
		out = append(out, plugin)
	}
	return out
}

// bufGenPluginFromNode decodes one mapping node under `plugins:` into a
// BufGenPlugin. Tolerates both v1 (`name:`) and v2 (`remote:` / `local:`)
// shapes in a single pass — whichever identity key appears first wins, which
// matches buf's own tolerance for mixed-style configs inside a file. The raw
// identity is transient: used to classify Language via
// languageFromPluginIdentity and then discarded.
func bufGenPluginFromNode(n *yaml.Node) BufGenPlugin {
	var plugin BufGenPlugin
	var identity string
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		val := n.Content[i+1]
		switch key {
		case "name", "remote":
			if identity == "" && val.Kind == yaml.ScalarNode {
				identity = strings.TrimSpace(val.Value)
			}
		case "local":
			if identity == "" {
				identity = bufGenLocalIdentity(val)
			}
		case "out":
			if val.Kind == yaml.ScalarNode {
				plugin.Out = strings.TrimSpace(val.Value)
			}
		case "opt":
			plugin.Opt = bufGenOpts(val)
		}
	}
	plugin.Language = languageFromPluginIdentity(identity)
	return plugin
}

// bufGenLocalIdentity extracts the display identity from a `local:` value. Buf
// accepts either a scalar (single path/name) or a sequence (argv-style list
// whose first element is the executable). We return the first element in
// either case — that's the name downstream languageFromPluginIdentity
// pattern-matches on.
func bufGenLocalIdentity(val *yaml.Node) string {
	switch val.Kind {
	case yaml.ScalarNode:
		return strings.TrimSpace(val.Value)
	case yaml.SequenceNode:
		if len(val.Content) > 0 && val.Content[0].Kind == yaml.ScalarNode {
			return strings.TrimSpace(val.Content[0].Value)
		}
	}
	return ""
}

// bufGenOpts normalises `opt:` into a []string. Buf accepts either a scalar
// (single `opt: paths=source_relative`) or a sequence (one option per line).
// Callers downstream only grep for known keys (e.g. "paths=source_relative"),
// so flattening is safe and avoids branching in the consumer.
func bufGenOpts(val *yaml.Node) []string {
	switch val.Kind {
	case yaml.ScalarNode:
		if v := strings.TrimSpace(val.Value); v != "" {
			return []string{v}
		}
	case yaml.SequenceNode:
		out := make([]string, 0, len(val.Content))
		for _, item := range val.Content {
			if item.Kind == yaml.ScalarNode {
				if v := strings.TrimSpace(item.Value); v != "" {
					out = append(out, v)
				}
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// languageFromPluginIdentity normalises a buf plugin identity into the
// implements_rpc language set {"go", "dart", "ts"}. Returns "" for plugins
// the resolver doesn't derive names for (java, python, validate, …) so the
// manifest builder can skip them without special-casing.
//
// Recognised shapes:
//
//   - Bare name (v1 `name:`): "go", "go-grpc", "dart", "es", "connect-es"
//   - Remote path (v2 `remote:`): "buf.build/protocolbuffers/go",
//     "buf.build/grpc/go", "buf.build/bufbuild/es", "buf.build/connectrpc/es"
//   - Local binary (v2 `local:`): "protoc-gen-go", "protoc-gen-go-grpc",
//     "protoc-gen-dart", "protoc-gen-es", "protoc-gen-connect-es"
//
// Matching is suffix-based on the trailing segment after any `/`, so new
// orgs hosting e.g. `buf.build/custom-org/go` still classify correctly.
// Unexported — no production caller outside this package, and the export
// contract was speculative. Re-export if/when an MCP tool needs it.
func languageFromPluginIdentity(identity string) string {
	if identity == "" {
		return ""
	}
	name := identity
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimPrefix(name, "protoc-gen-")
	switch {
	case name == "go", strings.HasPrefix(name, "go-"), name == "connect-go":
		return "go"
	case name == "dart", strings.HasPrefix(name, "dart-"), name == "connect-dart":
		return "dart"
	// "query" is buf.build/connectrpc/query (Connect-Query TypeScript hooks
	// generator); its last path segment is the literal word "query" with no
	// language-keyword alias. This arm is intentionally narrow: no HasPrefix
	// branch, so only the exact token classifies. The remote form
	// buf.build/connectrpc/query reaches this branch because the function
	// strips everything up to the final "/". A hypothetical unrelated plugin
	// whose last segment is also "query" would incorrectly classify as TS,
	// but (a) no such plugin exists in buf's public registry today and (b) a
	// misclassification here only adds an incorrect TS prefix constraint
	// rather than creating a false-positive implements_rpc edge.
	case name == "es", strings.HasPrefix(name, "es-"), name == "connect-es", name == "query":
		return "ts"
	}
	return ""
}
