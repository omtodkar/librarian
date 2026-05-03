package indexer

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveAndGuard_EmptyPathRejected verifies that an empty storedPath is
// rejected by resolveAndGuard rather than being silently resolved to the
// project root (which would cause the root directory to be stat'd and
// survive the sweep as a false negative).
func TestResolveAndGuard_EmptyPathRejected(t *testing.T) {
	absRoot := t.TempDir()
	path, ok := resolveAndGuard("", absRoot)
	if ok {
		t.Errorf("resolveAndGuard(\"\", absRoot) = (%q, true), want (\"\", false)", path)
	}
}

// TestResolveAndGuard_AbsoluteOutsideRootStatted verifies that an absolute
// storedPath that points outside ProjectRoot is resolved and stat'd normally
// (not rejected). This is the docs_dir-outside-ProjectRoot scenario: a user
// may legitimately have docs_dir: /home/user/notes and those rows should be
// pruneable even though /home/user/notes is not under ProjectRoot.
func TestResolveAndGuard_AbsoluteOutsideRootStatted(t *testing.T) {
	projectRoot := t.TempDir()
	// outsideDir is a sibling temp dir — guaranteed outside projectRoot.
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "external.md")

	resolvedPath, ok := resolveAndGuard(outsidePath, projectRoot)
	if !ok {
		t.Fatalf("resolveAndGuard(%q, %q) = (\"\", false), want (path, true) — absolute paths outside root must not be blocked", outsidePath, projectRoot)
	}
	if resolvedPath != outsidePath {
		t.Errorf("resolvedPath = %q, want %q", resolvedPath, outsidePath)
	}

	// The file doesn't exist on disk yet — stat should return ErrNotExist,
	// confirming the guard didn't short-circuit before the stat.
	_, statErr := os.Stat(resolvedPath)
	if !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("expected ErrNotExist for missing outside file, got %v", statErr)
	}
}
