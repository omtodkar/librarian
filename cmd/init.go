package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"librarian/internal/models"
	"librarian/internal/store"
	"librarian/internal/workspace"
)

var initWithReranker bool
var initDocDirs []string

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a Librarian workspace in the current directory",
	Long: `Creates a .librarian/ workspace in the current directory with default templates
(config.yaml, ignore, .gitignore, empty out/ and hooks/), and opens the SQLite
database.

The workspace holds all librarian-owned state. Platform integration files (CLAUDE.md,
.cursor/rules/, .codex/hooks.json, etc.) are written separately as thin pointers
into the workspace — see 'librarian <platform> install' commands.`,
	RunE: runInit,
}

func init() {
	initCmd.Flags().BoolVar(&initWithReranker, "with-reranker", false,
		"Also download the default in-process ONNX reranker (~300MB model + ONNX runtime)")
	initCmd.Flags().StringArrayVar(&initDocDirs, "doc-dir", nil,
		"Docs directory to index (repeatable; overrides auto-detection)")
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	ws := &workspace.Workspace{Root: cwd}

	for _, d := range []string{ws.Dir(), ws.OutDir(), ws.CacheDir(), ws.HooksDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}

	// Determine the doc_dirs to write into the generated config.
	chosenDirs := resolveDocDirs(cwd, initDocDirs)

	configYAML := buildConfigYAML(chosenDirs)

	writes := []struct {
		path, content string
	}{
		{ws.ConfigPath(), configYAML},
		{ws.IgnorePath(), defaultIgnoreFile},
		{ws.GitIgnorePath(), workspaceGitIgnore},
	}
	for _, w := range writes {
		if err := writeIfMissing(w.path, w.content); err != nil {
			return err
		}
	}

	s, err := store.Open(ws.DBPath(), nil, 0)
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	s.Close()

	fmt.Printf("Initialized Librarian workspace at %s\n\n", ws.Dir())

	// Optional reranker download. The workspace is already initialized, so a
	// pull failure is a warning, not fatal — the user can re-run the pull.
	if initWithReranker {
		id := models.DefaultRerankerID()
		fmt.Printf("Downloading reranker %q (this may take a while)...\n", id)
		if _, err := models.Pull(cmd.Context(), id, models.PullOptions{Progress: os.Stderr}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: reranker download failed: %v\n", err)
			fmt.Fprintf(os.Stderr, "  the workspace is ready; re-run 'librarian models pull %s' to retry.\n", id)
		} else {
			fmt.Printf("Reranker ready. Enable it by setting rerank.provider: onnx in config.yaml.\n")
		}
		fmt.Println()
	}

	fmt.Println("Next steps:")
	fmt.Println("  1. Edit .librarian/config.yaml (doc_dirs, embedding provider, model).")
	fmt.Println("  2. Set LIBRARIAN_EMBEDDING_API_KEY in your environment.")
	fmt.Println("  3. Run 'librarian index' to build the first index.")
	return nil
}

// resolveDocDirs determines which doc directories to write into the generated
// config. It honours the following precedence:
//
//  1. If --doc-dir flags were provided, use them verbatim (no detection, no prompt).
//  2. Otherwise auto-detect immediate children named "docs" (including root docs/).
//  3. If detection found 0 dirs → fall back to ["docs"].
//     If detection found 1 dir → use it directly (no prompt).
//     If detection found >1 dirs AND stdin is a TTY → prompt the user to confirm.
//     In non-TTY contexts (CI, pipe) with >1 dirs → proceed silently.
func resolveDocDirs(root string, explicit []string) []string {
	// Explicit override: skip detection and prompting.
	if len(explicit) > 0 {
		return explicit
	}

	detected := detectDocDirs(root)

	switch {
	case len(detected) == 0:
		// Nothing found — keep the conventional default.
		return []string{"docs"}

	case len(detected) == 1:
		// Exactly one candidate — use it without prompting.
		return detected

	default:
		// Multiple candidates — prompt only in interactive TTY sessions.
		if term.IsTerminal(int(os.Stdin.Fd())) {
			return confirmDocDirs(detected)
		}
		// Non-interactive: proceed with all detected dirs.
		return detected
	}
}

// confirmDocDirs prints the detected list and prompts the user to accept.
// Returns the detected list on acceptance (or empty input = default Yes).
// Falls back to ["docs"] on "n" / "N".
func confirmDocDirs(detected []string) []string {
	fmt.Printf("Detected %d docs directories:\n", len(detected))
	for _, d := range detected {
		fmt.Printf("  - %s\n", d)
	}
	fmt.Printf("Use these %d docs directories? [Y/n] ", len(detected))

	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)

	if strings.EqualFold(line, "n") {
		fmt.Println("Falling back to default 'docs' directory. Edit .librarian/config.yaml to adjust doc_dirs.")
		return []string{"docs"}
	}
	// Empty input or any non-"n" answer → accept.
	return detected
}

// detectDocDirs scans the immediate children of root (and root itself) for
// directories named "docs". It skips hidden directories, .librarian, and
// common noise directories (node_modules, vendor, .git, build, dist, out,
// __pycache__, .venv, target).
//
// The returned list is sorted deterministically and contains relative paths
// (e.g., "docs", "proto/docs", "astro-engine/docs").
func detectDocDirs(root string) []string {
	// Directories to skip when scanning for sub-repos.
	skipDirs := map[string]bool{
		"node_modules": true,
		"vendor":       true,
		".git":         true,
		".librarian":   true,
		"build":        true,
		"dist":         true,
		"out":          true,
		"__pycache__":  true,
		".venv":        true,
		"target":       true,
	}

	var found []string

	// Check if root itself has a docs/ directory.
	if info, err := os.Stat(filepath.Join(root, "docs")); err == nil && info.IsDir() {
		found = append(found, "docs")
	}

	// Scan immediate children of root.
	entries, err := os.ReadDir(root)
	if err != nil {
		return found
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()

		// Skip hidden / dot-prefixed directories.
		if strings.HasPrefix(name, ".") {
			continue
		}
		// Skip noise directories.
		if skipDirs[name] {
			continue
		}
		// Skip "docs" itself — already handled above.
		if name == "docs" {
			continue
		}

		// Check whether this child has a docs/ subdirectory.
		childDocs := filepath.Join(root, name, "docs")
		if info, err := os.Stat(childDocs); err == nil && info.IsDir() {
			found = append(found, filepath.Join(name, "docs"))
		}
	}

	sort.Strings(found)
	return found
}

// yamlQuoteDir returns a YAML double-quoted string for a directory path so
// that names containing special characters (colons, backslashes, quotes, etc.)
// are always syntactically safe in the generated config. Backslash and
// double-quote are the only characters that need escaping inside YAML
// double-quoted scalars.
func yamlQuoteDir(dir string) string {
	// Escape backslash first (must be before quote-escape to avoid doubling).
	dir = strings.ReplaceAll(dir, `\`, `\\`)
	dir = strings.ReplaceAll(dir, `"`, `\"`)
	return `"` + dir + `"`
}

// buildConfigYAML returns the full config.yaml content with a doc_dirs block
// that lists the given directories. All other template content is preserved
// byte-for-byte from the canonical template.
func buildConfigYAML(dirs []string) string {
	var sb strings.Builder
	sb.WriteString(configYAMLHead)
	sb.WriteString("doc_dirs:\n")
	for _, d := range dirs {
		sb.WriteString("  - ")
		sb.WriteString(yamlQuoteDir(d))
		sb.WriteString("\n")
	}
	sb.WriteString(configYAMLTail)
	return sb.String()
}

// configYAMLHead is the portion of the config template that precedes the
// doc_dirs block (including the leading comment and blank line).
const configYAMLHead = `# librarian workspace config — team-wide, safe to commit.
# API keys belong in environment variables (LIBRARIAN_EMBEDDING_API_KEY), not here.

`

// configYAMLTail is the portion of the config template that follows the
// doc_dirs block. All content here is byte-for-byte identical to the old
// defaultConfigYAML after the old "docs_dir: docs" line.
const configYAMLTail = `
embedding:
  provider: gemini         # gemini | openai (or any OpenAI-compatible endpoint)
  model: gemini-embedding-2   # 3072-dim multimodal; older text-embedding-004 is deprecated
  # base_url: for OpenAI-compatible endpoints (e.g. http://localhost:1234/v1)
  # batch_size: 100           # chunks per embedding API call (Gemini cap 100, OpenAI cap 2048)

chunking:
  max_tokens: 512
  min_tokens: 50
  overlap_lines: 3

office:
  # Per-sheet cell caps for .xlsx — prevents a spreadsheets-as-database
  # file from ballooning the index. Truncation is noted inline in the
  # generated markdown.
  xlsx_max_rows: 100
  xlsx_max_cols: 50
  # Include PowerPoint speaker notes as "### Notes" sections per slide.
  include_speaker_notes: true

pdf:
  # Cap on pages indexed per PDF. 0 = unlimited.
  # Large books produce proportional chunks, which can dominate
  # the index if left unbounded.
  max_pages: 0

# Optional cross-encoder reranking, applied after the signal-weighted re-rank.
# Disabled by default (provider empty). Two backends:
#   onnx   — in-process ONNX Runtime, no server. Download weights first:
#            'librarian models pull gte-reranker-modernbert-base'
#            (or 'librarian init --with-reranker'). Weights live in a shared
#            cache outside the repo ($XDG_DATA_HOME/librarian/models).
#   openai — any OpenAI-compatible /rerank endpoint (e.g. Infinity).
# NOTE: the 'model' value differs per backend — a registry short-id for onnx,
# a full HuggingFace id for openai.
# rerank:
#   provider: onnx
#   model: gte-reranker-modernbert-base
#   # model_path: /abs/override   # onnx only; default = shared models cache
#   top_k: 20            # signal-reranked candidates fed to the cross-encoder
#   timeout_ms: 3000     # per-call deadline; falls back to signal ranking on timeout
#
#   # --- openai / Infinity alternative ---
#   # provider: openai
#   # model: Alibaba-NLP/gte-reranker-modernbert-base
#   # base_url: http://127.0.0.1:7997   # NO /v1 prefix for Infinity

# Code-graph pass. Walks the workspace root (not doc_dirs) so 'librarian
# neighbors / path / explain / report' see every source file in the project.
# Separate from doc_dirs because the knowledge base (search_docs / get_context)
# stays curated prose; the graph covers the whole codebase.
graph:
  # Run the code-graph pass. Set to false for a docs-only workspace:
  # search_docs / get_context still work, but neighbors / path / explain /
  # report will have no data. Equivalent to passing --skip-graph on every
  # 'librarian index', without having to remember the flag.
  enabled: true
  # Honor .gitignore (root + nested) — each sub-project's own gitignore
  # already excludes its build outputs, so this covers most cases.
  honor_gitignore: true
  # Skip files whose first ~1 KiB contains a canonical generator banner
  # (Go "Code generated ... DO NOT EDIT.", Meta "@generated", Python
  # "Generated by ... DO NOT EDIT.", HTML "Generated by ..."). Content-
  # based, so hand-written files are never false-positived by extension
  # alone.
  detect_generated: true
  # Stack on top of built-in defaults (.git, .librarian, node_modules,
  # vendor, target, build, dist, out, __pycache__, .venv, .next, .nuxt,
  # .svelte-kit, .dart_tool, .idea, .vscode, coverage, .turbo, .nx, .yarn,
  # .cache, .parcel-cache, *.egg-info, bazel-*).
  # exclude_patterns:
  #   - "generated/**"
  # Restrict the graph walk to these subdirectories (empty = walk all).
  # Useful for monorepos when you're only touching a slice.
  # roots:
  #   - services/auth
  #   - libs/core
  # Goroutines the graph pass may use. 0 (default) auto-picks based on
  # file count and GOMAXPROCS; 1 forces serial; N>1 uses a fixed pool.
  # SQLite writes serialize, so values above ~8 give diminishing returns.
  # max_workers: 0

# Python-specific indexing knobs. The relative-import resolver collapses
# 'from . import utils' (inside mypkg/a.py) and 'from mypkg import utils'
# (elsewhere) onto a single sym:mypkg.utils graph node — "who imports X?"
# queries see the full fan-in instead of a fragmented one.
# python:
#   # src_roots lists directories whose immediate children are top-level
#   # Python packages. Files under a matching root skip the __init__.py
#   # walk and anchor at the root boundary — covers PEP 420 namespace
#   # packages and src-layout projects. Empty (default): __init__.py walk
#   # first, then a virtual directory package as last resort.
#   src_roots:
#     - src

code_file_patterns:
  - "*.go"
  - "*.ts"
  - "*.tsx"
  - "*.py"
  - "*.rs"
  - "*.java"
  - "*.rb"

exclude_patterns:
  - "node_modules/**"
  - ".git/**"
  - "vendor/**"
  - ".librarian/**"
  - ".claude/worktrees/**"
  - ".claude/agents/**"
  - ".claude/skills/**"
`

// writeIfMissing writes content to path only if the file does not already exist.
// Re-running 'librarian init' on an existing workspace therefore preserves any
// user-edited templates.
func writeIfMissing(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

const defaultIgnoreFile = `# Files librarian should skip during indexing (gitignore-style patterns, one per line).
# These stack on top of cfg.exclude_patterns in config.yaml.

node_modules/
vendor/
.git/
dist/
build/
`

// workspaceGitIgnore classifies artifacts under .librarian/ as local-only vs
// team-shared. See the workspace README for the full taxonomy.
const workspaceGitIgnore = `# librarian state — user-specific, local-only (do not commit)
librarian.db
librarian.db-journal
librarian.db-wal
librarian.db-shm

# ephemeral outputs
out/manifest.json
out/cost.json

# optional: uncomment to keep extraction cache local
# commented   = shared; teammates' first rebuild is fast, no re-extraction cost
# uncommented = local;  smaller repo, teammates re-extract on first build
# out/cache/
`
