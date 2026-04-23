package install

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"librarian/internal/workspace"
)

// newWS scaffolds a complete librarian workspace (mkdir .librarian/ + .git/)
// and returns the *workspace.Workspace. Used by every end-to-end test below.
func newWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{".librarian", ".librarian/out", ".librarian/hooks", ".git"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &workspace.Workspace{Root: dir}
}

func TestRun_AllPlatformsEndToEnd(t *testing.T) {
	ws := newWS(t)
	var outBuf bytes.Buffer

	written, err := Run(ws, Options{All: true, Out: &outBuf})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(written) == 0 {
		t.Fatal("expected Run to write files, got none")
	}

	// Shared templates exist.
	mustExist(t, ws.RulesPath())
	mustExist(t, ws.SkillPath())
	hookPath := filepath.Join(ws.HooksDir(), "sessionstart.sh")
	mustExist(t, hookPath)
	info, err := os.Stat(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("%s not executable (mode=%v)", hookPath, info.Mode().Perm())
	}

	// Per-platform artefacts.
	mustContain(t, filepath.Join(ws.Root, "CLAUDE.md"), "librarian")
	mustContain(t, filepath.Join(ws.Root, "AGENTS.md"), "librarian")
	mustContain(t, filepath.Join(ws.Root, "GEMINI.md"), "librarian")
	mustContain(t, filepath.Join(ws.Root, ".cursor", "rules", "librarian.mdc"), "Librarian")

	// JSON hook files.
	for _, jsonPath := range []string{
		filepath.Join(ws.Root, ".claude", "settings.json"),
		filepath.Join(ws.Root, ".codex", "hooks.json"),
		filepath.Join(ws.Root, ".gemini", "settings.json"),
	} {
		var doc map[string]any
		b, err := os.ReadFile(jsonPath)
		if err != nil {
			t.Fatalf("reading %s: %v", jsonPath, err)
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatalf("invalid JSON at %s: %v", jsonPath, err)
		}
		hooks, _ := doc["hooks"].(map[string]any)
		if _, ok := hooks["SessionStart"].([]any); !ok {
			t.Errorf("no SessionStart hook in %s", jsonPath)
		}
	}

	// Git hook.
	mustContain(t, filepath.Join(ws.Root, ".git", "hooks", "post-commit"), "librarian index")
}

func TestRun_IdempotentReRun(t *testing.T) {
	ws := newWS(t)
	buf := &bytes.Buffer{}

	if _, err := Run(ws, Options{All: true, Out: buf}); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotDir(t, ws.Root)

	// Second run with the same options: no file contents should change.
	if _, err := Run(ws, Options{All: true, Out: buf}); err != nil {
		t.Fatal(err)
	}
	after := snapshotDir(t, ws.Root)
	for path, content := range snapshot {
		if after[path] != content {
			t.Errorf("reinstall changed %s:\n--- before ---\n%s\n--- after ---\n%s",
				path, content, after[path])
		}
	}
}

func TestRun_SecondRunReportsNothingWritten(t *testing.T) {
	ws := newWS(t)
	buf := &bytes.Buffer{}

	if _, err := Run(ws, Options{All: true, Out: buf}); err != nil {
		t.Fatal(err)
	}
	written, err := Run(ws, Options{All: true, Out: buf})
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != 0 {
		t.Errorf("second Run should report 0 files written, got %d: %v", len(written), written)
	}
}

func TestRun_PreservesUserCLAUDEContent(t *testing.T) {
	ws := newWS(t)
	claudePath := filepath.Join(ws.Root, "CLAUDE.md")
	userPreface := "# My Project\n\nProject instructions above librarian.\n\n"
	if err := os.WriteFile(claudePath, []byte(userPreface), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ws, Options{Platforms: []string{"claude"}, NoGitHook: true, Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	content := readString(t, claudePath)
	if !strings.Contains(content, "# My Project") {
		t.Errorf("user content lost:\n%s", content)
	}
	if !strings.Contains(content, "Librarian") {
		t.Errorf("librarian block missing:\n%s", content)
	}
}

func TestRun_PreservesExistingClaudeSettingsHooks(t *testing.T) {
	ws := newWS(t)
	settingsPath := filepath.Join(ws.Root, ".claude", "settings.json")
	mustWriteJSON(t, settingsPath, map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{"type": "command", "command": "bd prime"},
					},
				},
			},
			"PreCompact": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{"type": "command", "command": "bd prime"},
					},
				},
			},
		},
	})

	if _, err := Run(ws, Options{Platforms: []string{"claude"}, NoGitHook: true, Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}

	doc := mustReadJSON(t, settingsPath)
	hooks := doc["hooks"].(map[string]any)

	// bd prime SessionStart must still be there.
	sessionEntries := hooks["SessionStart"].([]any)
	if len(sessionEntries) != 2 {
		t.Errorf("expected 2 SessionStart entries, got %d", len(sessionEntries))
	}
	// PreCompact must be untouched.
	preCompact := hooks["PreCompact"].([]any)
	if len(preCompact) != 1 {
		t.Errorf("PreCompact was clobbered, got %d entries", len(preCompact))
	}
}

func TestRun_DryRunWritesNothing(t *testing.T) {
	ws := newWS(t)
	if _, err := Run(ws, Options{All: true, DryRun: true, Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	// No files should have been created besides the pre-seeded workspace skeleton.
	if _, err := os.Stat(filepath.Join(ws.Root, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("dry-run wrote CLAUDE.md: err=%v", err)
	}
	if _, err := os.Stat(ws.RulesPath()); !os.IsNotExist(err) {
		t.Errorf("dry-run wrote rules.md: err=%v", err)
	}
}

func TestRun_UnknownPlatformErrors(t *testing.T) {
	ws := newWS(t)
	_, err := Run(ws, Options{Platforms: []string{"copilot"}, NoGitHook: true, Out: &bytes.Buffer{}})
	if err == nil {
		t.Error("expected error for unknown platform, got nil")
	}
	if !strings.Contains(err.Error(), "copilot") {
		t.Errorf("error should name unknown platform, got: %v", err)
	}
}

// -- helpers --

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file at %s, got err: %v", path, err)
	}
}

func mustContain(t *testing.T, path, substr string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if !strings.Contains(string(b), substr) {
		t.Errorf("%s missing %q:\n%s", path, substr, b)
	}
}

// snapshotDir returns a map of path -> content for every regular file under root.
// Used to assert that a reinstall leaves every file byte-identical.
func snapshotDir(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
