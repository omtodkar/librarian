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

	replaced := false
	for i, elem := range entries {
		entry, ok := elem.(map[string]any)
		if !ok {
			continue
		}
		inner, _ := entry["hooks"].([]any)
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); cmd == command {
				// Preserve every field on the outer entry (matcher, user-added
				// keys like description) and replace only the inner hooks list.
				// An earlier version rebuilt the entry from scratch and silently
				// reset matcher="*.go" back to "" on reinstall.
				entry["hooks"] = []any{
					map[string]any{"type": "command", "command": command},
				}
				entries[i] = entry
				replaced = true
				break
			}
		}
		if replaced {
			break
		}
	}
	if !replaced {
		entries = append(entries, map[string]any{
			"matcher": "",
			"hooks": []any{
				map[string]any{"type": "command", "command": command},
			},
		})
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
	if err := os.WriteFile(path, out, existingModeOr(path, 0o644)); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

// removeJSONHook is the inverse of upsertJSONHook. It removes every entry in
// root.hooks.<event> whose inner hooks list contains a command matching the
// `command` string. Other entries (e.g. the project's own `bd prime`) are
// preserved untouched; an entry is dropped entirely only when its inner
// hooks list becomes empty after pulling out the librarian hook.
//
// If `hooks` ends up empty after the removal, the top-level `hooks` key is
// deleted so the file doesn't carry a stranded `"hooks": {}` object forever.
// The file itself stays on disk — it's user-owned (.claude/settings.json
// may contain other settings) and deletion would be hostile.
//
// Idempotent: calling twice is a no-op on the second. Missing file → returns
// (false, nil) silently because there's nothing to undo.
func removeJSONHook(path, event, command string) (changed bool, err error) {
	raw, readErr := os.ReadFile(path)
	if os.IsNotExist(readErr) {
		return false, nil
	}
	if readErr != nil {
		return false, fmt.Errorf("reading %s: %w", path, readErr)
	}

	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return false, fmt.Errorf("parsing %s: %w (fix or delete the file and re-run)", path, err)
	}
	if root == nil {
		return false, nil
	}
	hooks, _ := root["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	entries, _ := hooks[event].([]any)
	if len(entries) == 0 {
		return false, nil
	}

	// Filter out any entry whose inner hooks list contains our command.
	// Preserves entries that wrap other commands (e.g. bd prime) verbatim.
	kept := make([]any, 0, len(entries))
	for _, elem := range entries {
		entry, ok := elem.(map[string]any)
		if !ok {
			kept = append(kept, elem)
			continue
		}
		inner, hasInnerArray := entry["hooks"].([]any)
		if !hasInnerArray {
			// Entry lacks the expected `hooks` array — user-authored in a
			// non-standard shape. Preserve verbatim rather than treating
			// absent-or-malformed as "empty" and dropping user config.
			kept = append(kept, entry)
			continue
		}
		var innerKept []any
		for _, h := range inner {
			hm, _ := h.(map[string]any)
			if cmd, _ := hm["command"].(string); cmd == command {
				continue
			}
			innerKept = append(innerKept, h)
		}
		if len(innerKept) == 0 {
			// Whole entry was only the librarian hook — drop it.
			continue
		}
		// Build a fresh entry copy rather than mutating the original map
		// in place. upsertJSONHook follows the same defensive pattern;
		// avoids subtle bugs if the semantic-idempotency compare at the
		// end is ever reordered relative to this update.
		fresh := make(map[string]any, len(entry))
		for k, v := range entry {
			fresh[k] = v
		}
		fresh["hooks"] = innerKept
		kept = append(kept, fresh)
	}

	if len(kept) == 0 {
		delete(hooks, event)
	} else {
		hooks[event] = kept
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	}

	out, err := marshalIndentSortedKeys(root)
	if err != nil {
		return false, fmt.Errorf("serialising %s: %w", path, err)
	}

	// Semantic-idempotency check (same pattern as upsertJSONHook): compare
	// the re-normalised original to our output so reruns don't churn the
	// file over formatting differences.
	if reparsed, err := reparse(raw); err == nil {
		if normalised, err := marshalIndentSortedKeys(reparsed); err == nil && bytes.Equal(out, normalised) {
			return false, nil
		}
	}

	if err := os.WriteFile(path, out, existingModeOr(path, 0o644)); err != nil {
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
