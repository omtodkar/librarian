package indexer

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"librarian/internal/store"
)

// protoRPCMetadataMarker is the literal substring every proto rpc symbol
// node carries in its metadata and no other node kind does — the proto
// grammar's SymbolMetadata is the sole emitter of `"input_type":` on
// Unit.Metadata, and that flows through unitMetadataJSON into
// graph_nodes.metadata. Used by buildImplementsRPCEdges to identify rpc
// nodes via a single LIKE scan without re-reading .proto files.
const protoRPCMetadataMarker = `"input_type":`

// rpcCandidate is a prospective generated-code symbol derivation for a proto
// rpc Unit.Path. Each per-language naming convention produces zero or more
// candidates; the resolver probes each against the store and, when a buf
// manifest is available, additionally requires the store node's SourcePath
// to be rooted under the proto's language-specific codegen prefix.
//
// Carrying the language alongside the name lets the tightening check look up
// the correct prefix per candidate — a fixture with only a Go plugin in
// buf.gen.yaml should still tighten Go candidates while leaving Dart / TS
// candidates on the name-only path (since their prefixes aren't known).
type rpcCandidate struct {
	Name     string
	Language string
}

// buildImplementsRPCEdges is the post-graph-pass resolver (lib-6wz / lib-4kb)
// that connects every language's generated-code version of an rpc back to its
// proto declaration via an `implements_rpc` edge (symbol → symbol).
//
// For each rpc symbol node already persisted in graph_nodes (`pkg.Svc.Method`),
// the resolver registers a fixed set of expected derived names per codegen
// language and probes the store for matching symbol nodes. Each hit produces
// one edge from the generated symbol to the proto rpc — the proto symbol is
// the target, generated symbols are the sources.
//
// Per-language derivations (all share the proto package as the namespace
// prefix):
//
//   - Go (protoc-gen-go + grpc-go):
//     `pkg.SvcServer.Method`, `pkg.SvcClient.Method`,
//     `pkg.UnimplementedSvcServer.Method` — method PascalCase (same as proto).
//   - Dart (protoc-gen-dart):
//     `pkg.SvcClient.methodName`, `pkg.SvcBase.methodName` — method
//     lowerCamelCase.
//   - TypeScript (@bufbuild/protoc-gen-es):
//     `pkg.SvcClient.methodName`, `pkg.Svc.methodName` — method
//     lowerCamelCase.
//
// lib-4kb path tightening: when a buf manifest exists for the proto file that
// owns the rpc, the resolver additionally requires the candidate symbol's
// source_path to live under the manifest's language-specific codegen prefix.
// Hand-written code that happens to match a derivation (e.g. a
// non-generated `auth.AuthServiceClient.login`) drops out cleanly because
// its source path is outside the generator's output tree. When the manifest
// is absent, or missing a prefix for the candidate's language, the check
// falls back to name-only resolution so environments without buf.gen.yaml
// keep the lib-6wz behaviour instead of losing edges.
//
// Data source: graph_nodes, queried via the `"input_type":` metadata marker.
// This means the resolver works correctly on incremental re-index runs where
// the .proto file's hash is unchanged and its parse is skipped — the
// persisted rpc nodes are still found, so a new Go/Dart/TS implementer added
// in the same run still links to its unchanged proto rpc. It also avoids the
// every-run double-parse cost that earlier drafts of this resolver paid.
//
// Edges that fail to write (e.g. FK constraint on a racing delete) are
// skipped silently; the next re-index will re-emit them.
//
// Reads the path-tightening manifest via idx.currentBufManifest — stashed
// by IndexProjectGraph right before calling here, and cleared afterwards.
// Keeping the manifest off the method signature preserves lib-6wz's API so
// existing callers (and per-iteration timing / result accounting) don't
// shift when lib-4kb lands.
func (idx *Indexer) buildImplementsRPCEdges(result *GraphResult) {
	rpcNodes, err := idx.store.ListSymbolNodesWithMetadataContaining(protoRPCMetadataMarker)
	if err != nil {
		// Surface the failure on result.Errors rather than swallowing it —
		// matches indexGraphFile / buildGraphEdges so a SQLite I/O or WAL
		// error doesn't silently produce zero implements_rpc edges with no
		// diagnostic in the CLI output.
		result.Errors = append(result.Errors, fmt.Sprintf("implements_rpc resolver: %s", err))
		return
	}
	for _, n := range rpcNodes {
		rpcPath := strings.TrimPrefix(n.ID, "sym:")
		if rpcPath == n.ID {
			// Defensive: only symbol nodes should match the query (kind filter
			// is in SQL), but if a row somehow has an unprefixed id, skip
			// rather than emit a nonsense edge source.
			continue
		}
		result.EdgesAdded += idx.linkRPCImplementations(rpcPath, n.SourcePath)
	}
}

// linkRPCImplementations probes the store for generated-code derivations of a
// single rpc Unit.Path (dotted `pkg.Svc.Method`) and emits one
// `implements_rpc` edge per existing match. Returns the number of edges
// emitted so the caller can roll it into GraphResult.EdgesAdded.
//
// Bundles the per-language derivations into a flat candidate list and dedupes
// via a local set — Dart's `SvcClient.methodName` and TS's `SvcClient.methodName`
// are identical strings, and probing the store twice for the same node would
// double-count edges on the same (from, to, kind) key.
//
// When a manifest is available (via idx.currentBufManifest) and has a prefix
// for the candidate's language, the candidate's node.SourcePath is
// additionally required to live under that prefix — codegen-derivation
// check, dropping lib-6wz's MVP false positives (lib-4kb). No prefix for
// the language → fall back to name-only for that candidate; the tighter
// check is opt-in per-language.
func (idx *Indexer) linkRPCImplementations(rpcPath, rpcSourcePath string) int {
	parts := strings.Split(rpcPath, ".")
	if len(parts) < 3 {
		return 0
	}
	method := parts[len(parts)-1]
	svc := parts[len(parts)-2]
	pkg := strings.Join(parts[:len(parts)-2], ".")
	if pkg == "" || svc == "" || method == "" {
		return 0
	}
	lcMethod := lowerFirst(method)

	// Order matters for stable edge iteration during tests; dedupe via `seen`
	// in the loop below catches SvcClient collisions between Dart and TS.
	candidates := []rpcCandidate{
		// Go (protoc-gen-go + grpc-go): PascalCase method, three surface
		// types per service (server interface, client interface, default
		// unimplemented server struct).
		{pkg + "." + svc + "Server." + method, "go"},
		{pkg + "." + svc + "Client." + method, "go"},
		{pkg + ".Unimplemented" + svc + "Server." + method, "go"},
		// Dart (protoc-gen-dart): lowerCamelCase method, client + base types.
		{pkg + "." + svc + "Client." + lcMethod, "dart"},
		{pkg + "." + svc + "Base." + lcMethod, "dart"},
		// TypeScript (@bufbuild/protoc-gen-es): lowerCamelCase method, client
		// + service-interface types. SvcClient collides with Dart; dedupe.
		{pkg + "." + svc + "Client." + lcMethod, "ts"},
		{pkg + "." + svc + "." + lcMethod, "ts"},
	}

	target := store.SymbolNodeID(rpcPath)
	seen := map[string]bool{}
	emitted := 0
	for _, cand := range candidates {
		if seen[cand.Name] {
			continue
		}
		seen[cand.Name] = true
		src := store.SymbolNodeID(cand.Name)
		// Self-edge guard. Reachable when the proto rpc's method name already
		// starts with a lowercase letter (`rpc list(...)`), because lowerFirst
		// then leaves `method` untouched and the TS `pkg.Svc.methodName`
		// candidate collapses to the rpc's own sym: id. Legal in proto,
		// unconventional enough that its exercise lives in a dedicated test.
		if src == target {
			continue
		}
		node, err := idx.store.GetNode(src)
		if err != nil || node == nil {
			continue
		}
		if !candidateWithinCodegenTree(node, cand, rpcSourcePath, idx.currentBufManifest) {
			continue
		}
		if err := idx.store.UpsertEdge(store.Edge{
			From: src,
			To:   target,
			Kind: store.EdgeKindImplementsRPC,
		}); err != nil {
			continue
		}
		emitted++
	}
	return emitted
}

// candidateWithinCodegenTree reports whether the candidate symbol's
// source_path is within the proto's codegen output tree for the candidate's
// language — the lib-4kb tightening check.
//
// Fallback rules (all treated as "no tightening possible, accept name-only"):
//
//   - No manifest at all (nil) → no buf.gen.yaml in project; lib-6wz semantics.
//   - Manifest has no entry for the rpc's proto file → proto file never
//     had its options indexed AND had no rpc nodes processed this pass.
//   - Manifest entry has no prefix for the candidate's language → plugin
//     set doesn't cover this language; keep the candidate.
//   - Candidate node has no source_path → placeholder or malformed row;
//     can't prove it's outside the tree, so keep it rather than drop.
//
// When a prefix is available AND the candidate's source path is NOT under
// it, the candidate is dropped — this is where the false positives go.
func candidateWithinCodegenTree(node *store.Node, cand rpcCandidate, rpcSourcePath string, manifest *BufManifest) bool {
	if manifest == nil || rpcSourcePath == "" {
		return true
	}
	prefix, ok := manifest.LookupPrefix(rpcSourcePath, cand.Language)
	if !ok {
		return true
	}
	if node.SourcePath == "" {
		return true
	}
	return hasPathPrefix(node.SourcePath, prefix)
}

// lowerFirst returns s with its first rune lowercased. Used for PascalCase →
// lowerCamelCase method-name conversion used by protoc-gen-dart and
// @bufbuild/protoc-gen-es. Returns s unchanged when empty or when the first
// rune is already lowercase (idempotent on already-lowerCamelCase names).
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	if !unicode.IsUpper(r) {
		return s
	}
	return string(unicode.ToLower(r)) + s[size:]
}
