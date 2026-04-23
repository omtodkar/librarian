// Package workspace locates and represents a .librarian/ project workspace and
// exposes the paths librarian writes and reads.
//
// Discovery walks up from a starting directory looking for a .librarian/ directory,
// the same way git locates its repo root. All librarian-owned state lives under
// .librarian/; platform-owned integration points (CLAUDE.md, .cursor/rules/, etc.)
// are expected to be thin pointers to files inside the workspace.
package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DirName is the directory name that marks a librarian workspace root.
const DirName = ".librarian"

// ErrNotFound is returned by Find when no .librarian/ directory is reachable by
// walking up from the start directory to the filesystem root.
var ErrNotFound = errors.New("no librarian workspace found (run 'librarian init')")

// Workspace identifies a project root that contains a .librarian/ directory.
type Workspace struct {
	// Root is the absolute path of the directory containing .librarian/.
	Root string
}

// Find walks up from startDir looking for a .librarian/ directory. Returns
// ErrNotFound if none exists up to the filesystem root.
func Find(startDir string) (*Workspace, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return nil, fmt.Errorf("resolving start directory: %w", err)
	}
	dir := abs
	for {
		candidate := filepath.Join(dir, DirName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return &Workspace{Root: dir}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, ErrNotFound
		}
		dir = parent
	}
}

// Dir returns the .librarian/ directory path.
func (w *Workspace) Dir() string { return filepath.Join(w.Root, DirName) }

// ConfigPath returns the path to .librarian/config.yaml.
func (w *Workspace) ConfigPath() string { return filepath.Join(w.Dir(), "config.yaml") }

// DBPath returns the path to the SQLite database.
func (w *Workspace) DBPath() string { return filepath.Join(w.Dir(), "librarian.db") }

// OutDir returns the directory that holds generated artifacts (GRAPH_REPORT.md,
// graph.html, graph.json, cache/).
func (w *Workspace) OutDir() string { return filepath.Join(w.Dir(), "out") }

// CacheDir returns the SHA256 per-file cache directory under out/.
func (w *Workspace) CacheDir() string { return filepath.Join(w.OutDir(), "cache") }

// IgnorePath returns .librarian/ignore — gitignore-style excludes applied by the
// indexer on top of user-configured patterns.
func (w *Workspace) IgnorePath() string { return filepath.Join(w.Dir(), "ignore") }

// RulesPath returns .librarian/rules.md — the single source of truth for assistant
// instructions. Platform-owned files (CLAUDE.md, AGENTS.md, etc.) point here.
func (w *Workspace) RulesPath() string { return filepath.Join(w.Dir(), "rules.md") }

// SkillPath returns .librarian/skill.md — the skill definition invoked by /librarian.
func (w *Workspace) SkillPath() string { return filepath.Join(w.Dir(), "skill.md") }

// HooksDir returns the shell-shim directory that platform hook configs reference.
func (w *Workspace) HooksDir() string { return filepath.Join(w.Dir(), "hooks") }

// GitIgnorePath returns .librarian/.gitignore — the nested gitignore that marks
// user-specific artifacts (librarian.db, ephemeral outputs) so they aren't committed.
func (w *Workspace) GitIgnorePath() string { return filepath.Join(w.Dir(), ".gitignore") }
