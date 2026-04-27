package mcpserver

import (
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

// TestMCPStableSchema guards the stable parameter set for every registered
// tool. If a stable parameter is accidentally renamed, removed, or its JSON
// Schema type changed, this test fails — giving downstream MCP consumers
// (skills, extensions, slash commands) an early warning before the breakage
// reaches them.
//
// "Stable" here means the field name and type are locked until a major-version
// bump (v2). The classification for each field is documented in docs/mcp-tools.md.
func TestMCPStableSchema(t *testing.T) {
	srv := server.NewMCPServer("librarian", "0.2.0")

	// Register all tools with nil deps — registration only captures deps in
	// handler closures; the closures are never invoked in this test.
	registerSearchDocs(srv, nil, nil, false)
	registerExpandChunks(srv, nil)
	registerGetDocument(srv, nil, nil)
	registerGetContext(srv, nil, nil, false)
	registerListDocuments(srv, nil)
	registerUpdateDocs(srv, nil, nil, nil)
	registerCaptureSession(srv, nil, nil, nil)
	registerTraceRPC(srv, nil, nil)

	tools := srv.ListTools()

	// stableFields maps each tool to its STABLE parameters with expected JSON
	// Schema type strings. Both name and type are locked until v2.
	type param struct {
		name     string
		wantType string
	}
	stableFields := map[string][]param{
		"search_docs": {
			{"query", "string"},
			{"limit", "number"},
			{"include_refs", "boolean"},
			{"include_body", "boolean"},
			{"budget", "number"},
		},
		"expand_chunks": {
			{"ids", "array"},
		},
		"get_context": {
			{"query", "string"},
			{"limit", "number"},
		},
		"get_document": {
			{"file_path", "string"},
		},
		"list_documents": {
			{"doc_type", "string"},
		},
		"update_docs": {
			{"file_path", "string"},
			{"content", "string"},
			{"reindex", "string"},
		},
		"capture_session": {
			{"title", "string"},
			{"body", "string"},
			{"category", "string"},
		},
		"trace_rpc": {
			{"rpc", "string"},
			{"format", "string"},
		},
	}

	for toolName, params := range stableFields {
		st, ok := tools[toolName]
		if !ok {
			t.Errorf("stable tool %q missing from tools/list", toolName)
			continue
		}
		for _, p := range params {
			raw, exists := st.Tool.InputSchema.Properties[p.name]
			if !exists {
				t.Errorf("tool %q: stable parameter %q missing from InputSchema (docs/mcp-tools.md marks this STABLE)", toolName, p.name)
				continue
			}
			// InputSchema.Properties values are map[string]any from mcp-go's JSON Schema representation.
			propMap, ok := raw.(map[string]any)
			if !ok {
				t.Errorf("tool %q: parameter %q schema is not a map (got %T)", toolName, p.name, raw)
				continue
			}
			gotType, _ := propMap["type"].(string)
			if gotType != p.wantType {
				t.Errorf("tool %q: stable parameter %q type changed: want %q got %q (type change is a breaking API change)", toolName, p.name, p.wantType, gotType)
			}
		}
	}
}
