package indexer_test

import (
	"encoding/json"
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
	// Fixture has 2 implementers (one Go, one Dart) so the direction-filter
	// wiring should return exactly that pair. An at-least-one check would
	// pass on a regression that silently drops one language's edges.
	if len(edges) != 2 {
		t.Fatalf("expected exactly 2 implements_rpc edges (Go + Dart implementers); got %d: %+v", len(edges), edges)
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

// TestImplementsRPC_BufManifestDropsHandWrittenFalsePositive pins the core
// lib-4kb tightening: when a buf.gen.yaml exists with a `go` plugin outputting
// to `gen/go`, a hand-written `auth.AuthServiceClient.Login` living under
// `internal/customauth/` no longer emits an implements_rpc edge, while the
// real generated copy under `gen/go/authpb/` still links. Documents the
// promise in the bead: "Fixture with hand-written ... that should NOT link /
// fixture with generated ... that SHOULD link".
func TestImplementsRPC_BufManifestDropsHandWrittenFalsePositive(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"buf.gen.yaml": `version: v1
plugins:
  - name: go
    out: gen/go
`,
		"api/auth.proto": `syntax = "proto3";
package auth;

option go_package = "github.com/example/authpb";

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
		// Hand-written client OUTSIDE gen/go — must be dropped by the
		// tightening check even though its name matches the Go-Client
		// derivation exactly.
		"internal/customauth/client.go": `package auth

type AuthServiceClient struct{ endpoint string }

func (c *AuthServiceClient) Login() error { return nil }
`,
		// Generated-equivalent under gen/go/authpb — must still link (the
		// manifest's go prefix is gen/go/authpb from go_package).
		"gen/go/authpb/auth.pb.go": `package auth

type AuthServiceServer struct{}

func (s *AuthServiceServer) Login() error { return nil }
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("auth.AuthService.Login")
	handWritten := store.SymbolNodeID("auth.AuthServiceClient.Login")
	generated := store.SymbolNodeID("auth.AuthServiceServer.Login")

	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	// Pin exact edge count: 1 in-prefix Go implementer links, the hand-
	// written one drops. An over-drop to 0 would also pass the loops below
	// vacuously — count assertion guards against that regression.
	if len(edges) != 1 {
		t.Fatalf("expected exactly 1 implements_rpc edge (the in-prefix generated one); got %d: %+v", len(edges), edges)
	}
	for _, e := range edges {
		if e.From == handWritten {
			t.Errorf("lib-4kb tightening regressed: hand-written %s still linked to proto rpc", handWritten)
		}
	}
	foundGen := false
	for _, e := range edges {
		if e.From == generated {
			foundGen = true
			break
		}
	}
	if !foundGen {
		t.Errorf("expected generated %s to link to proto rpc %s under manifest prefix gen/go/authpb; got %+v", generated, target, edges)
	}
}

// TestImplementsRPC_BufManifestSourceRelativePath pins the paths=source_relative
// convention: with that opt set on the Go plugin, generated Go code lands
// under `<out>/<proto-source-dir>` rather than `<out>/<last-segment-of-go_package>`.
// The fixture puts generated code under gen/go/api and expects the manifest
// to match it.
func TestImplementsRPC_BufManifestSourceRelativePath(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"buf.gen.yaml": `version: v1
plugins:
  - name: go
    out: gen/go
    opt:
      - paths=source_relative
`,
		"api/auth.proto": `syntax = "proto3";
package auth;

option go_package = "github.com/example/authpb";

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
		// Under paths=source_relative, protoc-gen-go writes to
		// gen/go/<proto-source-dir> — so gen/go/api is the tight prefix
		// and go_package is ignored for path matching.
		"gen/go/api/auth.pb.go": `package auth

type AuthServiceServer struct{}

func (s *AuthServiceServer) Login() error { return nil }
`,
		// Under the default (non-source-relative) convention this would
		// match gen/go/authpb; under source_relative it's OUTSIDE the
		// tight prefix and must be dropped.
		"gen/go/authpb/auth.pb.go": `package auth

type AuthServiceClient struct{}

func (c *AuthServiceClient) Login() error { return nil }
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("auth.AuthService.Login")
	inTree := store.SymbolNodeID("auth.AuthServiceServer.Login")
	outTree := store.SymbolNodeID("auth.AuthServiceClient.Login")

	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	foundIn := false
	for _, e := range edges {
		if e.From == inTree {
			foundIn = true
		}
		if e.From == outTree {
			t.Errorf("paths=source_relative tightening regressed: %s under gen/go/authpb still linked despite manifest prefix gen/go/api", outTree)
		}
	}
	if !foundIn {
		t.Errorf("expected %s under gen/go/api to link; got %+v", inTree, edges)
	}
}

// TestImplementsRPC_BufManifestMultiLanguage pins per-language tightening:
// Go + Dart + TS plugins each output to different directories, and each
// language's implementer is linked iff it lives under its own manifest
// prefix. Fixture uses one implementer per language placed in-prefix so
// the test checks positive linking across all three derivations at once —
// any language whose prefix computation regressed would silently drop its
// edge.
func TestImplementsRPC_BufManifestMultiLanguage(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"buf.gen.yaml": `version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: gen/go
  - local: protoc-gen-dart
    out: gen/dart
  - remote: buf.build/bufbuild/es
    out: gen/ts
`,
		"api/auth.proto": `syntax = "proto3";
package auth;

option go_package = "github.com/example/authpb";

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
		// Go under gen/go/authpb — in-prefix (default convention).
		"gen/go/authpb/auth.pb.go": `package auth

type AuthServiceServer struct{}

func (s *AuthServiceServer) Login() error { return nil }
`,
		// Dart under gen/dart/api — in-prefix (source-relative by default).
		"gen/dart/api/auth.pb.dart": `library auth;

class AuthServiceBase {
  void login() {}
}
`,
		// TS under gen/ts/api — in-prefix. File stem ("auth") becomes the
		// synthetic JS/TS "package" in Unit.Path. Real protoc-gen-es output
		// carries a _pb suffix; that's a pre-existing lib-6wz naming-match
		// limitation (name-match only, no _pb stripping) and is out of
		// scope for lib-4kb which only tightens path matching.
		"gen/ts/api/auth.ts": `export class AuthService {
  login(): void {}
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("auth.AuthService.Login")
	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}

	got := map[string]string{} // src → source_path of node
	for _, e := range edges {
		node, err := s.GetNode(e.From)
		if err != nil || node == nil {
			continue
		}
		got[e.From] = node.SourcePath
	}

	// One in-prefix implementer per language; assert each linked AND that
	// the edge set is exactly three (any fourth edge would signal a spurious
	// match leaking through the tightening check — per-case loop alone
	// would miss that regression).
	cases := []struct {
		src            string
		wantSourcePath string
	}{
		{store.SymbolNodeID("auth.AuthServiceServer.Login"), "gen/go/authpb/auth.pb.go"},
		{store.SymbolNodeID("auth.AuthServiceBase.login"), "gen/dart/api/auth.pb.dart"},
		{store.SymbolNodeID("auth.AuthService.login"), "gen/ts/api/auth.ts"},
	}
	if len(got) != len(cases) {
		t.Errorf("expected exactly %d implements_rpc edges (one per language); got %d: %+v", len(cases), len(got), got)
	}
	for _, c := range cases {
		sp, ok := got[c.src]
		if !ok {
			t.Errorf("missing implements_rpc edge from %s; got %+v", c.src, got)
			continue
		}
		// Full-path compare — the fixture pins exact file locations so we
		// don't need hasPathPrefix's prefix semantics here (which would
		// force an internal helper export just for one test assertion).
		if sp != c.wantSourcePath {
			t.Errorf("edge source %s has source_path %q, want %q", c.src, sp, c.wantSourcePath)
		}
	}
}

// TestImplementsRPC_BufManifestDropsOutOfPrefixDart pins Dart-side tightening
// in isolation: a hand-written Dart class in a different library (so it
// doesn't collide with a generated equivalent on the sym: id) whose name
// matches a Dart derivation must NOT link when a buf manifest restricts the
// Dart prefix to gen/dart/api. Counterpart to the Go-side false-positive
// drop — proves the tightening check is wired per-language.
func TestImplementsRPC_BufManifestDropsOutOfPrefixDart(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"buf.gen.yaml": `version: v1
plugins:
  - name: dart
    out: gen/dart
`,
		"api/auth.proto": `syntax = "proto3";
package auth;

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
		// Hand-written Dart file in a completely unrelated library — the
		// library name picks a distinct proto-package-like prefix
		// ("legacyauth") so the sym: id doesn't collide with any in-prefix
		// equivalent. Its Unit.Path becomes legacyauth.AuthServiceBase.login,
		// which doesn't match any auth.* derivation — we use auth.proto's
		// rpc and a Dart file whose library deliberately matches "auth" so
		// that name MATCHES but path fails.
		// Different tack: place a file under legacy/dart that uses
		// library auth — the sym: id auth.AuthServiceBase.login IS what
		// the derivation probes for. Without a competing in-prefix Dart
		// implementer, the resolver must drop this outright via the
		// path check (tightening).
		"legacy/dart/auth.dart": `library auth;

class AuthServiceBase {
  void login() {}
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("auth.AuthService.Login")
	outOfPrefix := store.SymbolNodeID("auth.AuthServiceBase.login")
	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	// Exact count: 0 edges. The fixture has only the out-of-prefix Dart
	// file — over-drop to 0 AND under-drop that keeps the false-positive
	// would both fail the per-edge assertion below, but only the over-drop
	// path would pass a bare len(edges)!=0 check vacuously in the reverse
	// direction. Assert both simultaneously.
	if len(edges) != 0 {
		t.Errorf("expected 0 implements_rpc edges (only out-of-prefix Dart file exists); got %d: %+v", len(edges), edges)
	}
	for _, e := range edges {
		if e.From == outOfPrefix {
			t.Errorf("Dart tightening regressed: out-of-prefix %s (source %q) still linked", outOfPrefix, "legacy/dart/auth.dart")
		}
	}
}

// TestImplementsRPC_BufManifestMissingFallsBack pins the no-buf.gen.yaml
// fallback: without the config file the resolver reverts to lib-6wz's
// name-only matching, preserving the known false positive — linking the
// hand-written AuthServiceClient as before. Regression guard against a
// future "require buf.gen.yaml" change that would silently drop edges in
// projects that don't use buf.
func TestImplementsRPC_BufManifestMissingFallsBack(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		// No buf.gen.yaml — resolver must fall back to name-only.
		"api/auth.proto": `syntax = "proto3";
package auth;

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
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
		t.Errorf("without buf.gen.yaml the lib-6wz fallback should still emit the hand-written edge; got %+v", edges)
	}

	// And no buf_manifest graph_node should have been written.
	manifestNodes, err := s.ListNodesByKindWithMetadataContaining(store.NodeKindBufManifest, "")
	if err != nil {
		t.Fatalf("ListNodesByKindWithMetadataContaining: %v", err)
	}
	if len(manifestNodes) != 0 {
		t.Errorf("expected zero buf_manifest nodes without buf.gen.yaml; got %d: %+v", len(manifestNodes), manifestNodes)
	}
}

// TestImplementsRPC_BufManifestNodePersisted pins that each proto file gets
// a queryable buf_manifest graph_node via GetNode. MCP tools need to read
// these downstream to show per-proto codegen layouts without re-parsing
// buf.gen.yaml.
func TestImplementsRPC_BufManifestNodePersisted(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"buf.gen.yaml": `version: v1
plugins:
  - name: go
    out: gen/go
  - name: dart
    out: gen/dart
`,
		"api/auth.proto": `syntax = "proto3";
package auth;

option go_package = "github.com/example/authpb";

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	node, err := s.GetNode(store.BufManifestNodeID("api/auth.proto"))
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node == nil {
		t.Fatal("buf_manifest node not persisted for api/auth.proto")
	}
	if node.Kind != store.NodeKindBufManifest {
		t.Errorf("node.Kind = %q, want %q", node.Kind, store.NodeKindBufManifest)
	}
	if node.SourcePath != "api/auth.proto" {
		t.Errorf("node.SourcePath = %q, want %q", node.SourcePath, "api/auth.proto")
	}

	// Metadata must round-trip through the JSON schema the resolver expects.
	// LangPrefixes is now map[string][]string (multi-prefix policy, lib-4g2.1).
	var got struct {
		ProtoPath    string              `json:"proto_path"`
		ProtoPackage string              `json:"proto_package"`
		LangPrefixes map[string][]string `json:"lang_prefixes"`
	}
	if err := json.Unmarshal([]byte(node.Metadata), &got); err != nil {
		t.Fatalf("unmarshal metadata %q: %v", node.Metadata, err)
	}
	if got.ProtoPath != "api/auth.proto" {
		t.Errorf("proto_path = %q, want %q", got.ProtoPath, "api/auth.proto")
	}
	if got.ProtoPackage != "auth" {
		t.Errorf("proto_package = %q, want %q", got.ProtoPackage, "auth")
	}
	goPfx := got.LangPrefixes["go"]
	if len(goPfx) != 1 || goPfx[0] != "gen/go/authpb" {
		t.Errorf("lang_prefixes[go] = %v, want [gen/go/authpb]", goPfx)
	}
	dartPfx := got.LangPrefixes["dart"]
	if len(dartPfx) != 1 || dartPfx[0] != "gen/dart/api" {
		t.Errorf("lang_prefixes[dart] = %v, want [gen/dart/api]", dartPfx)
	}
}

// TestImplementsRPC_BufManifestNodePersistedWithoutFileOptions pins the
// loadProtoFilesFromRPCNodes branch: a proto file with rpc(s) but no
// file-level `option *_package` still gets a buf_manifest graph_node so
// Dart / TS candidates (whose prefixes don't depend on `*_package`
// values) can still benefit from path tightening. Without this branch,
// projects that intentionally omit `option go_package` would silently
// lose Dart/TS tightening too.
//
// Expectation: node present, ProtoPath + ProtoPackage filled from the
// rpc symbol's sym: id, LangPrefixes contains only Dart/TS (Go absent
// because no go_package means no Go prefix can be computed).
func TestImplementsRPC_BufManifestNodePersistedWithoutFileOptions(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"buf.gen.yaml": `version: v1
plugins:
  - name: go
    out: gen/go
  - name: dart
    out: gen/dart
`,
		// No `option *_package` here — the proto rpc still lands in graph_nodes
		// (because the rpc symbol projection is unrelated to file options),
		// but loadProtoOptionFiles can't find this file. loadProtoFilesFromRPCNodes
		// must pick it up via the rpc node's source_path.
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
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	node, err := s.GetNode(store.BufManifestNodeID("api/auth.proto"))
	if err != nil {
		t.Fatalf("GetNode: %v", err)
	}
	if node == nil {
		t.Fatal("buf_manifest node not persisted for proto with rpcs but no file-level options — loadProtoFilesFromRPCNodes branch regressed")
	}
	if node.Kind != store.NodeKindBufManifest {
		t.Errorf("node.Kind = %q, want %q", node.Kind, store.NodeKindBufManifest)
	}
	if node.SourcePath != "api/auth.proto" {
		t.Errorf("node.SourcePath = %q, want %q", node.SourcePath, "api/auth.proto")
	}

	// LangPrefixes is now map[string][]string (multi-prefix policy, lib-4g2.1).
	var got struct {
		ProtoPath    string              `json:"proto_path"`
		ProtoPackage string              `json:"proto_package"`
		LangPrefixes map[string][]string `json:"lang_prefixes"`
	}
	if err := json.Unmarshal([]byte(node.Metadata), &got); err != nil {
		t.Fatalf("unmarshal metadata %q: %v", node.Metadata, err)
	}
	if got.ProtoPath != "api/auth.proto" {
		t.Errorf("proto_path = %q, want %q", got.ProtoPath, "api/auth.proto")
	}
	// Package recovered from the rpc symbol sym:auth.AuthService.Login →
	// first len-2 segments joined → "auth".
	if got.ProtoPackage != "auth" {
		t.Errorf("proto_package = %q, want %q (recovered from rpc symbol path)", got.ProtoPackage, "auth")
	}
	// Go prefix absent — no go_package, no source_relative. Dart / TS
	// prefixes present because their convention is source-dir-based.
	if pfx, ok := got.LangPrefixes["go"]; ok {
		t.Errorf("lang_prefixes[go] = %v present without go_package; want absent", pfx)
	}
	dartPfx := got.LangPrefixes["dart"]
	if len(dartPfx) != 1 || dartPfx[0] != "gen/dart/api" {
		t.Errorf("lang_prefixes[dart] = %v, want [gen/dart/api]", dartPfx)
	}
	// Non-empty LangPrefixes check — a regression that emits an empty map
	// would still round-trip the struct, so assert length explicitly.
	if len(got.LangPrefixes) == 0 {
		t.Errorf("lang_prefixes empty; expected Dart prefix to be present even without file-level options")
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

// TestImplementsRPC_ConnectGoHandlerLinks pins the protoc-gen-connect-go
// server-side interface naming: `FooServiceHandler` (not `FooServiceServer`).
// Without the Handler candidate added by lib-4g2.1, Connect-Go server impls
// never linked to their proto rpc — this test is the first-class guard.
func TestImplementsRPC_ConnectGoHandlerLinks(t *testing.T) {
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
		// protoc-gen-connect-go emits the server-side handler interface as
		// FooServiceHandler; server implementors embed this, not FooServiceServer.
		// Package name must match the proto package so Unit.Path lines up with
		// what the resolver probes (auth.AuthServiceHandler.Login).
		"gen/go/authpb/authpbconnect/auth.connect.go": `package auth

type AuthServiceHandler struct{}

func (s *AuthServiceHandler) Login() error { return nil }
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("auth.AuthService.Login")
	src := store.SymbolNodeID("auth.AuthServiceHandler.Login")

	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected exactly 1 implements_rpc edge (Handler suffix); got %d: %+v", len(edges), edges)
	}
	if edges[0].From != src {
		t.Errorf("edge source = %s, want %s", edges[0].From, src)
	}
}

// TestImplementsRPC_ConnectGoMultiPluginManifest pins the multi-prefix manifest
// policy for the canonical Connect-Go buf.gen.yaml shape: protoc-gen-go writes
// message types to gen/go and protoc-gen-connect-go writes service interfaces
// to gen/connect. Both plugins classify as language "go" but output to
// distinct directories. With the old first-wins policy, symbols under the
// second directory would be silently dropped as false positives. lib-4g2.1
// fixes this by collecting both prefixes; the resolver accepts a candidate
// if it falls under ANY prefix in the slice.
//
// The fixture also places a hand-written Go file in internal/customauth/ to
// confirm that the multi-prefix tightening still drops out-of-tree symbols.
func TestImplementsRPC_ConnectGoMultiPluginManifest(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"buf.gen.yaml": `version: v2
plugins:
  - local: protoc-gen-go
    out: gen/go
  - local: protoc-gen-connect-go
    out: gen/connect
`,
		"api/auth.proto": `syntax = "proto3";
package auth;

option go_package = "github.com/example/authpb";

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
		// grpc-go style: under gen/go/authpb (first plugin's out-dir).
		"gen/go/authpb/auth.pb.go": `package auth

type AuthServiceServer struct{}

func (s *AuthServiceServer) Login() error { return nil }
`,
		// connect-go style: under gen/connect/authpb (second plugin's out-dir).
		"gen/connect/authpb/auth.connect.go": `package auth

type AuthServiceHandler struct{}

func (s *AuthServiceHandler) Login() error { return nil }
`,
		// Hand-written unimplemented stub that is a valid derivation candidate
		// (matches pkg.UnimplementedSvcServer.Method) but lives OUTSIDE both
		// codegen output trees. Must be dropped by multi-prefix tightening.
		// Using UnimplementedAuthServiceServer (not AuthServiceClient) so the
		// negative assertion clearly exercises tightening on a name that the
		// resolver would otherwise link.
		"internal/customauth/unimpl.go": `package auth

type UnimplementedAuthServiceServer struct{}

func (UnimplementedAuthServiceServer) Login() error { return nil }
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	target := store.SymbolNodeID("auth.AuthService.Login")
	serverSrc := store.SymbolNodeID("auth.AuthServiceServer.Login")
	handlerSrc := store.SymbolNodeID("auth.AuthServiceHandler.Login")
	// The hand-written UnimplementedAuthServiceServer matches the
	// pkg.UnimplementedSvcServer.Method derivation — a real candidate that
	// the resolver would link under name-only matching. With multi-prefix
	// tightening it must be dropped because it lives outside both codegen trees.
	outOfTree := store.SymbolNodeID("auth.UnimplementedAuthServiceServer.Login")

	edges, err := s.Neighbors(target, "in", store.EdgeKindImplementsRPC)
	if err != nil {
		t.Fatalf("Neighbors: %v", err)
	}
	// Exactly 2 edges: one from gen/go/authpb and one from gen/connect/authpb.
	if len(edges) != 2 {
		t.Fatalf("expected exactly 2 implements_rpc edges (Server + Handler); got %d: %+v", len(edges), edges)
	}
	sources := map[string]bool{}
	for _, e := range edges {
		sources[e.From] = true
		if e.From == outOfTree {
			t.Errorf("multi-prefix tightening regressed: out-of-tree %s (internal/customauth/unimpl.go) still linked", outOfTree)
		}
	}
	if !sources[serverSrc] {
		t.Errorf("missing implements_rpc edge from %s (gen/go/authpb prefix)", serverSrc)
	}
	if !sources[handlerSrc] {
		t.Errorf("missing implements_rpc edge from %s (gen/connect/authpb prefix)", handlerSrc)
	}

	// Also verify that the buf_manifest node's Go lang_prefixes slice
	// contains both out-dirs in declaration order.
	manifestNode, err := s.GetNode(store.BufManifestNodeID("api/auth.proto"))
	if err != nil || manifestNode == nil {
		t.Fatalf("buf_manifest node missing: %v", err)
	}
	var meta struct {
		LangPrefixes map[string][]string `json:"lang_prefixes"`
	}
	if err := json.Unmarshal([]byte(manifestNode.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal manifest metadata: %v", err)
	}
	goPfx := meta.LangPrefixes["go"]
	if len(goPfx) != 2 {
		t.Fatalf("manifest lang_prefixes[go] = %v, want 2 entries", goPfx)
	}
	if goPfx[0] != "gen/go/authpb" {
		t.Errorf("manifest goPfx[0] = %q, want gen/go/authpb", goPfx[0])
	}
	if goPfx[1] != "gen/connect/authpb" {
		t.Errorf("manifest goPfx[1] = %q, want gen/connect/authpb", goPfx[1])
	}
}

// TestImplementsRPC_ConnectLocalPluginClassified pins that a buf.gen.yaml
// declaring `local: protoc-gen-connect-go` (as opposed to a remote plugin
// at buf.build/connectrpc/go) correctly classifies as language "go" in the
// persisted buf_manifest node. Without the connect-go arm added by lib-4g2.1
// in languageFromPluginIdentity, local connect-go plugins produce no Go
// prefix in the manifest, causing every Go candidate for that proto to fall
// through to name-only matching and possibly accept false positives.
func TestImplementsRPC_ConnectLocalPluginClassified(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"buf.gen.yaml": `version: v2
plugins:
  - local: protoc-gen-connect-go
    out: gen/connect
`,
		"api/auth.proto": `syntax = "proto3";
package auth;

option go_package = "github.com/example/authpb";

service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}

message LoginRequest {}
message LoginReply {}
`,
		"gen/connect/authpb/auth.connect.go": `package auth

type AuthServiceHandler struct{}

func (s *AuthServiceHandler) Login() error { return nil }
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// Manifest must classify the local plugin as "go" and persist the prefix.
	manifestNode, err := s.GetNode(store.BufManifestNodeID("api/auth.proto"))
	if err != nil || manifestNode == nil {
		t.Fatalf("buf_manifest node missing: %v", err)
	}
	var meta struct {
		LangPrefixes map[string][]string `json:"lang_prefixes"`
	}
	if err := json.Unmarshal([]byte(manifestNode.Metadata), &meta); err != nil {
		t.Fatalf("unmarshal manifest metadata: %v", err)
	}
	goPfx := meta.LangPrefixes["go"]
	if len(goPfx) != 1 || goPfx[0] != "gen/connect/authpb" {
		t.Errorf("manifest lang_prefixes[go] = %v, want [gen/connect/authpb] (local protoc-gen-connect-go should classify as go)", goPfx)
	}

	// The Handler symbol under gen/connect/authpb must link to the proto rpc.
	target := store.SymbolNodeID("auth.AuthService.Login")
	src := store.SymbolNodeID("auth.AuthServiceHandler.Login")
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
		t.Errorf("expected implements_rpc edge from %s to %s (local connect-go classified as go); got %+v", src, target, edges)
	}
}
