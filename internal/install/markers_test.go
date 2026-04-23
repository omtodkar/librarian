package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertMarkedBlock_FreshFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	changed, err := upsertMarkedBlock(path, "hello world")
	if err != nil {
		t.Fatalf("fresh write: %v", err)
	}
	if !changed {
		t.Error("fresh write should report changed=true")
	}
	content := readString(t, path)
	if !strings.Contains(content, markerStart) || !strings.Contains(content, markerEnd) {
		t.Errorf("markers missing from fresh write:\n%s", content)
	}
	if !strings.Contains(content, "hello world") {
		t.Errorf("body missing from fresh write:\n%s", content)
	}
}

func TestUpsertMarkedBlock_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	if _, err := upsertMarkedBlock(path, "body v1"); err != nil {
		t.Fatal(err)
	}
	changed, err := upsertMarkedBlock(path, "body v1")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second call with identical body should be no-op")
	}
}

func TestUpsertMarkedBlock_ReplacesOldBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	if _, err := upsertMarkedBlock(path, "old body"); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertMarkedBlock(path, "new body"); err != nil {
		t.Fatal(err)
	}
	content := readString(t, path)
	if strings.Contains(content, "old body") {
		t.Errorf("old body still present after replace:\n%s", content)
	}
	if !strings.Contains(content, "new body") {
		t.Errorf("new body missing after replace:\n%s", content)
	}
	// Only one pair of markers — the old block should be fully replaced.
	if strings.Count(content, markerStart) != 1 || strings.Count(content, markerEnd) != 1 {
		t.Errorf("expected exactly one marker pair after replace:\n%s", content)
	}
}

func TestUpsertMarkedBlock_PreservesUserContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	userContent := "# My Project\n\nSome user instructions.\n\n## Section\n\nMore text.\n"
	if err := os.WriteFile(path, []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := upsertMarkedBlock(path, "librarian block"); err != nil {
		t.Fatal(err)
	}
	content := readString(t, path)

	for _, want := range []string{"# My Project", "Some user instructions.", "## Section", "More text."} {
		if !strings.Contains(content, want) {
			t.Errorf("user content %q lost after append:\n%s", want, content)
		}
	}
	if !strings.Contains(content, "librarian block") {
		t.Errorf("librarian block missing:\n%s", content)
	}

	// Re-run: should be a no-op now that markers exist.
	changed, err := upsertMarkedBlock(path, "librarian block")
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second run should not re-append")
	}
}

func TestUpsertMarkedBlock_UserContentAboveAndBelow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	// First install (file didn't exist).
	if _, err := upsertMarkedBlock(path, "librarian v1"); err != nil {
		t.Fatal(err)
	}
	// User adds content above and below the markers.
	initial := readString(t, path)
	manual := "# Header above\n\n" + initial + "\n## Footer below\n"
	if err := os.WriteFile(path, []byte(manual), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reinstall with a different body.
	if _, err := upsertMarkedBlock(path, "librarian v2"); err != nil {
		t.Fatal(err)
	}
	content := readString(t, path)

	if !strings.Contains(content, "# Header above") {
		t.Errorf("user header above lost:\n%s", content)
	}
	if !strings.Contains(content, "## Footer below") {
		t.Errorf("user footer below lost:\n%s", content)
	}
	if strings.Contains(content, "librarian v1") {
		t.Errorf("old librarian body still present:\n%s", content)
	}
	if !strings.Contains(content, "librarian v2") {
		t.Errorf("new librarian body missing:\n%s", content)
	}
}

// Incomplete block: file has markerStart but no markerEnd. Before fix this
// would append a second start marker on reinstall. upsertMarkedBlock must
// detect the torn state and not duplicate.
func TestUpsertMarkedBlock_TornBlockDoesNotDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	torn := "# Project\n\n" + markerStart + "\nstale body (no end marker)\n"
	if err := os.WriteFile(path, []byte(torn), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := upsertMarkedBlock(path, "fresh body"); err != nil {
		t.Fatal(err)
	}
	content := readString(t, path)

	if strings.Count(content, markerStart) > 1 {
		t.Errorf("duplicate start marker after torn-block install:\n%s", content)
	}
	// A correct installer should at minimum not end up with MORE than one
	// block. Either it replaces-from-start (ideal) or it leaves a torn block
	// alone — but it must not accumulate duplicates on repeat runs.
	if _, err := upsertMarkedBlock(path, "fresh body"); err != nil {
		t.Fatal(err)
	}
	content2 := readString(t, path)
	if strings.Count(content2, markerStart) != strings.Count(content, markerStart) {
		t.Errorf("marker count changed between reinstalls (drift):\nfirst=%d\nsecond=%d",
			strings.Count(content, markerStart), strings.Count(content2, markerStart))
	}
}

// CRLF line endings are common on Windows-authored files and in some editors.
// The marker constants are LF-terminated; bytes.Index should still find them
// because the marker strings themselves contain no line-ending bytes. Reinstall
// must not append a duplicate block.
func TestUpsertMarkedBlock_CRLFExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	if _, err := upsertMarkedBlock(path, "body v1"); err != nil {
		t.Fatal(err)
	}

	// Convert the whole file to CRLF line endings, as a Windows editor might.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	crlf := strings.ReplaceAll(string(b), "\n", "\r\n")
	if err := os.WriteFile(path, []byte(crlf), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := upsertMarkedBlock(path, "body v2"); err != nil {
		t.Fatal(err)
	}
	content := readString(t, path)
	if strings.Count(content, markerStart) != 1 {
		t.Errorf("CRLF file accumulated duplicate markers:\n%q", content)
	}
	if !strings.Contains(content, "body v2") {
		t.Errorf("body not updated in CRLF file:\n%q", content)
	}
}

func readString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}
