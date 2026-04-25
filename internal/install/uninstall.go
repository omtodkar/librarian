package install

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"librarian/internal/workspace"
)

// UninstallOptions control Uninstall. Zero value = interactive TTY prompt
// over only the installed platforms, git post-commit block stripped,
// dry-run off, workspace preserved.
type UninstallOptions struct {
	// All unwires every supported platform without interactive prompt.
	All bool

	// Platforms restricts the unwire set to these keys. Mutually exclusive
	// with All in practice (All wins if both are set).
	Platforms []string

	// Full additionally removes the .librarian/ workspace after unwiring.
	// Prompts for confirmation unless Yes is set.
	Full bool

	// DryRun prints what would be done without touching disk.
	DryRun bool

	// Yes bypasses the confirmation prompt for destructive actions (Full).
	Yes bool

	In  io.Reader
	Out io.Writer
}

// Uninstall reverses librarian install against ws according to opts. Runs the
// platform-selected Uninstall closures, optionally strips the git post-commit
// block, and optionally (Full) removes the .librarian/ workspace. Returns the
// ordered list of paths removed/modified for summary reporting.
func Uninstall(ws *workspace.Workspace, opts UninstallOptions) ([]string, error) {
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.DryRun {
		fmt.Fprintln(opts.Out, "-- DRY RUN — no files will be written --")
	}

	platforms, err := selectPlatformsByFlags(opts.In, opts.Out, ws.Root, opts.All, opts.Platforms)
	if err != nil {
		return nil, err
	}

	var removed []string
	for _, p := range platforms {
		suffix := ""
		if opts.DryRun {
			suffix = " (dry-run)"
		}
		fmt.Fprintf(opts.Out, "-> %s%s\n", p.Name, suffix)
		if opts.DryRun {
			continue
		}
		paths, err := p.Uninstall(ws, opts.Out)
		if err != nil {
			return removed, fmt.Errorf("uninstalling %s: %w", p.Name, err)
		}
		removed = append(removed, paths...)
	}

	// Git post-commit hook — always attempted when a repo is present.
	// Unlike install's --no-git-hook opt-out, uninstall has no reason to
	// preserve the librarian block in a user's hook (they're removing it).
	hookSuffix := ""
	if opts.DryRun {
		hookSuffix = " (dry-run)"
	}
	fmt.Fprintf(opts.Out, "-> git post-commit hook%s\n", hookSuffix)
	if !opts.DryRun {
		path, changed, err := uninstallGitPostCommit(ws, opts.Out)
		if err != nil {
			return removed, fmt.Errorf("uninstalling git post-commit hook: %w", err)
		}
		if changed {
			removed = append(removed, path)
		}
	}

	// --full workspace removal. Destructive; confirm unless --yes.
	if opts.Full {
		acted, err := uninstallWorkspace(ws, opts)
		if err != nil {
			return removed, err
		}
		if acted {
			removed = append(removed, ws.Dir())
		}
	}

	return removed, nil
}

// (selectRequestedForUninstall previously duplicated selectRequested here
// verbatim; consolidated into selectPlatformsByFlags shared between the
// two commands.)

// uninstallWorkspace performs the --full workspace deletion. Confirms
// unless opts.Yes. Returns acted=true only when the directory was actually
// removed, so the caller's summary accurately reports what changed:
//   - Missing workspace (user already deleted it)          → (false, nil)
//   - DryRun                                               → (false, nil) with a "would remove" line
//   - User answered "N" at the confirmation prompt         → (false, nil) with "aborted" line
//   - Removed successfully                                 → (true, nil)
//
// Previously returned only error, and the caller unconditionally appended
// ws.Dir() to the removed-paths list — falsely reporting removal in all
// three no-op cases.
func uninstallWorkspace(ws *workspace.Workspace, opts UninstallOptions) (bool, error) {
	if _, err := os.Stat(ws.Dir()); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("stat %s: %w", ws.Dir(), err)
	}

	fmt.Fprintln(opts.Out, "-> workspace")
	if opts.DryRun {
		fmt.Fprintf(opts.Out, "  would remove %s\n", ws.Dir())
		return false, nil
	}
	if !opts.Yes {
		fmt.Fprintf(opts.Out, "  This will delete %s (config, database, cache, generated outputs).\n  Continue? [y/N] ", ws.Dir())
		reader := bufio.NewReader(opts.In)
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return false, fmt.Errorf("reading confirmation: %w", err)
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			fmt.Fprintln(opts.Out, "  aborted — workspace preserved")
			return false, nil
		}
	}
	if err := os.RemoveAll(ws.Dir()); err != nil {
		return false, fmt.Errorf("removing %s: %w", ws.Dir(), err)
	}
	fmt.Fprintf(opts.Out, "  removed %s\n", ws.Dir())
	return true, nil
}
