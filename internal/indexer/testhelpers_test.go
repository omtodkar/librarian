package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile creates parent directories then writes content at dir/relPath.
// Shared across gitignore_test.go, walker_graph_test.go, and any other
// internal-package test that needs quick filesystem fixtures.
func writeFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	abs := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdirall %s: %v", abs, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("writefile %s: %v", abs, err)
	}
}
