package connectes_test

import (
	"testing"

	"librarian/internal/indexer"
	"librarian/internal/indexer/handlers/code/connectes"
)

// TestDetect_GeneratedConnectEsFile verifies that realistic generated _connect.ts
// shapes are detected as connect-es files (i.e. parse returns units).
func TestDetect_GeneratedConnectEsFile(t *testing.T) {
	h := connectes.New()

	cases := []struct {
		name    string
		path    string
		content string
	}{
		{
			name: "single service unary",
			path: "gen/ts/api/auth_connect.ts",
			content: `import { MethodKind } from "@bufbuild/protobuf";
import { LoginRequest, LoginReply } from "./auth_pb.js";

export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: {
    login: {
      name: "Login",
      kind: MethodKind.Unary,
      I: LoginRequest,
      O: LoginReply,
    },
  },
} as const;
`,
		},
		{
			name: "multiple methods with streaming",
			path: "gen/ts/chat_connect.ts",
			content: `import { MethodKind } from "@bufbuild/protobuf";

export const ChatService = {
  typeName: "chat.v1.ChatService",
  methods: {
    send: {
      name: "Send",
      kind: MethodKind.ServerStreaming,
    },
    receive: {
      name: "Receive",
      kind: MethodKind.BiDiStreaming,
    },
  },
} as const;
`,
		},
		{
			name: "connectweb suffix variant",
			path: "gen/ts/auth_connectweb.ts",
			content: `export const FooService = {
  typeName: "foo.v1.FooService",
  methods: {
    doThing: {
      name: "DoThing",
    },
  },
} as const;
`,
		},
		{
			name: "nested namespace typeName",
			path: "gen/ts/api/v2/user_connect.ts",
			content: `export const UserService = {
  typeName: "com.example.api.v2.UserService",
  methods: {
    createUser: {},
    getUser: {},
  },
} as const;
`,
		},
		{
			name: "comment-heavy file",
			path: "gen/ts/svc_connect.ts",
			content: `// This file is generated. Do not edit.

export const SomeService = {
  // The proto type name
  typeName: "pkg.SomeService",
  methods: {
    // Login RPC
    login: {
      name: "Login",
    },
  },
} as const;
`,
		},
		{
			name: "plain js connect file",
			path: "gen/js/auth_connect.js",
			content: `export const AuthService = {
  typeName: "auth.AuthService",
  methods: {
    login: {
      name: "Login",
    },
  },
};
`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			doc, err := h.Parse(c.path, []byte(c.content))
			if err != nil {
				t.Fatalf("Parse error: %v", err)
			}
			if len(doc.Units) == 0 {
				t.Errorf("expected Units (detect=true) for path=%s but got none", c.path)
			}
		})
	}
}

// TestDetect_HandWrittenConnectLookalike verifies that a file named foo_connect.ts
// with no typeName property does NOT trigger connect-es detection.
func TestDetect_HandWrittenConnectLookalike(t *testing.T) {
	h := connectes.New()

	content := `export const FooService = {
  login: () => {},
  logout: () => {},
} as const;
`
	doc, err := h.Parse("src/foo_connect.ts", []byte(content))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(doc.Units) != 0 {
		t.Errorf("expected no Units for hand-written lookalike, got %d: %+v", len(doc.Units), doc.Units)
	}
}

// TestDetect_NonConnectFile verifies that a regular .ts file (no connect suffix)
// produces no connect-es units even if it contains "typeName".
func TestDetect_NonConnectFile(t *testing.T) {
	h := connectes.New()

	content := `export const Config = {
  typeName: "app.Config",
  value: "hello",
};
`
	doc, err := h.Parse("src/config.ts", []byte(content))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	if len(doc.Units) != 0 {
		t.Errorf("expected no Units for non-connect file, got %d", len(doc.Units))
	}
}

// TestExtract_ServiceDefinition verifies that a single service export produces
// the correct Units with expected paths, titles, and metadata.
func TestExtract_ServiceDefinition(t *testing.T) {
	h := connectes.New()

	content := `import { MethodKind } from "@bufbuild/protobuf";

export const AuthService = {
  typeName: "auth.v1.AuthService",
  methods: {
    login: {
      name: "Login",
      kind: MethodKind.Unary,
    },
    logout: {
      name: "Logout",
      kind: MethodKind.Unary,
    },
  },
} as const;
`
	doc, err := h.Parse("gen/ts/auth_connect.ts", []byte(content))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if len(doc.Units) != 2 {
		t.Fatalf("expected 2 units, got %d: %+v", len(doc.Units), doc.Units)
	}

	for _, u := range doc.Units {
		if u.Kind != "method" {
			t.Errorf("unit %q: Kind = %q, want \"method\"", u.Title, u.Kind)
		}
		wantPath := "auth.v1.AuthService." + u.Title
		if u.Path != wantPath {
			t.Errorf("unit %q: Path = %q, want %q", u.Title, u.Path, wantPath)
		}
		if u.Metadata == nil {
			t.Errorf("unit %q: Metadata is nil", u.Title)
			continue
		}
		if stub, _ := u.Metadata["connect_es_stub"].(bool); !stub {
			t.Errorf("unit %q: connect_es_stub = %v, want true", u.Title, u.Metadata["connect_es_stub"])
		}
		if svc, _ := u.Metadata["service_typename"].(string); svc != "auth.v1.AuthService" {
			t.Errorf("unit %q: service_typename = %q, want %q", u.Title, svc, "auth.v1.AuthService")
		}
		if mk, _ := u.Metadata["method_key"].(string); mk != u.Title {
			t.Errorf("unit %q: method_key = %q, want %q", u.Title, mk, u.Title)
		}
		if sk, _ := u.Metadata["streaming_kind"].(string); sk != "Unary" {
			t.Errorf("unit %q: streaming_kind = %q, want %q", u.Title, sk, "Unary")
		}
	}

	// Verify both expected method keys are present.
	titles := map[string]bool{}
	for _, u := range doc.Units {
		titles[u.Title] = true
	}
	for _, want := range []string{"login", "logout"} {
		if !titles[want] {
			t.Errorf("missing unit with title %q", want)
		}
	}
}

// TestExtract_MultipleServicesPerFile verifies that a file with two service exports
// produces units for both services.
func TestExtract_MultipleServicesPerFile(t *testing.T) {
	h := connectes.New()

	content := `export const FooService = {
  typeName: "pkg.FooService",
  methods: {
    doFoo: {
      name: "DoFoo",
    },
  },
} as const;

export const BarService = {
  typeName: "pkg.BarService",
  methods: {
    doBar: {
      name: "DoBar",
    },
    doBarBaz: {
      name: "DoBarBaz",
    },
  },
} as const;
`
	doc, err := h.Parse("gen/ts/multi_connect.ts", []byte(content))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if len(doc.Units) != 3 {
		t.Fatalf("expected 3 units (1 from FooService + 2 from BarService), got %d: %+v", len(doc.Units), doc.Units)
	}

	wantPaths := map[string]bool{
		"pkg.FooService.doFoo":    false,
		"pkg.BarService.doBar":    false,
		"pkg.BarService.doBarBaz": false,
	}
	for _, u := range doc.Units {
		if _, ok := wantPaths[u.Path]; !ok {
			t.Errorf("unexpected unit path %q", u.Path)
			continue
		}
		wantPaths[u.Path] = true
	}
	for path, seen := range wantPaths {
		if !seen {
			t.Errorf("missing unit for path %q", path)
		}
	}
}

// TestExtract_StreamingKinds verifies extraction of different MethodKind variants.
func TestExtract_StreamingKinds(t *testing.T) {
	h := connectes.New()

	content := `import { MethodKind } from "@bufbuild/protobuf";

export const StreamService = {
  typeName: "stream.v1.StreamService",
  methods: {
    serverStream: { kind: MethodKind.ServerStreaming },
    clientStream: { kind: MethodKind.ClientStreaming },
    bidi:         { kind: MethodKind.BiDiStreaming },
    unary:        { kind: MethodKind.Unary },
    noKind:       {},
  },
} as const;
`
	doc, err := h.Parse("gen/ts/stream_connect.ts", []byte(content))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if len(doc.Units) != 5 {
		t.Fatalf("expected 5 units, got %d", len(doc.Units))
	}

	wantKinds := map[string]string{
		"serverStream": "ServerStreaming",
		"clientStream": "ClientStreaming",
		"bidi":         "BiDiStreaming",
		"unary":        "Unary",
		"noKind":       "",
	}
	for _, u := range doc.Units {
		want, ok := wantKinds[u.Title]
		if !ok {
			t.Errorf("unexpected unit title %q", u.Title)
			continue
		}
		got, _ := u.Metadata["streaming_kind"].(string)
		if got != want {
			t.Errorf("unit %q: streaming_kind = %q, want %q", u.Title, got, want)
		}
	}
}

// TestChunk_ReturnsEmpty verifies that Chunk always returns no chunks — connect-es
// symbols are graph-only and do not participate in the embedding store.
func TestChunk_ReturnsEmpty(t *testing.T) {
	h := connectes.New()
	content := `export const S = { typeName: "x.S", methods: { doIt: {} } } as const;`
	doc, err := h.Parse("gen/s_connect.ts", []byte(content))
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	chunks, err := h.Chunk(doc, indexer.ChunkOpts{})
	if err != nil {
		t.Fatalf("Chunk error: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected no chunks from connect-es handler, got %d", len(chunks))
	}
}
