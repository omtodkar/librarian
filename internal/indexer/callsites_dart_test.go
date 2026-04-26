package indexer_test

import (
	"testing"

	"librarian/internal/store"
)

// dartProtoFixture is a reusable proto fixture with two services and multiple
// rpcs used across Dart call-site tests.
const dartProtoFixtureAuth = `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
  rpc Logout (LogoutRequest) returns (LogoutReply);
  rpc Whoami (WhoamiRequest) returns (WhoamiReply);
}
message LoginRequest {}
message LoginReply {}
message LogoutRequest {}
message LogoutReply {}
message WhoamiRequest {}
message WhoamiReply {}
`

// dartPbgrpcFixtureAuth is the grpc-dart generated stub for auth.v1.
// The `library auth.v1;` directive is critical: it sets the package name so
// the symbol paths (auth.v1.AuthServiceClient.login) match the proto package
// prefix the implements_rpc resolver probes for.
// protoc-gen-dart emits lowerCamelCase method names on AuthServiceClient.
const dartPbgrpcFixtureAuth = `library auth.v1;

class AuthServiceClient {
  AuthServiceClient(dynamic channel);
  void login(dynamic request) {}
  void logout(dynamic request) {}
  void whoami(dynamic request) {}
}

class AuthServiceBase {
  void login(dynamic request) {}
  void logout(dynamic request) {}
  void whoami(dynamic request) {}
}
`

// dartChatPbgrpcFixture is the grpc-dart generated stub for a streaming rpc.
// The `library chat.v1;` directive aligns symbol paths with the proto package.
const dartChatPbgrpcFixture = `library chat.v1;

class ChatServiceClient {
  ChatServiceClient(dynamic channel);
  void chat(dynamic request) {}
}

class ChatServiceBase {
  void chat(dynamic request) {}
}
`

// TestCallRPC_DartDirectConstructorAndCall verifies the baseline pattern:
// a top-level function constructs a grpc-dart client and calls one method.
// Asserts exactly one call_rpc edge from the function symbol to the proto rpc.
func TestCallRPC_DartDirectConstructorAndCall(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto":          dartProtoFixtureAuth,
		"gen/dart/auth.pbgrpc.dart": dartPbgrpcFixtureAuth,
		"lib/auth_handler.dart": `
Future<void> handleLogin(ClientChannel channel) async {
  final client = AuthServiceClient(channel);
  await client.login(LoginRequest());
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	rpcID := store.SymbolNodeID("auth.v1.AuthService.Login")
	edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected exactly 1 call_rpc edge into auth.v1.AuthService.Login; got %d: %+v", len(edges), edges)
	}
	e := edges[0]
	wantCaller := store.SymbolNodeID("auth_handler.handleLogin")
	if e.From != wantCaller {
		t.Errorf("edge.From = %q, want %q", e.From, wantCaller)
	}
	if e.To != rpcID {
		t.Errorf("edge.To = %q, want %q", e.To, rpcID)
	}
	if e.Kind != store.EdgeKindCallRPC {
		t.Errorf("edge.Kind = %q, want %q", e.Kind, store.EdgeKindCallRPC)
	}
}

// TestCallRPC_DartClassFieldPattern verifies the Flutter-style service class
// pattern: _authClient field constructed in the class constructor, called from
// a named method. Edge must come from the METHOD symbol, not the field or class.
func TestCallRPC_DartClassFieldPattern(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto":          dartProtoFixtureAuth,
		"gen/dart/auth.pbgrpc.dart": dartPbgrpcFixtureAuth,
		"lib/auth_service.dart": `
class AuthService {
  late AuthServiceClient _authClient;

  void init(ClientChannel channel) {
    _authClient = AuthServiceClient(channel);
  }

  Future<void> performLogin() async {
    await _authClient.login(LoginRequest());
  }
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	rpcID := store.SymbolNodeID("auth.v1.AuthService.Login")
	edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 call_rpc edge into auth.v1.AuthService.Login; got %d: %+v", len(edges), edges)
	}
	wantCaller := store.SymbolNodeID("auth_service.AuthService.performLogin")
	if edges[0].From != wantCaller {
		t.Errorf("edge.From = %q, want %q", edges[0].From, wantCaller)
	}
}

// TestCallRPC_DartGetterPattern verifies the getter pattern: a class exposes a
// grpc-dart client via an expression-body getter, and a method of the same class
// calls it. The edge must come from the METHOD that calls the getter, not the getter.
func TestCallRPC_DartGetterPattern(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto":          dartProtoFixtureAuth,
		"gen/dart/auth.pbgrpc.dart": dartPbgrpcFixtureAuth,
		"lib/auth_widget.dart": `
class AuthWidget {
  final ClientChannel _channel;

  AuthWidget(this._channel);

  AuthServiceClient get _authClient => AuthServiceClient(_channel);

  Future<void> handleLogin() async {
    await _authClient.login(LoginRequest());
  }
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	rpcID := store.SymbolNodeID("auth.v1.AuthService.Login")
	edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 call_rpc edge into auth.v1.AuthService.Login; got %d: %+v", len(edges), edges)
	}
	wantCaller := store.SymbolNodeID("auth_widget.AuthWidget.handleLogin")
	if edges[0].From != wantCaller {
		t.Errorf("edge.From = %q, want %q", edges[0].From, wantCaller)
	}
}

// TestCallRPC_DartStreamingRPC verifies that streaming RPCs emit call_rpc
// edges regardless of streaming cardinality.
func TestCallRPC_DartStreamingRPC(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/chat.proto": `syntax = "proto3";
package chat.v1;
service ChatService {
  rpc Chat (ChatRequest) returns (stream ChatResponse);
}
message ChatRequest {}
message ChatResponse {}
`,
		"gen/dart/chat.pbgrpc.dart": dartChatPbgrpcFixture,
		"lib/chat_page.dart": `
class ChatPage {
  final ClientChannel _channel;
  ChatPage(this._channel);

  ResponseStream<ChatResponse> startChat() {
    final client = ChatServiceClient(_channel);
    return client.chat(ChatRequest());
  }
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	rpcID := store.SymbolNodeID("chat.v1.ChatService.Chat")
	edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 call_rpc edge (streaming); got %d: %+v", len(edges), edges)
	}
	wantCaller := store.SymbolNodeID("chat_page.ChatPage.startChat")
	if edges[0].From != wantCaller {
		t.Errorf("edge.From = %q, want %q", edges[0].From, wantCaller)
	}
}

// TestCallRPC_DartMultiRPCSameBinding verifies that calling multiple RPC methods
// on a single client binding emits one call_rpc edge per method call.
func TestCallRPC_DartMultiRPCSameBinding(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto":          dartProtoFixtureAuth,
		"gen/dart/auth.pbgrpc.dart": dartPbgrpcFixtureAuth,
		"lib/auth_bloc.dart": `
Future<void> syncUser(ClientChannel channel) async {
  final client = AuthServiceClient(channel);
  await client.login(LoginRequest());
  await client.whoami(WhoamiRequest());
  await client.logout(LogoutRequest());
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	callerID := store.SymbolNodeID("auth_bloc.syncUser")
	wantRPCs := []string{
		"auth.v1.AuthService.Login",
		"auth.v1.AuthService.Whoami",
		"auth.v1.AuthService.Logout",
	}
	for _, rpcPath := range wantRPCs {
		rpcID := store.SymbolNodeID(rpcPath)
		edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
		if err != nil {
			t.Fatalf("Neighbors(call_rpc, %s): %v", rpcPath, err)
		}
		found := false
		for _, e := range edges {
			if e.From == callerID {
				found = true
			}
		}
		if !found {
			t.Errorf("expected call_rpc edge from %s to %s; edges: %+v", callerID, rpcID, edges)
		}
	}

	// Exactly one caller per rpc (no duplicates).
	for _, rpcPath := range wantRPCs {
		rpcID := store.SymbolNodeID(rpcPath)
		edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
		if err != nil {
			t.Fatalf("Neighbors(call_rpc, %s): %v", rpcPath, err)
		}
		if len(edges) != 1 {
			t.Errorf("%s: expected 1 call_rpc edge; got %d: %+v", rpcPath, len(edges), edges)
		}
	}
}

// TestCallRPC_DartOutOfScope_GetIt documents the v1 limitation: clients
// acquired via GetIt dependency injection do NOT produce call_rpc edges.
// The binding tracker requires a direct constructor call in the same file.
func TestCallRPC_DartOutOfScope_GetIt(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto":          dartProtoFixtureAuth,
		"gen/dart/auth.pbgrpc.dart": dartPbgrpcFixtureAuth,
		// GetIt.I<AuthServiceClient>() is NOT a direct constructor call;
		// the binding tracker correctly ignores it.
		"lib/di_widget.dart": `
import 'package:get_it/get_it.dart';

class DiWidget {
  void doWork() {
    final client = GetIt.I<AuthServiceClient>();
    client.login(LoginRequest());
  }
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// No call_rpc edge should exist — DI-acquired client is not linked.
	rpcID := store.SymbolNodeID("auth.v1.AuthService.Login")
	edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc): %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("DI GetIt pattern: expected 0 call_rpc edges; got %d: %+v", len(edges), edges)
	}
}

// TestCallRPC_DartNonClientCall verifies that a method call on a non-gRPC type
// that shares the same method name does NOT produce a call_rpc edge.
// The binding tracker uses the implements_rpc oracle, so only types registered
// as grpc-dart client classes are recognized.
func TestCallRPC_DartNonClientCall(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto":          dartProtoFixtureAuth,
		"gen/dart/auth.pbgrpc.dart": dartPbgrpcFixtureAuth,
		// AuthRepository is a hand-written domain class, NOT a grpc-dart
		// generated client. It has a `login` method but its constructor does
		// not appear in the implements_rpc registry, so no binding is created.
		"lib/auth_repository.dart": `
class AuthRepository {
  AuthRepository(this._db);
  final Database _db;
  Future<void> login(LoginRequest req) async {}
}

class AuthPage {
  Future<void> doLogin(Database db) async {
    final repo = AuthRepository(db);
    await repo.login(LoginRequest());
  }
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// No call_rpc edge — AuthRepository is not in the implements_rpc registry.
	rpcID := store.SymbolNodeID("auth.v1.AuthService.Login")
	edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc): %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("non-client call: expected 0 call_rpc edges; got %d: %+v", len(edges), edges)
	}
}

// TestCallRPC_DartAnonymousEnclosure documents the skip rule: a call inside
// a raw anonymous closure with no named ancestor does NOT produce an edge.
// This mirrors the TS detector's enclosure-skip policy.
func TestCallRPC_DartAnonymousEnclosure(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto":          dartProtoFixtureAuth,
		"gen/dart/auth.pbgrpc.dart": dartPbgrpcFixtureAuth,
		// The client call is inside an IIFE-style anonymous function at module
		// level — no named function ancestor exists, so the edge is skipped.
		"lib/module_scope.dart": `
import 'dart:async';

final _channel = ClientChannel('localhost');
final _client = AuthServiceClient(_channel);

// Top-level anonymous call: no enclosing named function → edge skipped.
final _ = () { _client.login(LoginRequest()); }();
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	rpcID := store.SymbolNodeID("auth.v1.AuthService.Login")
	edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc): %v", err)
	}
	if len(edges) != 0 {
		t.Errorf("anonymous enclosure: expected 0 call_rpc edges; got %d: %+v", len(edges), edges)
	}
}
