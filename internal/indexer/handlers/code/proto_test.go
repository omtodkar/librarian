package code_test

import (
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code"
)

const protoSample = `syntax = "proto3";
package foo.v1;

option go_package = "github.com/example/v1;examplev1";
option java_package = "com.example.v1";
option (default_timeout_ms) = 5000;

import "google/protobuf/timestamp.proto";
import public "public_dep.proto";
import weak "weak_dep.proto";

// Greeter provides RPCs.
service Greeter {
  // SayHello says hi.
  rpc SayHello (HelloRequest) returns (HelloReply);
  rpc StreamServer (HelloRequest) returns (stream HelloReply);
  rpc StreamClient (stream HelloRequest) returns (HelloReply);
  rpc StreamBoth (stream HelloRequest) returns (stream HelloReply);
}

// HelloRequest is a request.
message HelloRequest {
  string name = 1;
  repeated string aliases = 2;
  optional int32 age = 3 [deprecated = true];
  map<string, Metadata> tags = 4;

  message Nested {
    string inner = 1;
  }

  enum Priority {
    LOW = 0;
    HIGH = 1;
  }

  oneof payload {
    string text = 6;
    int64 number = 7;
  }
}

enum Status {
  STATUS_UNSPECIFIED = 0;
  STATUS_OK = 1;
}

extend Foo {
  string extra_field = 100;
}
`

// Core: every declaration kind + name is captured as a Unit with the right
// Title and Kind.
func TestProtoGrammar_ParseExtractsDeclarationKinds(t *testing.T) {
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("api/greeter.proto", []byte(protoSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Format != "proto" {
		t.Errorf("Format = %q, want %q", doc.Format, "proto")
	}
	if doc.Title != "foo.v1" {
		t.Errorf("Title = %q, want %q (from package directive)", doc.Title, "foo.v1")
	}

	type tk struct{ title, kind string }
	got := map[tk]bool{}
	for _, u := range doc.Units {
		got[tk{u.Title, u.Kind}] = true
	}
	for _, w := range []tk{
		{"Greeter", "service"},
		{"SayHello", "rpc"},
		{"StreamServer", "rpc"},
		{"StreamClient", "rpc"},
		{"StreamBoth", "rpc"},
		{"HelloRequest", "message"},
		{"name", "field"},
		{"aliases", "field"},
		{"age", "field"},
		{"tags", "field"},
		{"Nested", "message"},
		{"inner", "field"},
		{"Priority", "enum"},
		{"payload", "oneof"},
		{"text", "field"},
		{"number", "field"},
		{"Status", "enum"},
	} {
		if !got[w] {
			t.Errorf("missing Unit {title=%q, kind=%q}", w.title, w.kind)
		}
	}
}

// PackageName surfaces as ParsedDoc.Title via the shared walker's fallback.
func TestProtoGrammar_PackageNameFromDirective(t *testing.T) {
	src := []byte(`syntax = "proto3";
package foo.bar.baz;
message X {}
`)
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("x.proto", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Title != "foo.bar.baz" {
		t.Errorf("Title = %q, want %q", doc.Title, "foo.bar.baz")
	}
}

// No package → file stem fallback.
func TestProtoGrammar_NoPackageFallsBackToStem(t *testing.T) {
	src := []byte(`syntax = "proto3";
message X {}
`)
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("api/greeter.proto", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if doc.Title != "greeter" {
		t.Errorf("Title = %q, want %q (stem fallback)", doc.Title, "greeter")
	}
}

// Imports: three forms surface as Reference.Kind="import". public/weak
// markers land in Metadata.import_kind.
func TestProtoGrammar_ImportsIncludePublicWeakMarkers(t *testing.T) {
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("api/greeter.proto", []byte(protoSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	byPath := map[string]indexer.Reference{}
	for _, r := range doc.Refs {
		if r.Kind == "import" {
			byPath[r.Target] = r
		}
	}
	for _, tc := range []struct {
		path, importKind string
	}{
		{"google/protobuf/timestamp.proto", ""},
		{"public_dep.proto", "public"},
		{"weak_dep.proto", "weak"},
	} {
		ref, ok := byPath[tc.path]
		if !ok {
			t.Errorf("missing import ref for %q", tc.path)
			continue
		}
		got, _ := ref.Metadata["import_kind"].(string)
		if got != tc.importKind {
			t.Errorf("import %q: import_kind = %q, want %q", tc.path, got, tc.importKind)
		}
	}
}

// RPC: each streaming flavor sets the right pair of streaming booleans AND
// populates input_type / output_type Metadata.
func TestProtoGrammar_RPCMetadataStreamingAndTypes(t *testing.T) {
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("api/greeter.proto", []byte(protoSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	cases := []struct {
		rpc                      string
		clientStream             bool
		serverStream             bool
		inputType, outputType    string
	}{
		{"SayHello", false, false, "HelloRequest", "HelloReply"},
		{"StreamServer", false, true, "HelloRequest", "HelloReply"},
		{"StreamClient", true, false, "HelloRequest", "HelloReply"},
		{"StreamBoth", true, true, "HelloRequest", "HelloReply"},
	}
	for _, tc := range cases {
		u := findUnit(doc, tc.rpc)
		if u == nil {
			t.Errorf("rpc %s Unit missing", tc.rpc)
			continue
		}
		if u.Kind != "rpc" {
			t.Errorf("%s Kind = %q, want rpc", tc.rpc, u.Kind)
		}
		if got, _ := u.Metadata["input_type"].(string); got != tc.inputType {
			t.Errorf("%s input_type = %q, want %q", tc.rpc, got, tc.inputType)
		}
		if got, _ := u.Metadata["output_type"].(string); got != tc.outputType {
			t.Errorf("%s output_type = %q, want %q", tc.rpc, got, tc.outputType)
		}
		if got, _ := u.Metadata["client_streaming"].(bool); got != tc.clientStream {
			t.Errorf("%s client_streaming = %v, want %v", tc.rpc, got, tc.clientStream)
		}
		if got, _ := u.Metadata["server_streaming"].(bool); got != tc.serverStream {
			t.Errorf("%s server_streaming = %v, want %v", tc.rpc, got, tc.serverStream)
		}
	}
}

// Field metadata: field_number lands as an int; oneof children also carry
// Metadata["oneof"] pointing at the enclosing oneof name.
func TestProtoGrammar_FieldMetadataNumberAndOneof(t *testing.T) {
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("api/greeter.proto", []byte(protoSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Regular field
	u := findByPath(doc, "foo.v1.HelloRequest.name")
	if u == nil {
		t.Fatalf("field name Unit missing")
	}
	if got, _ := u.Metadata["field_number"].(int); got != 1 {
		t.Errorf("name.field_number = %v, want 1", u.Metadata["field_number"])
	}

	// Map field
	u = findByPath(doc, "foo.v1.HelloRequest.tags")
	if u == nil {
		t.Fatalf("field tags Unit missing")
	}
	if got, _ := u.Metadata["field_number"].(int); got != 4 {
		t.Errorf("tags.field_number = %v, want 4", u.Metadata["field_number"])
	}

	// Both oneof members must carry the enclosing oneof's name plus
	// their own field_number — a regression in ordering or metadata
	// propagation would show up as either a swapped number or a missing
	// oneof key on the second sibling.
	for _, tc := range []struct {
		path string
		num  int
	}{
		{"foo.v1.HelloRequest.payload.text", 6},
		{"foo.v1.HelloRequest.payload.number", 7},
	} {
		u = findByPath(doc, tc.path)
		if u == nil {
			t.Fatalf("oneof field Unit missing: %s", tc.path)
		}
		if got, _ := u.Metadata["field_number"].(int); got != tc.num {
			t.Errorf("%s.field_number = %v, want %d", tc.path, u.Metadata["field_number"], tc.num)
		}
		if got, _ := u.Metadata["oneof"].(string); got != "payload" {
			t.Errorf("%s.oneof = %q, want payload", tc.path, got)
		}
	}
}

// Map fields with primitive value types must still pick the right identifier
// for Unit.Title — the key/value type tokens sit one level deeper than
// NamedChild iteration, so no built-in keyword can ever be misread as the
// field name.
func TestProtoGrammar_MapFieldPrimitiveValueType(t *testing.T) {
	src := []byte(`syntax = "proto3";
package mp;

message Foo {
  map<string, string> labels = 1;
  map<int32, int64> scores = 2;
}
`)
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("mp.proto", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, tc := range []struct {
		path string
		num  int
	}{
		{"mp.Foo.labels", 1},
		{"mp.Foo.scores", 2},
	} {
		u := findByPath(doc, tc.path)
		if u == nil {
			t.Fatalf("map_field Unit missing: %s", tc.path)
		}
		if u.Kind != "field" {
			t.Errorf("%s Kind = %q, want field", tc.path, u.Kind)
		}
		if got, _ := u.Metadata["field_number"].(int); got != tc.num {
			t.Errorf("%s.field_number = %v, want %d", tc.path, u.Metadata["field_number"], tc.num)
		}
	}
}

// Deprecated label: `[deprecated = true]` surfaces as Signal{kind=label}.
func TestProtoGrammar_DeprecatedFieldEmitsLabel(t *testing.T) {
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("api/greeter.proto", []byte(protoSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	u := findByPath(doc, "foo.v1.HelloRequest.age")
	if u == nil {
		t.Fatalf("age Unit missing")
	}
	if !hasSignal(u.Signals, "label", "deprecated") {
		t.Errorf("expected deprecated label signal on age; got %+v", u.Signals)
	}
	// The non-deprecated sibling MUST NOT carry the label.
	u = findByPath(doc, "foo.v1.HelloRequest.name")
	if u == nil {
		t.Fatalf("name Unit missing")
	}
	if hasSignal(u.Signals, "label", "deprecated") {
		t.Errorf("name should not carry deprecated label; got %+v", u.Signals)
	}
}

// `extend Foo { ... }` emits Reference.Kind="inherits" Metadata.relation="extends".
func TestProtoGrammar_ExtendEmitsInheritsRef(t *testing.T) {
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("api/greeter.proto", []byte(protoSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var found []indexer.Reference
	for _, r := range doc.Refs {
		if r.Kind == "inherits" {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 inherits ref (for `extend Foo`), got %d: %+v", len(found), found)
	}
	if found[0].Target != "Foo" {
		t.Errorf("inherits Target = %q, want Foo", found[0].Target)
	}
	if rel, _ := found[0].Metadata["relation"].(string); rel != "extends" {
		t.Errorf("inherits relation = %q, want extends", rel)
	}
}

// proto2 lets `extend Foo { ... }` sit inside a message body. The handler's
// Imports hook must walk the subtree (not just root children) so nested
// extends also emit an inherits Reference.
func TestProtoGrammar_NestedExtendInsideMessageEmitsInheritsRef(t *testing.T) {
	src := []byte(`syntax = "proto2";
package nx;

message Outer {
  optional int32 id = 1;
  extend google.protobuf.FieldOptions {
    optional string tag = 50000;
  }
}
`)
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("nx.proto", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var inherits []indexer.Reference
	for _, r := range doc.Refs {
		if r.Kind == "inherits" {
			inherits = append(inherits, r)
		}
	}
	if len(inherits) != 1 {
		t.Fatalf("expected 1 inherits ref for nested extend, got %d: %+v", len(inherits), inherits)
	}
	if inherits[0].Target != "google.protobuf.FieldOptions" {
		t.Errorf("inherits Target = %q, want google.protobuf.FieldOptions", inherits[0].Target)
	}
	if rel, _ := inherits[0].Metadata["relation"].(string); rel != "extends" {
		t.Errorf("inherits relation = %q, want extends", rel)
	}
}

// File-level `option go_package` / `option java_package` / etc. land on
// ParsedDoc.Metadata["options"]. Custom options (with parens) stay out —
// they feed runtime behaviour, not package routing.
func TestProtoGrammar_FileOptionsExtracted(t *testing.T) {
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("api/greeter.proto", []byte(protoSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	opts, _ := doc.Metadata["options"].(map[string]string)
	if opts == nil {
		t.Fatalf("no options map on ParsedDoc.Metadata; got %+v", doc.Metadata)
	}
	if got := opts["go_package"]; got != "github.com/example/v1;examplev1" {
		t.Errorf("go_package = %q, want github.com/example/v1;examplev1", got)
	}
	if got := opts["java_package"]; got != "com.example.v1" {
		t.Errorf("java_package = %q, want com.example.v1", got)
	}
	if _, has := opts["default_timeout_ms"]; has {
		t.Errorf("custom option (default_timeout_ms) leaked into file options: %+v", opts)
	}
	// Pin the exact cardinality so a custom option that loses its parens
	// in a future grammar update (and thus shows up as a simple identifier
	// LHS) would force this test to fail loudly rather than slip through.
	if len(opts) != 2 {
		t.Errorf("expected exactly 2 file options (go_package + java_package), got %d: %+v", len(opts), opts)
	}
}

// proto2 variance: `optional` / `required` fields still emit Units with
// field_number Metadata. `group` projects as an inline message Unit.
func TestProtoGrammar_Proto2VarianceAndGroups(t *testing.T) {
	src := []byte(`syntax = "proto2";
package g;

message Foo {
  required int32 id = 1;
  optional string name = 2;
  optional group Result = 3 {
    optional int32 code = 1;
    optional string message = 2;
  }
}
`)
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("g.proto", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Required / optional fields are still Units of Kind="field".
	if u := findByPath(doc, "g.Foo.id"); u == nil || u.Kind != "field" {
		t.Errorf("id field missing or wrong Kind: %+v", u)
	} else if got, _ := u.Metadata["field_number"].(int); got != 1 {
		t.Errorf("id.field_number = %v, want 1", u.Metadata["field_number"])
	}
	if u := findByPath(doc, "g.Foo.name"); u == nil || u.Kind != "field" {
		t.Errorf("name field missing or wrong Kind: %+v", u)
	}

	// Group projects as a message Unit.
	u := findByPath(doc, "g.Foo.Result")
	if u == nil {
		t.Fatalf("group Result Unit missing")
	}
	if u.Kind != "message" {
		t.Errorf("group Result Kind = %q, want message", u.Kind)
	}
	if got, _ := u.Metadata["field_number"].(int); got != 3 {
		t.Errorf("group field_number = %v, want 3", u.Metadata["field_number"])
	}
	// Fields inside the group emit as normal fields under the group's path.
	if u := findByPath(doc, "g.Foo.Result.code"); u == nil || u.Kind != "field" {
		t.Errorf("group-inner field code missing or wrong Kind: %+v", u)
	} else if got, _ := u.Metadata["field_number"].(int); got != 1 {
		t.Errorf("code.field_number = %v, want 1", u.Metadata["field_number"])
	}
}

// Shared structural invariants apply to every grammar — running them on
// the proto sample guards against Unit.Path / Kind / Title shape drift.
func TestProtoGrammar_SatisfiesGrammarInvariants(t *testing.T) {
	h := code.New(code.NewProtoGrammar())
	code.AssertGrammarInvariants(t, h, "api/greeter.proto", []byte(protoSample))
}

// Nested messages appear as distinct Units with hierarchical Paths.
func TestProtoGrammar_NestedMessagePaths(t *testing.T) {
	h := code.New(code.NewProtoGrammar())
	doc, err := h.Parse("api/greeter.proto", []byte(protoSample))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if u := findByPath(doc, "foo.v1.HelloRequest.Nested"); u == nil {
		t.Errorf("nested message Nested missing")
	}
	if u := findByPath(doc, "foo.v1.HelloRequest.Nested.inner"); u == nil {
		t.Errorf("nested message field inner missing")
	}
	if u := findByPath(doc, "foo.v1.HelloRequest.Priority"); u == nil {
		t.Errorf("nested enum Priority missing")
	}
}
