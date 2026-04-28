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

	written, err := Run(ws, InstallOptions{All: true, Out: &outBuf})
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
	mustContain(t, filepath.Join(ws.Root, ".github", "copilot-instructions.md"), "librarian")
	mustContain(t, filepath.Join(ws.Root, "CONVENTIONS.md"), "librarian")

	// JSON hook files. Gemini intentionally omitted — Gemini CLI has no
	// SessionStart hook API, so writing .gemini/settings.json would claim
	// success for a no-op.
	for _, jsonPath := range []string{
		filepath.Join(ws.Root, ".claude", "settings.json"),
		filepath.Join(ws.Root, ".codex", "hooks.json"),
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
	// Gemini's pointer file is written; its JSON hook file is not.
	if _, err := os.Stat(filepath.Join(ws.Root, ".gemini", "settings.json")); !os.IsNotExist(err) {
		t.Errorf(".gemini/settings.json should not be written (no Gemini hooks API): err=%v", err)
	}

	// Claude Code skill must land at .claude/skills/librarian/SKILL.md so
	// /librarian is actually invocable.
	mustExist(t, filepath.Join(ws.Root, ".claude", "skills", "librarian", "SKILL.md"))

	// Git hook — template uses a resolved $bin variable so the literal
	// "librarian index" doesn't appear; assert on the invariant call shape.
	mustContain(t, filepath.Join(ws.Root, ".git", "hooks", "post-commit"), `"$bin" index`)
}

func TestRun_IdempotentReRun(t *testing.T) {
	ws := newWS(t)
	buf := &bytes.Buffer{}

	if _, err := Run(ws, InstallOptions{All: true, Out: buf}); err != nil {
		t.Fatal(err)
	}
	snapshot := snapshotDir(t, ws.Root)

	// Second run with the same options: no file contents should change.
	if _, err := Run(ws, InstallOptions{All: true, Out: buf}); err != nil {
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

	if _, err := Run(ws, InstallOptions{All: true, Out: buf}); err != nil {
		t.Fatal(err)
	}
	written, err := Run(ws, InstallOptions{All: true, Out: buf})
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
	if _, err := Run(ws, InstallOptions{Platforms: []string{"claude"}, NoGitHook: true, Out: &bytes.Buffer{}}); err != nil {
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

	if _, err := Run(ws, InstallOptions{Platforms: []string{"claude"}, NoGitHook: true, Out: &bytes.Buffer{}}); err != nil {
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
	if _, err := Run(ws, InstallOptions{All: true, DryRun: true, Out: &bytes.Buffer{}}); err != nil {
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
	_, err := Run(ws, InstallOptions{Platforms: []string{"nonesuch"}, NoGitHook: true, Out: &bytes.Buffer{}})
	if err == nil {
		t.Error("expected error for unknown platform, got nil")
	}
	if !strings.Contains(err.Error(), "nonesuch") {
		t.Errorf("error should name unknown platform, got: %v", err)
	}
	// Updated "known" list must name the new platforms so users see them in the
	// error message.
	for _, want := range []string{"aider", "copilot", "opencode"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message should list %q as known; got: %v", want, err)
		}
	}
}

// Codex and OpenCode both point at AGENTS.md. Installing both must produce
// exactly one librarian block, with start preceding end — the
// upsertMarkedBlock idempotency is what makes shared pointer files safe,
// and a regression could re-introduce duplicate or inverted blocks.
func TestRun_OpenCodeSharesCodexAGENTS(t *testing.T) {
	ws := newWS(t)
	buf := &bytes.Buffer{}
	if _, err := Run(ws, InstallOptions{Platforms: []string{"codex", "opencode"}, NoGitHook: true, Out: buf}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	body := readString(t, filepath.Join(ws.Root, "AGENTS.md"))
	if got := strings.Count(body, "<!-- librarian:start"); got != 1 {
		t.Errorf("expected exactly 1 librarian:start marker, got %d:\n%s", got, body)
	}
	if got := strings.Count(body, "<!-- librarian:end"); got != 1 {
		t.Errorf("expected exactly 1 librarian:end marker, got %d:\n%s", got, body)
	}
	startIdx := strings.Index(body, "<!-- librarian:start")
	endIdx := strings.Index(body, "<!-- librarian:end")
	if startIdx < 0 || endIdx < 0 || startIdx >= endIdx {
		t.Errorf("markers out of order: start=%d end=%d body=%q", startIdx, endIdx, body)
	}
}

// Copilot's pointer lives at a nested path that doesn't exist pre-install.
// upsertBlockInFile MkdirAlls the parent when the file is missing; regression
// guard that Copilot benefits from it.
func TestRun_CopilotCreatesGithubDir(t *testing.T) {
	ws := newWS(t)
	if _, err := Run(ws, InstallOptions{Platforms: []string{"copilot"}, NoGitHook: true, Out: &bytes.Buffer{}}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	pointer := filepath.Join(ws.Root, ".github", "copilot-instructions.md")
	mustExist(t, pointer)
	mustContain(t, pointer, "librarian:start")
}

// Copilot's .github/copilot-instructions.md is more likely than CLAUDE.md to
// contain pre-existing user content (.github/ is commonly populated with
// workflows and other metadata). Mirrors TestRun_PreservesUserCLAUDEContent
// to guard that the librarian block is appended rather than clobbering.
func TestRun_PreservesUserCopilotContent(t *testing.T) {
	ws := newWS(t)
	pointerPath := filepath.Join(ws.Root, ".github", "copilot-instructions.md")
	if err := os.MkdirAll(filepath.Dir(pointerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	userPreface := "# Custom Copilot Rules\n\nExisting repo-specific Copilot guidance.\n\n"
	if err := os.WriteFile(pointerPath, []byte(userPreface), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ws, InstallOptions{Platforms: []string{"copilot"}, NoGitHook: true, Out: &bytes.Buffer{}}); err != nil {
		t.Fatal(err)
	}
	content := readString(t, pointerPath)
	if !strings.Contains(content, "Custom Copilot Rules") {
		t.Errorf("user content lost:\n%s", content)
	}
	if !strings.Contains(content, "librarian:start") {
		t.Errorf("librarian block missing:\n%s", content)
	}
}

// Aider has no hook API AND no auto-discovery for CONVENTIONS.md; users must
// add it to .aider.conf.yml themselves. The installer surfaces this via the
// warn writer only when something changed — a no-op reinstall stays silent
// so users aren't nagged every run.
func TestRun_AiderPrintsPostInstallNote(t *testing.T) {
	ws := newWS(t)

	// First install — note must print.
	firstBuf := &bytes.Buffer{}
	if _, err := Run(ws, InstallOptions{Platforms: []string{"aider"}, NoGitHook: true, Out: firstBuf}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mustContain(t, filepath.Join(ws.Root, "CONVENTIONS.md"), "librarian:start")
	if !strings.Contains(firstBuf.String(), ".aider.conf.yml") {
		t.Errorf("first install: expected .aider.conf.yml note; got:\n%s", firstBuf.String())
	}

	// Second install — CONVENTIONS.md already up to date, note must not repeat.
	secondBuf := &bytes.Buffer{}
	if _, err := Run(ws, InstallOptions{Platforms: []string{"aider"}, NoGitHook: true, Out: secondBuf}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(secondBuf.String(), ".aider.conf.yml") {
		t.Errorf("idempotent reinstall should not repeat the note; got:\n%s", secondBuf.String())
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
