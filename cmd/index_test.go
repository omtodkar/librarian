package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"librarian/internal/config"
	"librarian/internal/store"
)

// TestRunIndex_PruneMissingOnlySweeps wires the CLI command's runIndex
// against a tempdir-backed workspace with --skip-docs --skip-graph
// --prune-missing and confirms:
//
//  1. Neither index pass runs (we'd see embedder API failures otherwise —
//     the openai provider is a placeholder with no live endpoint).
//  2. The sweep deletes a stale code_files row whose path is gone from disk.
//  3. The summary line "Pruned N missing files (M code_files, K documents)."
//     is printed on stdout.
//
// Mirrors the reproducer scenario in lib-7gyz: a worktree got wiped,
// leaving stale code_files rows behind that need a catch-up sweep.
func TestRunIndex_PruneMissingOnlySweeps(t *testing.T) {
	tmp := t.TempDir()

	// Pre-populate the store with a code_files row whose path doesn't
	// exist on disk — simulates the "worktree removed out-of-band" leak.
	dbPath := filepath.Join(tmp, ".librarian", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath, nil, 0)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := s.AddCodeFile("ghost/missing.go", "go", "file"); err != nil {
		s.Close()
		t.Fatalf("seed AddCodeFile: %v", err)
	}
	s.Close()

	// Build a config that points at the tempdir DB and uses an embedder
	// that won't fail at construction time. The skip flags ensure neither
	// pass actually calls Embed, so the missing endpoint is fine.
	prevCfg := cfg
	t.Cleanup(func() { cfg = prevCfg })
	cfg = &config.Config{
		DocsDir:     filepath.Join(tmp, "docs"),
		DBPath:      dbPath,
		ProjectRoot: tmp,
		Chunking: config.ChunkingConfig{
			MaxTokens:    512,
			OverlapLines: 0,
			MinTokens:    1,
		},
		Embedding: config.EmbeddingConfig{
			Provider:  "openai",
			Model:     "test-model",
			BatchSize: 1,
		},
		Graph: config.GraphConfig{
			HonorGitignore:  false,
			DetectGenerated: true,
		},
	}

	// Save and restore the package-global flag vars touched by indexCmd's
	// init(). Each test gets a fresh slate; otherwise a parallel run could
	// see a stale --json and write JSON to stdout instead of the human
	// summary line we're asserting against.
	prevFlags := struct {
		force, dryRun, json, skipDocs, skipGraph, quiet, verbose, prune bool
		workers                                                         int
	}{
		indexForce, indexDryRun, indexJSON, indexSkipDocs, indexSkipGraph,
		indexQuiet, indexVerbose, indexPruneMissing, indexWorkers,
	}
	t.Cleanup(func() {
		indexForce = prevFlags.force
		indexDryRun = prevFlags.dryRun
		indexJSON = prevFlags.json
		indexSkipDocs = prevFlags.skipDocs
		indexSkipGraph = prevFlags.skipGraph
		indexQuiet = prevFlags.quiet
		indexVerbose = prevFlags.verbose
		indexPruneMissing = prevFlags.prune
		indexWorkers = prevFlags.workers
	})
	indexForce = false
	indexDryRun = false
	indexJSON = false
	indexSkipDocs = true
	indexSkipGraph = true
	indexQuiet = false
	indexVerbose = false
	indexPruneMissing = true
	indexWorkers = -1

	stdout := captureStdout(t, func() {
		if err := runIndex(indexCmd, nil); err != nil {
			t.Fatalf("runIndex: %v", err)
		}
	})

	// Verify the row was actually pruned.
	s2, err := store.Open(dbPath, nil, 0)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s2.Close()
	codeFiles, err := s2.ListCodeFiles()
	if err != nil {
		t.Fatalf("ListCodeFiles: %v", err)
	}
	if len(codeFiles) != 0 {
		t.Errorf("expected 0 code_files after prune, got %d (%+v)", len(codeFiles), codeFiles)
	}

	// And the summary line shows up on stdout.
	if !strings.Contains(stdout, "Pruned 1 missing files (1 code_files, 0 documents).") {
		t.Errorf("missing summary line in stdout; got:\n%s", stdout)
	}
}

// TestRunIndex_BothSkipsWithoutPruneRejected pins the validation that
// rejects --skip-docs + --skip-graph when --prune-missing isn't set;
// without it, runIndex would silently no-op which is surprising.
func TestRunIndex_BothSkipsWithoutPruneRejected(t *testing.T) {
	prevFlags := struct {
		skipDocs, skipGraph, prune bool
	}{indexSkipDocs, indexSkipGraph, indexPruneMissing}
	t.Cleanup(func() {
		indexSkipDocs = prevFlags.skipDocs
		indexSkipGraph = prevFlags.skipGraph
		indexPruneMissing = prevFlags.prune
	})
	indexSkipDocs = true
	indexSkipGraph = true
	indexPruneMissing = false

	if err := runIndex(indexCmd, nil); err == nil {
		t.Error("expected error when --skip-docs and --skip-graph are both set without --prune-missing, got nil")
	}
}

// captureStdout swaps os.Stdout for a pipe, runs fn, restores stdout, and
// returns whatever fn wrote. Tests that exercise CLI runE handlers need
// this because the handlers print directly via fmt.Print* rather than
// through an injectable writer.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan struct{})
	var buf bytes.Buffer
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	fn()
	w.Close()
	<-done
	return buf.String()
}
