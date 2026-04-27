package indexer_test

import (
	"testing"

	"librarian/internal/store"
)

// authConnectTS is a reusable connect-es generated stub fixture with two methods.
const authConnectTS = `import { MethodKind } from "@bufbuild/protobuf";
import { LoginRequest, LoginReply, LogoutRequest, LogoutReply, WhoamiRequest, WhoamiReply } from "./auth_pb.js";

export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: {
    login: {
      name: "Login",
      kind: MethodKind.Unary,
      I: LoginRequest,
      O: LoginReply,
    },
    logout: {
      name: "Logout",
      kind: MethodKind.Unary,
      I: LogoutRequest,
      O: LogoutReply,
    },
    whoami: {
      name: "Whoami",
      kind: MethodKind.Unary,
      I: WhoamiRequest,
      O: WhoamiReply,
    },
  },
} as const;
`

// TestCallRPC_NextjsDirectClient verifies the baseline: one Next.js page component
// calls createPromiseClient and invokes one method. Asserts exactly one call_rpc
// edge from the page's default-export function to the proto rpc symbol.
func TestCallRPC_NextjsDirectClient(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest { string user = 1; }
message LoginReply { bool ok = 1; }
`,
		"gen/ts/auth_connect.ts": authConnectTS,
		"app/page.tsx": `import { createPromiseClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { AuthService } from "../../gen/ts/auth_connect";

const transport = createConnectTransport({ baseUrl: "https://example.com" });

export default function Page() {
  const client = createPromiseClient(AuthService, transport);
  return client.login({ user: "alice" });
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// call_rpc edges target the proto rpc node (PascalCase), not the
	// lowerCamelCase connect-es stub. buildCallRPCEdges follows the stub's
	// implements_rpc edge to find the proto rpc before emitting.
	rpcID := store.SymbolNodeID("auth.v1.AuthService.Login")
	edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 call_rpc edge into auth.v1.AuthService.Login; got %d: %+v", len(edges), edges)
	}
	e := edges[0]
	wantCaller := store.SymbolNodeID("page.Page")
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

// TestCallRPC_NextjsMultipleMethods verifies that a single component calling three
// methods on the same client emits three distinct call_rpc edges.
func TestCallRPC_NextjsMultipleMethods(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
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
`,
		"gen/ts/auth_connect.ts": authConnectTS,
		"app/dashboard.tsx": `import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "../../gen/ts/auth_connect";

const transport = {};

export function Dashboard() {
  const client = createPromiseClient(AuthService, transport);
  client.login({});
  client.logout({});
  client.whoami({});
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	callerID := store.SymbolNodeID("dashboard.Dashboard")
	// call_rpc targets the proto rpc (PascalCase), not the stub (lowerCamelCase).
	wantRPCs := []string{
		"auth.v1.AuthService.Login",
		"auth.v1.AuthService.Logout",
		"auth.v1.AuthService.Whoami",
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

	// Verify no extra edges from unexpected callers.
	for _, rpcPath := range wantRPCs {
		rpcID := store.SymbolNodeID(rpcPath)
		edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
		if err != nil {
			t.Fatalf("Neighbors(call_rpc): %v", err)
		}
		if len(edges) != 1 {
			t.Errorf("rpc %s: expected 1 call_rpc edge; got %d: %+v", rpcPath, len(edges), edges)
		}
	}
}

// TestCallRPC_Destructuring verifies that destructured client bindings
// (const { login, logout } = createPromiseClient(...)) produce edges for
// each destructured method called in the same scope.
func TestCallRPC_Destructuring(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
  rpc Logout (LogoutRequest) returns (LogoutReply);
}
message LoginRequest {}
message LoginReply {}
message LogoutRequest {}
message LogoutReply {}
`,
		"gen/ts/auth_connect.ts": `export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: {
    login: { name: "Login" },
    logout: { name: "Logout" },
  },
} as const;
`,
		"src/auth_handler.ts": `import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "../gen/ts/auth_connect";

const transport = {};

export function handleAuth() {
  const { login, logout } = createPromiseClient(AuthService, transport);
  login({ user: "alice" });
  logout({});
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	callerID := store.SymbolNodeID("auth_handler.handleAuth")
	// call_rpc targets proto rpc (PascalCase).
	for _, rpcPath := range []string{"auth.v1.AuthService.Login", "auth.v1.AuthService.Logout"} {
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
			t.Errorf("expected call_rpc from %s to %s; edges=%+v", callerID, rpcID, edges)
		}
	}
}

// TestCallRPC_CreateClientAPI verifies that the newer createClient API
// produces the same edges as createPromiseClient.
func TestCallRPC_CreateClientAPI(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest {}
message LoginReply {}
`,
		"gen/ts/auth_connect.ts": `export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: {
    login: { name: "Login" },
  },
} as const;
`,
		"app/login_page.tsx": `import { createClient } from "@connectrpc/connect";
import { AuthService } from "../../gen/ts/auth_connect";

const transport = {};

export function LoginPage() {
  const client = createClient(AuthService, transport);
  return client.login({});
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// call_rpc targets proto rpc (PascalCase).
	rpcID := store.SymbolNodeID("auth.v1.AuthService.Login")
	edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 call_rpc edge for createClient API; got %d: %+v", len(edges), edges)
	}
	wantCaller := store.SymbolNodeID("login_page.LoginPage")
	if edges[0].From != wantCaller {
		t.Errorf("edge.From = %q, want %q", edges[0].From, wantCaller)
	}
}

// TestCallRPC_CrossFileExportedClient verifies the v2 cross-file case: a client
// constructed and exported in lib/clients.ts and invoked in app/page.tsx produces
// a call_rpc edge from page.Page to the proto rpc node.
func TestCallRPC_CrossFileExportedClient(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest {}
message LoginReply {}
`,
		"gen/ts/auth_connect.ts": `export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: {
    login: { name: "Login" },
  },
} as const;
`,
		// lib/clients.ts constructs the client and exports it.
		"lib/clients.ts": `import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "../gen/ts/auth_connect";
const transport = {};
export const authClient = createPromiseClient(AuthService, transport);
`,
		// app/page.tsx imports the pre-built client and calls it.
		"app/page.tsx": `import { authClient } from "../lib/clients";

export function Page() {
  return authClient.login({});
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// The proto rpc (PascalCase) should have one call_rpc edge from page.Page.
	rpcID := store.SymbolNodeID("auth.v1.AuthService.Login")
	edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc): %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 call_rpc edge into auth.v1.AuthService.Login; got %d: %+v", len(edges), edges)
	}
	wantCaller := store.SymbolNodeID("page.Page")
	if edges[0].From != wantCaller {
		t.Errorf("edge.From = %q, want %q", edges[0].From, wantCaller)
	}

	// The connect-es stub (lowerCamelCase) should never receive a call_rpc edge;
	// buildCallRPCEdges always follows implements_rpc to the proto rpc.
	stubID := store.SymbolNodeID("auth.v1.AuthService.login")
	stubEdges, err := s.Neighbors(stubID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc, stub): %v", err)
	}
	if len(stubEdges) != 0 {
		t.Errorf("expected 0 call_rpc edges on stub node; got %d: %+v", len(stubEdges), stubEdges)
	}
}

// TestCallRPC_NonConnectUsage_NoEdge verifies that regular property accesses
// and unrelated library calls do NOT produce call_rpc edges.
func TestCallRPC_NonConnectUsage_NoEdge(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest {}
message LoginReply {}
`,
		"gen/ts/auth_connect.ts": `export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: { login: { name: "Login" } },
} as const;
`,
		// This file never calls createPromiseClient; it just uses a plain object.
		"src/no_connect.ts": `import axios from "axios";
import { AuthService } from "../gen/ts/auth_connect";

export function doSomething() {
  // Direct property access — not a Connect-ES client call.
  const methods = AuthService.methods.login;
  // Axios call — unrelated.
  return axios.post("/login", {});
}
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// Check both nodes — neither should have call_rpc edges.
	for _, rpcPath := range []string{"auth.v1.AuthService.Login", "auth.v1.AuthService.login"} {
		rpcID := store.SymbolNodeID(rpcPath)
		edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
		if err != nil {
			t.Fatalf("Neighbors(call_rpc, %s): %v", rpcPath, err)
		}
		if len(edges) != 0 {
			t.Errorf("expected 0 call_rpc edges for non-connect usage on %s; got %d: %+v", rpcPath, len(edges), edges)
		}
	}
}

// TestCallRPC_CrossFileUnexportedClient verifies that a non-exported
// createPromiseClient call does NOT appear in the pre-pass export map and
// therefore does NOT generate a call_rpc edge when imported from another file.
func TestCallRPC_CrossFileUnexportedClient(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest {}
message LoginReply {}
`,
		"gen/ts/auth_connect.ts": `export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: { login: { name: "Login" } },
} as const;
`,
		// lib/internal.ts: client is NOT exported — should be excluded from pre-pass.
		"lib/internal.ts": `import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "../gen/ts/auth_connect";
const transport = {};
const authClient = createPromiseClient(AuthService, transport);
`,
		// app/page.tsx imports from the file but there is no exported client — no edge.
		"app/page.tsx": `import { authClient } from "../lib/internal";

export function Page() {
  return authClient.login({});
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
	if len(edges) != 0 {
		t.Errorf("unexported client: expected 0 call_rpc edges; got %d: %+v", len(edges), edges)
	}
}

// TestCallRPC_CrossFileAliasedImport verifies that aliased cross-file imports
// (import { authClient as client } from ...) produce a call_rpc edge using the
// local alias name.
func TestCallRPC_CrossFileAliasedImport(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest {}
message LoginReply {}
`,
		"gen/ts/auth_connect.ts": `export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: { login: { name: "Login" } },
} as const;
`,
		"lib/clients.ts": `import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "../gen/ts/auth_connect";
const transport = {};
export const authClient = createPromiseClient(AuthService, transport);
`,
		// Aliased import: authClient as client — the local alias drives edge detection.
		"app/page.tsx": `import { authClient as client } from "../lib/clients";

export function Page() {
  return client.login({});
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
		t.Fatalf("aliased import: expected 1 call_rpc edge; got %d: %+v", len(edges), edges)
	}
	wantCaller := store.SymbolNodeID("page.Page")
	if edges[0].From != wantCaller {
		t.Errorf("edge.From = %q, want %q", edges[0].From, wantCaller)
	}
}

// TestCallRPC_CrossFileDestructuredExportExcluded pins that a destructured
// export (export const { login } = createPromiseClient(...)) is intentionally
// excluded from the pre-pass export map. The pre-pass only handles identifier
// LHS bindings; object_pattern LHS is dropped. This is documented in the
// known limitations section.
func TestCallRPC_CrossFileDestructuredExportExcluded(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest {}
message LoginReply {}
`,
		"gen/ts/auth_connect.ts": `export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: { login: { name: "Login" } },
} as const;
`,
		// lib/methods.ts: destructured export — excluded from pre-pass by design.
		"lib/methods.ts": `import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "../gen/ts/auth_connect";
const transport = {};
export const { login } = createPromiseClient(AuthService, transport);
`,
		// app/page.tsx imports the destructured method — no cross-file edge expected.
		"app/page.tsx": `import { login } from "../lib/methods";

export function Page() {
  return login({});
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
	if len(edges) != 0 {
		t.Errorf("destructured export: expected 0 cross-file call_rpc edges; got %d: %+v", len(edges), edges)
	}
}

// TestCallRPC_CrossFileChainedReexportNotLinked pins the known limitation:
// chained re-exports (lib/index.ts re-exports from lib/clients.ts) are NOT
// followed by the pre-pass. Only the file with the direct factory call is
// indexed into clientExports.
func TestCallRPC_CrossFileChainedReexportNotLinked(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest {}
message LoginReply {}
`,
		"gen/ts/auth_connect.ts": `export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: { login: { name: "Login" } },
} as const;
`,
		// lib/clients.ts: direct factory call and export.
		"lib/clients.ts": `import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "../gen/ts/auth_connect";
const transport = {};
export const authClient = createPromiseClient(AuthService, transport);
`,
		// lib/index.ts: re-exports authClient — chained re-export, NOT in pre-pass.
		"lib/index.ts": `export { authClient } from "./clients";
`,
		// app/page.tsx imports from the chained re-export, NOT from the direct source.
		"app/page.tsx": `import { authClient } from "../lib/index";

export function Page() {
  return authClient.login({});
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
	if len(edges) != 0 {
		t.Errorf("chained re-export: expected 0 call_rpc edges (known limitation); got %d: %+v", len(edges), edges)
	}
}

// TestCallRPC_AnonymousEnclosure documents the skip rule: a call site inside
// an anonymous enclosure (e.g. inline JSX arrow prop or module-level IIFE)
// that has no named ancestor function does NOT produce an edge.
func TestCallRPC_AnonymousEnclosure(t *testing.T) {
	dir := t.TempDir()
	writeImplementsRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest {}
message LoginReply {}
`,
		"gen/ts/auth_connect.ts": `export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: { login: { name: "Login" } },
} as const;
`,
		// Module-level call: const result = client.login({}) at module scope.
		// There is no enclosing named function → the edge is skipped.
		"src/module_scope.ts": `import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "../gen/ts/auth_connect";

const transport = {};
const client = createPromiseClient(AuthService, transport);

// Module-level invocation — no enclosing named function. Edge skipped.
const result = client.login({});
`,
	})

	idx, s := openImplementsRPCStore(t, dir)
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// Check both nodes — anonymous enclosure skips the edge regardless of target.
	for _, rpcPath := range []string{"auth.v1.AuthService.Login", "auth.v1.AuthService.login"} {
		rpcID := store.SymbolNodeID(rpcPath)
		edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
		if err != nil {
			t.Fatalf("Neighbors(call_rpc, %s): %v", rpcPath, err)
		}
		if len(edges) != 0 {
			t.Errorf("expected 0 call_rpc edges for anonymous enclosure on %s; got %d: %+v", rpcPath, len(edges), edges)
		}
	}
}
