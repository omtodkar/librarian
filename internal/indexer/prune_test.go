package indexer_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"librarian/internal/config"
	"librarian/internal/indexer"
	_ "librarian/internal/indexer/handlers/defaults" // register markdown, code handlers
	"librarian/internal/store"
)

// chdir switches the test process into dir for the duration of the test
// and restores the original working directory on cleanup. Used by tests
// that exercise IndexDirectory with a relative docsDir — WalkDocs resolves
// it via filepath.Abs which would otherwise anchor on the package dir.
func chdir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prev)
	})
}

// newPruneTestIndexer builds an Indexer rooted at projectDir suitable for
// exercising both the docs and graph passes; mirrors newGraphTestIndexer
// but lives in this file to keep the prune-suite self-contained.
func newPruneTestIndexer(t *testing.T, projectDir string) (*indexer.Indexer, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(projectDir, ".librarian", "test.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(dbPath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	cfg := &config.Config{
		DocsDir:     filepath.Join(projectDir, "docs"),
		DBPath:      dbPath,
		ProjectRoot: projectDir,
		Chunking: config.ChunkingConfig{
			MaxTokens:    512,
			OverlapLines: 0,
			MinTokens:    1,
		},
		Graph: config.GraphConfig{
			HonorGitignore:  false,
			DetectGenerated: true,
		},
		CodeFilePatterns: []string{"*.go"},
	}
	return indexer.New(s, cfg, fakeEmbedder{dim: 4}), s
}

// TestPruneMissingFiles_CodeFiles indexes a project with two Go files,
// deletes one on disk, runs the sweep, and asserts the deleted file's
// code_files row plus all of its symbol nodes are gone, while the other
// file (and its symbols) survive untouched. This is the headline
// "lib-7gyz M3 catches files deleted in isolation" assertion.
func TestPruneMissingFiles_CodeFiles(t *testing.T) {
	tmp := t.TempDir()
	keepRel := "keep/keep.go"
	dropRel := "drop/drop.go"

	files := map[string]string{
		keepRel: "package keep\n\nfunc Keep() string { return \"keep\" }\n",
		dropRel: "package drop\n\nfunc Drop() string { return \"drop\" }\n",
	}
	for rel, content := range files {
		abs := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	idx, s := newPruneTestIndexer(t, tmp)
	if _, err := idx.IndexProjectGraph(tmp, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	// Sanity: both code_files rows exist before the sweep.
	codeFilesBefore, err := s.ListCodeFiles()
	if err != nil {
		t.Fatalf("ListCodeFiles before: %v", err)
	}
	if len(codeFilesBefore) != 2 {
		t.Fatalf("expected 2 code_files rows before sweep, got %d (%+v)", len(codeFilesBefore), codeFilesBefore)
	}

	// Sanity: the dropped file has at least one symbol node so the
	// post-sweep "no symbols" assertion is meaningful.
	dropSymbolsBefore, err := s.ListNodesByKind(store.NodeKindSymbol)
	if err != nil {
		t.Fatalf("ListNodesByKind before: %v", err)
	}
	dropSymbolCountBefore := 0
	for _, n := range dropSymbolsBefore {
		if n.SourcePath == dropRel {
			dropSymbolCountBefore++
		}
	}
	if dropSymbolCountBefore == 0 {
		t.Fatalf("expected at least 1 symbol node sourced from %s before sweep, got 0", dropRel)
	}

	// Delete one file from disk while leaving the row in the DB.
	if err := os.Remove(filepath.Join(tmp, dropRel)); err != nil {
		t.Fatalf("remove drop file: %v", err)
	}

	res, err := idx.PruneMissingFiles(tmp)
	if err != nil {
		t.Fatalf("PruneMissingFiles: %v", err)
	}
	if res.CodeFilesDeleted != 1 {
		t.Errorf("CodeFilesDeleted = %d, want 1", res.CodeFilesDeleted)
	}
	if res.DocumentsDeleted != 0 {
		t.Errorf("DocumentsDeleted = %d, want 0", res.DocumentsDeleted)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %v", res.Errors)
	}

	// The kept file's row survives; the dropped file's row is gone.
	codeFilesAfter, err := s.ListCodeFiles()
	if err != nil {
		t.Fatalf("ListCodeFiles after: %v", err)
	}
	if len(codeFilesAfter) != 1 {
		t.Fatalf("expected 1 code_files row after sweep, got %d (%+v)", len(codeFilesAfter), codeFilesAfter)
	}
	if codeFilesAfter[0].FilePath != keepRel {
		t.Errorf("surviving code_file = %q, want %q", codeFilesAfter[0].FilePath, keepRel)
	}

	// And every symbol node sourced from the deleted file is gone too —
	// confirms PruneMissingFiles routed through DeleteGeneratedFile (which
	// tears down symbols + file node + code_files row in one transaction).
	symbolsAfter, err := s.ListNodesByKind(store.NodeKindSymbol)
	if err != nil {
		t.Fatalf("ListNodesByKind after: %v", err)
	}
	for _, n := range symbolsAfter {
		if n.SourcePath == dropRel {
			t.Errorf("symbol node still references deleted file: id=%s label=%s", n.ID, n.Label)
		}
	}
}

// TestPruneMissingFiles_Documents indexes a docs/ tree with two markdown
// files, deletes one, runs the sweep, and asserts only the deleted file's
// documents row is gone.
func TestPruneMissingFiles_Documents(t *testing.T) {
	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keepRel := "docs/keep.md"
	dropRel := "docs/drop.md"
	if err := os.WriteFile(filepath.Join(tmp, keepRel),
		[]byte("# Keep\n\nThis file stays.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, dropRel),
		[]byte("# Drop\n\nThis file gets removed before the sweep.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, s := newPruneTestIndexer(t, tmp)
	// Index using the relative form "docs" so stored FilePath values are
	// "docs/keep.md" / "docs/drop.md" — matching how the CLI invokes it.
	// chdir to tmp for the duration of the docs pass so filepath.Abs("docs")
	// inside WalkDocs resolves under the fixture rather than the test pkg dir.
	chdir(t, tmp)
	if _, err := idx.IndexDirectory("docs", true); err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}

	docsBefore, err := s.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments before: %v", err)
	}
	if len(docsBefore) != 2 {
		t.Fatalf("expected 2 documents before sweep, got %d", len(docsBefore))
	}

	if err := os.Remove(filepath.Join(tmp, dropRel)); err != nil {
		t.Fatalf("remove drop doc: %v", err)
	}

	res, err := idx.PruneMissingFiles(tmp)
	if err != nil {
		t.Fatalf("PruneMissingFiles: %v", err)
	}
	if res.DocumentsDeleted != 1 {
		t.Errorf("DocumentsDeleted = %d, want 1", res.DocumentsDeleted)
	}
	if res.CodeFilesDeleted != 0 {
		t.Errorf("CodeFilesDeleted = %d, want 0", res.CodeFilesDeleted)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %v", res.Errors)
	}

	docsAfter, err := s.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments after: %v", err)
	}
	if len(docsAfter) != 1 {
		t.Fatalf("expected 1 document after sweep, got %d", len(docsAfter))
	}
	if !strings.HasSuffix(docsAfter[0].FilePath, "keep.md") {
		t.Errorf("surviving document = %q, want a path ending in keep.md", docsAfter[0].FilePath)
	}
}

// TestPruneMissingFiles_Mixed deletes one code file AND one document in
// the same sweep and verifies PruneResult counts both independently.
func TestPruneMissingFiles_Mixed(t *testing.T) {
	tmp := t.TempDir()
	docsDir := filepath.Join(tmp, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	codeFiles := map[string]string{
		"keep.go": "package main\n\nfunc Keep() {}\n",
		"drop.go": "package main\n\nfunc Drop() {}\n",
	}
	for rel, content := range codeFiles {
		if err := os.WriteFile(filepath.Join(tmp, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	docFiles := map[string]string{
		"docs/keep.md": "# Keep\n\nstays\n",
		"docs/drop.md": "# Drop\n\ngoes\n",
	}
	for rel, content := range docFiles {
		if err := os.WriteFile(filepath.Join(tmp, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	idx, s := newPruneTestIndexer(t, tmp)
	if _, err := idx.IndexProjectGraph(tmp, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}
	chdir(t, tmp)
	if _, err := idx.IndexDirectory("docs", true); err != nil {
		t.Fatalf("IndexDirectory: %v", err)
	}

	if err := os.Remove(filepath.Join(tmp, "drop.go")); err != nil {
		t.Fatalf("remove drop.go: %v", err)
	}
	if err := os.Remove(filepath.Join(tmp, "docs/drop.md")); err != nil {
		t.Fatalf("remove drop.md: %v", err)
	}

	res, err := idx.PruneMissingFiles(tmp)
	if err != nil {
		t.Fatalf("PruneMissingFiles: %v", err)
	}
	if res.CodeFilesDeleted != 1 {
		t.Errorf("CodeFilesDeleted = %d, want 1", res.CodeFilesDeleted)
	}
	if res.DocumentsDeleted != 1 {
		t.Errorf("DocumentsDeleted = %d, want 1", res.DocumentsDeleted)
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %v", res.Errors)
	}

	codeFilesAfter, err := s.ListCodeFiles()
	if err != nil {
		t.Fatalf("ListCodeFiles after: %v", err)
	}
	if len(codeFilesAfter) != 1 || !strings.HasSuffix(codeFilesAfter[0].FilePath, "keep.go") {
		t.Errorf("post-sweep code_files = %+v, want exactly one keep.go", codeFilesAfter)
	}
	docsAfter, err := s.ListDocuments()
	if err != nil {
		t.Fatalf("ListDocuments after: %v", err)
	}
	if len(docsAfter) != 1 || !strings.HasSuffix(docsAfter[0].FilePath, "keep.md") {
		t.Errorf("post-sweep documents = %+v, want exactly one keep.md", docsAfter)
	}
}

// TestPruneMissingFiles_NoOpWhenAllFilesPresent confirms the sweep is a
// no-op when nothing is missing on disk — important to keep --prune-missing
// safe to run on every index without surprising users with stale-row
// false positives caused by transient ENOENT-equivalents.
func TestPruneMissingFiles_NoOpWhenAllFilesPresent(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "a.go"),
		[]byte("package main\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, s := newPruneTestIndexer(t, tmp)
	if _, err := idx.IndexProjectGraph(tmp, true); err != nil {
		t.Fatalf("IndexProjectGraph: %v", err)
	}

	res, err := idx.PruneMissingFiles(tmp)
	if err != nil {
		t.Fatalf("PruneMissingFiles: %v", err)
	}
	if res.CodeFilesDeleted != 0 || res.DocumentsDeleted != 0 || len(res.Errors) != 0 {
		t.Errorf("expected no-op sweep, got %+v", res)
	}
	codeFilesAfter, err := s.ListCodeFiles()
	if err != nil {
		t.Fatalf("ListCodeFiles after: %v", err)
	}
	if len(codeFilesAfter) != 1 {
		t.Errorf("expected 1 code_files row preserved, got %d", len(codeFilesAfter))
	}
}
