// Package install writes assistant-platform integration pointers — one file
// per platform at the project root (CLAUDE.md, AGENTS.md, CONVENTIONS.md,
// GEMINI.md, .cursor/rules/librarian.mdc, .github/copilot-instructions.md) —
// that reference shared templates (rules.md, skill.md, hook shims) in
// .librarian/. See registry.go for the full list. The installer is idempotent:
// re-running it refreshes librarian-managed blocks without touching user
// content around them.
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
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"librarian/internal/workspace"
)

// hookCommand is the exact command string written into every JSON hook config.
// Keeping it a package constant guarantees the idempotency lookup in
// upsertJSONHook matches across platforms.
const hookCommand = "bash .librarian/hooks/sessionstart.sh"

// Platform describes one assistant integration — its human name, CLI key,
// detection heuristic, and install/uninstall actions. Install writes the
// platform's integration pointers and returns the paths touched; Uninstall
// reverses that (strips marker blocks, removes hook entries, deletes per-
// platform extras) and returns the paths affected. Both are idempotent —
// running twice produces zero changes on the second call.
type Platform struct {
	Name      string
	Key       string
	Detected  func(root string) bool
	Install   func(ws *workspace.Workspace, warn io.Writer) ([]string, error)
	Uninstall func(ws *workspace.Workspace, warn io.Writer) ([]string, error)
}

// InstallOptions control Run. Zero value = interactive TTY prompt over all
// platforms, git post-commit hook installed, dry-run off. Paired with
// UninstallOptions in uninstall.go — keeping the Install/Uninstall prefix
// parallel so tools searching for one find the other.
type InstallOptions struct {
	All       bool
	Platforms []string
	NoGitHook bool
	DryRun    bool
	In        io.Reader
	Out       io.Writer
}

// PlatformKeys returns the alphabetised list of supported platform keys,
// derived from the registry. cmd/install.go uses it for the --platforms
// flag description and the unknown-platform error surfaces it too, so
// adding a new platform automatically updates both.
func PlatformKeys() []string {
	all := allPlatforms()
	keys := make([]string, 0, len(all))
	for _, p := range all {
		keys = append(keys, p.Key)
	}
	sort.Strings(keys)
	return keys
}

// Run executes an install against ws according to opts. It writes the shared
// templates (rules.md, skill.md, sessionstart.sh), selects platforms, installs
// each, and optionally installs the git post-commit hook. Returns the ordered
// list of paths written or updated (for summary reporting).
func Run(ws *workspace.Workspace, opts InstallOptions) ([]string, error) {
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

	platforms, err := selectPlatformsByFlags(opts.In, opts.Out, ws.Root, opts.All, opts.Platforms)
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
			path, changed, err := installGitPostCommit(ws, opts.Out)
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

// selectPlatformsByFlags resolves the caller's --all / --platforms / interactive
// choice to a concrete platform slice. Shared between Run (install) and
// Uninstall so both commands make identical decisions on the same inputs.
//
//   - all=true             → every supported platform
//   - keys non-empty       → only those keys (errors on unknown key)
//   - both false           → interactive prompt pre-checking detected ones
func selectPlatformsByFlags(in io.Reader, out io.Writer, root string, all bool, keys []string) ([]*Platform, error) {
	platforms := allPlatforms()
	switch {
	case all:
		return platforms, nil
	case len(keys) > 0:
		byKey := make(map[string]*Platform, len(platforms))
		for _, p := range platforms {
			byKey[p.Key] = p
		}
		result := make([]*Platform, 0, len(keys))
		for _, key := range keys {
			p, ok := byKey[key]
			if !ok {
				return nil, fmt.Errorf("unknown platform %q (known: %s)", key, strings.Join(PlatformKeys(), ", "))
			}
			result = append(result, p)
		}
		return result, nil
	default:
		return selectPlatforms(in, out, platforms, root)
	}
}

// writeSharedTemplates writes the librarian-owned files every platform shares:
// rules.md and skill.md (user-editable — only written if missing), plus
// sessionstart.sh (librarian-managed — refreshed when content differs).
// Dry-run compares against on-disk content (reads are side-effect-free) so it
// accurately predicts what a real run would change.
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
	changed, err := writeExecutableIfChanged(hookPath, tmplSessionStart, dryRun)
	if err != nil {
		return written, err
	}
	if changed {
		written = append(written, hookPath)
	}
	return written, nil
}

// writeExecutableIfChanged writes body to path with 0o755 perms only if content
// differs, keeping re-install summaries accurate. If content matches but the
// executable bit is missing (user chmod'd it away), re-assert +x. When dryRun
// is true, only the prediction is returned — no disk mutation, no chmod.
func writeExecutableIfChanged(path, body string, dryRun bool) (bool, error) {
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == body {
		info, statErr := os.Stat(path)
		if statErr == nil && info.Mode().Perm()&0o100 != 0 {
			return false, nil
		}
		if dryRun {
			return true, nil
		}
		return true, os.Chmod(path, 0o755)
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	if dryRun {
		return true, nil
	}
	return true, writeWithMode(path, []byte(body), 0o755)
}

// installMarkerAndHook is the generic path followed by every markerPlatform
// (see registry.go). hookConfig may be "" when a platform has no SessionStart
// hook API — in that case we write only the pointer file and skip the JSON
// merge.
func installMarkerAndHook(ws *workspace.Workspace, pointerFile, hookConfig string, warn io.Writer) ([]string, error) {
	var written []string

	pointerPath := filepath.Join(ws.Root, pointerFile)
	if changed, err := upsertMarkedBlock(pointerPath, tmplPointer, warn); err != nil {
		return written, err
	} else if changed {
		written = append(written, pointerPath)
	}

	if hookConfig == "" {
		return written, nil
	}

	hookPath := filepath.Join(ws.Root, hookConfig)
	if changed, err := upsertJSONHook(hookPath, "SessionStart", hookCommand); err != nil {
		return written, err
	} else if changed {
		written = append(written, hookPath)
	}
	return written, nil
}

// uninstallMarkerAndHook is the inverse of installMarkerAndHook. Strips the
// librarian block from pointerFile and (if hookConfig != "") removes the
// SessionStart entry from hookConfig. Idempotent: missing files / absent
// markers / absent hook entries all return cleanly with changed=false.
func uninstallMarkerAndHook(ws *workspace.Workspace, pointerFile, hookConfig string, warn io.Writer) ([]string, error) {
	var removed []string

	pointerPath := filepath.Join(ws.Root, pointerFile)
	if changed, err := removeMarkedBlock(pointerPath, warn); err != nil {
		return removed, err
	} else if changed {
		removed = append(removed, pointerPath)
	}

	if hookConfig == "" {
		return removed, nil
	}

	hookPath := filepath.Join(ws.Root, hookConfig)
	if changed, err := removeJSONHook(hookPath, "SessionStart", hookCommand); err != nil {
		return removed, err
	} else if changed {
		removed = append(removed, hookPath)
	}
	return removed, nil
}

// uninstallClaudeSkill reverses installClaudeSkill. Removes
// .claude/skills/librarian/SKILL.md and opportunistically rmdirs the
// librarian/ and skills/ parents when they become empty. Leaves .claude/
// alone — it's user-owned and contains other state (settings.json).
func uninstallClaudeSkill(ws *workspace.Workspace) ([]string, error) {
	skillDir := filepath.Join(ws.Root, ".claude", "skills", "librarian")
	skillPath := filepath.Join(skillDir, "SKILL.md")

	if _, err := os.Stat(skillPath); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat %s: %w", skillPath, err)
	}
	if err := os.Remove(skillPath); err != nil {
		return nil, fmt.Errorf("removing %s: %w", skillPath, err)
	}

	// Best-effort rmdir of parents; ignore errors (non-empty dir means
	// another tool's files live there — we shouldn't touch them).
	removeEmptyDir(skillDir)
	removeEmptyDir(filepath.Dir(skillDir)) // .claude/skills

	return []string{skillPath}, nil
}

// removeEmptyDir removes dir when its contents are empty. Best-effort: any
// error (dir missing, dir non-empty, permission) is swallowed because the
// caller never wants uninstall to fail on parent-dir cleanup.
func removeEmptyDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) > 0 {
		return
	}
	_ = os.Remove(dir)
}

// installClaudeSkill places the workspace skill.md at .claude/skills/librarian/SKILL.md
// so Claude Code discovers the `/librarian` slash-skill. Claude Code only reads
// skills from .claude/skills/<name>/SKILL.md — .librarian/skill.md alone is
// never loaded.
//
// Reads from .librarian/skill.md (not the embedded template) so user edits to
// the canonical workspace copy propagate on reinstall. Falls back to the
// embedded template only if the workspace copy is missing — writeSharedTemplates
// runs first, so the workspace copy always exists on the real install path.
func installClaudeSkill(ws *workspace.Workspace) ([]string, error) {
	source, err := os.ReadFile(ws.SkillPath())
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("reading %s: %w", ws.SkillPath(), err)
		}
		source = []byte(tmplSkillMD)
	}

	dest := filepath.Join(ws.Root, ".claude", "skills", "librarian", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("creating %s: %w", filepath.Dir(dest), err)
	}
	existing, err := os.ReadFile(dest)
	if err == nil && bytes.Equal(existing, source) {
		return nil, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading %s: %w", dest, err)
	}
	if err := os.WriteFile(dest, source, 0o644); err != nil {
		return nil, fmt.Errorf("writing %s: %w", dest, err)
	}
	return []string{dest}, nil
}
