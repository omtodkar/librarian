package install

import (
	"io"
	"path/filepath"

	"librarian/internal/workspace"
)

func claudePlatform() *Platform {
	return &Platform{
		Name: "Claude Code",
		Key:  "claude",
		Detected: func(root string) bool {
			return fileExists(filepath.Join(root, "CLAUDE.md")) ||
				fileExists(filepath.Join(root, ".claude", "settings.json"))
		},
		Install: func(ws *workspace.Workspace, _ io.Writer) ([]string, error) {
			return installMarkerAndHook(ws, "CLAUDE.md", ".claude/settings.json")
		},
	}
}
