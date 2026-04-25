package indexer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitignoreMatcher_RootRule(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "*.log\n")

	m, err := loadGitignores(root)
	if err != nil {
		t.Fatalf("loadGitignores: %v", err)
	}
	if !m.Matches("debug.log") {
		t.Errorf("debug.log should be ignored by root *.log")
	}
	if m.Matches("debug.txt") {
		t.Errorf("debug.txt should NOT be ignored")
	}
	if !m.Matches("sub/nested.log") {
		t.Errorf("sub/nested.log should be ignored by root *.log (gitignore *.x is recursive)")
	}
}

// TestGitignoreMatcher_NestedNegation pins the fix for the negation-dropping
// bug: a deeper .gitignore's `!pattern` must un-ignore something the root
// .gitignore ignored. The original Matches impl only ever set matched = true,
// so this case silently reported the file as ignored.
func TestGitignoreMatcher_NestedNegation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "*.log\n")
	writeFile(t, root, "sub/.gitignore", "!important.log\n")

	m, err := loadGitignores(root)
	if err != nil {
		t.Fatalf("loadGitignores: %v", err)
	}

	if !m.Matches("sub/other.log") {
		t.Errorf("sub/other.log should be ignored by root *.log (no negation applies)")
	}
	if m.Matches("sub/important.log") {
		t.Errorf("sub/important.log should be UN-ignored by sub/.gitignore !important.log")
	}
}

func TestGitignoreMatcher_NoGitignores(t *testing.T) {
	root := t.TempDir()

	m, err := loadGitignores(root)
	if err != nil {
		t.Fatalf("loadGitignores: %v", err)
	}
	if m.Matches("anything.go") {
		t.Errorf("empty matcher should ignore nothing")
	}
}

// TestGitignoreMatcher_PrunesNodeModules pins the pre-walk optimisation:
// .gitignore files inside node_modules/vendor must not be loaded (there can
// be thousands in a real project, and the walker won't visit them anyway).
func TestGitignoreMatcher_PrunesNodeModules(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".gitignore", "*.log\n")
	// A .gitignore inside node_modules that would un-ignore *.log if loaded.
	writeFile(t, root, "node_modules/pkg/.gitignore", "!trap.log\n")

	m, err := loadGitignores(root)
	if err != nil {
		t.Fatalf("loadGitignores: %v", err)
	}

	// If the node_modules .gitignore had been loaded, this would be false
	// (the negation would un-ignore trap.log). The prune keeps it ignored,
	// as any reasonable user would expect.
	if !m.Matches("node_modules/pkg/trap.log") {
		t.Errorf("node_modules .gitignore files must not be loaded (found unexpected un-ignore)")
	}
}

// TestGitignoreMatcher_EscapedLiteralBang pins that `\!name` (literal bang,
// not negation) in a nested .gitignore correctly matches the literal file.
//
// Under the hood the translation strips the `\` because after baseDir
// prefixing, the `!` is mid-pattern and no longer ambiguous with gitignore's
// negation syntax (which only applies at pattern position 0). The test
// validates the user-visible behaviour — the file is matched — without
// asserting which internal mechanism achieved it.
func TestGitignoreMatcher_EscapedLiteralBang(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "sub/.gitignore", "\\!trap\n")
	writeFile(t, root, "sub/!trap", "content")
	writeFile(t, root, "sub/normal.log", "content")

	m, err := loadGitignores(root)
	if err != nil {
		t.Fatalf("loadGitignores: %v", err)
	}
	if !m.Matches("sub/!trap") {
		t.Error(`"sub/!trap" should be matched by \!trap in sub/.gitignore`)
	}
	if m.Matches("sub/normal.log") {
		t.Error(`"sub/normal.log" should NOT be matched — \!trap is a literal-name pattern`)
	}

	// translateGitignoreLine directly: the escape should be dropped, the
	// baseDir prefix applied, the hasInternalSlash / recursive decision
	// made correctly. This is the round-2 correction — the round-1 impl
	// kept the `\` which sabhiram ignored mid-pattern.
	got := translateGitignoreLine("\\!trap", "sub")
	want := "sub/**/!trap"
	if got != want {
		t.Errorf("translateGitignoreLine: got %q; want %q", got, want)
	}
}

// TestGitignoreMatcher_MalformedReturnsError pins that a malformed
// .gitignore propagates via error return rather than printing to stderr —
// matches the "errors bubble up" convention of the indexer package.
func TestGitignoreMatcher_MalformedReturnsError(t *testing.T) {
	// sabhiram's CompileIgnoreFile parses line-by-line and is remarkably
	// permissive — there's no easy way to produce a "malformed" content
	// that it rejects. Instead we exercise the failure path by making the
	// file unreadable (permission denied). On systems where the test
	// runner has read-any access (root in some CI containers), skip.
	root := t.TempDir()
	gitignorePath := filepath.Join(root, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("*.log\n"), 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(gitignorePath, 0o644) // restore for cleanup

	_, err := loadGitignores(root)
	if err == nil {
		// Test env may have bypass-read permissions; note but don't fail.
		t.Skip("test environment allowed reading 0o000 file; can't exercise error path")
	}
	if !strings.Contains(err.Error(), "reading gitignore") {
		t.Errorf("error should mention 'reading gitignore': %v", err)
	}
}

func TestGitignoreMatcher_NilReceiver(t *testing.T) {
	var m *gitignoreMatcher
	if m.Matches("anything") {
		t.Errorf("nil matcher should return false")
	}
}
