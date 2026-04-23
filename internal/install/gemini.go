package install

import (
	"io"
	"path/filepath"

	"librarian/internal/workspace"
)

// Gemini CLI's hook API is evolving. GEMINI.md is the guaranteed integration
// path; the .gemini/settings.json entry is best-effort using the Claude-style
// JSON shape and harmless if the platform ignores it.
func geminiPlatform() *Platform {
	return &Platform{
		Name: "Gemini CLI",
		Key:  "gemini",
		Detected: func(root string) bool {
			return fileExists(filepath.Join(root, "GEMINI.md")) ||
				fileExists(filepath.Join(root, ".gemini"))
		},
		Install: func(ws *workspace.Workspace, _ io.Writer) ([]string, error) {
			return installMarkerAndHook(ws, "GEMINI.md", ".gemini/settings.json")
		},
	}
}
