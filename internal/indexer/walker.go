package indexer

import (
	"os"
	"path/filepath"
	"strings"
)

type WalkResult struct {
	FilePath string // Relative path from working directory (e.g., "docs/auth.md")
	AbsPath  string // Absolute path on disk
}

// WalkDocs walks docsDir and returns files whose extensions have a registered handler
// in reg. Exclude patterns skip matching paths. reg must be non-nil; a registry with no
// handlers yields an empty result.
func WalkDocs(docsDir string, excludePatterns []string, reg *Registry) ([]WalkResult, error) {
	var results []WalkResult

	absDocsDir, err := filepath.Abs(docsDir)
	if err != nil {
		return nil, err
	}

	err = filepath.Walk(absDocsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == ".librarian" {
				return filepath.SkipDir
			}
			return nil
		}

		if reg.HandlerFor(path) == nil {
			return nil
		}

		relPath, err := filepath.Rel(absDocsDir, path)
		if err != nil {
			return err
		}

		// Check exclude patterns
		for _, pattern := range excludePatterns {
			if matched, _ := filepath.Match(pattern, relPath); matched {
				return nil
			}
			// Handle ** glob patterns
			if strings.Contains(pattern, "**") {
				base := strings.ReplaceAll(pattern, "**"+string(filepath.Separator), "")
				base = strings.ReplaceAll(base, "**", "")
				if base != "" && strings.Contains(relPath, base) {
					return nil
				}
			}
		}

		results = append(results, WalkResult{
			FilePath: filepath.Join(docsDir, relPath),
			AbsPath:  path,
		})

		return nil
	})

	return results, err
}
