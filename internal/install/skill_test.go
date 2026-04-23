package install

import (
	"os"
	"path/filepath"
	"testing"

	"librarian/internal/workspace"
)

// Claude Code discovers skills only from .claude/skills/<name>/SKILL.md, so
// installClaudeSkill is critical to the /librarian slash-skill actually firing.
// These tests cover the three paths that aren't exercised by the end-to-end
// TestRun_AllPlatformsEndToEnd: idempotent no-op, user-edit propagation from
// the workspace copy, and template fallback when the workspace copy is missing.

func TestInstallClaudeSkill_IdempotentWhenUnchanged(t *testing.T) {
	ws := newWS(t)
	// Pre-seed the workspace copy so installClaudeSkill reads from disk.
	if err := os.WriteFile(ws.SkillPath(), []byte(tmplSkillMD), 0o644); err != nil {
		t.Fatal(err)
	}

	written1, err := installClaudeSkill(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(written1) != 1 {
		t.Fatalf("expected 1 path on first install, got %d: %v", len(written1), written1)
	}
	written2, err := installClaudeSkill(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(written2) != 0 {
		t.Errorf("second call with matching content should report no writes; got %v", written2)
	}
}

func TestInstallClaudeSkill_PropagatesUserEditsFromWorkspaceCopy(t *testing.T) {
	ws := newWS(t)
	// User edits the canonical workspace copy (documented behaviour).
	customBody := "---\nname: librarian\ndescription: custom\n---\n# Custom body\n"
	if err := os.WriteFile(ws.SkillPath(), []byte(customBody), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := installClaudeSkill(ws); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(ws.Root, ".claude", "skills", "librarian", "SKILL.md")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != customBody {
		t.Errorf("user edits to .librarian/skill.md did not propagate:\nwant:\n%s\ngot:\n%s",
			customBody, got)
	}
}

func TestInstallClaudeSkill_FallsBackToTemplateWhenWorkspaceCopyMissing(t *testing.T) {
	dir := t.TempDir()
	// Deliberately no .librarian/skill.md — exercise the template fallback
	// that protects against racy install ordering.
	if err := os.MkdirAll(filepath.Join(dir, ".librarian"), 0o755); err != nil {
		t.Fatal(err)
	}
	ws := &workspace.Workspace{Root: dir}

	if _, err := installClaudeSkill(ws); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(ws.Root, ".claude", "skills", "librarian", "SKILL.md")
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != tmplSkillMD {
		t.Errorf("expected template fallback body; got %d bytes that don't match embed", len(got))
	}
}
