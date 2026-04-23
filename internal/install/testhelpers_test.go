package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Shared test helpers used by install_test.go and jsonhook_test.go. Kept in a
// dedicated file so feature-named test files (jsonhook_test.go, etc.) don't
// grow unrelated infrastructure.

func mustWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("invalid JSON at %s: %v\n%s", path, err, b)
	}
	return m
}

func assertJSONContainsHook(t *testing.T, path, event, command string) {
	t.Helper()
	m := mustReadJSON(t, path)
	hooks, _ := m["hooks"].(map[string]any)
	entries, _ := hooks[event].([]any)
	for _, e := range entries {
		inner, _ := e.(map[string]any)["hooks"].([]any)
		for _, h := range inner {
			if h.(map[string]any)["command"].(string) == command {
				return
			}
		}
	}
	t.Errorf("expected %s hook %q in %s; not found", event, command, path)
}
