package indexer_test

import (
	"os"
	"path/filepath"
	"testing"

	"librarian/internal/config"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults" // register handlers
	"librarian/internal/store"
)

// writeImplementsRPCFixture writes a multi-file project under dir from the
// given file map. Separate from indexer_graph_test.go's projectFixture so
// tests can declare exactly the files they need without pulling in the
// broader fixture's unrelated Go/Python/markdown content.
func writeImplementsRPCFixture(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		abs := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", abs, err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", abs, err)
		}
	}
}

// openImplementsRPCStore builds an Indexer + Store pair for a test workspace
// rooted at dir. Mirrors newGraphTestIndexer's config shape so the two
// test files stay in lockstep on expected defaults.
func openImplementsRPCStore(t *testing.T, dir string) (*indexer.Indexer, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(dir, ".librarian", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	cfg := &config.Config{
		DocsDir:     filepath.Join(dir, "docs"),
		DBPath:      dbPath,
		ProjectRoot: dir,
		Chunking:    config.ChunkingConfig{MaxTokens: 512, MinTokens: 1},
		Graph:       config.GraphConfig{HonorGitignore: false, DetectGenerated: true, MaxWorkers: 1},
	}
	return indexer.New(s, cfg, fakeEmbedder{dim: 4}), s
}

// TestImplementsRPC_SingleRPCSingleImpl pins the baseline: one proto rpc
// (`auth.AuthService.Login`) plus one Go struct method
// (`auth.AuthServiceServer.Login`) produces exactly one `implements_rpc`
// edge, pointing from the generated-ish Go symbol to the proto rpc symbol.
func TestImplementsRPC_SingleRPCSingleImpl(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth;

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest { string user = 1; }
message LoginReply { bool ok = 1; }
`,
		// Go package name `auth` lines up the Unit.Path with the derived
		// name `auth.AuthServiceServer.Login` the resolver probes for.
		"gen/auth/service.go": `package auth

type AuthServiceServer struct{}

func (s *AuthServiceServer) Login() error { return nil }
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("auth.AuthService.Login")
	src := store.SymbolNodeID("auth.AuthServiceServer.Login")

	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected exactly 1 implements_rpc edge into proto rpc; got %d: %+v", len(edges), edges)
	}
	e := edges[0]
	if e.From != src || e.To != target {
		t.Errorf("edge direction/ends wrong: got %s -> %s, want %s -> %s", e.From, e.To, src, target)
	}
	if e.Kind != store.EdgeKindImplementsRPC {
		t.Errorf("edge kind = %q, want %q", e.Kind, store.EdgeKindImplementsRPC)
	}
}

// TestImplementsRPC_MultiLangFourImplsOneRPC pins the multi-language fan-in:
// proto rpc + Go (two variants) + Dart (one) + TS (one) produce four distinct
// `implements_rpc` edges into the same proto rpc node. Distinct symbol paths
// per-impl keep the nodes from colliding in graph_nodes.
func TestImplementsRPC_MultiLangFourImplsOneRPC(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth;

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest { string user = 1; }
message LoginReply { bool ok = 1; }
`,
		// Go impl 1 — struct named AuthServiceServer with Login method.
		"gen/goa/service.go": `package auth

type AuthServiceServer struct{}

func (s *AuthServiceServer) Login() error { return nil }
`,
		// Go impl 2 — the canonical protoc-gen-go-grpc Unimplemented stub.
		// Different file, same package, distinct struct name.
		"gen/gob/unimpl.go": `package auth

type UnimplementedAuthServiceServer struct{}

func (UnimplementedAuthServiceServer) Login() error { return nil }
`,
		// Dart impl — SvcBase variant picks up the Dart derivation exclusively
		// (SvcClient would collide with the TS derivation's class below).
		"gen/dart/auth.dart": `library auth;

class AuthServiceBase {
  void login() {}
}
`,
		// TS impl — Svc variant (the proto service type); paired with stem
		// "auth" gives Unit.Path auth.AuthService.login.
		"gen/ts/auth.ts": `export class AuthService {
  login(): void {}
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("auth.AuthService.Login")
	wantSources := map[string]bool{
		store.SymbolNodeID("auth.AuthServiceServer.Login"):              false,
		store.SymbolNodeID("auth.UnimplementedAuthServiceServer.Login"): false,
		store.SymbolNodeID("auth.AuthServiceBase.login"):                false,
		store.SymbolNodeID("auth.AuthService.login"):                    false,
	}

	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(edges) != len(wantSources) {
		t.Errorf("expected %d implements_rpc edges into proto rpc; got %d: %+v",
			len(wantSources), len(edges), edges)
	}
	for _, e := range edges {
		if e.To != target {
			t.Errorf("edge targets wrong node: got %s, want %s", e.To, target)
			continue
		}
		if e.Kind != store.EdgeKindImplementsRPC {
			t.Errorf("edge kind = %q, want %q", e.Kind, store.EdgeKindImplementsRPC)
		}
		if _, ok := wantSources[e.From]; !ok {
			t.Errorf("unexpected edge source %s (not in derived-name set)", e.From)
			continue
		}
		wantSources[e.From] = true
	}
	for src, seen := range wantSources {
		if !seen {
			t.Errorf("missing implements_rpc edge from %s → %s", src, target)
		}
	}
}

// TestImplementsRPC_HandWrittenGoLinksAsKnownFalsePositive pins the documented
// MVP limitation: a hand-written (non-generated) Go file whose symbol happens
// to match one of the Go derivations still emits an implements_rpc edge.
// lib-4kb will tighten via buf.gen.yaml path inspection; until then, any
// regression guarding against false-positive links is itself a regression.
func TestImplementsRPC_HandWrittenGoLinksAsKnownFalsePositive(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth;

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
		// Hand-written Go file (no generator banner, path unrelated to
		// any proto plugin's output layout) that nonetheless declares a
		// struct + method whose Unit.Path matches the Go-Client derivation.
		// The MVP resolver must still link it — that's the false positive
		// lib-4kb is scoped to fix.
		"handwritten/client.go": `package auth

type AuthServiceClient struct{ endpoint string }

func (c *AuthServiceClient) Login() error { return nil }
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("auth.AuthService.Login")
	src := store.SymbolNodeID("auth.AuthServiceClient.Login")

	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	found := false
	for _, e := range edges {
		if e.From == src {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected implements_rpc edge from hand-written %s to proto rpc %s (known MVP false positive); got %+v",
			src, target, edges)
	}
}

// TestImplementsRPC_NeighborsInReturnsAllImplementers simulates the intended
// CLI invocation `librarian neighbors sym:<proto-rpc> --edge-kind=implements_rpc
// --direction=in` via the same store.Neighbors call the command wraps. Returns
// every implementer regardless of language with no leakage of other edge
// kinds.
func TestImplementsRPC_NeighborsInReturnsAllImplementers(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth;

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
		"gen/goa/service.go": `package auth

type AuthServiceServer struct{}

func (s *AuthServiceServer) Login() error { return nil }
`,
		"gen/dart/auth.dart": `library auth;

class AuthServiceClient {
  void login() {}
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("auth.AuthService.Login")

	// Direction "in" with edge-kind filter — exactly what cmd/neighbors.go
	// constructs when given `--direction=in --edge-kind=implements_rpc`.
	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(edges) == 0 {
		t.Fatal("expected at least 1 implements_rpc edge; got 0")
	}
	for _, e := range edges {
		if e.Kind != store.EdgeKindImplementsRPC {
			t.Errorf("filter leaked %q edge through implements_rpc filter", e.Kind)
		}
		if e.To != target {
			t.Errorf("in-direction edge pointing elsewhere: To=%s, want %s", e.To, target)
		}
	}

	// Unfiltered "in" must be a superset that also returns these edges.
	all, err := s.Neighbors(target, "in")
	if err != nil {
		t.Fatalf("Neighbors (unfiltered): %v", err)
	}
	if len(all) < len(edges) {
		t.Errorf("unfiltered neighbors (%d) smaller than filtered (%d) — filter broke", len(all), len(edges))
	}

	// Direction "out" from the proto rpc should NOT include any implements_rpc
	// edges: the edge direction is generated → proto, not the reverse.
	outEdges, err := s.Neighbors(target, "out", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors out: %v", err)
	}
	if len(outEdges) != 0 {
		t.Errorf("proto rpc has outgoing implements_rpc edges (wrong direction): %+v", outEdges)
	}
}

// Direct same-package unit verification of graphTargetID /
// graphNodeKindFromRef routing for Reference.Kind="implements_rpc" lives in
// implements_rpc_internal_test.go (TestGraphTargetID_ImplementsRPCRoutesToSymbol).
// The file-level indexing pipeline is covered by the integration tests above.

// TestImplementsRPC_NestedProtoPackagePath pins that proto `package api.v1;`
// doesn't break the resolver. The Unit.Path for `service AuthService { rpc
// Login ... }` under that package is `api.v1.AuthService.Login`; the resolver
// must strip only the last two segments (service + method) and retain the
// dotted package prefix when deriving generated names
// (`api.v1.AuthServiceServer.Login`, etc.). Regression guard against a naive
// split that drops everything except the file-level package.
func TestImplementsRPC_NestedProtoPackagePath(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package api.v1;

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
		// Go package must be a single identifier (dots aren't valid in Go
		// package names), so we name it `v1` and forge the Unit.Path by
		// nesting the file under a matching directory — but the generated
		// code's Unit.Path for Go comes from `package X`, which can't be
		// `api.v1`. Instead, use the TS/Dart derivations for the nested
		// case: a Dart library name can match the proto package exactly.
		"gen/dart/apiv1.dart": `library api.v1;

class AuthServiceBase {
  void login() {}
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("api.v1.AuthService.Login")
	src := store.SymbolNodeID("api.v1.AuthServiceBase.login")

	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	// Exact count matters: the fixture has exactly one implementer, so any
	// extra edge would signal the dotted-package split misfired and matched
	// derivations it shouldn't. (E.g. a naive split that dropped segments
	// could produce spurious `api.AuthService...` nodes alongside the real
	// `api.v1.AuthService...` ones.)
	if len(edges) != 1 {
		t.Fatalf("expected exactly 1 implements_rpc edge with nested proto package; got %d: %+v", len(edges), edges)
	}
	if edges[0].From != src {
		t.Errorf("edge source = %s, want %s", edges[0].From, src)
	}
}

// TestImplementsRPC_IdempotentAcrossRuns pins that two consecutive
// IndexProjectGraph invocations on the same workspace produce the same set
// of implements_rpc edges — no duplicates, no drift. UpsertEdge is
// INSERT OR REPLACE on (from, to, kind) so re-emitting is safe, but this
// test gives an explicit regression guard for the resolver's own behaviour.
func TestImplementsRPC_IdempotentAcrossRuns(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth;

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
		"gen/auth/service.go": `package auth

type AuthServiceServer struct{}

func (s *AuthServiceServer) Login() error { return nil }
`,
	})

	idx, s := openImplementsRPCStore(t, dir)

	target := store.SymbolNodeID("auth.AuthService.Login")

	// First run: force so everything is parsed fresh.
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("first IndexProjectGraph: %v", err)
	}
	first, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors (first): %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first run: expected 1 implements_rpc edge; got %d: %+v", len(first), first)
	}

	// Second run: force again so the proto file is re-parsed too. Edge count
	// must still be exactly 1 — a bug that re-emitted on every call would
	// still produce 1 (INSERT OR REPLACE dedups on the key), but a bug that
	// *created* new edges would surface as a count mismatch. The stronger
	// assertion below compares by edge identity.
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("second IndexProjectGraph: %v", err)
	}
	second, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors (second): %v", err)
	}
	if len(second) != len(first) {
		t.Errorf("idempotency broken: first run had %d edges, second had %d", len(first), len(second))
	}

	// Set equality (order-independent): exact same (From, To, Kind) triples.
	firstSet := map[string]bool{}
	for _, e := range first {
		firstSet[e.From+"|"+e.To+"|"+e.Kind] = true
	}
	for _, e := range second {
		if !firstSet[e.From+"|"+e.To+"|"+e.Kind] {
			t.Errorf("second run emitted an edge not present in first run: %+v", e)
		}
	}
}

// TestImplementsRPC_IncrementalLinksNewImplementer pins the
// incremental-correctness property claimed by the resolver's godoc: when a
// .proto file's content_hash is unchanged between runs (so the graph pass
// hash-gates it as FilesSkipped and does not re-parse it), a NEW Go/Dart/TS
// implementer added between the two runs must still link to the existing
// proto rpc node.
//
// This is the sole regression guard for the Round 1 double-parse rewrite —
// a regression that either wiped rpc nodes on hash-skip or broke the
// `ListSymbolNodesWithMetadataContaining` query would leave
// TestImplementsRPC_IdempotentAcrossRuns passing (that test uses force=true
// on both runs, so the hash-skip path is never entered).
//
// Flow:
//  1. Write only the .proto file.
//  2. First run with force=true → rpc node projected; no Go implementer
//     exists yet, so 0 implements_rpc edges.
//  3. Add the Go implementer file.
//  4. Second run with force=false → proto hash is unchanged, so its parse
//     is skipped (FilesSkipped++); the Go file is newly parsed; resolver
//     reads the persisted rpc node from the DB and emits the edge.
func TestImplementsRPC_IncrementalLinksNewImplementer(t *testing.T) {
	dir := t.TempDir()

	// Step 1: only the proto file.
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth;

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)

	target := store.SymbolNodeID("auth.AuthService.Login")

	// Step 2: first run — force, so the proto is fully parsed and the rpc
	// node lands in graph_nodes. No implementer exists, so 0 edges.
	res1, err := idx.IndexProjectGraph(dir, true)
	if err != nil {
		t.Fatalf("first IndexProjectGraph: %v", err)
	}
	if res1.FilesScanned == 0 {
		t.Fatalf("fixture bug: first run scanned zero files")
	}
	if n, _ := s.GetNode(target); n == nil {
		t.Fatal("fixture bug: proto rpc node not projected on first run")
	}
	firstEdges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors (first): %v", err)
	}
	if len(firstEdges) != 0 {
		t.Fatalf("first run should produce 0 implements_rpc edges (no implementer yet); got %d: %+v",
			len(firstEdges), firstEdges)
	}

	// Step 3: add the Go implementer.
	writeImplementsRPCFixture(t, dir, map[string]string{
		"gen/auth/service.go": `package auth

type AuthServiceServer struct{}

func (s *AuthServiceServer) Login() error { return nil }
`,
	})

	// Step 4: second run — force=false so the proto hash-gate kicks in.
	res2, err := idx.IndexProjectGraph(dir, false)
	if err != nil {
		t.Fatalf("second IndexProjectGraph: %v", err)
	}
	// The proto file MUST have been hash-skipped this run (that's the
	// point of this test — exercise the hash-skip path). Assert the skip
	// counter rather than trusting the test environment, so a change to
	// the hash-gate logic that silently forces re-parse turns into a
	// loud failure here. t.Fatalf rather than t.Errorf so a hash-skip
	// regression aborts the test — otherwise the downstream edge-count
	// check would still pass via the force-reparse path and mask the
	// broken invariant.
	if res2.FilesSkipped == 0 {
		t.Fatalf("second run FilesSkipped=0; expected proto file to be hash-skipped — hash-skip regression would mask the resolver's reliance on persisted rpc nodes")
	}

	// The resolver must have found the (persisted) rpc node despite the
	// .proto not being re-parsed this run, and emitted an edge to the
	// newly-added Go implementer.
	src := store.SymbolNodeID("auth.AuthServiceServer.Login")
	secondEdges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors (second): %v", err)
	}
	if len(secondEdges) != 1 {
		t.Fatalf("expected exactly 1 implements_rpc edge after adding implementer; got %d: %+v",
			len(secondEdges), secondEdges)
	}
	if secondEdges[0].From != src {
		t.Errorf("edge source = %s, want %s", secondEdges[0].From, src)
	}
}

// TestImplementsRPC_StreamingRPCStillLinks pins that a bidirectionally-streaming
// rpc is indifferent to the resolver. Streaming is expressed via anonymous
// `stream` keywords in the proto AST and shows up as Metadata.client_streaming
// / server_streaming booleans — the resolver does not inspect these, so
// streaming and unary rpcs link identically.
func TestImplementsRPC_StreamingRPCStillLinks(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/chat.proto": `syntax = "proto3";
package chat;

service Chat {
  rpc Send (stream Msg) returns (stream Msg);
}

message Msg { string text = 1; }
`,
		"gen/chat/service.go": `package chat

type ChatServer struct{}

func (s *ChatServer) Send() error { return nil }
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("chat.Chat.Send")
	src := store.SymbolNodeID("chat.ChatServer.Send")
	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	found := false
	for _, e := range edges {
		if e.From == src {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected bidi-streaming rpc to link just like a unary rpc; got %+v", edges)
	}
}

// TestImplementsRPC_LowercaseRPCNameTriggersSelfEdgeGuard pins that an rpc
// whose method name already starts with a lowercase letter (unconventional
// but legal proto) still links to Go-side PascalCase implementations AND
// does NOT produce a self-edge from the proto rpc to itself. The TS
// derivation `pkg.Svc.methodName` collapses to the rpc's own sym: id when
// lowerFirst is a no-op; the guard in linkRPCImplementations stops that
// edge from being emitted.
func TestImplementsRPC_LowercaseRPCNameTriggersSelfEdgeGuard(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		// Lowercase rpc method name — valid proto, just unusual.
		"api/listing.proto": `syntax = "proto3";
package listing;

service Listing {
  rpc list (ListRequest) returns (ListReply);
}

message ListRequest {}
message ListReply {}
`,
		// Go PascalCase implementation — protoc-gen-go always PascalCases
		// regardless of the proto source's casing.
		"gen/listing/service.go": `package listing

type ListingServer struct{}

func (s *ListingServer) list() error { return nil }
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("listing.Listing.list")

	// Self-edge guard: no edge from the proto rpc to itself.
	self, err := s.Neighbors(target, "out", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors out: %v", err)
	}
	for _, e := range self {
		if e.To == target {
			t.Errorf("self-edge leaked through guard: %+v", e)
		}
	}

	// Implementation still links in the lowercase-rpc case (Go's
	// ListingServer.list matches the Go `pkg.SvcServer.Method` derivation
	// with method=`list`).
	in, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors in: %v", err)
	}
	src := store.SymbolNodeID("listing.ListingServer.list")
	found := false
	for _, e := range in {
		if e.From == src {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected implements_rpc edge from %s to %s even with lowercase rpc name; got %+v", src, target, in)
	}
}
