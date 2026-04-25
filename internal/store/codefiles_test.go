package store

import (
	"sync"
	"testing"
)

// TestAddOrUpdateCodeFile_InsertThenUpdate pins the two code paths:
// first call inserts a new row with a stable UUID; second call with a
// different hash updates in place and does NOT rotate the UUID (callers
// like the graph pass depend on the ID staying stable so contains-edges
// to symbol nodes don't dangle).
func TestAddOrUpdateCodeFile_InsertThenUpdate(t *testing.T) {
	s := newTestStore(t)
	const path = "internal/auth/oauth.go"

	cf1, err := s.AddOrUpdateCodeFile(path, "go", "hash-v1")
	if err != nil {
		t.Fatalf("first AddOrUpdate: %v", err)
	}
	if cf1.ID == "" {
		t.Fatal("expected non-empty id after insert")
	}
	if cf1.ContentHash != "hash-v1" {
		t.Errorf("ContentHash: got %q want %q", cf1.ContentHash, "hash-v1")
	}

	cf2, err := s.AddOrUpdateCodeFile(path, "go", "hash-v2")
	if err != nil {
		t.Fatalf("second AddOrUpdate: %v", err)
	}
	if cf2.ID != cf1.ID {
		t.Errorf("id rotated on update: %q → %q (should stay stable)", cf1.ID, cf2.ID)
	}
	if cf2.ContentHash != "hash-v2" {
		t.Errorf("ContentHash not updated: got %q want %q", cf2.ContentHash, "hash-v2")
	}
}

// TestDeleteSymbolsForFile pins that only symbol-kind nodes sourced to the
// given path are removed; the code_file node itself and mentions edges from
// docs stay intact. This matches how the graph pass invalidates stale
// symbols when content_hash changes.
func TestDeleteSymbolsForFile(t *testing.T) {
	s := newTestStore(t)
	const path = "internal/auth/oauth.go"

	// code_file node — must survive.
	if err := s.UpsertNode(Node{
		ID: CodeFileNodeID(path), Kind: NodeKindCodeFile, Label: path, SourcePath: path,
	}); err != nil {
		t.Fatalf("upsert code_file: %v", err)
	}

	// two symbol nodes from this file — must be deleted.
	for _, fqn := range []string{"auth.Login", "auth.Validate"} {
		if err := s.UpsertNode(Node{
			ID: SymbolNodeID(fqn), Kind: NodeKindSymbol, Label: fqn, SourcePath: path,
		}); err != nil {
			t.Fatalf("upsert symbol %s: %v", fqn, err)
		}
	}

	// symbol from a different file — must also survive.
	if err := s.UpsertNode(Node{
		ID: SymbolNodeID("util.Hash"), Kind: NodeKindSymbol,
		Label: "util.Hash", SourcePath: "internal/util/hash.go",
	}); err != nil {
		t.Fatalf("upsert foreign symbol: %v", err)
	}

	if err := s.DeleteSymbolsForFile(path); err != nil {
		t.Fatalf("DeleteSymbolsForFile: %v", err)
	}

	// code_file stays
	if got, _ := s.GetNode(CodeFileNodeID(path)); got == nil {
		t.Error("code_file node was unexpectedly removed")
	}
	// symbols sourced to path gone
	for _, fqn := range []string{"auth.Login", "auth.Validate"} {
		if got, _ := s.GetNode(SymbolNodeID(fqn)); got != nil {
			t.Errorf("symbol %s should have been deleted", fqn)
		}
	}
	// foreign symbol stays
	if got, _ := s.GetNode(SymbolNodeID("util.Hash")); got == nil {
		t.Error("symbol from a different file was unexpectedly removed")
	}
}

// TestAddOrUpdateCodeFile_ParallelInsertsDontRace pins the fix for the race
// that would bite the PR 3 parallel worker pool: two workers both processing
// the same previously-unseen file path simultaneously must not both hit
// "no row → INSERT" and fail one with a UNIQUE constraint violation. The
// atomic INSERT ... ON CONFLICT DO UPDATE handles it; this test fires N
// goroutines all calling AddOrUpdateCodeFile for the same path and asserts
// all succeed without error.
func TestAddOrUpdateCodeFile_ParallelInsertsDontRace(t *testing.T) {
	s := newTestStore(t)
	const path = "internal/contention.go"
	const workers = 16

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.AddOrUpdateCodeFile(path, "go", "hash"); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	var got []error
	for e := range errs {
		got = append(got, e)
	}
	if len(got) > 0 {
		t.Errorf("parallel AddOrUpdateCodeFile produced %d errors; want 0. first: %v", len(got), got[0])
	}

	// Exactly one row should exist.
	cf, err := s.GetCodeFileByPath(path)
	if err != nil {
		t.Fatalf("GetCodeFileByPath: %v", err)
	}
	if cf == nil {
		t.Fatal("expected a row after parallel inserts; got nil")
	}
}

// TestEnsureColumn_Idempotent exercises the additive-migration path: opening
// the same DB file twice must not error (content_hash should be a no-op on
// the second Open because the column already exists).
func TestEnsureColumn_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test.db"

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	s1.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open (idempotent migration): %v", err)
	}
	s2.Close()
}
