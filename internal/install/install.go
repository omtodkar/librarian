// Package install writes assistant-platform integration pointers — CLAUDE.md,
// AGENTS.md, GEMINI.md, and .cursor/rules/librarian.mdc at the project root —
// that reference shared templates (rules.md, skill.md, hook shims) in
// .librarian/. The installer is idempotent: re-running it refreshes librarian-
// managed blocks without touching user content around them.
//
// See internal/install/templates/ for the source text of every file written.
// All filesystem mutations go through two helpers:
//
//   - upsertMarkedBlock — for user-owned text files (CLAUDE.md et al.). Wraps
//     the librarian block in <!-- librarian:start -->…<!-- librarian:end -->.
//   - upsertJSONHook    — for JSON hook configs (.claude/settings.json et al.).
//     Merges a SessionStart entry by command string.
package install

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"librarian/internal/workspace"
)

// hookCommand is the exact command string written into every JSON hook config.
// Keeping it a package constant guarantees the idempotency lookup in
// upsertJSONHook matches across platforms.
const hookCommand = "bash .librarian/hooks/sessionstart.sh"

// Platform describes one assistant integration — its human name, CLI key,
// detection heuristic, and install action. Adding a new platform means adding
// a new *Platform to allPlatforms() in registry.go.
type Platform struct {
	Name     string
	Key      string
	Detected func(root string) bool
	Install  func(ws *workspace.Workspace, out io.Writer) ([]string, error)
}

// Options control Run. Zero value = interactive TTY prompt over all platforms,
// git post-commit hook installed, dry-run off.
type Options struct {
	All       bool
	Platforms []string
	NoGitHook bool
	DryRun    bool
	In        io.Reader
	Out       io.Writer
}

// Run executes an install against ws according to opts. It writes the shared
// templates (rules.md, skill.md, sessionstart.sh), selects platforms, installs
// each, and optionally installs the git post-commit hook. Returns the ordered
// list of paths written or updated (for summary reporting).
func Run(ws *workspace.Workspace, opts Options) ([]string, error) {
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.DryRun {
		fmt.Fprintln(opts.Out, "-- DRY RUN — no files will be written --")
	}

	var written []string

	shared, err := writeSharedTemplates(ws, opts.DryRun)
	if err != nil {
		return nil, err
	}
	written = append(written, shared...)

	platforms, err := selectRequested(ws.Root, opts)
	if err != nil {
		return nil, err
	}

	for _, p := range platforms {
		suffix := ""
		if opts.DryRun {
			suffix = " (dry-run)"
		}
		fmt.Fprintf(opts.Out, "-> %s%s\n", p.Name, suffix)
		if opts.DryRun {
			continue
		}
		paths, err := p.Install(ws, opts.Out)
		if err != nil {
			return written, fmt.Errorf("installing %s: %w", p.Name, err)
		}
		written = append(written, paths...)
	}

	if !opts.NoGitHook {
		fmt.Fprintln(opts.Out, "-> git post-commit hook")
		if !opts.DryRun {
			path, changed, err := installGitPostCommit(ws)
			if err != nil {
				return written, fmt.Errorf("installing git post-commit hook: %w", err)
			}
			if changed {
				written = append(written, path)
			}
		}
	}
	return written, nil
}

func selectRequested(root string, opts Options) ([]*Platform, error) {
	all := allPlatforms()
	switch {
	case opts.All:
		return all, nil
	case len(opts.Platforms) > 0:
		byKey := make(map[string]*Platform, len(all))
		for _, p := range all {
			byKey[p.Key] = p
		}
		out := make([]*Platform, 0, len(opts.Platforms))
		for _, key := range opts.Platforms {
			p, ok := byKey[key]
			if !ok {
				return nil, fmt.Errorf("unknown platform %q (known: claude, codex, cursor, gemini)", key)
			}
			out = append(out, p)
		}
		return out, nil
	default:
		return selectPlatforms(opts.In, opts.Out, all, root)
	}
}

// writeSharedTemplates writes the librarian-owned files every platform shares:
// rules.md and skill.md (user-editable — only written if missing), plus
// sessionstart.sh (librarian-managed — refreshed when content differs). In
// dry-run mode nothing touches disk; returned paths are the writes that *would*
// happen on a real run.
func writeSharedTemplates(ws *workspace.Workspace, dryRun bool) ([]string, error) {
	if !dryRun {
		if err := os.MkdirAll(ws.HooksDir(), 0o755); err != nil {
			return nil, fmt.Errorf("creating hooks dir: %w", err)
		}
	}

	var written []string

	userEditable := []struct {
		path, body string
	}{
		{ws.RulesPath(), tmplRulesMD},
		{ws.SkillPath(), tmplSkillMD},
	}
	for _, w := range userEditable {
		if _, err := os.Stat(w.path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return written, fmt.Errorf("stat %s: %w", w.path, err)
		}
		if !dryRun {
			if err := os.WriteFile(w.path, []byte(w.body), 0o644); err != nil {
				return written, fmt.Errorf("writing %s: %w", w.path, err)
			}
		}
		written = append(written, w.path)
	}

	hookPath := filepath.Join(ws.HooksDir(), "sessionstart.sh")
	if dryRun {
		// Dry-run is pessimistic about hooks: no way to know without reading
		// disk whether the script differs from the embed. List it as planned.
		written = append(written, hookPath)
	} else {
		changed, err := writeExecutableIfChanged(hookPath, tmplSessionStart)
		if err != nil {
			return written, err
		}
		if changed {
			written = append(written, hookPath)
		}
	}
	return written, nil
}

// writeExecutableIfChanged writes body to path with 0o755 perms only if content
// differs, keeping re-install summaries accurate. If content matches but the
// executable bit is missing (someone chmod'd it away), just chmod.
func writeExecutableIfChanged(path, body string) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == body {
		info, statErr := os.Stat(path)
		if statErr == nil && info.Mode().Perm()&0o100 != 0 {
			return false, nil
		}
		return true, os.Chmod(path, 0o755)
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return false, fmt.Errorf("chmod +x %s: %w", path, err)
	}
	return true, nil
}

// installMarkerAndHook is the generic path followed by the three platforms that
// use both a pointer file (CLAUDE.md / AGENTS.md / GEMINI.md) and a JSON hook
// config (.claude/settings.json / .codex/hooks.json / .gemini/settings.json).
// Returns the absolute paths of files that actually changed on disk.
func installMarkerAndHook(ws *workspace.Workspace, pointerFile, hookConfig string) ([]string, error) {
	var written []string

	pointerPath := filepath.Join(ws.Root, pointerFile)
	if changed, err := upsertMarkedBlock(pointerPath, tmplPointer); err != nil {
		return written, err
	} else if changed {
		written = append(written, pointerPath)
	}

	hookPath := filepath.Join(ws.Root, hookConfig)
	if changed, err := upsertJSONHook(hookPath, "SessionStart", hookCommand); err != nil {
		return written, err
	} else if changed {
		written = append(written, hookPath)
	}
	return written, nil
}
