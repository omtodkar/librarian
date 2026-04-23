package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"librarian/internal/install"
	"librarian/internal/workspace"
)

var (
	installAll       bool
	installPlatforms string
	installNoGitHook bool
	installDryRun    bool
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install Librarian integration for assistant platforms",
	Long: `Writes thin pointer files that make assistant platforms (Claude Code, Codex,
Cursor, Gemini CLI) discover Librarian automatically.

Pointers are written as delimited <!-- librarian:start/end --> blocks so reinstalls
are idempotent and user content around the block is preserved. Hook entries in JSON
configs (.claude/settings.json, .codex/hooks.json, .gemini/settings.json) are merged
by command string so other hooks (bd prime, custom scripts) are left untouched.

Default behaviour when run on a TTY is an interactive checklist with detected
platforms pre-checked. Scripted callers should pass --all or --platforms.

Examples:
  librarian install                        # interactive
  librarian install --all                  # all four platforms, no prompt
  librarian install --platforms=claude,cursor
  librarian install --dry-run              # show what would change
  librarian install --no-git-hook          # skip .git/hooks/post-commit`,
	RunE: runInstall,
}

func init() {
	installCmd.Flags().BoolVar(&installAll, "all", false, "Install for all supported platforms, skip prompt")
	installCmd.Flags().StringVar(&installPlatforms, "platforms", "", "Comma-separated platform keys (claude,codex,cursor,gemini)")
	installCmd.Flags().BoolVar(&installNoGitHook, "no-git-hook", false, "Skip .git/hooks/post-commit auto-rebuild")
	installCmd.Flags().BoolVar(&installDryRun, "dry-run", false, "Print planned writes without touching disk")
	rootCmd.AddCommand(installCmd)
}

func runInstall(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}
	ws, err := workspace.Find(cwd)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}

	opts := install.Options{
		All:       installAll,
		NoGitHook: installNoGitHook,
		DryRun:    installDryRun,
		In:        os.Stdin,
		Out:       os.Stdout,
	}
	if installPlatforms != "" {
		for _, key := range strings.Split(installPlatforms, ",") {
			key = strings.TrimSpace(key)
			if key != "" {
				opts.Platforms = append(opts.Platforms, key)
			}
		}
	}

	written, err := install.Run(ws, opts)
	if err != nil {
		return err
	}

	fmt.Println()
	if len(written) == 0 {
		fmt.Println("Nothing changed — everything is up to date.")
		return nil
	}
	verb := "Wrote/updated"
	if installDryRun {
		verb = "Would write/update"
	}
	fmt.Printf("%s %d file(s):\n", verb, len(written))
	for _, p := range written {
		display := p
		if rel, err := filepath.Rel(ws.Root, p); err == nil && !strings.HasPrefix(rel, "..") {
			display = rel
		}
		fmt.Printf("  %s\n", display)
	}
	return nil
}
