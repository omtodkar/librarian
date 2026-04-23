package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// upsertJSONHook merges a librarian SessionStart hook entry into a Claude
// Code-style settings.json. Claude Code and several derivatives (Gemini CLI,
// experimental Codex hooks) accept the same shape:
//
//	{
//	  "hooks": {
//	    "SessionStart": [
//	      { "matcher": "", "hooks": [ { "type": "command", "command": "..." } ] },
//	      ...
//	    ]
//	  }
//	}
//
// We find/replace by command string — any existing entry whose command points at
// the librarian hook script is updated; otherwise a new entry is appended. Other
// hooks (like the project's own `bd prime`) are preserved untouched.
//
// Returns true if the file was changed.
func upsertJSONHook(path, event, command string) (changed bool, err error) {
	raw, readErr := os.ReadFile(path)
	var root map[string]any
	switch {
	case os.IsNotExist(readErr):
		root = map[string]any{}
	case readErr != nil:
		return false, fmt.Errorf("reading %s: %w", path, readErr)
	default:
		if err := json.Unmarshal(raw, &root); err != nil {
			return false, fmt.Errorf("parsing %s: %w (fix or delete the file and re-run)", path, err)
		}
		if root == nil {
			root = map[string]any{}
		}
	}

	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		root["hooks"] = hooks
	}
	entries, _ := hooks[event].([]any)

	newEntry := map[string]any{
		"matcher": "",
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	}

	replaced := false
	for i, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); cmd == command {
				entries[i] = newEntry
				replaced = true
				break
			}
		}
		if replaced {
			break
		}
	}
	if !replaced {
		entries = append(entries, newEntry)
	}
	hooks[event] = entries

	out, err := marshalIndentSortedKeys(root)
	if err != nil {
		return false, fmt.Errorf("serialising %s: %w", path, err)
	}

	// Semantic idempotency: compare after re-normalising the original through
	// the same writer. A byte-level diff would report spurious changes for any
	// file whose original formatting (indent width, key order) differed from
	// encoding/json's defaults, causing reinstalls to churn the file.
	if reparsed, err := reparse(raw); err == nil {
		if normalised, err := marshalIndentSortedKeys(reparsed); err == nil && bytes.Equal(out, normalised) {
			return false, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("creating parent directory for %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

// marshalIndentSortedKeys produces pretty JSON with stable key ordering so
// reinstalling the hook doesn't generate spurious diffs on re-run.
// encoding/json already sorts map keys in v1, but we round-trip explicitly
// through json.Indent to normalise whitespace exactly how Claude Code writes
// its own settings files.
func marshalIndentSortedKeys(v any) ([]byte, error) {
	// json.Marshal on a map[string]any already writes keys sorted.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// reparse round-trips raw JSON bytes through map[string]any. Used to compare
// semantic equality with the installer's output — two files that marshal to
// the same normalised form are considered identical regardless of original
// formatting differences.
func reparse(raw []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]any{}
	}
	return m, nil
}
