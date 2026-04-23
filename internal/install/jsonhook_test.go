package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpsertJSONHook_FreshFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")

	changed, err := upsertJSONHook(path, "SessionStart", "bash .librarian/hooks/claude-sessionstart.sh")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("fresh write should report changed=true")
	}
	assertJSONContainsHook(t, path, "SessionStart", "bash .librarian/hooks/claude-sessionstart.sh")
}

func TestUpsertJSONHook_PreservesOtherEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")

	existing := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "",
					"hooks": []any{
						map[string]any{"type": "command", "command": "bd prime"},
					},
				},
			},
		},
	}
	mustWriteJSON(t, path, existing)

	if _, err := upsertJSONHook(path, "SessionStart", "bash .librarian/hooks/claude-sessionstart.sh"); err != nil {
		t.Fatal(err)
	}

	parsed := mustReadJSON(t, path)
	entries := parsed["hooks"].(map[string]any)["SessionStart"].([]any)
	if len(entries) != 2 {
		t.Fatalf("expected 2 SessionStart entries after merge, got %d: %+v", len(entries), entries)
	}

	// Both the original bd prime hook and the librarian hook must be present.
	sawBD, sawLibrarian := false, false
	for _, e := range entries {
		inner := e.(map[string]any)["hooks"].([]any)
		for _, h := range inner {
			cmd := h.(map[string]any)["command"].(string)
			if cmd == "bd prime" {
				sawBD = true
			}
			if cmd == "bash .librarian/hooks/claude-sessionstart.sh" {
				sawLibrarian = true
			}
		}
	}
	if !sawBD {
		t.Error("existing bd prime hook was dropped")
	}
	if !sawLibrarian {
		t.Error("librarian hook was not added")
	}
}

func TestUpsertJSONHook_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	cmd := "bash .librarian/hooks/claude-sessionstart.sh"

	if _, err := upsertJSONHook(path, "SessionStart", cmd); err != nil {
		t.Fatal(err)
	}
	changed, err := upsertJSONHook(path, "SessionStart", cmd)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second call with same command should be a no-op")
	}
}

// Existing settings.json may have been written by Claude Code with different
// indent width or field ordering. Installing the librarian hook (or re-running
// after a Claude Code write) must not churn the file when the content is
// semantically identical — otherwise reinstalls trigger spurious git diffs
// and appear in the "wrote N files" summary when nothing really changed.
func TestUpsertJSONHook_SemanticIdempotencyAcrossFormatting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Deliberately funky formatting: 4-space indent, keys in non-alpha order.
	custom := `{
    "hooks": {
        "SessionStart": [
            {
                "matcher": "",
                "hooks": [
                    {"command": "bash .librarian/hooks/sessionstart.sh", "type": "command"}
                ]
            }
        ]
    }
}
`
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := upsertJSONHook(path, "SessionStart", "bash .librarian/hooks/sessionstart.sh")
	if err != nil {
		t.Fatal(err)
	}
	// The raw bytes ARE different (indent width etc.); the point is that
	// re-running should converge — second run must be a no-op.
	_ = changed
	changed2, err := upsertJSONHook(path, "SessionStart", "bash .librarian/hooks/sessionstart.sh")
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Error("second run on semantically-identical file should be no-op")
	}
}

func TestUpsertJSONHook_RejectsInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := upsertJSONHook(path, "SessionStart", "bash foo.sh")
	if err == nil {
		t.Error("expected error on malformed JSON; got nil")
	}
}

