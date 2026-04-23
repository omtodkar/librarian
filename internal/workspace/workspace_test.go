package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// resolveSymlinks normalises a path through symlinks so comparisons work on
// systems (like macOS) where /tmp is a symlink to /private/tmp.
func resolveSymlinks(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return resolved
}

func TestFind_FoundAtStart(t *testing.T) {
	root := resolveSymlinks(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, DirName), 0o755); err != nil {
		t.Fatal(err)
	}

	ws, err := Find(root)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if resolveSymlinks(t, ws.Root) != root {
		t.Errorf("Root = %q, want %q", ws.Root, root)
	}
}

func TestFind_FoundInAncestor(t *testing.T) {
	root := resolveSymlinks(t, t.TempDir())
	if err := os.MkdirAll(filepath.Join(root, DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(root, "sub", "deep")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	ws, err := Find(subdir)
	if err != nil {
		t.Fatalf("Find from subdir: %v", err)
	}
	if resolveSymlinks(t, ws.Root) != root {
		t.Errorf("Root = %q, want %q", ws.Root, root)
	}
}

func TestFind_NotFound(t *testing.T) {
	root := resolveSymlinks(t, t.TempDir())
	_, err := Find(root)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestFind_IgnoresFile(t *testing.T) {
	// A regular file named .librarian should NOT be treated as a workspace.
	root := resolveSymlinks(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(root, DirName), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Find(root)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound when .librarian is a file, got %v", err)
	}
}

func TestPaths(t *testing.T) {
	ws := &Workspace{Root: filepath.FromSlash("/project")}
	want := map[string]string{
		"Dir":           filepath.FromSlash("/project/.librarian"),
		"ConfigPath":    filepath.FromSlash("/project/.librarian/config.yaml"),
		"DBPath":        filepath.FromSlash("/project/.librarian/librarian.db"),
		"OutDir":        filepath.FromSlash("/project/.librarian/out"),
		"CacheDir":      filepath.FromSlash("/project/.librarian/out/cache"),
		"IgnorePath":    filepath.FromSlash("/project/.librarian/ignore"),
		"RulesPath":     filepath.FromSlash("/project/.librarian/rules.md"),
		"SkillPath":     filepath.FromSlash("/project/.librarian/skill.md"),
		"HooksDir":      filepath.FromSlash("/project/.librarian/hooks"),
		"GitIgnorePath": filepath.FromSlash("/project/.librarian/.gitignore"),
	}

	got := map[string]string{
		"Dir":           ws.Dir(),
		"ConfigPath":    ws.ConfigPath(),
		"DBPath":        ws.DBPath(),
		"OutDir":        ws.OutDir(),
		"CacheDir":      ws.CacheDir(),
		"IgnorePath":    ws.IgnorePath(),
		"RulesPath":     ws.RulesPath(),
		"SkillPath":     ws.SkillPath(),
		"HooksDir":      ws.HooksDir(),
		"GitIgnorePath": ws.GitIgnorePath(),
	}

	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s = %q, want %q", k, got[k], w)
		}
	}
}
