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
	Long: `Writes thin pointer files that make assistant platforms discover Librarian
automatically. Supported platforms: Aider, Claude Code, Codex, Cursor, Gemini CLI,
GitHub Copilot, OpenCode. See --platforms for accepted keys.

Pointers are written as delimited <!-- librarian:start/end --> blocks so reinstalls
are idempotent and user content around the block is preserved. Hook entries in JSON
configs (.claude/settings.json, .codex/hooks.json) are merged by command string so
other hooks (bd prime, custom scripts) are left untouched. Platforms without a
SessionStart hook API (Aider, Gemini CLI, GitHub Copilot, OpenCode) get only the
pointer file. Codex and OpenCode both read AGENTS.md; the block is idempotent so
enabling both writes once. Aider doesn't auto-discover CONVENTIONS.md; the
installer prints a reminder to add it to .aider.conf.yml.

Default behaviour when run on a TTY is an interactive checklist with detected
platforms pre-checked. Scripted callers should pass --all or --platforms.

Examples:
  librarian install                        # interactive
  librarian install --all                  # every supported platform, no prompt
  librarian install --platforms=claude,cursor
  librarian install --dry-run              # show what would change
  librarian install --no-git-hook          # skip .git/hooks/post-commit`,
	RunE: runInstall,
}

func init() {
	installCmd.Flags().BoolVar(&installAll, "all", false, "Install for all supported platforms, skip prompt")
	installCmd.Flags().StringVar(&installPlatforms, "platforms", "",
		fmt.Sprintf("Comma-separated platform keys (%s)", strings.Join(install.PlatformKeys(), ",")))
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
		fmt.Printf("  %s\n", relOrAbs(ws.Root, p))
	}

	// Warn about gitignored files. Teammates who clone the repo won't receive
	// these and must re-run `librarian install` locally — surfacing that here
	// prevents silent divergence between collaborators' assistant behaviour.
	if !installDryRun {
		if ignored := install.FilterGitignored(ws.Root, written); len(ignored) > 0 {
			fmt.Println()
			fmt.Println("Note: these files are gitignored and won't propagate to teammates:")
			for _, p := range ignored {
				fmt.Printf("  %s\n", relOrAbs(ws.Root, p))
			}
			fmt.Println("Teammates must run 'librarian install' locally to wire them up.")
		}
	}
	return nil
}

// relOrAbs returns the path relative to base when it resolves to somewhere
// inside base, otherwise the original absolute path. Keeps summary output
// short for project-local files while staying correct for anything outside.
func relOrAbs(base, path string) string {
	if rel, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return path
}
