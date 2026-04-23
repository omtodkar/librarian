package install

import (
	"io"
	"path/filepath"

	"librarian/internal/workspace"
)

// Codex's hook API is less stable than Claude Code's at the time of writing.
// The AGENTS.md pointer is the guaranteed integration path; the .codex/hooks.json
// entry is best-effort and harmless if Codex ignores the file.
func codexPlatform() *Platform {
	return &Platform{
		Name: "Codex",
		Key:  "codex",
		Detected: func(root string) bool {
			return fileExists(filepath.Join(root, "AGENTS.md")) ||
				fileExists(filepath.Join(root, ".codex"))
		},
		Install: func(ws *workspace.Workspace, _ io.Writer) ([]string, error) {
			return installMarkerAndHook(ws, "AGENTS.md", ".codex/hooks.json")
		},
	}
}
