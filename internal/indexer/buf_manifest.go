package indexer

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"librarian/internal/store"
)

// BufManifestEntry is the per-proto-file output of buildBufManifest — the
// working subset of `buf.gen.yaml` ∩ `.proto` options that the implements_rpc
// resolver needs to tighten a candidate match from name-only to name+path.
//
// ProtoPath is the workspace-relative .proto path (matches the proto node's
// source_path, which is the resolver's starting point for every rpc node).
// ProtoPackage mirrors the proto file's `package foo.bar;` directive and
// matches the dotted-prefix the resolver derives candidates under.
//
// LangPrefixes maps language keys ("go" / "dart" / "ts") to workspace-relative
// codegen path prefixes. Values are directory paths without a trailing slash
// so callers use hasPathPrefix uniformly regardless of which language the
// candidate came from. A language absent from the map means no tight prefix
// is known for it — the resolver falls through to name-only for THAT
// language's candidates, preserving lib-6wz's behaviour.
type BufManifestEntry struct {
	ProtoPath    string            `json:"proto_path"`
	ProtoPackage string            `json:"proto_package,omitempty"`
	LangPrefixes map[string]string `json:"lang_prefixes"`
}

// BufManifest is the whole-project manifest assembled from every indexed
// buf.gen.yaml + .proto. Keyed by proto file path (which is what the resolver
// retrieves first from a proto rpc node's source_path). A nil manifest means
// no buf.gen.yaml was ever found — lib-6wz's name-only resolver takes over.
type BufManifest struct {
	// Entries maps workspace-relative proto path → entry for that file.
	Entries map[string]*BufManifestEntry
}

// bufGenPluginSerialized mirrors config.BufGenPlugin with JSON tags matching
// what the YAML handler wrote. Kept package-local to avoid dragging the
// config package into store consumers that just want the manifest output.
type bufGenPluginSerialized struct {
	Language string   `json:"language,omitempty"`
	Out      string   `json:"out"`
	Opt      []string `json:"opt,omitempty"`
}

// buildBufManifest is the post-graph-pass step (before the implements_rpc
// resolver) that assembles an in-memory BufManifest from every code_file
// graph_node whose metadata carries either "buf_gen" (buf.gen.yaml plugin
// list) or "options" (proto file-level *_package options). Errors surface on
// result.Errors rather than being fatal — a broken manifest falls back to
// name-only resolution, matching the no-buf.gen.yaml path.
//
// The assembled manifest is also persisted as one buf_manifest graph_node per
// proto file (id = "bufgen:<proto-path>", kind = "buf_manifest"). MCP tools
// reading the graph downstream can inspect per-file codegen path prefixes
// without re-parsing buf.gen.yaml — a requirement from the task spec.
//
// Returns nil when there's no buf.gen.yaml or no proto options to act on;
// the resolver treats nil as "no manifest" and falls back wholesale.
func (idx *Indexer) buildBufManifest(result *GraphResult) *BufManifest {
	plugins, err := idx.loadBufGenPlugins()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("buf manifest: load buf.gen.yaml: %s", err))
		return nil
	}
	if len(plugins) == 0 {
		// No buf.gen.yaml anywhere in the project — fall back entirely. Don't
		// emit buf_manifest nodes or touch the store.
		return nil
	}

	protoFiles, err := idx.loadProtoOptionFiles()
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("buf manifest: load proto options: %s", err))
		return nil
	}
	// A buf.gen.yaml with no proto files yet indexed is a valid intermediate
	// state (e.g. early indexing on a fresh checkout). Rather than skipping
	// here, we still enumerate proto files from graph_nodes via a separate
	// marker — the proto rpc `"input_type":` marker — so files without file-
	// level options still get a default manifest entry. We also use the rpc
	// nodes' sym: paths to stamp each entry's ProtoPackage field for display.
	rpcFiles, err := loadProtoFilesFromRPCNodes(idx)
	if err != nil {
		// Match the loader-error convention established by loadBufGenPlugins /
		// loadProtoOptionFiles: surface on result.Errors so the CLI shows one
		// diagnostic instead of producing an empty-but-silent manifest. Don't
		// bail — the options-only proto files above may still produce usable
		// entries.
		result.Errors = append(result.Errors, fmt.Sprintf("buf manifest: load proto rpc nodes: %s", err))
	}
	for protoPath, pkg := range rpcFiles {
		pf, ok := protoFiles[protoPath]
		if !ok {
			pf = protoFileEntry{}
		}
		if pf.pkg == "" {
			pf.pkg = pkg
		}
		protoFiles[protoPath] = pf
	}

	manifest := &BufManifest{Entries: map[string]*BufManifestEntry{}}
	for protoPath, pf := range protoFiles {
		entry := assembleManifestEntry(protoPath, pf, plugins)
		manifest.Entries[protoPath] = entry

		meta, err := json.Marshal(entry)
		if err != nil {
			// Marshal failure on our own struct would mean a programming
			// error, not a user input issue — surface it but don't bail on
			// the remaining entries.
			result.Errors = append(result.Errors, fmt.Sprintf("buf manifest: marshal %s: %s", protoPath, err))
			continue
		}
		err = idx.store.UpsertNode(store.Node{
			ID:         store.BufManifestNodeID(protoPath),
			Kind:       store.NodeKindBufManifest,
			Label:      entry.ProtoPackage,
			SourcePath: protoPath,
			Metadata:   string(meta),
		})
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("buf manifest: upsert %s: %s", protoPath, err))
		}
	}
	return manifest
}

// loadBufGenPlugins scans every code_file graph_node whose metadata contains
// the "buf_gen" key marker and returns the decoded plugin list from each.
// Multiple buf.gen.yaml files across the project get flattened into one list
// — the resolver only cares about per-language out-dirs, and picking the
// first one per language is close enough for the tightening heuristic.
func (idx *Indexer) loadBufGenPlugins() ([]bufGenPluginSerialized, error) {
	nodes, err := idx.store.ListNodesByKindWithMetadataContaining(store.NodeKindCodeFile, `"buf_gen"`)
	if err != nil {
		return nil, err
	}
	var out []bufGenPluginSerialized
	for _, n := range nodes {
		// Skip non-buf yaml files that happen to contain the literal string
		// "buf_gen" in their metadata (defensive — none today, but LIKE would
		// otherwise false-positive on anything with that substring).
		if !isBufGenFile(n.SourcePath) {
			continue
		}
		plugins, err := decodeBufGenFromNodeMetadata(n.Metadata)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", n.SourcePath, err)
		}
		out = append(out, plugins...)
	}
	return out, nil
}

// isBufGenFile mirrors config.IsBufGenFile without the cross-package import
// (config → indexer is already an established direction via handler
// registration, so the reverse would cycle). The filename rule is small and
// stable enough that a local duplicate is cheaper than factoring a shared
// constants package.
func isBufGenFile(p string) bool {
	base := filepath.Base(p)
	return base == "buf.gen.yaml" || base == "buf.gen.yml"
}

// hasSourceRelativeOpt mirrors config.BufGenHasSourceRelative for the same
// reason as isBufGenFile — tiny helper, duplicating avoids a package cycle.
func hasSourceRelativeOpt(opts []string) bool {
	for _, o := range opts {
		if strings.TrimSpace(o) == "paths=source_relative" {
			return true
		}
	}
	return false
}

// decodeBufGenFromNodeMetadata pulls the "buf_gen" array out of a code_file
// node's metadata JSON. Tolerates a missing key (returns nil) so callers
// don't have to pre-check.
func decodeBufGenFromNodeMetadata(meta string) ([]bufGenPluginSerialized, error) {
	if meta == "" || meta == "{}" {
		return nil, nil
	}
	var outer struct {
		BufGen []bufGenPluginSerialized `json:"buf_gen"`
	}
	if err := json.Unmarshal([]byte(meta), &outer); err != nil {
		return nil, err
	}
	return outer.BufGen, nil
}

// protoFileEntry is the per-proto-file intermediate the manifest assembler
// works off. options holds the file-level `option <name> = "<value>"` map
// emitted by the proto grammar's PostProcess. Nil/empty is valid — the
// manifest entry still gets default per-language prefixes derived from the
// proto's directory.
type protoFileEntry struct {
	options map[string]string
	// pkg is the proto `package` directive; populated lazily by the assembler
	// when it's cheap to derive (from the proto rpc node metadata iteration).
	pkg string
}

// loadProtoOptionFiles scans every code_file graph_node whose metadata
// contains "options" and returns a map from proto path to its options. Only
// .proto files that have at least one file-level option land here — .proto
// files with zero options get picked up by loadProtoFilesFromRPCNodes.
func (idx *Indexer) loadProtoOptionFiles() (map[string]protoFileEntry, error) {
	nodes, err := idx.store.ListNodesByKindWithMetadataContaining(store.NodeKindCodeFile, `"options"`)
	if err != nil {
		return nil, err
	}
	out := map[string]protoFileEntry{}
	for _, n := range nodes {
		if !strings.HasSuffix(n.SourcePath, ".proto") {
			// "options" as a substring is common enough (any config schema
			// with an "options" key could land here); filter to actual proto
			// files so the manifest stays proto-scoped.
			continue
		}
		opts, err := decodeProtoOptionsFromNodeMetadata(n.Metadata)
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", n.SourcePath, err)
		}
		out[n.SourcePath] = protoFileEntry{options: opts}
	}
	return out, nil
}

// decodeProtoOptionsFromNodeMetadata pulls the "options" map out of a
// code_file node's metadata JSON.
func decodeProtoOptionsFromNodeMetadata(meta string) (map[string]string, error) {
	if meta == "" || meta == "{}" {
		return nil, nil
	}
	var outer struct {
		Options map[string]string `json:"options"`
	}
	if err := json.Unmarshal([]byte(meta), &outer); err != nil {
		return nil, err
	}
	return outer.Options, nil
}

// loadProtoFilesFromRPCNodes returns a map from .proto file path → proto
// package name for every proto file that has at least one rpc symbol node.
// The package is recovered from the rpc symbol's sym: id (`pkg.Svc.Method` →
// first len-2 segments joined). Catches .proto files with no file-level
// options — the manifest entry is still useful for Dart/TS, whose generator
// conventions don't depend on `*_package` options.
//
// Lookups use the same "input_type" marker the lib-6wz resolver leans on
// (proto-grammar-only Unit.Metadata key), so no extra schema is required.
// Errors are surfaced to the caller rather than swallowed: a
// SQLite I/O failure should show up on result.Errors alongside the other
// manifest loaders, not silently produce an empty manifest.
func loadProtoFilesFromRPCNodes(idx *Indexer) (map[string]string, error) {
	out := map[string]string{}
	rpcs, err := idx.store.ListSymbolNodesWithMetadataContaining(protoRPCMetadataMarker)
	if err != nil {
		return out, err
	}
	for _, n := range rpcs {
		if n.SourcePath == "" {
			continue
		}
		// Extract proto package from `sym:pkg.Svc.Method` by trimming the
		// `sym:` prefix and dropping the last two dotted segments.
		raw := strings.TrimPrefix(n.ID, "sym:")
		parts := strings.Split(raw, ".")
		if len(parts) < 3 {
			// Malformed rpc path — keep the proto path entry but leave
			// package empty. Won't break the resolver since it looks up
			// by proto path, not package.
			if _, ok := out[n.SourcePath]; !ok {
				out[n.SourcePath] = ""
			}
			continue
		}
		pkg := strings.Join(parts[:len(parts)-2], ".")
		if existing, ok := out[n.SourcePath]; ok && existing != "" {
			continue
		}
		out[n.SourcePath] = pkg
	}
	return out, nil
}

// assembleManifestEntry computes per-language prefixes for a single proto
// file using the plugin list harvested from every buf.gen.yaml. The function
// is intentionally permissive: languages without a matching plugin get no
// entry in LangPrefixes, not an error, so the resolver naturally falls back
// for that language alone.
//
// Go prefix convention:
//
//   - paths=source_relative → <out>/<proto-source-dir>
//   - otherwise            → <out>/<last-segment-of-go_package-option>
//     (protoc-gen-go's default; go_package's pre-`;` path segment decides
//     the package directory)
//
// Dart / TS prefix convention:
//
//   - Both behave like source_relative by default; prefix = <out>/<proto-source-dir>.
//     paths=source_relative is either a no-op or not meaningful for these
//     generators, so we don't branch on it.
//
// When a convention can't be applied (Go without go_package AND without
// source_relative), that language simply doesn't get a prefix in the entry.
func assembleManifestEntry(protoPath string, pf protoFileEntry, plugins []bufGenPluginSerialized) *BufManifestEntry {
	entry := &BufManifestEntry{
		ProtoPath:    protoPath,
		ProtoPackage: pf.pkg,
		LangPrefixes: map[string]string{},
	}
	// path.Dir expects forward-slash-separated input. filepath.ToSlash is a
	// no-op on unix but collapses `\` into `/` on Windows where the walker
	// may hand us native-separator paths. Keeping the prefix forward-slash-
	// rooted is the codebase's workspace-relative convention (matches every
	// CodeFileNodeID / SymbolNodeID producer).
	protoDir := path.Dir(filepath.ToSlash(protoPath))
	if protoDir == "." {
		protoDir = ""
	}

	for _, p := range plugins {
		if p.Language == "" || p.Out == "" {
			continue
		}
		// First plugin per language wins; the resolver only needs one
		// prefix per language per proto file. This means a repo with both
		// `go` and `go-grpc` plugins listed (which share the same out-dir
		// in virtually every real config) gets a stable prefix either way.
		if _, ok := entry.LangPrefixes[p.Language]; ok {
			continue
		}

		prefix := computeLangPrefix(p, protoDir, pf.options)
		if prefix == "" {
			continue
		}
		entry.LangPrefixes[p.Language] = prefix
	}
	return entry
}

// computeLangPrefix applies the per-language prefix convention for one plugin
// given the proto file's directory and options. Returns "" when no prefix
// can be derived with the data we have, signalling the caller to leave that
// language out of LangPrefixes (resolver falls back to name-only for it).
func computeLangPrefix(p bufGenPluginSerialized, protoDir string, opts map[string]string) string {
	out := strings.TrimSuffix(p.Out, "/")
	if out == "" {
		return ""
	}
	switch p.Language {
	case "go":
		if hasSourceRelativeOpt(p.Opt) {
			return joinPrefix(out, protoDir)
		}
		pkg := opts["go_package"]
		if seg := goPackageLastSegment(pkg); seg != "" {
			return joinPrefix(out, seg)
		}
		// No path convention available — can't tighten Go candidates.
		return ""
	case "dart", "ts":
		// Both generators place files mirroring the source tree, so
		// paths=source_relative / default produce the same prefix.
		return joinPrefix(out, protoDir)
	}
	return ""
}

// joinPrefix builds a "<base>/<sub>" path with a trailing-slash-free result,
// collapsing the degenerate empty-sub case to just base. filepath.Join would
// also work but normalises separators on Windows; we want slash-joined
// workspace-relative paths regardless of the host OS.
func joinPrefix(base, sub string) string {
	base = strings.TrimSuffix(base, "/")
	sub = strings.Trim(sub, "/")
	if sub == "" {
		return base
	}
	return base + "/" + sub
}

// goPackageLastSegment returns the trailing path segment of a `go_package`
// value, which is the directory protoc-gen-go writes the generated file to.
//
// Shapes accepted:
//
//   - "github.com/example/authpb"              → "authpb"
//   - "github.com/example/authpb;authpb"       → "authpb"
//   - "github.com/example/authpb;pkgname"      → "authpb"  (path wins; the
//     post-`;` tail is the Go package identifier, not a directory)
//   - "authpb"                                 → "authpb"
//   - ""                                       → ""
func goPackageLastSegment(val string) string {
	if val == "" {
		return ""
	}
	pathPart := val
	if i := strings.Index(pathPart, ";"); i >= 0 {
		pathPart = pathPart[:i]
	}
	pathPart = strings.TrimSpace(pathPart)
	pathPart = strings.TrimSuffix(pathPart, "/")
	if idx := strings.LastIndex(pathPart, "/"); idx >= 0 {
		return pathPart[idx+1:]
	}
	return pathPart
}

// LookupPrefix returns the codegen path prefix for a given proto file and
// language, or ("", false) when no manifest entry or language-specific
// prefix exists. A nil manifest always returns ("", false) so callers can
// dispatch their no-manifest fallback without a separate nil check.
func (m *BufManifest) LookupPrefix(protoPath, language string) (string, bool) {
	if m == nil {
		return "", false
	}
	entry, ok := m.Entries[protoPath]
	if !ok {
		return "", false
	}
	prefix, ok := entry.LangPrefixes[language]
	return prefix, ok
}

// hasPathPrefix reports whether sourcePath is rooted at prefix — i.e. matches
// prefix exactly or lives one or more directory levels below. Used by the
// implements_rpc resolver to decide whether a candidate symbol's source file
// is within a proto's codegen output tree.
//
// Both inputs are path.Clean'd before the comparison so `..` / repeated
// separators / trailing slashes can't produce false-positive matches
// (`gen/go` vs `gen/go/../go/authpb`). Empty prefix returns false
// (degenerate "any path matches" is worse than falling back to name-only
// for that case).
//
// Inputs are assumed forward-slash workspace-relative — the buf manifest
// normalises its own prefixes (assembleManifestEntry runs filepath.ToSlash),
// and the walker emits forward-slash source paths on unix while the
// code_file node's source_path is the same workspace-relative value. Windows
// paths that reached this layer with `\` separators would need to be
// normalised upstream — not done here to avoid paying the cost per call.
//
// Unexported — only the in-package resolver (candidateWithinCodegenTree)
// needs it; the previous exported form was called from external test code
// and was dead weight in the production API surface.
func hasPathPrefix(sourcePath, prefix string) bool {
	sp := path.Clean(strings.TrimSuffix(sourcePath, "/"))
	pf := path.Clean(strings.TrimSuffix(prefix, "/"))
	if sourcePath == "" || prefix == "" || pf == "." || sp == "." {
		return false
	}
	return sp == pf || strings.HasPrefix(sp, pf+"/")
}
