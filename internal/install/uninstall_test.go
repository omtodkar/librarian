package install

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"librarian/internal/workspace"
)

// newWS and snapshotDir are defined in install_test.go — reused here.

// TestUninstall_RoundTripPreservesUserContent pins the core round-trip
// invariant: seed a file with user prose, install the librarian block,
// uninstall, and confirm the file returns to its pre-install state
// byte-for-byte.
func TestUninstall_RoundTripPreservesUserContent(t *testing.T) {
	ws := newWS(t)
	userOriginal := "# My Project\n\nUser prose here.\n"
	if err := os.WriteFile(filepath.Join(ws.Root, "CLAUDE.md"), []byte(userOriginal), 0o644); err != nil {
		t.Fatal(err)
	}

	// Install just Claude so we're exercising the marker path.
	var warn bytes.Buffer
	if _, err := Run(ws, InstallOptions{Platforms: []string{"claude"}, NoGitHook: true, Out: &warn}); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Uninstall.
	if _, err := Uninstall(ws, UninstallOptions{Platforms: []string{"claude"}, Out: &warn}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(ws.Root, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if string(got) != userOriginal {
		t.Errorf("round-trip lost user content:\n got=%q\nwant=%q", string(got), userOriginal)
	}
}

// TestUninstall_FullRemovesWorkspace pins --full: after install + uninstall
// --full --yes, the .librarian/ directory is gone and no pointer files
// remain at the project root.
func TestUninstall_FullRemovesWorkspace(t *testing.T) {
	ws := newWS(t)

	var warn bytes.Buffer
	if _, err := Run(ws, InstallOptions{All: true, NoGitHook: true, Out: &warn}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := os.Stat(ws.Dir()); err != nil {
		t.Fatalf("workspace should exist post-install: %v", err)
	}

	if _, err := Uninstall(ws, UninstallOptions{All: true, Full: true, Yes: true, Out: &warn}); err != nil {
		t.Fatalf("uninstall --full: %v", err)
	}
	if _, err := os.Stat(ws.Dir()); !os.IsNotExist(err) {
		t.Errorf("workspace should be deleted; err=%v", err)
	}

	// No pointer files should remain.
	for _, name := range []string{"CLAUDE.md", "AGENTS.md", "GEMINI.md", "CONVENTIONS.md"} {
		if _, err := os.Stat(filepath.Join(ws.Root, name)); err == nil {
			t.Errorf("%s should be removed; still present", name)
		}
	}
}

// TestUninstall_PreservesBdPrimeHook pins the safety contract for JSON
// hook configs: a non-librarian SessionStart entry must survive uninstall.
func TestUninstall_PreservesBdPrimeHook(t *testing.T) {
	ws := newWS(t)

	// Seed settings.json with a bd prime hook before install.
	claudeDir := filepath.Join(ws.Root, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "bd prime"}
        ]
      }
    ]
  }
}
`
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	if _, err := Run(ws, InstallOptions{Platforms: []string{"claude"}, NoGitHook: true, Out: &warn}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := Uninstall(ws, UninstallOptions{Platforms: []string{"claude"}, Out: &warn}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json should still exist: %v", err)
	}
	if !strings.Contains(string(got), "bd prime") {
		t.Errorf("bd prime hook was lost:\n%s", got)
	}
	if strings.Contains(string(got), "librarian/hooks/sessionstart.sh") {
		t.Errorf("librarian hook should be gone:\n%s", got)
	}
}

// TestUninstall_Idempotent pins that running uninstall twice produces zero
// changes on the second call.
func TestUninstall_Idempotent(t *testing.T) {
	ws := newWS(t)

	var warn bytes.Buffer
	if _, err := Run(ws, InstallOptions{All: true, NoGitHook: true, Out: &warn}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := Uninstall(ws, UninstallOptions{All: true, Out: &warn}); err != nil {
		t.Fatalf("first uninstall: %v", err)
	}

	second, err := Uninstall(ws, UninstallOptions{All: true, Out: &warn})
	if err != nil {
		t.Fatalf("second uninstall: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second uninstall should be a no-op; got %d paths: %v", len(second), second)
	}
}

// TestUninstall_DryRunWritesNothing pins that --dry-run mutates nothing.
func TestUninstall_DryRunWritesNothing(t *testing.T) {
	ws := newWS(t)

	var warn bytes.Buffer
	if _, err := Run(ws, InstallOptions{All: true, NoGitHook: true, Out: &warn}); err != nil {
		t.Fatalf("install: %v", err)
	}
	before := snapshotDir(t, ws.Root)

	if _, err := Uninstall(ws, UninstallOptions{All: true, DryRun: true, Out: &warn}); err != nil {
		t.Fatalf("dry-run uninstall: %v", err)
	}
	after := snapshotDir(t, ws.Root)

	if len(before) != len(after) {
		t.Errorf("dry-run changed file count: %d → %d", len(before), len(after))
	}
	for path, content := range before {
		if after[path] != content {
			t.Errorf("dry-run changed %s:\n before=%q\n after =%q", path, content, after[path])
		}
	}
}

// TestUninstall_FullSkipsMissingWorkspace pins graceful handling when
// .librarian/ doesn't exist — --full --yes should no-op cleanly.
func TestUninstall_FullSkipsMissingWorkspace(t *testing.T) {
	dir := t.TempDir()
	// No .librarian/ ever created.
	ws := &workspace.Workspace{Root: dir}

	var warn bytes.Buffer
	if _, err := Uninstall(ws, UninstallOptions{All: true, Full: true, Yes: true, Out: &warn}); err != nil {
		t.Fatalf("uninstall --full on empty dir should not error: %v", err)
	}
}

// TestUninstall_SharedAGENTSMD pins the documented behaviour that
// uninstalling Codex (or OpenCode) strips the AGENTS.md block for both —
// a single marker block backs both platforms.
func TestUninstall_SharedAGENTSMD(t *testing.T) {
	ws := newWS(t)

	var warn bytes.Buffer
	if _, err := Run(ws, InstallOptions{Platforms: []string{"codex", "opencode"}, NoGitHook: true, Out: &warn}); err != nil {
		t.Fatalf("install: %v", err)
	}
	agents := filepath.Join(ws.Root, "AGENTS.md")
	if _, err := os.Stat(agents); err != nil {
		t.Fatalf("AGENTS.md should exist post-install: %v", err)
	}

	// Uninstall Codex only — the shared block should still be stripped.
	if _, err := Uninstall(ws, UninstallOptions{Platforms: []string{"codex"}, Out: &warn}); err != nil {
		t.Fatalf("uninstall codex: %v", err)
	}
	if _, err := os.Stat(agents); !os.IsNotExist(err) {
		t.Errorf("AGENTS.md block should be stripped (file deleted since it was librarian-only); got %v", err)
	}
}

// TestUninstall_TornBlockWarnsAndSkips pins conservative handling of a
// CLAUDE.md with start marker but no end marker — warn, leave untouched.
func TestUninstall_TornBlockWarnsAndSkips(t *testing.T) {
	ws := newWS(t)

	torn := "# User Content\n\n" + markerStart + "\nsome body without end marker\nstill more content\n"
	claudePath := filepath.Join(ws.Root, "CLAUDE.md")
	if err := os.WriteFile(claudePath, []byte(torn), 0o644); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	if _, err := Uninstall(ws, UninstallOptions{Platforms: []string{"claude"}, Out: &warn}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	got, _ := os.ReadFile(claudePath)
	if string(got) != torn {
		t.Errorf("torn block should have been left untouched:\n got=%q\nwant=%q", string(got), torn)
	}
	if !strings.Contains(warn.String(), "torn librarian block") {
		t.Errorf("expected torn-block warning; got %q", warn.String())
	}
}

// TestUninstall_UnknownPlatformErrors pins that naming a bogus platform
// produces a clear error listing valid keys.
func TestUninstall_UnknownPlatformErrors(t *testing.T) {
	ws := newWS(t)
	_, err := Uninstall(ws, UninstallOptions{Platforms: []string{"bogus-platform"}, Out: &bytes.Buffer{}})
	if err == nil {
		t.Fatal("expected error for unknown platform")
	}
	if !strings.Contains(err.Error(), "bogus-platform") {
		t.Errorf("error should name the bad key: %v", err)
	}
	for _, known := range PlatformKeys() {
		if !strings.Contains(err.Error(), known) {
			t.Errorf("error should list valid key %q: %v", known, err)
		}
	}
}

// TestUninstall_CursorRmdirEmptyParents pins that .cursor/rules/ is
// rmdir'd when librarian.mdc was its only file.
func TestUninstall_CursorRmdirEmptyParents(t *testing.T) {
	ws := newWS(t)

	var warn bytes.Buffer
	if _, err := Run(ws, InstallOptions{Platforms: []string{"cursor"}, NoGitHook: true, Out: &warn}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, err := Uninstall(ws, UninstallOptions{Platforms: []string{"cursor"}, Out: &warn}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	rulesDir := filepath.Join(ws.Root, ".cursor", "rules")
	if _, err := os.Stat(rulesDir); !os.IsNotExist(err) {
		// Dir present → inspect; it may remain if Cursor had other rules,
		// but in this test it was librarian-only.
		entries, _ := os.ReadDir(rulesDir)
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		sort.Strings(names)
		t.Errorf(".cursor/rules/ should be rmdir'd when librarian.mdc was its only file; found: %v", names)
	}
}
// TestRemoveMarkedBlock_PreservesUserContent pins that user text above
// the librarian block survives byte-for-byte. Replicates the real install
// shape (block appended at end of file), so uninstall should return the
// file to its pre-install state.
func TestRemoveMarkedBlock_PreservesUserContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	// What the user had pre-install (note trailing newline — typical
	// markdown file shape).
	userOriginal := "# User Heading\n\nUser prose here.\n"
	// What the install would leave behind: appendBlock adds a blank-line
	// separator plus the marker-wrapped block.
	installed := userOriginal + "\n" + markerStart + "\nlibrarian-owned body\n" + markerEnd + "\n"
	if err := os.WriteFile(path, []byte(installed), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := removeMarkedBlock(path, nil)
	if err != nil {
		t.Fatalf("removeMarkedBlock: %v", err)
	}
	if !changed {
		t.Error("expected changed=true")
	}

	got, _ := os.ReadFile(path)
	if string(got) != userOriginal {
		t.Errorf("content should restore to pre-install:\n got=%q\nwant=%q", string(got), userOriginal)
	}
}

// TestRemoveMarkedBlock_DeletesLibrarianOnlyFile pins that a file
// containing only the librarian block is removed from disk entirely —
// prevents orphaned empty CLAUDE.md / AGENTS.md files after uninstall.
func TestRemoveMarkedBlock_DeletesLibrarianOnlyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	content := markerStart + "\nlibrarian body\n" + markerEnd + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := removeMarkedBlock(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("expected changed=true")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should have been deleted (empty after block removal); err=%v", err)
	}
}

// TestRemoveMarkedBlock_MissingFileIsNoOp pins idempotency: a second run
// when the file is already gone must return (false, nil).
func TestRemoveMarkedBlock_MissingFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "never-existed.md")

	changed, err := removeMarkedBlock(path, nil)
	if err != nil {
		t.Errorf("missing file should not error; got %v", err)
	}
	if changed {
		t.Error("expected changed=false for missing file")
	}
}

// TestRemoveMarkedBlock_NoMarkersIsNoOp pins that a file with no markers
// (user never installed, or already uninstalled) is left untouched.
func TestRemoveMarkedBlock_NoMarkersIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	content := "# User prose only\n\nNo librarian markers here.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := removeMarkedBlock(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected changed=false when no markers present")
	}
	got, _ := os.ReadFile(path)
	if string(got) != content {
		t.Error("content mutated despite no markers")
	}
}

// TestRemoveMarkedBlock_TornBlockWarnsAndSkips pins conservative behaviour:
// a file with a start marker but no end marker gets a warning and is left
// untouched — uninstall refuses to destructively delete past a stray marker.
func TestRemoveMarkedBlock_TornBlockWarnsAndSkips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")
	content := "# User Prose\n\n" + markerStart + "\nbody without end marker\n\nmore content.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	var warn bytes.Buffer
	changed, err := removeMarkedBlock(path, &warn)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected changed=false on torn block")
	}
	got, _ := os.ReadFile(path)
	if string(got) != content {
		t.Errorf("torn block should have been left untouched:\n got=%q\nwant=%q", string(got), content)
	}
	if !strings.Contains(warn.String(), "torn librarian block") {
		t.Errorf("expected torn-block warning; got %q", warn.String())
	}
}

// TestRemoveJSONHook_PreservesOtherEntries pins the PR-critical invariant:
// a SessionStart entry wrapping a `bd prime` command must survive when we
// remove only the librarian entry.
func TestRemoveJSONHook_PreservesOtherEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// Two SessionStart entries: bd prime (keep) + librarian (remove).
	initial := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "bd prime"}
        ]
      },
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "bash .librarian/hooks/sessionstart.sh"}
        ]
      }
    ]
  }
}
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := removeJSONHook(path, "SessionStart", hookCommand)
	if err != nil {
		t.Fatalf("removeJSONHook: %v", err)
	}
	if !changed {
		t.Error("expected changed=true when librarian entry was present")
	}

	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "bd prime") {
		t.Error("bd prime entry was lost during removal")
	}
	if strings.Contains(string(got), "librarian/hooks/sessionstart.sh") {
		t.Error("librarian entry should be gone")
	}
}

// TestRemoveJSONHook_EmptyHooksKeyGetsDeleted pins that when librarian was
// the sole SessionStart entry, the empty `"hooks": {}` wrapper doesn't
// linger in the user's settings.json.
func TestRemoveJSONHook_EmptyHooksKeyGetsDeleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	initial := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "bash .librarian/hooks/sessionstart.sh"}
        ]
      }
    ]
  },
  "otherSetting": true
}
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := removeJSONHook(path, "SessionStart", hookCommand); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), `"hooks"`) {
		t.Error("empty hooks key should have been pruned")
	}
	if !strings.Contains(string(got), `"otherSetting"`) {
		t.Error("unrelated otherSetting should remain")
	}
}

// TestRemoveJSONHook_MissingFileIsNoOp pins idempotency for settings.json
// that never existed.
func TestRemoveJSONHook_MissingFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "never.json")

	changed, err := removeJSONHook(path, "SessionStart", hookCommand)
	if err != nil {
		t.Errorf("missing file should not error: %v", err)
	}
	if changed {
		t.Error("expected changed=false for missing file")
	}
}

// TestRemoveJSONHook_NoLibrarianEntryIsNoOp pins that a settings.json
// with only `bd prime` (never had librarian) is left untouched.
func TestRemoveJSONHook_NoLibrarianEntryIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	initial := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "bd prime"}
        ]
      }
    ]
  }
}
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := removeJSONHook(path, "SessionStart", hookCommand)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("expected changed=false when no librarian entry present")
	}
	got, _ := os.ReadFile(path)
	if !strings.Contains(string(got), "bd prime") {
		t.Error("bd prime entry should remain")
	}
}

// TestRemoveJSONHook_SecondCallIsNoOp pins idempotency under repeated
// invocation — running uninstall twice must not keep rewriting the file.
func TestRemoveJSONHook_SecondCallIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	initial := `{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {"type": "command", "command": "bash .librarian/hooks/sessionstart.sh"}
        ]
      }
    ]
  }
}
`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := removeJSONHook(path, "SessionStart", hookCommand); err != nil {
		t.Fatal(err)
	}
	changed, err := removeJSONHook(path, "SessionStart", hookCommand)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second call should be a no-op")
	}
}
