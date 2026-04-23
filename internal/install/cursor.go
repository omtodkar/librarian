package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"librarian/internal/workspace"
)

// cursorPlatform returns the Cursor installer. Writes:
//   - .cursor/rules/librarian.mdc — dedicated rule file (alwaysApply: true)
//
// No pointer block in user-owned files because Cursor doesn't read CLAUDE.md/
// AGENTS.md; the .mdc file is its native integration point.
func cursorPlatform() *Platform {
	return &Platform{
		Name: "Cursor",
		Key:  "cursor",
		Detected: func(root string) bool {
			return fileExists(filepath.Join(root, ".cursor"))
		},
		Install: installCursor,
	}
}

func installCursor(ws *workspace.Workspace, _ io.Writer) ([]string, error) {
	path := filepath.Join(ws.Root, ".cursor", "rules", "librarian.mdc")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating .cursor/rules: %w", err)
	}

	// Cursor rule file is librarian-owned in its entirety — always overwrite
	// so template updates land on re-install.
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == tmplCursorPointer {
		return nil, nil
	}
	if err := os.WriteFile(path, []byte(tmplCursorPointer), 0o644); err != nil {
		return nil, fmt.Errorf("writing %s: %w", path, err)
	}
	return []string{path}, nil
}
