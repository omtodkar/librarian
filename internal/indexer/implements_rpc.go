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

// buildImplementsRPCEdges is the post-graph-pass resolver (lib-6wz) that
// connects every language's generated-code version of an rpc back to its
// proto declaration via an `implements_rpc` edge (symbol → symbol).
//
// For each rpc symbol node already persisted in graph_nodes (`pkg.Svc.Method`),
// the resolver registers a fixed set of expected derived names per codegen
// language and probes the store for matching symbol nodes. Each hit produces
// one edge from the generated symbol to the proto rpc — the proto symbol is
// the target, generated symbols are the sources.
//
// Per-language derivations (all share the proto package as the namespace
// prefix; lib-4kb will tighten via buf.gen.yaml path matching):
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
// Known MVP false positives: a hand-written symbol whose name happens to match
// one of the derivations (e.g. a non-generated `auth.AuthServiceClient.login`)
// still links. The downstream bead lib-4kb tightens the match to codegen-owned
// files via buf.gen.yaml path inspection; the Phase 3 resolver deliberately
// accepts this looseness rather than blocking on that work.
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
		result.EdgesAdded += idx.linkRPCImplementations(rpcPath)
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
func (idx *Indexer) linkRPCImplementations(rpcPath string) int {
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

	// Order here matches the godoc; dedupe via `seen` in the loop below
	// catches the SvcClient collisions between Dart and TS.
	candidates := []string{
		// Go (protoc-gen-go + grpc-go): PascalCase method, three surface
		// types per service (server interface, client interface, default
		// unimplemented server struct).
		pkg + "." + svc + "Server." + method,
		pkg + "." + svc + "Client." + method,
		pkg + ".Unimplemented" + svc + "Server." + method,
		// Dart (protoc-gen-dart): lowerCamelCase method, client + base types.
		pkg + "." + svc + "Client." + lcMethod,
		pkg + "." + svc + "Base." + lcMethod,
		// TypeScript (@bufbuild/protoc-gen-es): lowerCamelCase method, client
		// + service-interface types. SvcClient collides with Dart; dedupe.
		pkg + "." + svc + "Client." + lcMethod,
		pkg + "." + svc + "." + lcMethod,
	}

	target := store.SymbolNodeID(rpcPath)
	seen := map[string]bool{}
	emitted := 0
	for _, name := range candidates {
		if seen[name] {
			continue
		}
		seen[name] = true
		src := store.SymbolNodeID(name)
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
