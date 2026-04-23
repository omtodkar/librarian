package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeHandler is a minimal FileHandler for walker tests. It reports a configurable
// set of extensions and does nothing else.
type fakeHandler struct {
	name string
	exts []string
}

func (f fakeHandler) Name() string                                    { return f.name }
func (f fakeHandler) Extensions() []string                            { return f.exts }
func (f fakeHandler) Parse(string, []byte) (*ParsedDoc, error)        { return nil, nil }
func (f fakeHandler) Chunk(*ParsedDoc, ChunkOpts) ([]Chunk, error)    { return nil, nil }

func writeTestFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// TestWalkDocs_EmptyRegistryYieldsEmptyWalk verifies that when the registry has no
// handlers, WalkDocs returns zero files. This is the acceptance gate for the
// registry-driven filtering behavior.
func TestWalkDocs_EmptyRegistryYieldsEmptyWalk(t *testing.T) {
	dir := t.TempDir()
	writeTestFiles(t, dir, "a.md", "b.markdown", "c.txt", "d.yaml")

	reg := NewRegistry()
	files, err := WalkDocs(dir, nil, reg)
	if err != nil {
		t.Fatalf("WalkDocs: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("empty registry returned %d files, want 0: %v", len(files), files)
	}
}

// TestWalkDocs_FilterByRegisteredExtensions verifies that only files whose extensions
// have a registered handler are returned.
func TestWalkDocs_FilterByRegisteredExtensions(t *testing.T) {
	dir := t.TempDir()
	writeTestFiles(t, dir, "a.md", "b.markdown", "c.txt", "d.yaml")

	reg := NewRegistry()
	reg.Register(fakeHandler{name: "mdfake", exts: []string{".md", ".markdown"}})

	files, err := WalkDocs(dir, nil, reg)
	if err != nil {
		t.Fatalf("WalkDocs: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(files), files)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[filepath.Base(f.FilePath)] = true
	}
	if !got["a.md"] || !got["b.markdown"] {
		t.Errorf("expected a.md and b.markdown, got %v", files)
	}
	if got["c.txt"] || got["d.yaml"] {
		t.Errorf("unregistered extensions leaked through: %v", files)
	}
}

// TestWalkDocs_ExcludePatternsStillApply verifies the exclude-pattern logic continues
// to skip files even when the registry would otherwise accept them.
func TestWalkDocs_ExcludePatternsStillApply(t *testing.T) {
	dir := t.TempDir()
	writeTestFiles(t, dir, "keep.md", "skip.md")

	reg := NewRegistry()
	reg.Register(fakeHandler{name: "mdfake", exts: []string{".md"}})

	files, err := WalkDocs(dir, []string{"skip.md"}, reg)
	if err != nil {
		t.Fatalf("WalkDocs: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0].FilePath) != "keep.md" {
		t.Errorf("exclude pattern failed: got %v", files)
	}
}
