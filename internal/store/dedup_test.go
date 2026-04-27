package store

import (
	"testing"
)

// TestDeduplicateByContent_IdenticalChunksAcrossFiles inserts 3 chunks with
// identical content from 3 different files. After a query, exactly one result
// must be returned and its Duplicates field must list the other two file paths.
func TestDeduplicateByContent_IdenticalChunksAcrossFiles(t *testing.T) {
	s := newTestStore(t)
	const model = "test-model"

	// Three separate documents (different file paths) with the same content.
	files := []string{"docs/a.md", "docs/b.md", "docs/c.md"}
	const sharedContent = "This is a license header that appears verbatim in every file."

	queryVec := []float64{1, 0, 0, 0}

	for i, fp := range files {
		doc, err := s.AddDocument(AddDocumentInput{FilePath: fp, Title: fp})
		if err != nil {
			t.Fatalf("AddDocument %s: %v", fp, err)
		}
		_, err = s.AddChunk(AddChunkInput{
			Vector:     queryVec,
			Content:    sharedContent,
			FilePath:   fp,
			ChunkIndex: uint32(i),
			DocID:      doc.ID,
			Model:      model,
		})
		if err != nil {
			t.Fatalf("AddChunk %s: %v", fp, err)
		}
	}

	chunks, err := s.SearchChunks("", queryVec, 5)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 result after dedup, got %d", len(chunks))
	}

	rep := chunks[0]
	if rep.Content != sharedContent {
		t.Fatalf("representative content mismatch: got %q", rep.Content)
	}
	if len(rep.Duplicates) != 2 {
		t.Fatalf("expected 2 duplicate paths, got %d: %v", len(rep.Duplicates), rep.Duplicates)
	}

	// The representative is the highest-ranked (lowest ID) chunk; its path is
	// files[0]. The other two must appear in Duplicates.
	dupSet := make(map[string]bool, 2)
	for _, p := range rep.Duplicates {
		dupSet[p] = true
	}
	if dupSet[rep.FilePath] {
		t.Errorf("representative file path %q must not be in Duplicates", rep.FilePath)
	}
	for _, fp := range files {
		if fp == rep.FilePath {
			continue
		}
		if !dupSet[fp] {
			t.Errorf("expected %q in Duplicates, got %v", fp, rep.Duplicates)
		}
	}
}

// TestDeduplicateByContent_SingletonPassthrough verifies that chunks with
// distinct content are not deduplicated and have no Duplicates field.
func TestDeduplicateByContent_SingletonPassthrough(t *testing.T) {
	s := newTestStore(t)
	const model = "test-model"

	doc, err := s.AddDocument(AddDocumentInput{FilePath: "docs/unique.md", Title: "Unique"})
	if err != nil {
		t.Fatalf("AddDocument: %v", err)
	}

	queryVec := []float64{1, 0, 0, 0}
	contents := []string{"alpha content", "beta content", "gamma content"}
	for i, c := range contents {
		_, err := s.AddChunk(AddChunkInput{
			Vector:     queryVec,
			Content:    c,
			FilePath:   doc.FilePath,
			ChunkIndex: uint32(i),
			DocID:      doc.ID,
			Model:      model,
		})
		if err != nil {
			t.Fatalf("AddChunk %d: %v", i, err)
		}
	}

	chunks, err := s.SearchChunks("", queryVec, 5)
	if err != nil {
		t.Fatalf("SearchChunks: %v", err)
	}
	if len(chunks) != len(contents) {
		t.Fatalf("expected %d results, got %d", len(contents), len(chunks))
	}
	for _, ch := range chunks {
		if len(ch.Duplicates) != 0 {
			t.Errorf("chunk %q: expected no Duplicates, got %v", ch.Content, ch.Duplicates)
		}
	}
}

// TestDeduplicateByContent_HybridSearch verifies deduplication also works
// via HybridSearch (which goes through hybridRerankWithSignals).
func TestDeduplicateByContent_HybridSearch(t *testing.T) {
	s := newTestStore(t)
	const model = "test-model"

	files := []string{"src/x.go", "src/y.go", "src/z.go"}
	const sharedContent = "package main // identical boilerplate across generated files"

	queryVec := []float64{1, 0, 0, 0}

	for i, fp := range files {
		doc, err := s.AddDocument(AddDocumentInput{FilePath: fp, Title: fp})
		if err != nil {
			t.Fatalf("AddDocument %s: %v", fp, err)
		}
		_, err = s.AddChunk(AddChunkInput{
			Vector:     queryVec,
			Content:    sharedContent,
			FilePath:   fp,
			ChunkIndex: uint32(i),
			DocID:      doc.ID,
			Model:      model,
		})
		if err != nil {
			t.Fatalf("AddChunk %s: %v", fp, err)
		}
	}

	chunks, err := s.HybridSearch(queryVec, "package main", 5)
	if err != nil {
		t.Fatalf("HybridSearch: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 result after hybrid dedup, got %d", len(chunks))
	}
	if len(chunks[0].Duplicates) != 2 {
		t.Fatalf("expected 2 duplicate paths, got %d: %v", len(chunks[0].Duplicates), chunks[0].Duplicates)
	}
}

// TestDeduplicateByContent_Unit directly exercises the deduplicateByContent
// helper with pre-built scoredChunk slices.
func TestDeduplicateByContent_Unit(t *testing.T) {
	identical := "identical content string"
	makeChunk := func(id, fp, content string) scoredChunk {
		return scoredChunk{
			chunk:      DocChunk{ID: id, FilePath: fp, Content: content},
			finalScore: 1.0,
		}
	}

	t.Run("three_identical", func(t *testing.T) {
		input := []scoredChunk{
			makeChunk("1", "a.md", identical),
			makeChunk("2", "b.md", identical),
			makeChunk("3", "c.md", identical),
		}
		got := deduplicateByContent(input)
		if len(got) != 1 {
			t.Fatalf("expected 1 result, got %d", len(got))
		}
		if got[0].chunk.FilePath != "a.md" {
			t.Errorf("expected representative a.md, got %s", got[0].chunk.FilePath)
		}
		if len(got[0].chunk.Duplicates) != 2 {
			t.Errorf("expected 2 duplicates, got %v", got[0].chunk.Duplicates)
		}
	})

	t.Run("singletons", func(t *testing.T) {
		input := []scoredChunk{
			makeChunk("1", "a.md", "alpha"),
			makeChunk("2", "b.md", "beta"),
		}
		got := deduplicateByContent(input)
		if len(got) != 2 {
			t.Fatalf("expected 2 results, got %d", len(got))
		}
		for _, sc := range got {
			if len(sc.chunk.Duplicates) != 0 {
				t.Errorf("singleton %s must have no Duplicates", sc.chunk.FilePath)
			}
		}
	})

	t.Run("beyond_window_unchanged", func(t *testing.T) {
		// Fill the first dedupWindow slots: (dedupWindow-1) unique entries plus
		// one pair of duplicates at positions 0 and 1. This ensures the
		// hasDups=true branch fires and the beyond-window append path is reached.
		// Two additional identical chunks beyond the window must pass through
		// without being deduplicated (they are outside the scan window).
		input := make([]scoredChunk, 0, dedupWindow+2)
		// Position 0 and 1 share content — triggers real dedup path.
		dupContent := "duplicate within window content"
		input = append(input, makeChunk("0", "dup-a.md", dupContent))
		input = append(input, makeChunk("1", "dup-b.md", dupContent))
		for i := 2; i < dedupWindow; i++ {
			input = append(input, makeChunk(string(rune('a'+i)), "unique.md",
				"unique content "+string(rune('a'+i))))
		}
		beyondContent := "beyond window identical"
		input = append(input, makeChunk("x", "x.md", beyondContent))
		input = append(input, makeChunk("y", "y.md", beyondContent))

		got := deduplicateByContent(input)
		// The pair at 0/1 deduplicates to 1 representative; the (dedupWindow-2)
		// unique entries survive; both beyond-window entries pass through.
		wantLen := 1 + (dedupWindow - 2) + 2
		if len(got) != wantLen {
			t.Fatalf("expected %d results, got %d", wantLen, len(got))
		}
		// Representative for the in-window duplicate must have 1 duplicate path.
		rep := got[0]
		if rep.chunk.Content != dupContent {
			t.Errorf("expected representative to hold dupContent, got %q", rep.chunk.Content)
		}
		if len(rep.chunk.Duplicates) != 1 || rep.chunk.Duplicates[0] != "dup-b.md" {
			t.Errorf("expected Duplicates=[dup-b.md], got %v", rep.chunk.Duplicates)
		}
		// The two beyond-window chunks must be present and have no Duplicates.
		beyondCount := 0
		for _, sc := range got {
			if sc.chunk.Content == beyondContent {
				beyondCount++
				if len(sc.chunk.Duplicates) != 0 {
					t.Errorf("beyond-window chunk %s must have no Duplicates", sc.chunk.FilePath)
				}
			}
		}
		if beyondCount != 2 {
			t.Errorf("expected 2 beyond-window entries, found %d", beyondCount)
		}
	})
}
