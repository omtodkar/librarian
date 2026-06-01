package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/config"
	_ "librarian/internal/indexer/handlers/defaults"
	"librarian/internal/store"
)

// captureEmbedder returns zero-vectors. Sufficient for wiring tests.
type captureEmbedder struct{ dim int }

func (f captureEmbedder) Embed(_ string) ([]float64, error) { return make([]float64, f.dim), nil }
func (f captureEmbedder) EmbedBatch(texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i := range texts {
		out[i] = make([]float64, f.dim)
	}
	return out, nil
}
func (f captureEmbedder) Model() string { return "fake-embedder" }

// setupCaptureEnv creates a temp workspace with an open store and config.
func setupCaptureEnv(t *testing.T) (*store.Store, *config.Config) {
	t.Helper()
	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(tmp, "test.db"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		DocsDir: docsDir,
		Chunking: config.ChunkingConfig{
			MaxTokens:    512,
			MinTokens:    10,
			OverlapLines: 0,
		},
	}
	return st, cfg
}

func callCaptureTool(t *testing.T, st *store.Store, cfg *config.Config, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	srv := server.NewMCPServer("librarian", "test")
	registerCaptureSession(srv, st, cfg, captureEmbedder{dim: 4})

	req := mcp.CallToolRequest{}
	req.Params.Name = "capture_session"
	req.Params.Arguments = args

	tools := srv.ListTools()
	tool, ok := tools["capture_session"]
	if !ok {
		t.Fatal("capture_session tool not registered")
	}

	result, err := tool.Handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

// TestCaptureSession_WritesFile verifies the file is created with the correct
// frontmatter and content.
func TestCaptureSession_WritesFile(t *testing.T) {
	st, cfg := setupCaptureEnv(t)

	result := callCaptureTool(t, st, cfg, map[string]any{
		"title":      "Auth Flow Deep Dive",
		"body":       "## Overview\n\nThe auth flow uses JWT tokens with short expiry times.",
		"category":   "decisions",
		"session_id": "sess-123",
		"author":     "Alice",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %v", toolResultText(t, result))
	}

	today := time.Now().Format("2006-01-02")
	expectedPath := filepath.Join(cfg.DocsDir, "decisions", today+"-auth-flow-deep-dive.md")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("file not written at %s: %v", expectedPath, err)
	}

	content := string(data)
	for _, want := range []string{
		"type: decisions",
		"source: ai-capture",
		`session_id: "sess-123"`,
		`author: "Alice"`,
		"date: " + today,
		"# Auth Flow Deep Dive",
		"JWT tokens with short expiry",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("file missing %q", want)
		}
	}
}

// TestCaptureSession_OptionalFieldsAbsent verifies that session_id and author
// are omitted from frontmatter when not supplied.
func TestCaptureSession_OptionalFieldsAbsent(t *testing.T) {
	st, cfg := setupCaptureEnv(t)

	result := callCaptureTool(t, st, cfg, map[string]any{
		"title": "Minimal Capture Test",
		"body":  "A minimal capture with no optional fields provided at all.",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %v", toolResultText(t, result))
	}

	today := time.Now().Format("2006-01-02")
	expectedPath := filepath.Join(cfg.DocsDir, "sessions", today+"-minimal-capture-test.md")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("file not written at %s: %v", expectedPath, err)
	}

	content := string(data)
	if strings.Contains(content, "session_id:") {
		t.Error("session_id key must be absent when not supplied")
	}
	if strings.Contains(content, "author:") {
		t.Error("author key must be absent when not supplied")
	}
}

// TestCaptureSession_DefaultCategory uses "sessions" when category is omitted.
func TestCaptureSession_DefaultCategory(t *testing.T) {
	st, cfg := setupCaptureEnv(t)

	result := callCaptureTool(t, st, cfg, map[string]any{
		"title": "My Session Notes",
		"body":  "Some notes here about what happened during this session.",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %v", toolResultText(t, result))
	}

	today := time.Now().Format("2006-01-02")
	expectedPath := filepath.Join(cfg.DocsDir, "sessions", today+"-my-session-notes.md")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("default-category file not found at %s: %v", expectedPath, err)
	}
}

// TestCaptureSession_ReturnsChunkIDs verifies chunk IDs appear in the output.
// Body sections are written with ≥8 words each so they exceed MinTokens=10.
func TestCaptureSession_ReturnsChunkIDs(t *testing.T) {
	st, cfg := setupCaptureEnv(t)

	body := "The indexer runs two passes over all project files.\n\n" +
		"## Pass 1: Docs\n\nThe docs pass walks the configured docs directory and embeds each chunk.\n\n" +
		"## Pass 2: Graph\n\nThe graph pass walks the project root and extracts code symbols and edges."

	result := callCaptureTool(t, st, cfg, map[string]any{
		"title": "Indexing Pipeline Notes",
		"body":  body,
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %v", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	if !strings.Contains(text, "Chunk IDs:") {
		t.Errorf("output missing 'Chunk IDs:'\n%s", text)
	}
}

// TestCaptureSession_IsSearchable is an integration test: capture a document,
// then verify its chunks are stored with the expected file path and that the
// captured content appears in the chunk text.
func TestCaptureSession_IsSearchable(t *testing.T) {
	st, cfg := setupCaptureEnv(t)

	body := "We rate-limit GraphQL mutations at the gateway layer using token buckets " +
		"to prevent abuse and ensure fair resource allocation across all tenants."

	result := callCaptureTool(t, st, cfg, map[string]any{
		"title": "GraphQL Rate Limiting Strategy",
		"body":  body,
	})
	if result.IsError {
		t.Fatalf("capture failed: %v", toolResultText(t, result))
	}

	vec := make([]float64, 4)
	chunks, err := st.SearchChunks("", vec, 10)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("search returned no chunks — capture was not indexed")
	}

	var found bool
	for _, c := range chunks {
		if strings.Contains(c.FilePath, "graphql-rate-limiting-strategy") {
			found = true
			if !strings.Contains(c.Content, "rate-limit") || !strings.Contains(c.Content, "GraphQL") {
				t.Errorf("chunk content missing expected terms; got: %s", c.Content)
			}
			break
		}
	}
	if !found {
		paths := make([]string, len(chunks))
		for i, c := range chunks {
			paths[i] = c.FilePath
		}
		t.Errorf("captured file not found in search results; got file paths: %v", paths)
	}
}

// TestCaptureSession_EmptyTitleFallback verifies that a title that slugifies to
// "" (e.g. all special characters) falls back to the "capture" slug.
func TestCaptureSession_EmptyTitleFallback(t *testing.T) {
	st, cfg := setupCaptureEnv(t)

	result := callCaptureTool(t, st, cfg, map[string]any{
		"title": "---!!!---",
		"body":  "Body content with enough words to meet the minimum token threshold.",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %v", toolResultText(t, result))
	}

	today := time.Now().Format("2006-01-02")
	expectedPath := filepath.Join(cfg.DocsDir, "sessions", today+"-capture.md")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("fallback slug file not found at %s: %v", expectedPath, err)
	}
}

// TestCaptureSession_NewlineStripping verifies that \n, \r, and \r\n in title,
// session_id, and author are all stripped before YAML frontmatter / heading output.
func TestCaptureSession_NewlineStripping(t *testing.T) {
	st, cfg := setupCaptureEnv(t)

	result := callCaptureTool(t, st, cfg, map[string]any{
		"title":      "Injected\nTitle",
		"body":       "Safe body with enough words to exceed the minimum token count here.",
		"session_id": "sess\r\n456",
		"author":     "Bob\rEvil",
	})

	if result.IsError {
		t.Fatalf("unexpected error: %v", toolResultText(t, result))
	}

	today := time.Now().Format("2006-01-02")
	expectedPath := filepath.Join(cfg.DocsDir, "sessions", today+"-injected-title.md")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("file not found at %s: %v", expectedPath, err)
	}
	content := string(data)

	// All line-break variants must be stripped from frontmatter values.
	for _, bad := range []string{"sess\r\n456", "sess\n456", "sess\r456", "Bob\rEvil", "Bob\nEvil"} {
		if strings.Contains(content, bad) {
			t.Errorf("raw line-break sequence %q not stripped from file", bad)
		}
	}
	// Heading must not contain raw line breaks.
	if strings.Contains(content, "Injected\nTitle") || strings.Contains(content, "Injected\rTitle") {
		t.Error("title line-break not stripped from heading")
	}
}

// TestCaptureSession_SpecialCharsInFrontmatter verifies that '#' in session_id / author
// is not silently truncated and that flow-map syntax does not break YAML frontmatter.
func TestCaptureSession_SpecialCharsInFrontmatter(t *testing.T) {
	st, cfg := setupCaptureEnv(t)

	result := callCaptureTool(t, st, cfg, map[string]any{
		"title":      "Special Chars Test",
		"body":       "Body content with enough words to meet the minimum token threshold for indexing.",
		"session_id": "sess-abc#1",
		"author":     "{key: val}",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %v", toolResultText(t, result))
	}

	today := time.Now().Format("2006-01-02")
	expectedPath := filepath.Join(cfg.DocsDir, "sessions", today+"-special-chars-test.md")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("file not found at %s: %v", expectedPath, err)
	}
	content := string(data)

	// The full session_id value must survive — '#' must not truncate it.
	if !strings.Contains(content, `"sess-abc#1"`) {
		t.Errorf("session_id value truncated by '#'; content:\n%s", content)
	}
	// The author flow-map value must be quoted, not raw.
	if !strings.Contains(content, `"{key: val}"`) {
		t.Errorf("author flow-map value not quoted; content:\n%s", content)
	}
}

// setupMultiDirEnv creates a temp workspace with two docs directories and an
// open store. The config uses DocDirs (multi-dir) to exercise ResolvedDocDirs.
func setupMultiDirEnv(t *testing.T) (*store.Store, *config.Config, string, string) {
	t.Helper()
	tmp := t.TempDir()
	dir1 := filepath.Join(tmp, "primary-docs")
	dir2 := filepath.Join(tmp, "secondary-docs")
	for _, d := range []string{dir1, dir2} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(filepath.Join(tmp, "test.db"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := &config.Config{
		DocDirs: []string{dir1, dir2},
		Chunking: config.ChunkingConfig{
			MaxTokens:    512,
			MinTokens:    10,
			OverlapLines: 0,
		},
	}
	return st, cfg, dir1, dir2
}

// TestCaptureSession_DefaultsToFirstDocDir verifies that when docs_dir is
// omitted the file is written under ResolvedDocDirs()[0] (the primary dir).
func TestCaptureSession_DefaultsToFirstDocDir(t *testing.T) {
	st, cfg, primaryDir, _ := setupMultiDirEnv(t)

	result := callCaptureTool(t, st, cfg, map[string]any{
		"title": "Primary Dir Default",
		"body":  "Content verifying the primary docs dir is used when docs_dir is omitted.",
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %v", toolResultText(t, result))
	}

	today := time.Now().Format("2006-01-02")
	expectedPath := filepath.Join(primaryDir, "sessions", today+"-primary-dir-default.md")
	if _, err := os.Stat(expectedPath); err != nil {
		t.Errorf("file not found at primary dir path %s: %v", expectedPath, err)
	}
}

// TestCaptureSession_DocsDirOverride verifies that supplying docs_dir routes
// the write to the specified configured directory.
func TestCaptureSession_DocsDirOverride(t *testing.T) {
	st, cfg, primaryDir, secondaryDir := setupMultiDirEnv(t)

	result := callCaptureTool(t, st, cfg, map[string]any{
		"title":    "Secondary Dir Override",
		"body":     "Content verifying that an explicit docs_dir routes to the secondary directory.",
		"docs_dir": secondaryDir,
	})

	if result.IsError {
		t.Fatalf("expected success, got error: %v", toolResultText(t, result))
	}

	today := time.Now().Format("2006-01-02")
	inSecondary := filepath.Join(secondaryDir, "sessions", today+"-secondary-dir-override.md")
	if _, err := os.Stat(inSecondary); err != nil {
		t.Errorf("file not found in secondary dir at %s: %v", inSecondary, err)
	}

	// The file must NOT appear in the primary dir.
	inPrimary := filepath.Join(primaryDir, "sessions", today+"-secondary-dir-override.md")
	if _, err := os.Stat(inPrimary); err == nil {
		t.Errorf("file must not be written to primary dir %s", inPrimary)
	}
}

// TestCaptureSession_InvalidDocsDirReturnsError verifies that an unrecognised
// docs_dir value is rejected with a clear error listing the valid choices.
func TestCaptureSession_InvalidDocsDirReturnsError(t *testing.T) {
	st, cfg, _, _ := setupMultiDirEnv(t)

	result := callCaptureTool(t, st, cfg, map[string]any{
		"title":    "Invalid Dir Test",
		"body":     "This capture targets an unconfigured directory and should fail.",
		"docs_dir": "/tmp/not-a-configured-dir",
	})

	if !result.IsError {
		t.Fatal("expected tool error for invalid docs_dir, got success")
	}
	text := toolResultText(t, result)
	if !strings.Contains(text, "not a configured docs directory") {
		t.Errorf("error message should mention 'not a configured docs directory'; got: %s", text)
	}
}

// TestCaptureSession_RelativeDocDirsWithProjectRoot verifies that when
// DocDirs entries are relative (the normal production case) and ProjectRoot is
// set, passing the corresponding absolute path as docs_dir validates correctly,
// and the captured file lands under ProjectRoot/relDir/subDir/filename.
func TestCaptureSession_RelativeDocDirsWithProjectRoot(t *testing.T) {
	tmp := t.TempDir()
	// Relative dir entry — as it would appear in a real workspace config.
	relDocDir := "proto/docs"
	absDocDir := filepath.Join(tmp, "proto", "docs")
	if err := os.MkdirAll(absDocDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(tmp, "test.db"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := &config.Config{
		// Relative entry — the production invariant.
		DocDirs:     []string{relDocDir},
		ProjectRoot: tmp,
		Chunking: config.ChunkingConfig{
			MaxTokens:    512,
			MinTokens:    10,
			OverlapLines: 0,
		},
	}

	// Pass the ABSOLUTE form of the docs_dir arg — this is the mismatch the
	// bug caused: relative config entry vs absolute caller arg.
	result := callCaptureTool(t, st, cfg, map[string]any{
		"title":    "Relative Dir Test",
		"body":     "Verifying that a relative doc_dir config entry matches an absolute docs_dir arg.",
		"docs_dir": absDocDir,
	})

	if result.IsError {
		t.Fatalf("expected success but got error: %s", toolResultText(t, result))
	}

	today := time.Now().Format("2006-01-02")
	// File must land under ProjectRoot/relDocDir/sessions/<date>-<slug>.md.
	expectedPath := filepath.Join(tmp, relDocDir, "sessions", today+"-relative-dir-test.md")
	if _, statErr := os.Stat(expectedPath); statErr != nil {
		t.Errorf("file not written at expected path %s: %v", expectedPath, statErr)
	}
}

// TestSlugify covers the slug generation helper.
func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Auth Flow Deep Dive", "auth-flow-deep-dive"},
		{"  Hello   World!  ", "hello-world"},
		{"GraphQL: Rate Limiting (v2)", "graphql-rate-limiting-v2"},
		{"", ""},
		{"---", ""},
	}
	for _, tc := range cases {
		got := slugify(tc.in)
		if got != tc.want {
			t.Errorf("slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
