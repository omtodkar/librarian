package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/store"
)

// newSearchTestStore opens a temp store, adds a document, inserts n chunks each
// with the given tokenCount, and returns the store. Chunks use a zero vector so
// the captureEmbedder (also returning zero) gets uniform similarity scores.
func newSearchTestStore(t *testing.T, n int, tokenCount uint32) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/test.db", nil, 0)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	doc, err := st.AddDocument(store.AddDocumentInput{FilePath: "docs/budget_test.md", Title: "Budget"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}
	vec := make([]float64, 4)
	for i := 0; i < n; i++ {
		_, err := st.AddChunk(store.AddChunkInput{
			Vector:     vec,
			Content:    fmt.Sprintf("chunk %d content with several words here for testing", i),
			FilePath:   doc.FilePath,
			ChunkIndex: uint32(i),
			TokenCount: tokenCount,
			DocID:      doc.ID,
			Model:      "fake",
		})
		if err != nil {
			t.Fatalf("AddChunk %d: %v", i, err)
		}
	}
	return st
}

func callSearchDocsTool(t *testing.T, st *store.Store, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	srv := server.NewMCPServer("librarian", "test")
	registerSearchDocs(srv, st, captureEmbedder{dim: 4}, false)

	req := mcp.CallToolRequest{}
	req.Params.Name = "search_docs"
	req.Params.Arguments = args

	tools := srv.ListTools()
	tool, ok := tools["search_docs"]
	if !ok {
		t.Fatal("search_docs tool not registered")
	}
	result, err := tool.Handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

// TestSearchDocs_BudgetParam_LimitsResults is an integration wiring test: it
// verifies that the budget parameter is read by the handler and applied so that
// only chunks fitting within the budget appear in the output.
//
// 4 chunks × 400 tokens each; budget=1000: first 2 fit (800 ≤ 1000),
// third would bring total to 1200 which exceeds the budget.
func TestSearchDocs_BudgetParam_LimitsResults(t *testing.T) {
	st := newSearchTestStore(t, 4, 400)

	result := callSearchDocsTool(t, st, map[string]any{
		"query":  "chunk content",
		"limit":  float64(10),
		"budget": float64(1000),
	})

	if result.IsError {
		t.Fatalf("unexpected error: %v", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	count := strings.Count(text, "### Result")
	if count != 2 {
		t.Errorf("budget=1000 with 4×400-token chunks: want 2 results in output, got %d\n%s", count, text)
	}
}

// TestSearchDocs_BudgetZero_NoFilterApplied verifies budget=0 returns all
// chunks up to the limit (no budget filtering).
func TestSearchDocs_BudgetZero_NoFilterApplied(t *testing.T) {
	st := newSearchTestStore(t, 4, 400)

	result := callSearchDocsTool(t, st, map[string]any{
		"query":  "chunk content",
		"limit":  float64(10),
		"budget": float64(0),
	})

	if result.IsError {
		t.Fatalf("unexpected error: %v", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	count := strings.Count(text, "### Result")
	if count != 4 {
		t.Errorf("budget=0 (disabled): want 4 results, got %d\n%s", count, text)
	}
}
