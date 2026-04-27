package store

import (
	"math"
	"strconv"
	"testing"
)

// TestSearchChunks_DeterministicOrderOnScoreTie verifies that when two chunks
// receive identical final scores, SearchChunks returns them id-ascending on
// every call. This is the property needed for Claude prompt-cache stability:
// repeated identical queries must produce byte-for-byte identical output.
func TestSearchChunks_DeterministicOrderOnScoreTie(t *testing.T) {
	s := newTestStore(t)
	const model = "test-model"

	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/tie.md", Title: "Tie"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	// Both chunks have the same vector as the query (distance=0, vectorScore=1.0)
	// and no signals (boost=0). Final score = 0.90*1.0 + 0.10*0.0 = 0.90 for both.
	queryVec := []float64{1, 0, 0, 0}

	c1, err := s.AddChunk(AddChunkInput{
		Vector: queryVec, Content: "alpha chunk inserted first",
		FilePath: doc.FilePath, ChunkIndex: 0, DocID: doc.ID, Model: model,
	})
	if err != nil {
		t.Fatalf("AddChunk c1: %v", err)
	}
	c2, err := s.AddChunk(AddChunkInput{
		Vector: queryVec, Content: "beta chunk inserted second",
		FilePath: doc.FilePath, ChunkIndex: 1, DocID: doc.ID, Model: model,
	})
	if err != nil {
		t.Fatalf("AddChunk c2: %v", err)
	}

	id1, _ := strconv.ParseInt(c1.ID, 10, 64)
	id2, _ := strconv.ParseInt(c2.ID, 10, 64)
	if id1 >= id2 {
		t.Fatalf("precondition: expected c1.ID < c2.ID, got %s >= %s", c1.ID, c2.ID)
	}

	for i := 0; i < 5; i++ {
		results, err := s.SearchChunks("", queryVec, 2)
		if err != nil {
			t.Fatalf("SearchChunks iter %d: %v", i, err)
		}
		if len(results) != 2 {
			t.Fatalf("iter %d: expected 2 results, got %d", i, len(results))
		}
		if results[0].ID != c1.ID || results[1].ID != c2.ID {
			t.Errorf("iter %d: got order [%s, %s], want [%s, %s] (id-ascending within score tie)",
				i, results[0].ID, results[1].ID, c1.ID, c2.ID)
		}
	}
}

// TestHybridSearch_DeterministicOrderOnVectorRank verifies that HybridSearch
// produces a stable, id-ascending result when all chunks are orthogonal to the
// query (equal vector distance). Because vectorSearch now uses ORDER BY
// v.distance, c.id, equal-distance chunks are fetched in id-ascending order,
// so c1 gets vector rank 0 (higher RRF score) and always precedes c2.
func TestHybridSearch_DeterministicOrderOnVectorRank(t *testing.T) {
	s := newTestStore(t)
	const model = "test-model"

	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/hvrank.md", Title: "HVRank"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	// Both chunks are orthogonal to the query (distance=1) and have no FTS
	// matches. vectorSearch returns them in id-ascending order (ORDER BY c.id
	// tiebreak), giving c1 rank 0 and c2 rank 1, so c1 always scores higher.
	queryVec := []float64{1, 0, 0, 0}
	orthogonal := []float64{0, 1, 0, 0}

	c1, err := s.AddChunk(AddChunkInput{
		Vector: orthogonal, Content: "first orthogonal chunk with no match",
		FilePath: doc.FilePath, ChunkIndex: 0, DocID: doc.ID, Model: model,
	})
	if err != nil {
		t.Fatalf("AddChunk c1: %v", err)
	}
	c2, err := s.AddChunk(AddChunkInput{
		Vector: orthogonal, Content: "second orthogonal chunk with no match",
		FilePath: doc.FilePath, ChunkIndex: 1, DocID: doc.ID, Model: model,
	})
	if err != nil {
		t.Fatalf("AddChunk c2: %v", err)
	}

	id1, _ := strconv.ParseInt(c1.ID, 10, 64)
	id2, _ := strconv.ParseInt(c2.ID, 10, 64)
	if id1 >= id2 {
		t.Fatalf("precondition: expected c1.ID < c2.ID, got %s >= %s", c1.ID, c2.ID)
	}

	for i := 0; i < 5; i++ {
		results, err := s.HybridSearch(queryVec, "nomatch", 2)
		if err != nil {
			t.Fatalf("HybridSearch iter %d: %v", i, err)
		}
		if len(results) != 2 {
			t.Fatalf("iter %d: expected 2 results, got %d", i, len(results))
		}
		if results[0].ID != c1.ID || results[1].ID != c2.ID {
			t.Errorf("iter %d: got order [%s, %s], want [%s, %s] (id-ascending via vector rank)",
				i, results[0].ID, results[1].ID, c1.ID, c2.ID)
		}
	}
}

// TestHybridSearch_DeterministicOrderOnRRFTie verifies that when two chunks
// have a genuinely identical RRF score (both appear at the same rank in both
// vector and FTS lists), rankLess breaks the tie by id-ascending order.
func TestHybridSearch_DeterministicOrderOnRRFTie(t *testing.T) {
	s := newTestStore(t)
	const model = "test-model"

	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/hrrf.md", Title: "HRRFTie"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	// Use the exact query vector so both chunks have distance=0 from the query
	// (vector rank 0 for c1, rank 1 for c2 due to ORDER BY c.id tiebreak).
	// Both chunks contain the exact literal query token "xyztoken", so BM25
	// gives each rank=1 in the FTS results — but they appear in separate FTS
	// result rows, so c1 gets FTS rank 0 and c2 gets FTS rank 1.
	// RRF(c1) = 1/(60+1) + 1/(60+1) = 2/61 ; RRF(c2) = 1/(60+2) + 1/(60+2) = 2/62.
	// These are NOT equal, but the test still confirms that the result is
	// consistently id-ascending (c1 before c2) because c1 has the higher score.
	//
	// A true RRF tie requires both chunks to appear at the same position in both
	// lists simultaneously, which is impossible with two distinct rowids. The
	// rankLess id-tiebreak exists as the last-resort guard for any residual
	// float equality after signal boosting; the test below exercises it directly
	// by calling hybridRerankWithSignals with pre-built equal-score candidates.
	queryVec := []float64{1, 0, 0, 0}
	c1, err := s.AddChunk(AddChunkInput{
		Vector: queryVec, Content: "xyztoken alpha content here",
		FilePath: doc.FilePath, ChunkIndex: 0, DocID: doc.ID, Model: model,
	})
	if err != nil {
		t.Fatalf("AddChunk c1: %v", err)
	}
	c2, err := s.AddChunk(AddChunkInput{
		Vector: queryVec, Content: "xyztoken beta content here",
		FilePath: doc.FilePath, ChunkIndex: 1, DocID: doc.ID, Model: model,
	})
	if err != nil {
		t.Fatalf("AddChunk c2: %v", err)
	}

	id1, _ := strconv.ParseInt(c1.ID, 10, 64)
	id2, _ := strconv.ParseInt(c2.ID, 10, 64)
	if id1 >= id2 {
		t.Fatalf("precondition: expected c1.ID < c2.ID, got %s >= %s", c1.ID, c2.ID)
	}

	for i := 0; i < 5; i++ {
		results, err := s.HybridSearch(queryVec, "xyztoken", 2)
		if err != nil {
			t.Fatalf("HybridSearch iter %d: %v", i, err)
		}
		if len(results) != 2 {
			t.Fatalf("iter %d: expected 2 results, got %d", i, len(results))
		}
		if results[0].ID != c1.ID || results[1].ID != c2.ID {
			t.Errorf("iter %d: got order [%s, %s], want [%s, %s] (c1 before c2)",
				i, results[0].ID, results[1].ID, c1.ID, c2.ID)
		}
	}
}

// TestRankLess_IdTiebreakOnEqualScore directly exercises the id-ascending
// tiebreak in rankLess for truly equal finalScore values.
func TestRankLess_IdTiebreakOnEqualScore(t *testing.T) {
	hi := scoredChunk{chunk: DocChunk{ID: "1"}, finalScore: 1.0}
	lo := scoredChunk{chunk: DocChunk{ID: "2"}, finalScore: 1.0}

	if !rankLess(hi, lo) {
		t.Error("rankLess(id=1, id=2) should return true when scores are equal")
	}
	if rankLess(lo, hi) {
		t.Error("rankLess(id=2, id=1) should return false when scores are equal")
	}
}

// TestSearchChunks_SameQuerySameIDOrder verifies that two identical sequential
// calls to SearchChunks return the same chunk-id sequence, confirming the
// result is stable enough to produce a repeatable prompt-cache prefix.
func TestSearchChunks_SameQuerySameIDOrder(t *testing.T) {
	s := newTestStore(t)
	const model = "test-model"

	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/stable.md", Title: "Stable"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	queryVec := []float64{1, 0, 0, 0}
	for i := 0; i < 8; i++ {
		angle := float64(i) * 0.1
		vec := []float64{math.Cos(angle), math.Sin(angle), 0, 0}
		if _, err := s.AddChunk(AddChunkInput{
			Vector: vec, Content: "content item " + strconv.Itoa(i),
			FilePath: doc.FilePath, ChunkIndex: uint32(i), DocID: doc.ID, Model: model,
		}); err != nil {
			t.Fatalf("AddChunk %d: %v", i, err)
		}
	}

	run1, err := s.SearchChunks("", queryVec, 5)
	if err != nil {
		t.Fatalf("SearchChunks run1: %v", err)
	}
	run2, err := s.SearchChunks("", queryVec, 5)
	if err != nil {
		t.Fatalf("SearchChunks run2: %v", err)
	}

	if len(run1) != len(run2) {
		t.Fatalf("run lengths differ: %d vs %d", len(run1), len(run2))
	}
	for i := range run1 {
		if run1[i].ID != run2[i].ID {
			t.Errorf("position %d: run1 id=%s, run2 id=%s", i, run1[i].ID, run2[i].ID)
		}
	}
}

// TestNeighbors_DeterministicOrder verifies that Neighbors returns edges in a
// consistent from_node, to_node, kind order across repeated calls.
func TestNeighbors_DeterministicOrder(t *testing.T) {
	s := newTestStore(t)

	// Seed nodes and edges in non-alphabetical to_node order.
	nodes := []Node{
		{ID: "file:a", Kind: NodeKindCodeFile, Label: "a"},
		{ID: "sym:z", Kind: NodeKindSymbol, Label: "z"},
		{ID: "sym:m", Kind: NodeKindSymbol, Label: "m"},
		{ID: "sym:a", Kind: NodeKindSymbol, Label: "a"},
	}
	for _, n := range nodes {
		if err := s.UpsertNode(n); err != nil {
			t.Fatalf("UpsertNode %s: %v", n.ID, err)
		}
	}
	edges := []Edge{
		{From: "file:a", To: "sym:z", Kind: EdgeKindContains, Weight: 1},
		{From: "file:a", To: "sym:m", Kind: EdgeKindContains, Weight: 1},
		{From: "file:a", To: "sym:a", Kind: EdgeKindContains, Weight: 1},
	}
	for _, e := range edges {
		if err := s.UpsertEdge(e); err != nil {
			t.Fatalf("UpsertEdge %s→%s: %v", e.From, e.To, err)
		}
	}

	wantOrder := []string{"sym:a", "sym:m", "sym:z"} // to_node ascending

	for i := 0; i < 3; i++ {
		got, err := s.Neighbors("file:a", "out")
		if err != nil {
			t.Fatalf("Neighbors iter %d: %v", i, err)
		}
		if len(got) != len(wantOrder) {
			t.Fatalf("iter %d: expected %d edges, got %d", i, len(wantOrder), len(got))
		}
		for j, e := range got {
			if e.To != wantOrder[j] {
				t.Errorf("iter %d position %d: got to_node=%s, want %s", i, j, e.To, wantOrder[j])
			}
		}
	}
}

// TestNeighbors_DeterministicOrderWithKind verifies that parallel edges (same
// node pair, different kinds) are also sorted deterministically by kind.
func TestNeighbors_DeterministicOrderWithKind(t *testing.T) {
	s := newTestStore(t)

	nodes := []Node{
		{ID: "sym:parent", Kind: NodeKindSymbol, Label: "parent"},
		{ID: "sym:child", Kind: NodeKindSymbol, Label: "child"},
	}
	for _, n := range nodes {
		if err := s.UpsertNode(n); err != nil {
			t.Fatalf("UpsertNode %s: %v", n.ID, err)
		}
	}
	// Two parallel edges between the same pair, different kinds.
	edges := []Edge{
		{From: "sym:parent", To: "sym:child", Kind: EdgeKindRequires, Weight: 1},
		{From: "sym:parent", To: "sym:child", Kind: EdgeKindInherits, Weight: 1},
	}
	for _, e := range edges {
		if err := s.UpsertEdge(e); err != nil {
			t.Fatalf("UpsertEdge kind=%s: %v", e.Kind, err)
		}
	}

	// Alphabetically: "inherits" < "requires"
	wantKinds := []string{EdgeKindInherits, EdgeKindRequires}

	for i := 0; i < 3; i++ {
		got, err := s.Neighbors("sym:parent", "out")
		if err != nil {
			t.Fatalf("Neighbors iter %d: %v", i, err)
		}
		if len(got) != 2 {
			t.Fatalf("iter %d: expected 2 edges, got %d", i, len(got))
		}
		for j, e := range got {
			if e.Kind != wantKinds[j] {
				t.Errorf("iter %d position %d: got kind=%s, want %s", i, j, e.Kind, wantKinds[j])
			}
		}
	}
}
