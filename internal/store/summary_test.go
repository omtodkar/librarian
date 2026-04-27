package store

import (
	"testing"
)

// TestSummaryCache_UpsertAndGet pins the upsert + lookup round-trip for
// summary_cache. A second Upsert with the same hash must overwrite and not
// create a second row.
func TestSummaryCache_UpsertAndGet(t *testing.T) {
	s := newTestStore(t)

	const hash = "abc123"

	// GetChunkSummariesByHashes on an empty cache returns an empty map.
	got, err := s.GetChunkSummariesByHashes([]string{hash})
	if err != nil {
		t.Fatalf("GetChunkSummariesByHashes: %v", err)
	}
	if _, ok := got[hash]; ok {
		t.Error("expected cache miss before any upsert")
	}

	// Insert.
	if err := s.UpsertChunkSummary(hash, "first summary"); err != nil {
		t.Fatalf("UpsertChunkSummary insert: %v", err)
	}

	// Cache hit.
	got, err = s.GetChunkSummariesByHashes([]string{hash})
	if err != nil {
		t.Fatalf("GetChunkSummariesByHashes after insert: %v", err)
	}
	if got[hash] != "first summary" {
		t.Errorf("got %q want %q", got[hash], "first summary")
	}

	// Overwrite.
	if err := s.UpsertChunkSummary(hash, "second summary"); err != nil {
		t.Fatalf("UpsertChunkSummary overwrite: %v", err)
	}
	got, err = s.GetChunkSummariesByHashes([]string{hash})
	if err != nil {
		t.Fatalf("GetChunkSummariesByHashes after overwrite: %v", err)
	}
	if got[hash] != "second summary" {
		t.Errorf("overwrite: got %q want %q", got[hash], "second summary")
	}
}

// TestSummaryCache_MultipleHashes verifies batch lookup returns only the
// hashes that exist.
func TestSummaryCache_MultipleHashes(t *testing.T) {
	s := newTestStore(t)

	if err := s.UpsertChunkSummary("h1", "summary-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertChunkSummary("h2", "summary-2"); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetChunkSummariesByHashes([]string{"h1", "h2", "h3-missing"})
	if err != nil {
		t.Fatalf("GetChunkSummariesByHashes: %v", err)
	}
	if got["h1"] != "summary-1" {
		t.Errorf("h1: got %q want %q", got["h1"], "summary-1")
	}
	if got["h2"] != "summary-2" {
		t.Errorf("h2: got %q want %q", got["h2"], "summary-2")
	}
	if _, ok := got["h3-missing"]; ok {
		t.Error("h3-missing should not be in result")
	}
}

// TestSummaryCache_Empty verifies empty input returns empty map without error.
func TestSummaryCache_Empty(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetChunkSummariesByHashes(nil)
	if err != nil {
		t.Fatalf("GetChunkSummariesByHashes(nil): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}
