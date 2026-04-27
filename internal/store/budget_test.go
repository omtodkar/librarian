package store

import (
	"testing"
)

// TestApplyTokenBudget_BudgetCutsAtOverflow verifies the acceptance criterion:
// budget=1000 with chunks of 400,400,400,400 tokens → returns first 2
// (cumulative 800; third would bring total to 1200 > 1000).
func TestApplyTokenBudget_BudgetCutsAtOverflow(t *testing.T) {
	chunks := make([]DocChunk, 4)
	for i := range chunks {
		chunks[i] = DocChunk{
			ID:         string(rune('1' + i)),
			TokenCount: 400,
			Content:    "some content",
		}
	}
	got := ApplyTokenBudget(chunks, 1000)
	if len(got) != 2 {
		t.Fatalf("budget=1000 with 4×400-token chunks: want 2 results, got %d", len(got))
	}
	if got[0].ID != "1" || got[1].ID != "2" {
		t.Errorf("wrong chunks returned: got IDs %q %q, want \"1\" \"2\"", got[0].ID, got[1].ID)
	}
}

// TestApplyTokenBudget_ZeroDisabled verifies budget=0 is a no-op (returns all chunks).
func TestApplyTokenBudget_ZeroDisabled(t *testing.T) {
	chunks := make([]DocChunk, 5)
	for i := range chunks {
		chunks[i] = DocChunk{ID: string(rune('1' + i)), TokenCount: 400, Content: "content"}
	}
	got := ApplyTokenBudget(chunks, 0)
	if len(got) != 5 {
		t.Fatalf("budget=0 must return all chunks; want 5, got %d", len(got))
	}
}

// TestApplyTokenBudget_NegativeBudgetDisabled verifies negative budget is also a no-op.
func TestApplyTokenBudget_NegativeBudgetDisabled(t *testing.T) {
	chunks := []DocChunk{{ID: "1", TokenCount: 100, Content: "x"}}
	got := ApplyTokenBudget(chunks, -1)
	if len(got) != 1 {
		t.Fatalf("negative budget must return all chunks; want 1, got %d", len(got))
	}
}

// TestApplyTokenBudget_ExactlyFits verifies a chunk whose cumulative total equals
// the budget exactly is included.
func TestApplyTokenBudget_ExactlyFits(t *testing.T) {
	chunks := []DocChunk{
		{ID: "1", TokenCount: 500, Content: "a"},
		{ID: "2", TokenCount: 500, Content: "b"},
		{ID: "3", TokenCount: 1, Content: "c"},
	}
	got := ApplyTokenBudget(chunks, 1000)
	if len(got) != 2 {
		t.Fatalf("budget=1000, chunks 500+500+1: want 2, got %d", len(got))
	}
}

// TestApplyTokenBudget_FallbackApprox verifies that chunks with TokenCount=0 use
// the content-based heuristic instead of treating them as zero-cost.
func TestApplyTokenBudget_FallbackApprox(t *testing.T) {
	// approxTokens uses words/0.75; 3 words → int(3/0.75) = 4 tokens each.
	chunks := []DocChunk{
		{ID: "1", TokenCount: 0, Content: "one two three"},
		{ID: "2", TokenCount: 0, Content: "four five six"},
		{ID: "3", TokenCount: 0, Content: "seven eight nine"},
	}
	// 4+4 = 8 ≤ 9, but 4+4+4 = 12 > 9 → only first 2 should fit.
	got := ApplyTokenBudget(chunks, 9)
	if len(got) != 2 {
		t.Fatalf("fallback heuristic: budget=9 with 3×4-token chunks: want 2, got %d", len(got))
	}
}

// TestApplyTokenBudget_MixedTokenCount verifies that interleaved chunks with
// TokenCount>0 (use stored value) and TokenCount==0 (fall back to approxTokens)
// are both counted correctly toward the budget.
//
// Chunk layout:
//   chunk 1: TokenCount=500                        → 500 stored tokens
//   chunk 2: TokenCount=0, "one two three" (3 words) → int(3/0.75) = 4 approx
//   chunk 3: TokenCount=500                        → 500 stored tokens
//
// budget=505: 500+4=504 ≤ 505 but 504+500=1004 > 505 → only first 2 included.
func TestApplyTokenBudget_MixedTokenCount(t *testing.T) {
	chunks := []DocChunk{
		{ID: "1", TokenCount: 500, Content: "stored token count"},
		{ID: "2", TokenCount: 0, Content: "one two three"},
		{ID: "3", TokenCount: 500, Content: "stored token count again"},
	}
	got := ApplyTokenBudget(chunks, 505)
	if len(got) != 2 {
		t.Fatalf("mixed TokenCount budget=505: want 2 results, got %d", len(got))
	}
	if got[0].ID != "1" || got[1].ID != "2" {
		t.Errorf("wrong chunks: got IDs %q %q, want \"1\" \"2\"", got[0].ID, got[1].ID)
	}
}

// TestApplyTokenBudget_Empty verifies empty input returns empty output.
func TestApplyTokenBudget_Empty(t *testing.T) {
	got := ApplyTokenBudget(nil, 1000)
	if len(got) != 0 {
		t.Fatalf("empty input: want 0 results, got %d", len(got))
	}
}

// TestApplyTokenBudget_BudgetSmallerThanFirstChunk verifies that if the first
// chunk already exceeds the budget, zero chunks are returned.
func TestApplyTokenBudget_BudgetSmallerThanFirstChunk(t *testing.T) {
	chunks := []DocChunk{{ID: "1", TokenCount: 500, Content: "big"}}
	got := ApplyTokenBudget(chunks, 100)
	if len(got) != 0 {
		t.Fatalf("budget smaller than first chunk: want 0, got %d", len(got))
	}
}
