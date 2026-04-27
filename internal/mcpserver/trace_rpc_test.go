package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3" // register the fk-off test conn's driver
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/config"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults" // register handlers
	"librarian/internal/store"
)

// errGetNodeReader wraps a storeReader and returns a configured error from
// GetNode for specific node IDs. All other methods delegate unchanged. Used
// to exercise the GetNode I/O-error branches that are unreachable via a real
// SQLite store (which normalises sql.ErrNoRows to (nil, nil)).
type errGetNodeReader struct {
	storeReader
	errIDs map[string]error
}

func (e *errGetNodeReader) GetNode(id string) (*store.Node, error) {
	if err, ok := e.errIDs[id]; ok {
		return nil, err
	}
	return e.storeReader.GetNode(id)
}

// traceRPCEmbedder satisfies the indexer's Embedder interface for tests that
// only exercise the graph pass — vector contents are irrelevant here.
type traceRPCEmbedder struct{ dim int }

func (e traceRPCEmbedder) Embed(string) ([]float64, error)    { return make([]float64, e.dim), nil }
func (e traceRPCEmbedder) EmbedBatch(t []string) ([][]float64, error) {
	out := make([][]float64, len(t))
	for i := range t {
		out[i] = make([]float64, e.dim)
	}
	return out, nil
}
func (e traceRPCEmbedder) Model() string { return "trace-rpc-fake" }

// writeTraceRPCFixture writes a multi-file project under dir. Mirrors the
// helper used by implements_rpc_test.go so fixtures stay copy-paste
// compatible between the indexer-level and MCP-level test suites.
func writeTraceRPCFixture(t *testing.T, dir string, files map[string]string) {
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

// openTraceRPCStore builds an Indexer + Store pair rooted at dir and runs
// the graph pass with force=true so subsequent calls see a populated graph.
func openTraceRPCStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	dbPath := filepath.Join(dir, ".librarian", "trace-rpc-test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath, nil, 0)
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
	idx := indexer.New(s, cfg, traceRPCEmbedder{dim: 4})
	if _, err := idx.IndexProjectGraph(dir, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}
	return s
}

// crossLanguageProto is the fixture used by the happy-path and
// implementations-aggregation tests. Matches the shapes the
// implements_rpc resolver cross-links (Go server/client/stub + Dart + TS).
const crossLanguageProto = `syntax = "proto3";
package auth;

// LoginRequest carries credentials.
message LoginRequest {
  string user = 1;
  string pass = 2;
}
message LoginReply {
  bool ok = 1;
  string token = 2;
}

service AuthService {
  // Login authenticates a user.
  rpc Login (LoginRequest) returns (LoginReply);
  rpc Logout (LoginRequest) returns (LoginReply);
}
`

// TestTraceRPC_HappyPath_CrossLanguage pins the top-level end-to-end
// behaviour: proto + Go impl + Dart client + TS client → the tool returns
// the definition, all four implementers, both messages with fields, and the
// sibling rpc.
func TestTraceRPC_HappyPath_CrossLanguage(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		// Go server + client + unimplemented stub — three Go derivations.
		"gen/go/auth/service.go": `package auth

type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
func (s *AuthServiceServer) Logout() error { return nil }

type AuthServiceClient struct{}
func (c *AuthServiceClient) Login() error { return nil }
func (c *AuthServiceClient) Logout() error { return nil }

type UnimplementedAuthServiceServer struct{}
func (UnimplementedAuthServiceServer) Login() error { return nil }
func (UnimplementedAuthServiceServer) Logout() error { return nil }
`,
		// Dart client — the SvcBase + SvcClient derivations. Only SvcClient
		// collides with TS below; Dart's SvcClient derivation dedupes against
		// TS's in the resolver so we expect one auth.AuthServiceClient.login
		// edge total (node-set collision), not two. The Base variant is
		// unambiguously Dart.
		"gen/dart/auth.dart": `library auth;
class AuthServiceBase {
  void login() {}
  void logout() {}
}
class AuthServiceClient {
  void login() {}
  void logout() {}
}
`,
		// TS client — Svc.methodName derivation (the service-named class),
		// distinct sym: from Dart's AuthServiceClient above.
		"gen/ts/auth.ts": `export class AuthService {
  login(): void {}
  logout(): void {}
}
`,
	})

	s := openTraceRPCStore(t, dir)

	result, err := runTraceRPC(s, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}

	// Definition
	if result.Definition.Service != "AuthService" {
		t.Errorf("Definition.Service = %q, want %q", result.Definition.Service, "AuthService")
	}
	if result.Definition.Method != "Login" {
		t.Errorf("Definition.Method = %q, want %q", result.Definition.Method, "Login")
	}
	if result.Definition.Package != "auth" {
		t.Errorf("Definition.Package = %q, want %q", result.Definition.Package, "auth")
	}
	if result.Definition.InputType != "LoginRequest" {
		t.Errorf("Definition.InputType = %q, want %q", result.Definition.InputType, "LoginRequest")
	}
	if result.Definition.OutputType != "LoginReply" {
		t.Errorf("Definition.OutputType = %q, want %q", result.Definition.OutputType, "LoginReply")
	}
	if result.Definition.ProtoFile != "api/auth.proto" {
		t.Errorf("Definition.ProtoFile = %q, want %q", result.Definition.ProtoFile, "api/auth.proto")
	}
	if result.Definition.LineNumber == 0 {
		t.Errorf("Definition.LineNumber = 0, expected non-zero line of `rpc Login ...`")
	}
	if !strings.Contains(result.Definition.Docstring, "Login authenticates") {
		t.Errorf("Definition.Docstring = %q, expected leading `// Login authenticates a user.` comment", result.Definition.Docstring)
	}

	// Implementations: expect 6 derivations (3 Go — Server, Client,
	// Unimplemented stub; 2 Dart — Base, Client; 1 TS — Service). TS's
	// SvcClient collides with Dart's SvcClient on the candidate name, but
	// each concrete graph node has a distinct sym: id keyed on the source
	// file so we see both Dart AuthServiceClient.login AND the TS
	// AuthService.login as separate edges. Count the unique sym: IDs.
	// The 6 derivations enumerated here mirror the resolver's per-language
	// candidate set (internal/indexer/implements_rpc.go linkRPCImplementations):
	// Go protoc-gen-go+grpc-go emits Server/Client/Unimplemented, Dart
	// protoc-gen-dart emits Base/Client, TS @bufbuild/protoc-gen-es emits
	// Client/Service (SvcClient dedupes against Dart's on sym: id). The
	// hardcoded list is intentional — a regression that adds a new
	// derivation would silently slip past an "at least N" assertion; this
	// pin forces the resolver's candidate count to be updated here too.
	wantImpls := map[string]bool{
		"auth.AuthServiceServer.Login":              false, // Go server (grpc-go)
		"auth.AuthServiceClient.Login":              false, // Go client (grpc-go)
		"auth.UnimplementedAuthServiceServer.Login": false, // Go default stub
		"auth.AuthServiceBase.login":                false, // Dart base (protoc-gen-dart)
		"auth.AuthServiceClient.login":              false, // Dart client (collides with TS in sym-space, but Dart file extension distinguishes)
		"auth.AuthService.login":                    false, // TS service class (@bufbuild/protoc-gen-es)
	}
	for _, impl := range result.Implementations {
		if _, ok := wantImpls[impl.SymbolPath]; !ok {
			t.Errorf("unexpected implementation %q", impl.SymbolPath)
			continue
		}
		wantImpls[impl.SymbolPath] = true
	}
	for p, seen := range wantImpls {
		if !seen {
			t.Errorf("missing implementation %q; got %+v", p, result.Implementations)
		}
	}

	// Implementations: language + kind classification should surface useful
	// labels for at least the Go+Dart+TS entries.
	byPath := map[string]traceRPCImplementation{}
	for _, impl := range result.Implementations {
		byPath[impl.SymbolPath] = impl
	}
	if imp := byPath["auth.AuthServiceServer.Login"]; imp.Language != "go" || imp.Kind != "server" {
		t.Errorf("go server classify: language=%q kind=%q, want go/server", imp.Language, imp.Kind)
	}
	if imp := byPath["auth.UnimplementedAuthServiceServer.Login"]; imp.Kind != "stub" {
		t.Errorf("unimplemented stub classify: kind=%q, want stub", imp.Kind)
	}
	if imp := byPath["auth.AuthServiceBase.login"]; imp.Language != "dart" || imp.Kind != "base" {
		t.Errorf("dart base classify: language=%q kind=%q, want dart/base", imp.Language, imp.Kind)
	}
	if imp := byPath["auth.AuthService.login"]; imp.Language != "ts" || imp.Kind != "service" {
		t.Errorf("ts service classify: language=%q kind=%q, want ts/service", imp.Language, imp.Kind)
	}

	// LineNumber population — at least the Go server+client and the Dart
	// base entries should have non-zero line numbers (the scanTraceRPCMethodLine
	// heuristic matches `Login(` in Go/Dart/TS source).
	if imp := byPath["auth.AuthServiceServer.Login"]; imp.LineNumber == 0 {
		t.Errorf("Go server impl should have non-zero LineNumber; got %+v", imp)
	}
	if imp := byPath["auth.AuthServiceBase.login"]; imp.LineNumber == 0 {
		t.Errorf("Dart base impl should have non-zero LineNumber; got %+v", imp)
	}

	// Implementations are sorted by (language, symbol_path). Pin the first
	// two to catch a regression that would silently unsort the list.
	if len(result.Implementations) >= 2 {
		a, b := result.Implementations[0], result.Implementations[1]
		if a.Language > b.Language || (a.Language == b.Language && a.SymbolPath > b.SymbolPath) {
			t.Errorf("Implementations not sorted by (language, path): got %+v then %+v", a, b)
		}
	}

	// Input message — fields resolved from the indexed message symbols.
	if result.InputMessage == nil {
		t.Fatal("InputMessage nil")
	}
	if !result.InputMessage.Resolved {
		t.Errorf("InputMessage.Resolved = false; Note=%q", result.InputMessage.Note)
	}
	gotInputFields := map[string]int{}
	for _, f := range result.InputMessage.Fields {
		gotInputFields[f.Name] = f.FieldNumber
	}
	if gotInputFields["user"] != 1 {
		t.Errorf("InputMessage field user = %d, want 1 (got %+v)", gotInputFields["user"], result.InputMessage.Fields)
	}
	if gotInputFields["pass"] != 2 {
		t.Errorf("InputMessage field pass = %d, want 2", gotInputFields["pass"])
	}
	// Fields are sorted by (field_number, name). Slice-index the first two
	// explicitly so a future regression that randomises order falls out here.
	if len(result.InputMessage.Fields) >= 2 {
		if result.InputMessage.Fields[0].Name != "user" || result.InputMessage.Fields[0].FieldNumber != 1 {
			t.Errorf("InputMessage.Fields[0] = %+v, want {user, 1}", result.InputMessage.Fields[0])
		}
		if result.InputMessage.Fields[1].Name != "pass" || result.InputMessage.Fields[1].FieldNumber != 2 {
			t.Errorf("InputMessage.Fields[1] = %+v, want {pass, 2}", result.InputMessage.Fields[1])
		}
	}

	// Output message
	if result.OutputMessage == nil {
		t.Fatal("OutputMessage nil")
	}
	if !result.OutputMessage.Resolved {
		t.Errorf("OutputMessage.Resolved = false; Note=%q", result.OutputMessage.Note)
	}
	gotOutputFields := map[string]int{}
	for _, f := range result.OutputMessage.Fields {
		gotOutputFields[f.Name] = f.FieldNumber
	}
	if gotOutputFields["ok"] != 1 || gotOutputFields["token"] != 2 {
		t.Errorf("OutputMessage fields = %+v, want ok=1,token=2", result.OutputMessage.Fields)
	}

	// Related rpcs — the fixture has exactly one sibling (Logout).
	if len(result.RelatedRPCs) != 1 {
		t.Fatalf("RelatedRPCs count = %d, want 1: %+v", len(result.RelatedRPCs), result.RelatedRPCs)
	}
	if result.RelatedRPCs[0].Method != "Logout" {
		t.Errorf("RelatedRPCs[0].Method = %q, want Logout", result.RelatedRPCs[0].Method)
	}
	if result.RelatedRPCs[0].SymbolID != "sym:auth.AuthService.Logout" {
		t.Errorf("RelatedRPCs[0].SymbolID = %q, want sym:auth.AuthService.Logout", result.RelatedRPCs[0].SymbolID)
	}

	// Callers — no grammar emits call edges today, so the note must be set.
	if len(result.Callers) != 0 {
		t.Errorf("Callers should be empty at this indexing pass; got %+v", result.Callers)
	}
	if result.CallersNote == "" {
		t.Errorf("CallersNote must describe that call edges are unavailable")
	}
}

// TestTraceRPC_RPCNotFound pins the error shape: input is echoed back so the
// caller sees exactly what was searched for.
func TestTraceRPC_RPCNotFound(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
	})
	s := openTraceRPCStore(t, dir)

	_, err := runTraceRPC(s, dir, "auth.AuthService.DoesNotExist")
	if err == nil {
		t.Fatal("expected not-found error")
	}
	if !strings.Contains(err.Error(), "DoesNotExist") {
		t.Errorf("error should echo the missing rpc name; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should say not found; got %q", err.Error())
	}
}

// TestTraceRPC_NoImplementations pins that a rpc indexed without any
// implementer returns the definition with empty implementations / callers
// and no error.
func TestTraceRPC_NoImplementations(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		// Deliberately no Go/Dart/TS files.
	})
	s := openTraceRPCStore(t, dir)

	result, err := runTraceRPC(s, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	if len(result.Implementations) != 0 {
		t.Errorf("Implementations should be empty; got %+v", result.Implementations)
	}
	if len(result.Callers) != 0 {
		t.Errorf("Callers should be empty; got %+v", result.Callers)
	}
	// Definition must still be populated.
	if result.Definition.Method != "Login" {
		t.Errorf("Definition.Method = %q, want Login", result.Definition.Method)
	}
	if result.Definition.InputType != "LoginRequest" {
		t.Errorf("Definition.InputType = %q, want LoginRequest", result.Definition.InputType)
	}
}

// TestTraceRPC_MessageResolution_FieldsNotJustTypeName pins that
// input_message/output_message return field-level detail, not just the type
// name. Protects against a regression that drops the message-symbol lookup.
func TestTraceRPC_MessageResolution_FieldsNotJustTypeName(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
	})
	s := openTraceRPCStore(t, dir)

	result, err := runTraceRPC(s, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	if result.InputMessage == nil || !result.InputMessage.Resolved {
		t.Fatalf("InputMessage must resolve; got %+v", result.InputMessage)
	}
	if len(result.InputMessage.Fields) == 0 {
		t.Errorf("InputMessage.Fields empty — message resolution regressed from type-name-only")
	}
	if result.InputMessage.SymbolID != "sym:auth.LoginRequest" {
		t.Errorf("InputMessage.SymbolID = %q, want sym:auth.LoginRequest", result.InputMessage.SymbolID)
	}
	if result.InputMessage.ProtoFile == "" {
		t.Errorf("InputMessage.ProtoFile empty — expected proto path")
	}
}

// TestTraceRPC_InputFlexibility_AllForms pins that each of the four input
// shapes resolves to the same rpc. Collision-on-bare-method lives in the
// dedicated ambiguity test below.
func TestTraceRPC_InputFlexibility_AllForms(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
	})
	s := openTraceRPCStore(t, dir)

	cases := []struct {
		name  string
		input string
	}{
		{"symbol_id", "sym:auth.AuthService.Login"},
		{"dotted_path_full", "auth.AuthService.Login"},
		{"service_method", "AuthService.Login"},
		{"file_method", "api/auth.proto:Login"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := runTraceRPC(s, dir, tc.input)
			if err != nil {
				t.Fatalf("runTraceRPC(%q): %v", tc.input, err)
			}
			if result.Definition.SymbolID != "sym:auth.AuthService.Login" {
				t.Errorf("%s: got SymbolID=%q, want sym:auth.AuthService.Login", tc.name, result.Definition.SymbolID)
			}
		})
	}
}

// TestTraceRPC_AmbiguousInput_ListsCandidates pins that a suffix that matches
// multiple rpcs returns an error naming every candidate, giving the caller
// enough to pick the right qualifier.
func TestTraceRPC_AmbiguousInput_ListsCandidates(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		// Two proto files, two packages, two different services — but both
		// define a `Login` rpc. Bare "Login" is ambiguous; the service-only
		// suffix must be qualified to disambiguate.
		"api/auth.proto": `syntax = "proto3";
package auth;
message Req {}
message Rep {}
service AuthService {
  rpc Login (Req) returns (Rep);
}
`,
		"api/admin.proto": `syntax = "proto3";
package admin;
message Req {}
message Rep {}
service AdminService {
  rpc Login (Req) returns (Rep);
}
`,
	})
	s := openTraceRPCStore(t, dir)

	_, err := runTraceRPC(s, dir, "Login")
	if err == nil {
		t.Fatal("expected ambiguity error for bare `Login`")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention ambiguity; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "sym:auth.AuthService.Login") || !strings.Contains(err.Error(), "sym:admin.AdminService.Login") {
		t.Errorf("error should list both candidate sym: ids; got %q", err.Error())
	}
}

// TestTraceRPC_FileAndMethod_AmbiguousAcrossSamePathReturnsCandidates pins the
// file+method form's ambiguity branch: when a single proto file declares the
// same method name in two different services (non-standard but possible via
// a refactor mid-flight), the tool returns a listing error rather than an
// arbitrary pick.
func TestTraceRPC_FileAndMethod_AmbiguousAcrossSamePathReturnsCandidates(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth;
message Req {}
message Rep {}
service A {
  rpc Ping (Req) returns (Rep);
}
service B {
  rpc Ping (Req) returns (Rep);
}
`,
	})
	s := openTraceRPCStore(t, dir)

	_, err := runTraceRPC(s, dir, "api/auth.proto:Ping")
	if err == nil {
		t.Fatal("expected ambiguity error for bare Ping in multi-service file")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("error should mention ambiguity; got %q", err.Error())
	}
}

// TestTraceRPC_OutputModes_MarkdownAndJSON pins that both output modes are
// coherent end-to-end: markdown mentions the rpc+service and JSON parses
// back into the result struct with the same key facts.
func TestTraceRPC_OutputModes_MarkdownAndJSON(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		"gen/go/auth/service.go": `package auth
type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
`,
	})
	s := openTraceRPCStore(t, dir)

	result, err := runTraceRPC(s, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}

	// Markdown: the key bindings an AI assistant needs to understand the rpc.
	md := renderTraceRPCMarkdown(result)
	for _, need := range []string{
		"trace_rpc: auth.AuthService.Login",
		"**Service:** AuthService",
		"**Method:** Login",
		"## Implementations",
		"auth.AuthServiceServer.Login",
		"## Input message",
		"## Output message",
		"## Related RPCs",
		"Logout",
	} {
		if !strings.Contains(md, need) {
			t.Errorf("markdown missing %q; full:\n%s", need, md)
		}
	}
	// Length sanity — the bead's spec caps TTY output at under 200 lines.
	if got := strings.Count(md, "\n"); got > 200 {
		t.Errorf("markdown too long (%d lines, cap 200)", got)
	}

	// JSON round-trip: marshal the result and unmarshal into the same shape.
	// This catches JSON tag drift that would otherwise silently break
	// structured consumers.
	buf, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	var rt traceRPCResult
	if err := json.Unmarshal(buf, &rt); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	if rt.Definition.SymbolID != result.Definition.SymbolID {
		t.Errorf("json round-trip lost Definition.SymbolID: got %q, want %q", rt.Definition.SymbolID, result.Definition.SymbolID)
	}
	if len(rt.Implementations) != len(result.Implementations) {
		t.Errorf("json round-trip changed implementations count: got %d, want %d", len(rt.Implementations), len(result.Implementations))
	}
	if rt.InputMessage == nil || rt.InputMessage.TypeName != "LoginRequest" {
		t.Errorf("json round-trip lost InputMessage.TypeName: got %+v", rt.InputMessage)
	}
}

// TestTraceRPC_EmptyInput pins the empty-string rejection path — the
// user-facing error should say something more useful than "nil pointer".
func TestTraceRPC_EmptyInput(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
	})
	s := openTraceRPCStore(t, dir)

	_, err := runTraceRPC(s, dir, "   ")
	if err == nil {
		t.Fatal("expected empty-input error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error should mention empty input; got %q", err.Error())
	}
}

// TestTraceRPC_RelatedRPCs_StableOrdering pins sibling ordering: related rpcs
// must come back alphabetised by method so consumers can diff-check the
// response without sorting it themselves.
func TestTraceRPC_RelatedRPCs_StableOrdering(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/users.proto": `syntax = "proto3";
package users;

message Req {}
message Rep {}

service UserService {
  rpc Delete (Req) returns (Rep);
  rpc Create (Req) returns (Rep);
  rpc Get (Req) returns (Rep);
  rpc List (Req) returns (Rep);
}
`,
	})
	s := openTraceRPCStore(t, dir)

	result, err := runTraceRPC(s, dir, "users.UserService.Get")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	var got []string
	for _, r := range result.RelatedRPCs {
		got = append(got, r.Method)
	}
	want := []string{"Create", "Delete", "List"}
	if len(got) != len(want) {
		t.Fatalf("RelatedRPCs = %v, want %v (Get itself must be excluded)", got, want)
	}
	if !sort.StringsAreSorted(got) {
		t.Errorf("RelatedRPCs not alphabetical: %v", got)
	}
	for i, m := range want {
		if got[i] != m {
			t.Errorf("RelatedRPCs[%d] = %q, want %q", i, got[i], m)
		}
	}
}

// TestTraceRPC_StreamingFlagsSurface pins that streaming flags are reported
// on both the focal rpc and the related ones so the caller can see the
// service's streaming shape at a glance.
func TestTraceRPC_StreamingFlagsSurface(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/chat.proto": `syntax = "proto3";
package chat;
message Msg {}
service Chat {
  rpc Send (stream Msg) returns (stream Msg);
  rpc Ping (Msg) returns (Msg);
}
`,
	})
	s := openTraceRPCStore(t, dir)

	result, err := runTraceRPC(s, dir, "chat.Chat.Send")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	if !result.Definition.ClientStreaming || !result.Definition.ServerStreaming {
		t.Errorf("expected bidi streaming; got client=%v server=%v", result.Definition.ClientStreaming, result.Definition.ServerStreaming)
	}
	// Related rpc should be non-streaming.
	if len(result.RelatedRPCs) != 1 {
		t.Fatalf("RelatedRPCs = %+v, want 1 (Ping)", result.RelatedRPCs)
	}
	if result.RelatedRPCs[0].ClientStreaming || result.RelatedRPCs[0].ServerStreaming {
		t.Errorf("Ping should be unary; got client=%v server=%v",
			result.RelatedRPCs[0].ClientStreaming, result.RelatedRPCs[0].ServerStreaming)
	}
}

// TestTraceRPC_CallersSurfaceWhenPresent pins the (currently empty) callers
// path: if ANYTHING emits a `call` edge into an implementation node, the tool
// surfaces it. Uses a manual UpsertEdge to stand in for a hypothetical grammar
// that emits call edges — guards against a refactor that silently stops
// walking call edges.
func TestTraceRPC_CallersSurfaceWhenPresent(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		"gen/go/auth/service.go": `package auth
type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
`,
		// A synthetic caller file — indexed as a regular Go file so its
		// symbol node exists.
		"cmd/main.go": `package main
func callIt() {}
`,
	})
	s := openTraceRPCStore(t, dir)

	// Manually inject a call edge from main.callIt to the Go server's Login.
	// No grammar emits these yet, so the tool's callers section would be
	// empty without this synthetic edge — we use it to verify the walking
	// logic is wired rather than hardcoded to return nothing.
	implID := store.SymbolNodeID("auth.AuthServiceServer.Login")
	callerID := store.SymbolNodeID("main.callIt")
	if err := s.UpsertEdge(store.Edge{From: callerID, To: implID, Kind: "call"}); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}

	result, err := runTraceRPC(s, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	if len(result.Callers) != 1 {
		t.Fatalf("Callers = %+v, want 1", result.Callers)
	}
	if result.Callers[0].SymbolPath != "main.callIt" {
		t.Errorf("Callers[0].SymbolPath = %q, want main.callIt", result.Callers[0].SymbolPath)
	}
	// LineNumber should be populated via scanTraceRPCMethodLine over the
	// synthetic cmd/main.go (the `func callIt()` line). A zero here would
	// mean the per-caller line-scan pipeline regressed.
	if result.Callers[0].LineNumber == 0 {
		t.Errorf("Callers[0].LineNumber = 0, expected non-zero line of `func callIt()` in cmd/main.go; got %+v", result.Callers[0])
	}
	if result.Callers[0].Depth != 1 {
		t.Errorf("Callers[0].Depth = %d, want 1 (direct caller)", result.Callers[0].Depth)
	}
	// CallersNote must be cleared when at least one caller is present,
	// otherwise consumers would see BOTH the note and the callers list.
	if result.CallersNote != "" {
		t.Errorf("CallersNote should be empty when callers are present; got %q", result.CallersNote)
	}
}

// TestTraceRPC_CallersTransitiveBFS pins the transitive caller walk: when a
// chain `top → mid → impl` exists via `call` edges, trace_rpc returns both
// `top` (depth=2) and `mid` (depth=1). Regression guard against a
// direct-callers-only implementation shipped as "transitive" in docs/comments.
func TestTraceRPC_CallersTransitiveBFS(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		"gen/go/auth/service.go": `package auth
type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
`,
		"cmd/main.go": `package main
func topLevel() {}
func middle() {}
`,
	})
	s := openTraceRPCStore(t, dir)

	impl := store.SymbolNodeID("auth.AuthServiceServer.Login")
	mid := store.SymbolNodeID("main.middle")
	top := store.SymbolNodeID("main.topLevel")

	// Call graph: topLevel → middle → impl. The resolver should walk back
	// two hops.
	for _, e := range []store.Edge{
		{From: mid, To: impl, Kind: "call"},
		{From: top, To: mid, Kind: "call"},
	} {
		if err := s.UpsertEdge(e); err != nil {
			t.Fatalf("UpsertEdge: %v", err)
		}
	}

	result, err := runTraceRPC(s, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	gotDepths := map[string]int{}
	for _, c := range result.Callers {
		gotDepths[c.SymbolPath] = c.Depth
	}
	if gotDepths["main.middle"] != 1 {
		t.Errorf("main.middle depth = %d, want 1 (direct caller); got %+v", gotDepths["main.middle"], result.Callers)
	}
	if gotDepths["main.topLevel"] != 2 {
		t.Errorf("main.topLevel depth = %d, want 2 (transitive caller); got %+v", gotDepths["main.topLevel"], result.Callers)
	}
}

// TestTraceRPC_CallersBFSDepthCap pins the depth cap: a chain longer than
// traceRPCMaxCallerBFSDepth is truncated. Uses a chain of 5 hops and asserts
// the farthest caller (depth 5) is NOT returned while depth-3 caller is.
func TestTraceRPC_CallersBFSDepthCap(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		"gen/go/auth/service.go": `package auth
type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
`,
		"cmd/main.go": `package main
func h1() {}
func h2() {}
func h3() {}
func h4() {}
func h5() {}
`,
	})
	s := openTraceRPCStore(t, dir)

	impl := store.SymbolNodeID("auth.AuthServiceServer.Login")
	chain := []string{
		store.SymbolNodeID("main.h1"),
		store.SymbolNodeID("main.h2"),
		store.SymbolNodeID("main.h3"),
		store.SymbolNodeID("main.h4"),
		store.SymbolNodeID("main.h5"),
	}
	// h1 → impl (depth 1), h2 → h1 (depth 2), h3 → h2 (depth 3),
	// h4 → h3 (depth 4 — beyond cap), h5 → h4 (depth 5 — beyond cap).
	prev := impl
	for _, h := range chain {
		if err := s.UpsertEdge(store.Edge{From: h, To: prev, Kind: "call"}); err != nil {
			t.Fatalf("UpsertEdge: %v", err)
		}
		prev = h
	}

	result, err := runTraceRPC(s, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	got := map[string]int{}
	for _, c := range result.Callers {
		got[c.SymbolPath] = c.Depth
	}
	if _, ok := got["main.h1"]; !ok {
		t.Errorf("main.h1 (depth 1) missing: %+v", got)
	}
	if _, ok := got["main.h3"]; !ok {
		t.Errorf("main.h3 (depth 3, at cap) missing: %+v", got)
	}
	if d, ok := got["main.h4"]; ok {
		t.Errorf("main.h4 should be beyond depth cap; got depth=%d", d)
	}
	if _, ok := got["main.h5"]; ok {
		t.Errorf("main.h5 should be beyond depth cap; got %+v", got)
	}
}

// TestTraceRPC_NestedMessageRecursion pins B2: nested message types are
// recursively resolved, not dropped. The fixture has a message with a
// nested message containing its own fields, and trace_rpc must surface
// those nested fields.
func TestTraceRPC_NestedMessageRecursion(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/nested.proto": `syntax = "proto3";
package nested;

message Outer {
  string top_level = 1;
  Inner inner = 2;

  message Inner {
    string inner_name = 1;
    int32 inner_age = 2;
  }
}

message Reply {
  bool ok = 1;
}

service NestedService {
  rpc Do (Outer) returns (Reply);
}
`,
	})
	s := openTraceRPCStore(t, dir)

	result, err := runTraceRPC(s, dir, "nested.NestedService.Do")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}

	if result.InputMessage == nil || !result.InputMessage.Resolved {
		t.Fatalf("InputMessage must resolve; got %+v", result.InputMessage)
	}
	// Direct fields of Outer — nested fields must NOT leak in here (M4).
	gotDirect := map[string]bool{}
	for _, f := range result.InputMessage.Fields {
		gotDirect[f.Name] = true
	}
	if !gotDirect["top_level"] || !gotDirect["inner"] {
		t.Errorf("Outer.Fields missing top_level or inner: %+v", result.InputMessage.Fields)
	}
	// inner_name / inner_age belong to Inner, must NOT appear on Outer.
	if gotDirect["inner_name"] || gotDirect["inner_age"] {
		t.Errorf("Outer.Fields leaked nested message fields (M4 regression): %+v", result.InputMessage.Fields)
	}

	// NestedMessages must contain Inner with its own fields.
	if len(result.InputMessage.NestedMessages) != 1 {
		t.Fatalf("NestedMessages = %+v, want 1 (Inner)", result.InputMessage.NestedMessages)
	}
	inner := result.InputMessage.NestedMessages[0]
	if inner.TypeName != "Inner" {
		t.Errorf("nested.TypeName = %q, want Inner", inner.TypeName)
	}
	if !inner.Resolved {
		t.Errorf("nested Inner should be resolved; got %+v", inner)
	}
	gotInner := map[string]int{}
	for _, f := range inner.Fields {
		gotInner[f.Name] = f.FieldNumber
	}
	if gotInner["inner_name"] != 1 || gotInner["inner_age"] != 2 {
		t.Errorf("Inner fields = %+v, want inner_name=1,inner_age=2", inner.Fields)
	}
}

// TestTraceRPC_ResolveTypeFQ_LeadingDot pins that a proto type reference with
// a leading dot (`.auth.LoginRequest`, the proto "fully-qualified" syntax)
// resolves to the same message as the bare `LoginRequest`.
func TestTraceRPC_ResolveTypeFQ_LeadingDot(t *testing.T) {
	cases := []struct {
		rpcPackage, typeRef string
		want                string
	}{
		// Leading dot strips and keeps the rest.
		{"auth", ".auth.LoginRequest", "auth.LoginRequest"},
		{"", ".auth.LoginRequest", "auth.LoginRequest"},
		// Dotted without leading dot is treated as already qualified.
		{"foo", "auth.LoginRequest", "auth.LoginRequest"},
		// Bare name picks up the rpc's package.
		{"auth", "LoginRequest", "auth.LoginRequest"},
		{"auth.v1", "LoginRequest", "auth.v1.LoginRequest"},
		// Bare name with empty package returns the name unchanged.
		{"", "LoginRequest", "LoginRequest"},
		// Empty typeRef.
		{"auth", "", ""},
	}
	for _, tc := range cases {
		got := resolveTraceRPCTypeFQ(tc.rpcPackage, tc.typeRef)
		if got != tc.want {
			t.Errorf("resolveTraceRPCTypeFQ(%q, %q) = %q, want %q", tc.rpcPackage, tc.typeRef, got, tc.want)
		}
	}
}

// TestTraceRPC_LanguageDetectionTable pins all supported file extensions
// surface the expected language label. Expands reviewer M6: 5 previously
// uncovered cases (tsx/js/jsx/mjs/cjs/java/kt/kts/py/swift).
func TestTraceRPC_LanguageDetectionTable(t *testing.T) {
	cases := []struct{ path, want string }{
		{"gen/go/service.go", "go"},
		{"gen/dart/service.dart", "dart"},
		{"gen/ts/service.ts", "ts"},
		{"components/Button.tsx", "ts"},
		{"gen/js/service.js", "js"},
		{"components/Button.jsx", "js"},
		{"gen/js/service.mjs", "js"},
		{"gen/js/service.cjs", "js"},
		{"gen/java/Service.java", "java"},
		{"gen/kotlin/Service.kt", "kotlin"},
		{"build.gradle.kts", "kotlin"},
		{"gen/py/service.py", "python"},
		{"gen/swift/Service.swift", "swift"},
		{"api/auth.proto", "proto"},
		// Unknown extension → "".
		{"README.md", ""},
		{"no_extension", ""},
		// Empty path → "".
		{"", ""},
	}
	for _, tc := range cases {
		got := traceRPCLanguageFromSourcePath(tc.path)
		if got != tc.want {
			t.Errorf("traceRPCLanguageFromSourcePath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestTraceRPC_ResolveTraceRPCPath_Branches pins the three branches of
// resolveTraceRPCPath (absolute, project-rooted, empty-root+CWD). Reviewer
// flagged these as untested.
func TestTraceRPC_ResolveTraceRPCPath_Branches(t *testing.T) {
	// Absolute input is returned verbatim regardless of projectRoot.
	abs := "/absolute/path/to/file.go"
	if got := resolveTraceRPCPath("/some/root", abs); got != abs {
		t.Errorf("absolute path not returned verbatim: got %q, want %q", got, abs)
	}
	if got := resolveTraceRPCPath("", abs); got != abs {
		t.Errorf("absolute path with empty root not verbatim: got %q, want %q", got, abs)
	}

	// Project-rooted: join.
	if got := resolveTraceRPCPath("/project", "internal/auth.go"); got != "/project/internal/auth.go" {
		t.Errorf("project-rooted: got %q, want /project/internal/auth.go", got)
	}

	// Empty project root: filepath.Abs (CWD-relative). We just verify it
	// produces an absolute path — the exact CWD is environment-dependent.
	got := resolveTraceRPCPath("", "file.go")
	if !filepath.IsAbs(got) {
		t.Errorf("empty root should produce absolute path via filepath.Abs; got %q", got)
	}
}

// TestTraceRPC_ScanDefinitionSite_MissingFile pins that a missing file
// returns (0, "") rather than panicking.
func TestTraceRPC_ScanDefinitionSite_MissingFile(t *testing.T) {
	line, doc := scanTraceRPCDefinitionSite("/definitely/does/not/exist.proto", "Login")
	if line != 0 || doc != "" {
		t.Errorf("missing file: got (%d, %q), want (0, \"\")", line, doc)
	}
}

// TestTraceRPC_ScanDefinitionSite_LoginExtraGuard pins that scanning for
// "Login" does NOT match a prior "rpc LoginExtra" — the guard requires the
// character after the method name to be whitespace or '('. A naive prefix
// match would pick the first occurrence and return the wrong line.
func TestTraceRPC_ScanDefinitionSite_LoginExtraGuard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.proto")
	body := `syntax = "proto3";
package test;

service Thing {
  rpc LoginExtra (Req) returns (Rep);
  rpc Login (Req) returns (Rep);
}

message Req {}
message Rep {}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	line, _ := scanTraceRPCDefinitionSite(path, "Login")
	if line != 6 {
		t.Errorf("line = %d, want 6 (the `rpc Login (…)` line, not the LoginExtra line at 5)", line)
	}
}

// TestTraceRPC_ScanMethodLine_BasicAndWordBoundary pins
// scanTraceRPCMethodLine behaviours used to populate implementation /
// caller line numbers.
func TestTraceRPC_ScanMethodLine_BasicAndWordBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.go")
	body := `package auth

// MyLoginHelper should NOT be matched when searching for Login.
func MyLoginHelper() {}

// Login is the real declaration we expect to find.
func (s *AuthServiceServer) Login(ctx context.Context) error {
    return nil
}
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	line := scanTraceRPCMethodLine(path, "Login")
	// The declaration is on line 7 (1-indexed).
	if line != 7 {
		t.Errorf("line = %d, want 7 (the `func (s *...) Login(...)` line; MyLoginHelper must be skipped via whole-word guard)", line)
	}

	// Missing file → 0.
	if got := scanTraceRPCMethodLine("/does/not/exist", "Login"); got != 0 {
		t.Errorf("missing file: got %d, want 0", got)
	}
	// Empty path / method → 0.
	if got := scanTraceRPCMethodLine("", "Login"); got != 0 {
		t.Errorf("empty path: got %d, want 0", got)
	}
	if got := scanTraceRPCMethodLine(path, ""); got != 0 {
		t.Errorf("empty method: got %d, want 0", got)
	}
}

// TestTraceRPC_MCP_RoundTrip exercises registerTraceRPC end-to-end through
// a real MCP server: creates an in-memory server, registers the tool, and
// drives it via a mcp.CallToolRequest to verify format=json, format=markdown,
// missing rpc arg, and unsupported format are all handled. Reviewer M5.
func TestTraceRPC_MCP_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		"gen/go/auth/service.go": `package auth
type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
`,
	})
	s := openTraceRPCStore(t, dir)

	srv := server.NewMCPServer("test", "0.0.1", server.WithToolCapabilities(false))
	registerTraceRPC(srv, s, &config.Config{ProjectRoot: dir})

	callTool := func(args map[string]any) (*mcp.CallToolResult, bool) {
		t.Helper()
		// HandleMessage returns a JSONRPCMessage (either response or error).
		// Serialise then re-parse to expose the tool-result envelope — the
		// return type is an interface, and the tool-result struct it wraps
		// is the stable contract we care about here.
		msg := srv.HandleMessage(context.Background(), mustCallToolRequestJSON(t, args))
		raw, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		var parsed struct {
			Result *mcp.CallToolResult `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("unmarshal response %q: %v", raw, err)
		}
		if parsed.Error != nil {
			return nil, false
		}
		return parsed.Result, true
	}

	// Case 1: default (markdown) output.
	if res, ok := callTool(map[string]any{"rpc": "auth.AuthService.Login"}); ok {
		text := toolResultText(t, res)
		if !strings.Contains(text, "# trace_rpc: auth.AuthService.Login") {
			t.Errorf("default output missing markdown header; got:\n%s", text)
		}
		if res.IsError {
			t.Errorf("default call should not be IsError; got result=%+v", res)
		}
	} else {
		t.Fatal("default MCP call failed")
	}

	// Case 2: explicit json output — also verifies LineNumber is non-zero after
	// a fresh index (lib-r4s.5: graph_nodes now persists line_number).
	if res, ok := callTool(map[string]any{"rpc": "auth.AuthService.Login", "format": "json"}); ok {
		text := toolResultText(t, res)
		var parsed traceRPCResult
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			t.Fatalf("json output not valid JSON: %v\npayload:\n%s", err, text)
		}
		if parsed.Definition.Method != "Login" {
			t.Errorf("json output wrong method: %q", parsed.Definition.Method)
		}
		if parsed.Definition.LineNumber == 0 {
			t.Errorf("Definition.LineNumber = 0 after fresh index; expected non-zero (lib-r4s.5 regression check)")
		}
	} else {
		t.Fatal("json-format MCP call failed")
	}

	// Case 3: missing required `rpc` → IsError.
	if res, ok := callTool(map[string]any{}); ok {
		if !res.IsError {
			t.Errorf("missing rpc should produce IsError; got %+v", res)
		}
	} else {
		t.Fatal("missing-rpc MCP call failed")
	}

	// Case 4: unsupported format → IsError.
	if res, ok := callTool(map[string]any{"rpc": "auth.AuthService.Login", "format": "xml"}); ok {
		if !res.IsError {
			t.Errorf("unsupported format should produce IsError; got %+v", res)
		}
		text := toolResultText(t, res)
		if !strings.Contains(text, "unsupported format") {
			t.Errorf("error text should mention unsupported format; got %q", text)
		}
	} else {
		t.Fatal("bad-format MCP call failed")
	}
}

// mustCallToolRequestJSON builds a well-formed JSON-RPC tools/call payload
// for srv.HandleMessage, which expects serialized JSON bytes.
func mustCallToolRequestJSON(t *testing.T, args map[string]any) []byte {
	t.Helper()
	payload := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "trace_rpc",
			"arguments": args,
		},
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return buf
}

// toolResultText extracts concatenated text content from a CallToolResult.
// mcp-go's Content is a polymorphic slice; the trace_rpc tool only ever
// returns text content so the extraction is a simple switch.
func toolResultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestTraceRPC_HappyPathProducesNoWarnings pins that a well-formed fixture
// doesn't emit spurious warnings. Guards against a regression where the
// silent-swallow fix (reviewer #8/#9) false-positives on healthy nodes — a
// bug that would surface as warning noise on every call. The dangling-edge
// case itself isn't reachable in a single-writer test because graph_edges
// has ON DELETE CASCADE foreign keys; the warning path is defensive against
// concurrent-writer races and matches the wording the resolver uses in
// production.
func TestTraceRPC_HappyPathProducesNoWarnings(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		"gen/go/auth/service.go": `package auth
type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
`,
	})
	s := openTraceRPCStore(t, dir)

	result, err := runTraceRPC(s, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("happy path produced unexpected warnings: %v", result.Warnings)
	}
}

// TestTraceRPC_NestedMessageCapSurfaces pins the depth-cap note: a chain of
// nested messages deeper than traceRPCMaxNestedDepth should stop recursing
// and surface the cap in the Note field so consumers aren't misled.
func TestTraceRPC_NestedMessageCapSurfaces(t *testing.T) {
	dir := t.TempDir()
	// Build a 4-deep nested chain: Outer → L1 → L2 → L3 → L4. With the
	// current cap of 3, L4's contents should be unreachable.
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/deep.proto": `syntax = "proto3";
package deep;

message Outer {
  string a = 1;
  L1 l1 = 2;
  message L1 {
    string b = 1;
    L2 l2 = 2;
    message L2 {
      string c = 1;
      L3 l3 = 2;
      message L3 {
        string d = 1;
        L4 l4 = 2;
        message L4 {
          string e = 1;
        }
      }
    }
  }
}

message Reply { bool ok = 1; }

service S {
  rpc Op (Outer) returns (Reply);
}
`,
	})
	s := openTraceRPCStore(t, dir)

	result, err := runTraceRPC(s, dir, "deep.S.Op")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	// Walk the nested chain to the cap.
	m := result.InputMessage
	if m == nil {
		t.Fatal("InputMessage nil")
	}
	// depth 0 = Outer, depth 1 = L1, depth 2 = L2; at cap of 3 the
	// recursion stops with depth 2 still resolving its own fields.
	cur := m
	for depth := 0; depth < traceRPCMaxNestedDepth; depth++ {
		if len(cur.NestedMessages) == 0 {
			break
		}
		cur = &cur.NestedMessages[0]
	}
	// Once at the cap, the deepest still-rendered message must carry the
	// depth-cap Note so consumers aren't misled into thinking the tree ends
	// there. A regression that silently drops the note (or rewords it to
	// omit "depth") would pass a t.Logf — assert strictly.
	if cur.Note == "" {
		t.Errorf("deepest rendered message at depth cap must carry a Note; got empty on %+v", cur)
	} else if !strings.Contains(cur.Note, "depth") {
		t.Errorf("depth-cap Note must mention 'depth'; got %q", cur.Note)
	}
	// L4 shouldn't be reachable: check its sym: id isn't indexed in any
	// nested-message's chain we returned.
	visited := map[string]bool{}
	var walk func(msg *traceRPCMessage)
	walk = func(msg *traceRPCMessage) {
		if msg == nil {
			return
		}
		if msg.SymbolID != "" {
			visited[msg.SymbolID] = true
		}
		for i := range msg.NestedMessages {
			walk(&msg.NestedMessages[i])
		}
	}
	walk(m)
	if visited["sym:deep.Outer.L1.L2.L3.L4"] {
		t.Errorf("L4 should be beyond traceRPCMaxNestedDepth=%d; got visited=%v", traceRPCMaxNestedDepth, visited)
	}
}

// TestTraceRPC_WalkCallers_DanglingEdgeWarning pins the dangling-edge
// warning branch in walkTraceRPCCallers. The graph_edges schema enforces a
// foreign key to graph_nodes, so under normal use dangling edges can't
// exist. We simulate one by opening a second SQLite connection with FK
// checks disabled and inserting a `call` edge pointing at an rpc from a
// never-created source id. The BFS must then surface a warning rather than
// silently dropping the edge.
func TestTraceRPC_WalkCallers_DanglingEdgeWarning(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		"gen/go/auth/service.go": `package auth
type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
`,
	})
	s := openTraceRPCStore(t, dir)

	// Resolve the indexed implementation sym: id and use it as the BFS seed.
	implID := store.SymbolNodeID("auth.AuthServiceServer.Login")

	// Open a separate connection with FK off and insert a dangling call edge.
	// mattn/go-sqlite3 reads `_foreign_keys` off the DSN at connection time.
	dbPath := filepath.Join(dir, ".librarian", "trace-rpc-test.db")
	rawDB, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=0")
	if err != nil {
		t.Fatalf("open fk-off conn: %v", err)
	}
	defer rawDB.Close()
	if _, err := rawDB.Exec(
		`INSERT INTO graph_edges (from_node, to_node, kind, weight, metadata) VALUES (?, ?, 'call', 1, '{}')`,
		"sym:nonexistent.Ghost.caller", implID,
	); err != nil {
		t.Fatalf("insert dangling edge: %v", err)
	}

	// BFS seeded at the impl should encounter the dangling edge on depth 1.
	var warnings []string
	callers := walkTraceRPCCallers(s, []string{implID}, traceRPCMaxCallerBFSDepth, dir, &warnings)
	// The dangling node shouldn't show up as a caller.
	for _, c := range callers {
		if strings.Contains(c.SymbolPath, "Ghost") {
			t.Errorf("dangling caller leaked into Callers: %+v", c)
		}
	}
	// A warning MUST be emitted.
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "nonexistent.Ghost.caller") && (strings.Contains(w, "missing") || strings.Contains(w, "dangling")) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected dangling-edge warning for sym:nonexistent.Ghost.caller; got %v", warnings)
	}
}

// TestTraceRPC_WalkCallers_NeighborsErrorOnClosedStore pins that a Neighbors
// failure during BFS surfaces a warning rather than aborting the whole
// trace. We simulate the failure by opening a store, closing it via a
// dedicated connection (bypassing openTraceRPCStore's t.Cleanup which also
// closes), and invoking the walker against the closed handle. Every
// subsequent Neighbors call returns a "database is closed" error.
func TestTraceRPC_WalkCallers_NeighborsErrorOnClosedStore(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
	})
	// Open a throwaway store without registering t.Cleanup — we close it
	// manually below. openTraceRPCStore's t.Cleanup would double-close the
	// handle after the test returns, logging a spurious error.
	dbPath := filepath.Join(dir, ".librarian", "trace-rpc-closed.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Close first — every subsequent Neighbors call fails.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var warnings []string
	callers := walkTraceRPCCallers(s, []string{"sym:auth.AuthService.Login"}, 1, dir, &warnings)
	if len(callers) != 0 {
		t.Errorf("closed store should produce no callers; got %+v", callers)
	}
	if len(warnings) == 0 {
		t.Errorf("closed store should produce a Neighbors error warning; got none")
	}
	// At least one warning must reference the Neighbors-error wording the
	// walker emits — a regression that silently swallows errors would leave
	// warnings empty.
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "neighbors(call)") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning with 'neighbors(call)' wording; got %v", warnings)
	}
}

// TestTraceRPC_RenderMarkdown_WarningsSection pins the warnings section is
// rendered when non-empty and absent when empty. Without this assertion, a
// regression that silently dropped the Warnings block would go unnoticed.
func TestTraceRPC_RenderMarkdown_WarningsSection(t *testing.T) {
	// With warnings present.
	result := &traceRPCResult{
		Input: "test.rpc",
		Definition: traceRPCDefinition{
			Service: "S", Method: "M", FullyQualified: "test.S.M", SymbolID: "sym:test.S.M",
		},
		Warnings: []string{"first warning surfaced", "second warning surfaced"},
	}
	md := renderTraceRPCMarkdown(result)
	if !strings.Contains(md, "## Warnings") {
		t.Errorf("markdown missing ## Warnings section; got:\n%s", md)
	}
	for _, w := range result.Warnings {
		if !strings.Contains(md, w) {
			t.Errorf("markdown missing warning %q; got:\n%s", w, md)
		}
	}

	// Without warnings, the section should be absent (not just empty).
	result.Warnings = nil
	md = renderTraceRPCMarkdown(result)
	if strings.Contains(md, "## Warnings") {
		t.Errorf("markdown should omit ## Warnings when Warnings is empty; got:\n%s", md)
	}
}

// TestTraceRPC_RenderMarkdown_WithCallers pins caller-formatting in the
// markdown renderer: file:line, language suffix, and the `(hop N)` depth
// annotation for transitive callers. Without these assertions a regression
// that drops any of those pieces would go silently unnoticed at render
// level.
func TestTraceRPC_RenderMarkdown_WithCallers(t *testing.T) {
	result := &traceRPCResult{
		Input: "test.S.M",
		Definition: traceRPCDefinition{
			Service: "S", Method: "M", FullyQualified: "test.S.M", SymbolID: "sym:test.S.M",
		},
		Implementations: []traceRPCImplementation{},
		Callers: []traceRPCCaller{
			{SymbolPath: "main.callDirect", File: "cmd/main.go", LineNumber: 10, Language: "go", Depth: 1},
			{SymbolPath: "main.callIndirect", File: "cmd/main.go", LineNumber: 20, Language: "go", Depth: 2},
			{SymbolPath: "main.callUnknownLang", File: "scripts/util.sh", LineNumber: 0, Language: "", Depth: 1},
		},
	}
	md := renderTraceRPCMarkdown(result)

	// Direct caller: file:line + language.
	if !strings.Contains(md, "cmd/main.go:10") {
		t.Errorf("markdown missing file:line for direct caller; got:\n%s", md)
	}
	if !strings.Contains(md, "(go)") {
		t.Errorf("markdown missing language suffix for Go caller; got:\n%s", md)
	}
	// Transitive caller: `(hop 2)` annotation.
	if !strings.Contains(md, "(hop 2)") {
		t.Errorf("markdown missing `(hop 2)` annotation for depth-2 caller; got:\n%s", md)
	}
	// Depth-1 caller should NOT get a hop annotation (hop 1 is implicit).
	if strings.Contains(md, "(hop 1)") {
		t.Errorf("markdown unexpectedly annotated direct caller with `(hop 1)`; got:\n%s", md)
	}
	// A caller with no line number renders just `- file` without colon.
	if !strings.Contains(md, "scripts/util.sh") {
		t.Errorf("markdown missing file path for caller without line number; got:\n%s", md)
	}
}

// TestTraceRPC_ImplKind_TableCovers pins traceRPCImplKind return values
// across the documented derivations and the default-empty branch. Reviewer
// m8.
func TestTraceRPC_ImplKind_TableCovers(t *testing.T) {
	cases := []struct {
		path, service, want string
	}{
		// Server / client / stub / base / service family.
		{"auth.AuthServiceServer.Login", "AuthService", "server"},
		{"auth.AuthServiceClient.Login", "AuthService", "client"},
		{"auth.UnimplementedAuthServiceServer.Login", "AuthService", "stub"},
		{"auth.AuthServiceBase.login", "AuthService", "base"},
		{"auth.AuthService.login", "AuthService", "service"},
		// Unrelated container (not part of the derivation family) → "".
		{"auth.SomeOtherClass.Login", "AuthService", ""},
		// Empty service input → "".
		{"auth.AuthServiceServer.Login", "", ""},
		// Path too shallow → "".
		{"Login", "AuthService", ""},
	}
	for _, tc := range cases {
		got := traceRPCImplKind(tc.path, tc.service)
		if got != tc.want {
			t.Errorf("traceRPCImplKind(%q, %q) = %q, want %q", tc.path, tc.service, got, tc.want)
		}
	}
}

// TestTraceRPC_ResolveMessageFQ_EmptyDefensiveBranch pins the defensive
// fq=="" branch in resolveTraceRPCMessageFQ. Not reachable under normal use
// (resolveTraceRPCTypeFQ only returns "" when typeRef is empty, which means
// runTraceRPC wouldn't call into the resolver at all), but kept as a safety
// net for callers that might invoke the FQ helper directly in the future.
// Reviewer m9.
func TestTraceRPC_ResolveMessageFQ_EmptyDefensiveBranch(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
	})
	s := openTraceRPCStore(t, dir)

	msg := resolveTraceRPCMessageFQ(s, "", "NothingHere", 0)
	if msg.Resolved {
		t.Errorf("empty fq should not resolve; got %+v", msg)
	}
	if !strings.Contains(msg.Note, "derive") && !strings.Contains(msg.Note, "fully-qualified") {
		t.Errorf("empty fq Note should explain the failure; got %q", msg.Note)
	}
}

// TestTraceRPC_WalkCallers_QueueingOrderPrunesBadNodes pins m7: a BFS
// frontier that includes a dangling node should not spend a Neighbors
// round-trip on that node at the next depth. Exercised by threading an
// un-resolvable source through a depth-2 chain and asserting exactly one
// warning (for the dangling node), not two (which would happen if the
// walker queued it for depth 2 and re-errored there).
func TestTraceRPC_WalkCallers_QueueingOrderPrunesBadNodes(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		"gen/go/auth/service.go": `package auth
type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
`,
	})
	s := openTraceRPCStore(t, dir)

	implID := store.SymbolNodeID("auth.AuthServiceServer.Login")
	ghost := "sym:nonexistent.Ghost.caller"

	// FK-off: insert a dangling "call" edge from ghost → impl, AND a second
	// dangling edge from another ghost → ghost. A BFS that queues ghost
	// despite the nil-GetNode would discover the second edge at depth 2
	// and emit a second warning. Proper behaviour: only emit the
	// first-depth warning for ghost and never walk further.
	dbPath := filepath.Join(dir, ".librarian", "trace-rpc-test.db")
	rawDB, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=0")
	if err != nil {
		t.Fatalf("open fk-off: %v", err)
	}
	defer rawDB.Close()
	for _, edge := range []struct{ from, to string }{
		{ghost, implID},
		{"sym:nonexistent.Ghost2.caller", ghost},
	} {
		if _, err := rawDB.Exec(
			`INSERT INTO graph_edges (from_node, to_node, kind, weight, metadata) VALUES (?, ?, 'call', 1, '{}')`,
			edge.from, edge.to,
		); err != nil {
			t.Fatalf("insert dangling edge: %v", err)
		}
	}

	var warnings []string
	_ = walkTraceRPCCallers(s, []string{implID}, 3, dir, &warnings)

	// The second-depth edge has `ghost` as its target — since ghost is
	// never queued for depth 2, Neighbors(ghost) at depth 2 is never called,
	// so no second warning appears for "ghost" getNode failure at deeper
	// depth. We pin this as exactly-one-warning for the ghost node.
	ghostWarnings := 0
	for _, w := range warnings {
		if strings.Contains(w, "nonexistent.Ghost.caller") {
			ghostWarnings++
		}
	}
	if ghostWarnings != 1 {
		t.Errorf("expected exactly 1 warning for dangling ghost node (m7: bad nodes must not be queued); got %d in %v", ghostWarnings, warnings)
	}
}

// TestTraceRPC_GetNodeError_ImplementationNode pins the GetNode I/O-error
// branch in runTraceRPC's implements_rpc loop. The branch fires when GetNode
// returns a non-nil error (driver-layer failure), not the (nil, nil) not-found
// signal. We inject a dangling implements_rpc edge via a FK-off connection and
// wrap the store with errGetNodeReader to make GetNode fail for that node.
func TestTraceRPC_GetNodeError_ImplementationNode(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
	})
	s := openTraceRPCStore(t, dir)

	rpcID := store.SymbolNodeID("auth.AuthService.Login")
	fakeID := "sym:fake.FakeImpl.login"

	dbPath := filepath.Join(dir, ".librarian", "trace-rpc-test.db")
	rawDB, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=0")
	if err != nil {
		t.Fatalf("open fk-off conn: %v", err)
	}
	defer rawDB.Close()
	if _, err := rawDB.Exec(
		`INSERT INTO graph_edges (from_node, to_node, kind, weight, metadata) VALUES (?, ?, ?, 1, '{}')`,
		fakeID, rpcID, store.EdgeKindImplementsRPC,
	); err != nil {
		t.Fatalf("insert fake implements_rpc edge: %v", err)
	}

	mock := &errGetNodeReader{
		storeReader: s,
		errIDs:      map[string]error{fakeID: errors.New("simulated I/O error")},
	}

	result, err := runTraceRPC(mock, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}

	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, fakeID) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning for GetNode I/O error on %q; got %v", fakeID, result.Warnings)
	}
}

// TestTraceRPC_GetNodeError_CallerNode pins the GetNode I/O-error branch
// inside walkTraceRPCCallers. A dangling `call` edge is injected via FK-off
// and the store is wrapped so GetNode fails for that caller node ID.
func TestTraceRPC_GetNodeError_CallerNode(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		"gen/go/auth/service.go": `package auth
type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
`,
	})
	s := openTraceRPCStore(t, dir)

	implID := store.SymbolNodeID("auth.AuthServiceServer.Login")
	fakeCallerID := "sym:fake.Caller.method"

	dbPath := filepath.Join(dir, ".librarian", "trace-rpc-test.db")
	rawDB, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=0")
	if err != nil {
		t.Fatalf("open fk-off conn: %v", err)
	}
	defer rawDB.Close()
	if _, err := rawDB.Exec(
		`INSERT INTO graph_edges (from_node, to_node, kind, weight, metadata) VALUES (?, ?, 'call', 1, '{}')`,
		fakeCallerID, implID,
	); err != nil {
		t.Fatalf("insert fake call edge: %v", err)
	}

	mock := &errGetNodeReader{
		storeReader: s,
		errIDs:      map[string]error{fakeCallerID: errors.New("simulated I/O error")},
	}

	var warnings []string
	callers := walkTraceRPCCallers(mock, []string{implID}, traceRPCMaxCallerBFSDepth, dir, &warnings)

	for _, c := range callers {
		if strings.Contains(c.SymbolPath, "fake.Caller") {
			t.Errorf("errored caller should not appear in Callers: %+v", c)
		}
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, fakeCallerID) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning for GetNode I/O error on %q; got %v", fakeCallerID, warnings)
	}
}

// TestTraceRPC_GetNodeError_CallRPCCaller pins the GetNode I/O-error branch
// in runTraceRPC's call_rpc callers loop. A dangling `call_rpc` edge is
// injected via FK-off and the store is wrapped so GetNode fails for that node.
func TestTraceRPC_GetNodeError_CallRPCCaller(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
	})
	s := openTraceRPCStore(t, dir)

	rpcID := store.SymbolNodeID("auth.AuthService.Login")
	fakeCallerID := "sym:fake.Caller.callRPC"

	dbPath := filepath.Join(dir, ".librarian", "trace-rpc-test.db")
	rawDB, err := sql.Open("sqlite3", dbPath+"?_foreign_keys=0")
	if err != nil {
		t.Fatalf("open fk-off conn: %v", err)
	}
	defer rawDB.Close()
	if _, err := rawDB.Exec(
		`INSERT INTO graph_edges (from_node, to_node, kind, weight, metadata) VALUES (?, ?, ?, 1, '{}')`,
		fakeCallerID, rpcID, store.EdgeKindCallRPC,
	); err != nil {
		t.Fatalf("insert fake call_rpc edge: %v", err)
	}

	mock := &errGetNodeReader{
		storeReader: s,
		errIDs:      map[string]error{fakeCallerID: errors.New("simulated I/O error")},
	}

	result, err := runTraceRPC(mock, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}

	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, fakeCallerID) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning for GetNode I/O error on %q; got %v", fakeCallerID, result.Warnings)
	}
}

// TestTraceRPC_CallRPCCallersSurfaced pins the lib-4g2.3 integration:
// trace_rpc returns a Next.js caller in the Callers list when a call_rpc
// edge exists on the proto rpc node. This guards the wiring in runTraceRPC
// that queries call_rpc edges in addition to the transitive "call" BFS.
func TestTraceRPC_CallRPCCallersSurfaced(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest { string user = 1; }
message LoginReply { bool ok = 1; }
`,
		"gen/ts/auth_connect.ts": `import { MethodKind } from "@bufbuild/protobuf";
export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: {
    login: { name: "Login", kind: MethodKind.Unary },
  },
} as const;
`,
		"app/page.tsx": `import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "../../gen/ts/auth_connect";

const transport = {};

export default function Page() {
  const client = createPromiseClient(AuthService, transport);
  return client.login({ user: "alice" });
}
`,
	})

	s := openTraceRPCStore(t, dir)

	result, err := runTraceRPC(s, dir, "auth.v1.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}

	// call_rpc edges target the proto rpc node (PascalCase).
	rpcID := store.SymbolNodeID("auth.v1.AuthService.Login")
	edges, err := s.Neighbors(rpcID, "in", store.EdgeKindCallRPC)
	if err != nil {
		t.Fatalf("Neighbors(call_rpc): %v", err)
	}
	if len(edges) == 0 {
		t.Fatalf("expected call_rpc edge into auth.v1.AuthService.Login; none found (check callsites_rpc.go)")
	}

	// trace_rpc must surface exactly one call_rpc caller in result.Callers.
	if len(result.Callers) != 1 {
		t.Fatalf("runTraceRPC Callers = %d, want exactly 1 (page.Page via call_rpc); got %+v", len(result.Callers), result.Callers)
	}
	c := result.Callers[0]
	if c.SymbolPath != "page.Page" {
		t.Errorf("Callers[0].SymbolPath = %q, want %q", c.SymbolPath, "page.Page")
	}
	if c.Language != "ts" {
		t.Errorf("Callers[0].Language = %q, want %q", c.Language, "ts")
	}
	if c.Depth != 1 {
		t.Errorf("Callers[0].Depth = %d, want 1 (direct call_rpc caller)", c.Depth)
	}
	// CallersNote must be empty when callers are present.
	if result.CallersNote != "" {
		t.Errorf("CallersNote should be empty when callers present; got %q", result.CallersNote)
	}
}

// TestTraceRPC_FieldTypeMetadata verifies that scalar, repeated, and map field
// type metadata emitted by the proto grammar's protoFieldMetadata flows through
// graph_nodes.metadata and surfaces in the trace_rpc response's Fields slice.
func TestTraceRPC_FieldTypeMetadata(t *testing.T) {
	const fieldTypeProto = `syntax = "proto3";
package ftype;

message TypedRequest {
  string user = 1;
  repeated string roles = 2;
  map<string, int32> labels = 3;
}
message TypedReply {
  bool ok = 1;
}

service TypedSvc {
  rpc TypedRPC (TypedRequest) returns (TypedReply);
}
`
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/ftype.proto": fieldTypeProto,
	})
	s := openTraceRPCStore(t, dir)

	result, err := runTraceRPC(s, dir, "ftype.TypedSvc.TypedRPC")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	if result.InputMessage == nil || !result.InputMessage.Resolved {
		t.Fatalf("InputMessage not resolved; note=%q", func() string {
			if result.InputMessage != nil {
				return result.InputMessage.Note
			}
			return "nil"
		}())
	}

	byName := map[string]traceRPCField{}
	for _, f := range result.InputMessage.Fields {
		byName[f.Name] = f
	}

	// scalar string field
	user, ok := byName["user"]
	if !ok {
		t.Fatalf("field 'user' missing from InputMessage.Fields; got %+v", result.InputMessage.Fields)
	}
	if user.Type != "string" {
		t.Errorf("user.Type = %q, want string", user.Type)
	}
	if user.Repeated {
		t.Errorf("user.Repeated = true, want false")
	}

	// repeated string field
	roles, ok := byName["roles"]
	if !ok {
		t.Fatalf("field 'roles' missing from InputMessage.Fields")
	}
	if roles.Type != "string" {
		t.Errorf("roles.Type = %q, want string", roles.Type)
	}
	if !roles.Repeated {
		t.Errorf("roles.Repeated = false, want true")
	}

	// map field
	labels, ok := byName["labels"]
	if !ok {
		t.Fatalf("field 'labels' missing from InputMessage.Fields")
	}
	if labels.MapKeyType != "string" {
		t.Errorf("labels.MapKeyType = %q, want string", labels.MapKeyType)
	}
	if labels.MapValueType != "int32" {
		t.Errorf("labels.MapValueType = %q, want int32", labels.MapValueType)
	}
	if labels.Type != "" {
		t.Errorf("labels.Type = %q, want empty for map_field", labels.Type)
	}
}

// TestTraceRPC_CallerLineNumber_FastPath_CallRPC pins that when a call_rpc
// caller node has LineNumber > 0 (fresh index after lib-r4s.5), the
// persisted value is used directly — scanTraceRPCMethodLine is NOT called.
// We verify by UpsertNode-ing the caller with a sentinel LineNumber (9999)
// that the file scan could never produce for `function Page()` on line ~8.
// If the fast path is bypassed, the result would be a real file-scanned
// value (≠ 9999), causing the assertion to fail.
func TestTraceRPC_CallerLineNumber_FastPath_CallRPC(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest { string user = 1; }
message LoginReply { bool ok = 1; }
`,
		"gen/ts/auth_connect.ts": `import { MethodKind } from "@bufbuild/protobuf";
export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: {
    login: { name: "Login", kind: MethodKind.Unary },
  },
} as const;
`,
		"app/page.tsx": `import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "../../gen/ts/auth_connect";

const transport = {};

export default function Page() {
  const client = createPromiseClient(AuthService, transport);
  return client.login({ user: "alice" });
}
`,
	})

	s := openTraceRPCStore(t, dir)

	// Override the caller node's LineNumber with a sentinel (9999) that the
	// file scan would never produce — the real `function Page` is at line ~8.
	callerID := store.SymbolNodeID("page.Page")
	callerNode, err := s.GetNode(callerID)
	if err != nil || callerNode == nil {
		t.Fatalf("caller node %s not indexed; err=%v node=%v", callerID, err, callerNode)
	}
	callerNode.LineNumber = 9999
	if err := s.UpsertNode(*callerNode); err != nil {
		t.Fatalf("UpsertNode sentinel: %v", err)
	}

	result, err := runTraceRPC(s, dir, "auth.v1.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	if len(result.Callers) != 1 {
		t.Fatalf("Callers = %d, want 1; got %+v", len(result.Callers), result.Callers)
	}
	if result.Callers[0].LineNumber != 9999 {
		t.Errorf("call_rpc fast path: Callers[0].LineNumber = %d, want 9999 (sentinel from node, not file-scanned value)", result.Callers[0].LineNumber)
	}
}

// TestTraceRPC_CallerLineNumber_FastPath_BFS pins that when a BFS `call`
// caller node has LineNumber > 0, walkTraceRPCCallers uses the persisted
// value rather than calling scanTraceRPCMethodLine. Uses the same sentinel
// strategy: set LineNumber=8888 on the caller node after indexing and verify
// the result carries 8888 (not the file-scanned line of `func callIt()`).
func TestTraceRPC_CallerLineNumber_FastPath_BFS(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		"gen/go/auth/service.go": `package auth
type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
`,
		"cmd/main.go": `package main
func callIt() {}
`,
	})
	s := openTraceRPCStore(t, dir)

	// Inject the call edge that the BFS walker will traverse.
	implID := store.SymbolNodeID("auth.AuthServiceServer.Login")
	callerID := store.SymbolNodeID("main.callIt")
	if err := s.UpsertEdge(store.Edge{From: callerID, To: implID, Kind: "call"}); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}

	// Override the caller node's LineNumber with a sentinel (8888) that the
	// file scan would never produce — `func callIt()` is on line 2.
	callerNode, err := s.GetNode(callerID)
	if err != nil || callerNode == nil {
		t.Fatalf("caller node %s not indexed; err=%v node=%v", callerID, err, callerNode)
	}
	callerNode.LineNumber = 8888
	if err := s.UpsertNode(*callerNode); err != nil {
		t.Fatalf("UpsertNode sentinel: %v", err)
	}

	result, err := runTraceRPC(s, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	if len(result.Callers) != 1 {
		t.Fatalf("Callers = %d, want 1; got %+v", len(result.Callers), result.Callers)
	}
	if result.Callers[0].LineNumber != 8888 {
		t.Errorf("BFS fast path: Callers[0].LineNumber = %d, want 8888 (sentinel from node, not file-scanned value)", result.Callers[0].LineNumber)
	}
}

// TestTraceRPC_CallerLineNumber_LegacyFallback_BFS pins that when a BFS
// caller node has LineNumber == 0 (legacy pre-lib-r4s.5 node), the file scan
// fires and produces a non-zero result. Complements the fast-path test above
// by verifying the else branch.
func TestTraceRPC_CallerLineNumber_LegacyFallback_BFS(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": crossLanguageProto,
		"gen/go/auth/service.go": `package auth
type AuthServiceServer struct{}
func (s *AuthServiceServer) Login() error { return nil }
`,
		"cmd/main.go": `package main
func callIt() {}
`,
	})
	s := openTraceRPCStore(t, dir)

	implID := store.SymbolNodeID("auth.AuthServiceServer.Login")
	callerID := store.SymbolNodeID("main.callIt")
	if err := s.UpsertEdge(store.Edge{From: callerID, To: implID, Kind: "call"}); err != nil {
		t.Fatalf("UpsertEdge: %v", err)
	}

	// Force LineNumber = 0 to simulate a legacy node that hasn't been re-indexed.
	callerNode, err := s.GetNode(callerID)
	if err != nil || callerNode == nil {
		t.Fatalf("caller node %s not indexed; err=%v node=%v", callerID, err, callerNode)
	}
	callerNode.LineNumber = 0
	if err := s.UpsertNode(*callerNode); err != nil {
		t.Fatalf("UpsertNode zero: %v", err)
	}

	result, err := runTraceRPC(s, dir, "auth.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	if len(result.Callers) != 1 {
		t.Fatalf("Callers = %d, want 1; got %+v", len(result.Callers), result.Callers)
	}
	// File scan must have fired and found `func callIt()` at a non-zero line.
	if result.Callers[0].LineNumber == 0 {
		t.Errorf("legacy fallback: Callers[0].LineNumber = 0, expected non-zero (file scan must fire when LineNumber==0)")
	}
}

// TestTraceRPC_CallerLineNumber_LegacyFallback_CallRPC mirrors the BFS
// legacy-fallback test for the call_rpc site: when the caller node has
// LineNumber == 0, the file scan branch fires and returns a non-zero line.
// Symmetry pair for TestTraceRPC_CallerLineNumber_FastPath_CallRPC.
func TestTraceRPC_CallerLineNumber_LegacyFallback_CallRPC(t *testing.T) {
	dir := t.TempDir()
	writeTraceRPCFixture(t, dir, map[string]string{
		"api/auth.proto": `syntax = "proto3";
package auth.v1;
service AuthService {
  rpc Login (LoginRequest) returns (LoginReply);
}
message LoginRequest { string user = 1; }
message LoginReply { bool ok = 1; }
`,
		"gen/ts/auth_connect.ts": `import { MethodKind } from "@bufbuild/protobuf";
export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: {
    login: { name: "Login", kind: MethodKind.Unary },
  },
} as const;
`,
		"app/page.tsx": `import { createPromiseClient } from "@connectrpc/connect";
import { AuthService } from "../../gen/ts/auth_connect";

const transport = {};

export default function Page() {
  const client = createPromiseClient(AuthService, transport);
  return client.login({ user: "alice" });
}
`,
	})

	s := openTraceRPCStore(t, dir)

	// Force LineNumber = 0 to simulate a legacy node that hasn't been re-indexed.
	callerID := store.SymbolNodeID("page.Page")
	callerNode, err := s.GetNode(callerID)
	if err != nil || callerNode == nil {
		t.Fatalf("caller node %s not indexed; err=%v node=%v", callerID, err, callerNode)
	}
	callerNode.LineNumber = 0
	if err := s.UpsertNode(*callerNode); err != nil {
		t.Fatalf("UpsertNode zero: %v", err)
	}

	result, err := runTraceRPC(s, dir, "auth.v1.AuthService.Login")
	if err != nil {
		t.Fatalf("runTraceRPC: %v", err)
	}
	if len(result.Callers) != 1 {
		t.Fatalf("Callers = %d, want 1; got %+v", len(result.Callers), result.Callers)
	}
	// File scan must have fired for `function Page()` and returned a non-zero line.
	if result.Callers[0].LineNumber == 0 {
		t.Errorf("call_rpc legacy fallback: Callers[0].LineNumber = 0, expected non-zero (file scan must fire when LineNumber==0)")
	}
}

