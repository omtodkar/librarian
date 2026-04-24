package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"librarian/internal/store"
	"librarian/internal/workspace"
)

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

	writes := []struct {
		path, content string
	}{
		{ws.ConfigPath(), defaultConfigYAML},
		{ws.IgnorePath(), defaultIgnoreFile},
		{ws.GitIgnorePath(), workspaceGitIgnore},
	}
	for _, w := range writes {
		if err := writeIfMissing(w.path, w.content); err != nil {
			return err
		}
	}

	s, err := store.Open(ws.DBPath())
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	s.Close()

	fmt.Printf("Initialized Librarian workspace at %s\n\n", ws.Dir())
	fmt.Println("Next steps:")
	fmt.Println("  1. Edit .librarian/config.yaml (docs_dir, embedding provider, model).")
	fmt.Println("  2. Set LIBRARIAN_EMBEDDING_API_KEY in your environment.")
	fmt.Println("  3. Run 'librarian index' to build the first index.")
	return nil
}

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

const defaultConfigYAML = `# librarian workspace config — team-wide, safe to commit.
# API keys belong in environment variables (LIBRARIAN_EMBEDDING_API_KEY), not here.

docs_dir: docs

embedding:
  provider: gemini         # gemini | openai (or any OpenAI-compatible endpoint)
  model: gemini-embedding-2   # 3072-dim multimodal; older text-embedding-004 is deprecated
  # base_url: for OpenAI-compatible endpoints (e.g. http://localhost:1234/v1)

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
`

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
