package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// runGit runs git with the given args inside dir and fails the test on error.
// Used to set up real git repos for gitignore tests — mocking `git check-ignore`
// would miss the exit-code handling which is the whole point of these tests.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestFilterGitignored_FlagsIgnoredPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".claude/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeFile := filepath.Join(dir, ".claude", "settings.json")
	codexFile := filepath.Join(dir, ".codex", "hooks.json")
	for _, p := range []string{claudeFile, codexFile} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	ignored := FilterGitignored(dir, []string{claudeFile, codexFile})
	if len(ignored) != 1 || ignored[0] != claudeFile {
		t.Errorf("expected only .claude/settings.json ignored, got %v", ignored)
	}
}

func TestFilterGitignored_NoPathsIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	// No .gitignore — nothing should be flagged.
	tracked := filepath.Join(dir, "README.md")
	if err := os.WriteFile(tracked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ignored := FilterGitignored(dir, []string{tracked}); ignored != nil {
		t.Errorf("expected nil, got %v", ignored)
	}
}

func TestFilterGitignored_NotAGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Outside a git repo, git check-ignore exits 128 — our helper should
	// swallow the ExitError and return nil rather than failing the install.
	if ignored := FilterGitignored(dir, []string{path}); ignored != nil {
		t.Errorf("expected nil (fatal git error swallowed), got %v", ignored)
	}
}

func TestFilterGitignored_EmptyInput(t *testing.T) {
	// No paths — must not shell out at all.
	if ignored := FilterGitignored(t.TempDir(), nil); ignored != nil {
		t.Errorf("empty input should yield nil, got %v", ignored)
	}
}
