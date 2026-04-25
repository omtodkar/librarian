package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"librarian/internal/install"
	"librarian/internal/workspace"
)

var (
	uninstallAll       bool
	uninstallPlatforms string
	uninstallFull      bool
	uninstallDryRun    bool
	uninstallYes       bool
)

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Reverse 'librarian install' — unwire platform pointers, optionally delete workspace",
	Long: `Removes librarian integration from assistant platforms by stripping the
marker-delimited blocks from CLAUDE.md / AGENTS.md / GEMINI.md / CONVENTIONS.md /
.github/copilot-instructions.md, removing SessionStart hook entries keyed by the
librarian command string from .claude/settings.json and .codex/hooks.json, and
deleting per-platform extras (.claude/skills/librarian/SKILL.md, .cursor/rules/
librarian.mdc). The librarian block in .git/hooks/post-commit is stripped too;
the file is deleted if it ends up empty.

Defaults favour safety:

  - No-flag form unwires pointers but preserves .librarian/, so re-installing
    is just 'librarian install'. --full additionally removes .librarian/.
  - User content outside the librarian marker blocks is preserved byte-for-byte.
    A torn block (start marker without end) prints a warning and is left
    untouched — fix manually before re-running uninstall.
  - Other entries in .claude/settings.json / .codex/hooks.json (e.g. 'bd prime')
    are left untouched; only librarian-keyed entries are removed.

Shared files: Codex and OpenCode both register against AGENTS.md with a single
shared marker block. Uninstalling either strips the block for the other too
(the block is a single source of truth — there isn't a Codex-only or OpenCode-
only block to unwire separately).

Aider: the installer prints a reminder to add 'CONVENTIONS.md' to .aider.conf.yml;
uninstall prints the symmetric reminder to remove it. We don't edit .aider.conf.yml
directly — it's a user-owned YAML config we never wrote.

Examples:
  librarian uninstall                      # interactive, preserves .librarian/
  librarian uninstall --all                # unwire every installed platform
  librarian uninstall --platforms=claude,cursor
  librarian uninstall --full               # also rm -rf .librarian/
  librarian uninstall --full --yes         # skip confirmation prompt
  librarian uninstall --dry-run            # show what would change`,
	RunE: runUninstall,
}

func init() {
	uninstallCmd.Flags().BoolVar(&uninstallAll, "all", false, "Unwire every supported platform without prompt")
	uninstallCmd.Flags().StringVar(&uninstallPlatforms, "platforms", "",
		fmt.Sprintf("Comma-separated platform keys (%s)", strings.Join(install.PlatformKeys(), ",")))
	uninstallCmd.Flags().BoolVar(&uninstallFull, "full", false, "Also remove the .librarian/ workspace directory")
	uninstallCmd.Flags().BoolVar(&uninstallDryRun, "dry-run", false, "Print planned changes without touching disk")
	uninstallCmd.Flags().BoolVar(&uninstallYes, "yes", false, "Skip the confirmation prompt for --full")
	rootCmd.AddCommand(uninstallCmd)
}

func runUninstall(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting working directory: %w", err)
	}

	// Find the workspace. Unlike install, uninstall tolerates "no workspace
	// found" when --full is NOT set — a user may have already `rm -rf`d
	// .librarian/ and just wants to clean up the pointer files left behind
	// at the project root.
	var (
		ws      *workspace.Workspace
		wsFound bool
	)
	if found, err := workspace.Find(cwd); err == nil {
		ws = found
		wsFound = true
	} else {
		if uninstallFull {
			return fmt.Errorf("workspace: --full requires a discovered workspace: %w", err)
		}
		// Construct a pseudo-workspace rooted at CWD so pointer-file
		// cleanup still targets the right directory.
		ws = &workspace.Workspace{Root: cwd}
	}

	opts := install.UninstallOptions{
		All:    uninstallAll,
		Full:   uninstallFull,
		DryRun: uninstallDryRun,
		Yes:    uninstallYes,
		In:     os.Stdin,
		Out:    os.Stdout,
	}
	if uninstallPlatforms != "" {
		for _, key := range strings.Split(uninstallPlatforms, ",") {
			key = strings.TrimSpace(key)
			if key != "" {
				opts.Platforms = append(opts.Platforms, key)
			}
		}
	}

	removed, err := install.Uninstall(ws, opts)
	if err != nil {
		return err
	}

	fmt.Println()
	if len(removed) == 0 {
		fmt.Println("Nothing to uninstall — everything is already clean.")
		return nil
	}
	verb := "Removed/stripped"
	if uninstallDryRun {
		verb = "Would remove/strip"
	}
	fmt.Printf("%s %d path(s):\n", verb, len(removed))
	for _, p := range removed {
		fmt.Printf("  %s\n", relOrAbs(ws.Root, p))
	}

	// The "workspace preserved" hint only makes sense when a workspace was
	// actually discovered. When Find failed and we fell through to the
	// pseudo-workspace path (pointer-file cleanup only), there's nothing
	// to preserve — saying so would mislead the user into thinking
	// .librarian/ exists at cwd.
	if wsFound && !uninstallFull && !uninstallDryRun {
		fmt.Println()
		fmt.Printf("Workspace at %s preserved. Run 'librarian uninstall --full' to also delete it.\n",
			relOrAbs(ws.Root, ws.Dir()))
	}
	return nil
}
