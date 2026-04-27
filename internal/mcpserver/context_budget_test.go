package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"librarian/internal/store"
)

func callGetContextTool(t *testing.T, st *store.Store, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	srv := server.NewMCPServer("librarian", "test")
	registerGetContext(srv, st, captureEmbedder{dim: 4}, false)

	req := mcp.CallToolRequest{}
	req.Params.Name = "get_context"
	req.Params.Arguments = args

	tools := srv.ListTools()
	tool, ok := tools["get_context"]
	if !ok {
		t.Fatal("get_context tool not registered")
	}
	result, err := tool.Handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	return result
}

// TestGetContext_BudgetParam_LimitsResults verifies that the budget parameter is
// applied to get_context: 4 chunks × 1000 tokens each, budget=2500 → first 2
// fit (cumulative 2000 ≤ 2500), third would bring total to 3000 which exceeds
// the budget.
func TestGetContext_BudgetParam_LimitsResults(t *testing.T) {
	st := newSearchTestStore(t, 4, 1000)

	result := callGetContextTool(t, st, map[string]any{
		"query":  "chunk content",
		"limit":  float64(10),
		"budget": float64(2500),
	})

	if result.IsError {
		t.Fatalf("unexpected error: %v", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	// get_context emits "### file > section" per chunk in primary sources.
	count := strings.Count(text, "### docs/budget_test.md >")
	if count != 2 {
		t.Errorf("budget=2500 with 4×1000-token chunks: want 2 chunks in output, got %d\n%s", count, text)
	}
}

// TestGetContext_BudgetZero_NoFilterApplied verifies budget=0 returns all chunks
// up to the limit (budget disabled).
func TestGetContext_BudgetZero_NoFilterApplied(t *testing.T) {
	st := newSearchTestStore(t, 4, 1000)

	result := callGetContextTool(t, st, map[string]any{
		"query":  "chunk content",
		"limit":  float64(10),
		"budget": float64(0),
	})

	if result.IsError {
		t.Fatalf("unexpected error: %v", toolResultText(t, result))
	}

	text := toolResultText(t, result)
	count := strings.Count(text, "### docs/budget_test.md >")
	if count != 4 {
		t.Errorf("budget=0 (disabled): want 4 chunks in output, got %d\n%s", count, text)
	}
}
