// Package models manages local model artifacts (reranker ONNX weights, the
// ONNX Runtime shared library) used by the in-process onnx rerank provider.
//
// Artifacts live in a single shared, cross-workspace cache directory — NOT
// under a project's .librarian/, because the weights (hundreds of MB) are
// reused across every workspace on the machine. The layout mirrors the XDG
// convention already used by scripts/infinity.sh.
package models

import (
	"fmt"
	"os"
	"path/filepath"
)

// DataDir resolves the shared models cache directory. Precedence:
//
//  1. LIBRARIAN_MODELS_DIR (explicit override)
//  2. $XDG_DATA_HOME/librarian/models
//  3. ~/.local/share/librarian/models
//
// It does not create the directory — resolution is pure; Pull creates dirs as
// needed. (Windows uses the ~/.local/share fallback for v1; honoring
// %LOCALAPPDATA% is a tracked follow-up.)
func DataDir() (string, error) {
	if dir := os.Getenv("LIBRARIAN_MODELS_DIR"); dir != "" {
		return dir, nil
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "librarian", "models"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory for models cache: %w", err)
	}
	return filepath.Join(home, ".local", "share", "librarian", "models"), nil
}
