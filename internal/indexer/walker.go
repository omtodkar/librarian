package indexer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type WalkResult struct {
	FilePath string // Relative path from working directory (e.g., "docs/auth.md")
	AbsPath  string // Absolute path on disk
}

// defaultGraphExcludeDirs lists directory names the graph pass always prunes
// before descending. Covers the heavyweight output / dependency / IDE-metadata
// / build-tool-cache trees that virtually every project ignores, plus the
// framework-cache directories common in JS/TS monorepos.
//
// `.git` and `.librarian` are NOT in this list — they're in hardGraphExcludes
// (authoritative guard) so a future include-override mechanism can't resurface
// them.
//
// Directory-name matching fires at any depth — a nested `apps/web/node_modules`
// is pruned the same way as a root-level `node_modules` because the walker
// tests every dir's basename as it descends.
var defaultGraphExcludeDirs = map[string]bool{
	"node_modules":  true,
	"vendor":        true,
	"target":        true,
	"build":         true,
	"dist":          true,
	"out":           true,
	"__pycache__":   true,
	".venv":         true,
	"venv":          true,
	".next":         true,
	".nuxt":         true,
	".svelte-kit":   true,
	".dart_tool":    true,
	".idea":         true,
	".vscode":       true,
	"coverage":      true,
	".turbo":        true,
	".nx":           true,
	".yarn":         true,
	".cache":        true,
	".parcel-cache": true,
}

// defaultGraphExcludeGlobs lists filepath.Match patterns applied to a
// directory's basename during descent. Used for name families that can't
// be enumerated exactly: Python package metadata (`*.egg-info`) and Bazel
// workspace symlinks (`bazel-bin`, `bazel-out`, `bazel-testlogs`, and
// `bazel-<repo-name>` — one per workspace).
var defaultGraphExcludeGlobs = []string{
	"*.egg-info",
	"bazel-*",
}

// hardGraphExcludes are the directory names the user cannot override, no
// matter what ends up in `graph.exclude_patterns` / `.gitignore` /
// include-style overrides. `.git` contents are git plumbing; `.librarian`
// is our own workspace. Walking either serves no purpose and indexing
// them can corrupt state.
var hardGraphExcludes = map[string]bool{
	".git":       true,
	".librarian": true,
}

// matchesGlobPattern reports whether relPath is matched by pattern using
// filepath.Match plus a custom `**` substring fallback. Shared between
// WalkDocs and WalkGraph so both passes interpret user exclude patterns
// consistently. `filepath.Match` doesn't understand `**` globstars; the
// fallback strips the globstar segment and tests a substring match — a
// rough approximation that handles `node_modules/**` as "any path
// containing `node_modules/`".
func matchesGlobPattern(pattern, relPath string) bool {
	if matched, _ := filepath.Match(pattern, relPath); matched {
		return true
	}
	if strings.Contains(pattern, "**") {
		base := strings.ReplaceAll(pattern, "**"+string(filepath.Separator), "")
		base = strings.ReplaceAll(base, "**", "")
		if base != "" && strings.Contains(relPath, base) {
			return true
		}
	}
	return false
}

// GraphWalkConfig tunes WalkGraph behaviour.
//
// Distinct from config.GraphConfig: the walker has no knowledge of
// DetectGenerated (applied per-file in indexGraphFile), MaxWorkers, or
// ProgressMode — those operate above the walker layer. SkipFormats is an
// indexer concern (which handler formats the docs pass already covers)
// that doesn't belong in user-facing config. The three overlapping fields
// (HonorGitignore, ExcludePatterns, Roots) are translated explicitly in
// IndexProjectGraph.
type GraphWalkConfig struct {
	// HonorGitignore loads .gitignore files (root + nested) and skips paths
	// they ignore. Off = walk everything not matched by other exclusions.
	HonorGitignore bool

	// ExcludePatterns stack on top of the built-in defaults. Each is a
	// filepath.Match glob evaluated against the project-root-relative path.
	// Matches against a directory prune the whole subtree.
	ExcludePatterns []string

	// Roots, if non-empty, restricts the walk to these subdirectories of
	// rootDir (relative paths). Empty = walk the whole rootDir. Lets a
	// monorepo user graph-index only the slice they care about.
	Roots []string

	// SkipFormats lists handler.Name() values (e.g. "markdown", "docx",
	// "pdf") to skip. Used so files already covered by the docs pass
	// aren't double-indexed when docs_dir is under the project root.
	SkipFormats map[string]bool
}

// WalkGraph walks the project root and returns files eligible for the graph
// pass. Files are included only when:
//
//  1. Their extension has a registered handler.
//  2. Their handler is NOT in cfg.SkipFormats.
//  3. They're not matched by the hard excludes (.git, .librarian).
//  4. They're not matched by the built-in default directory excludes.
//  5. They're not matched by cfg.ExcludePatterns.
//  6. They're not ignored by any applicable .gitignore (when cfg.HonorGitignore).
//
// Returned FilePath is relative to rootDir so graph_nodes store consistent
// project-relative paths regardless of CWD.
func WalkGraph(rootDir string, cfg GraphWalkConfig, reg *Registry) ([]WalkResult, error) {
	absProject, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}

	var gi *gitignoreMatcher
	if cfg.HonorGitignore {
		gi, err = loadGitignores(absProject)
		if err != nil {
			return nil, err
		}
	}

	// matchesPattern applies user ExcludePatterns to a project-relative path,
	// delegating to matchesGlobPattern so both walkers interpret patterns
	// consistently.
	matchesPattern := func(relPath string) bool {
		for _, pattern := range cfg.ExcludePatterns {
			if matchesGlobPattern(pattern, relPath) {
				return true
			}
		}
		return false
	}

	skipDir := func(relFromProject, name string) bool {
		if defaultGraphExcludeDirs[name] {
			return true
		}
		for _, pattern := range defaultGraphExcludeGlobs {
			if matched, _ := filepath.Match(pattern, name); matched {
				return true
			}
		}
		if matchesPattern(relFromProject) {
			return true
		}
		if gi != nil && gi.Matches(relFromProject) {
			return true
		}
		return false
	}

	skipFile := func(relFromProject string, handler FileHandler) bool {
		if cfg.SkipFormats[handler.Name()] {
			return true
		}
		if matchesPattern(relFromProject) {
			return true
		}
		if gi != nil && gi.Matches(relFromProject) {
			return true
		}
		return false
	}

	// Resolve walk targets. When Roots is empty, walk the whole project;
	// otherwise walk each sub-root. Paths are always reported relative to
	// absProject so gitignore / exclude matching sees consistent inputs
	// regardless of which sub-root a file lives under.
	targets := []string{absProject}
	if len(cfg.Roots) > 0 {
		targets = targets[:0]
		for _, r := range cfg.Roots {
			abs := r
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(absProject, r)
			}
			targets = append(targets, abs)
		}
	}

	var results []WalkResult
	seen := make(map[string]bool)
	for _, target := range targets {
		// Skip non-existent roots silently-with-warning. A monorepo user
		// may configure `graph.roots: [services/auth, services/billing]`
		// before all those services exist in their checkout — aborting
		// the whole graph pass on a missing root would be hostile.
		// Warning goes to stderr so scripts parsing stdout (e.g. --json)
		// aren't affected.
		if _, statErr := os.Stat(target); os.IsNotExist(statErr) {
			fmt.Fprintf(os.Stderr, "librarian: graph.roots entry %q not found, skipping\n", target)
			continue
		}
		err := filepath.Walk(target, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			relFromProject, relErr := filepath.Rel(absProject, path)
			if relErr != nil {
				return relErr
			}

			if info.IsDir() {
				if relFromProject == "." {
					return nil
				}
				// Hard excludes apply first, by name alone, so a rogue
				// include_patterns override can never resurface them.
				if hardGraphExcludes[info.Name()] {
					return filepath.SkipDir
				}
				if skipDir(relFromProject, info.Name()) {
					return filepath.SkipDir
				}
				return nil
			}

			handler := reg.HandlerFor(path)
			if handler == nil {
				return nil
			}
			if skipFile(relFromProject, handler) {
				return nil
			}

			// Dedup: when cfg.Roots contains overlapping paths (e.g. both
			// "services" and "services/auth"), the same file could appear
			// twice without this guard.
			if seen[relFromProject] {
				return nil
			}
			seen[relFromProject] = true
			results = append(results, WalkResult{
				FilePath: relFromProject,
				AbsPath:  path,
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return results, nil
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
			// Reuse the hard-exclude and default-dir lists the graph pass
			// owns, so adding a new entry in one place propagates to both
			// walkers. The docs pass only needs these four historical
			// names, but letting WalkDocs see the full list is harmless —
			// nobody legitimately has a `docs/target/` tree they want
			// indexed as prose.
			name := info.Name()
			if hardGraphExcludes[name] || defaultGraphExcludeDirs[name] {
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

		// Check exclude patterns — same semantics as WalkGraph via the
		// shared matchesGlobPattern helper.
		for _, pattern := range excludePatterns {
			if matchesGlobPattern(pattern, relPath) {
				return nil
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
