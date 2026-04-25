package install

import (
	"fmt"
	"io"
	"path/filepath"

	"librarian/internal/workspace"
)

// markerPlatform describes a platform that installs by dropping a markers-
// delimited pointer into a user-owned text file and (optionally) merging a
// SessionStart entry into a JSON hook config. Cursor has genuinely different
// logic (dedicated .mdc file, no JSON) so it lives in cursor.go instead.
type markerPlatform struct {
	name        string
	key         string
	pointerFile string   // project-root-relative, e.g. "CLAUDE.md"
	hookConfig  string   // project-root-relative, e.g. ".claude/settings.json". "" skips the JSON step.
	sentinels   []string // files whose presence means the platform is "detected" in this repo
}

// markerPlatforms enumerates the platforms that share the pointer-plus-hook
// install pattern. Adding a new one = adding a row here.
//
// hookConfig="" skips the JSON hook merge — used for platforms that don't
// expose a SessionStart-style hook API yet (Gemini CLI, GitHub Copilot,
// OpenCode, Aider). The pointer file is the guaranteed path in those cases.
//
// Codex and OpenCode share AGENTS.md (per each tool's own convention). The
// block is idempotent, so installing both writes a single block; the second
// install is a no-op.
//
// Two platforms have per-key specialisations inlined into the Install
// closure (registry.go allPlatforms): Claude Code copies skill.md to
// .claude/skills/librarian/SKILL.md, Aider prints a post-install note
// reminding users to list CONVENTIONS.md in .aider.conf.yml (Aider doesn't
// auto-discover pointer files).
var markerPlatforms = []markerPlatform{
	{
		name:        "Claude Code",
		key:         "claude",
		pointerFile: "CLAUDE.md",
		hookConfig:  ".claude/settings.json",
		sentinels:   []string{"CLAUDE.md", ".claude/settings.json"},
	},
	{
		name:        "Codex",
		key:         "codex",
		pointerFile: "AGENTS.md",
		hookConfig:  ".codex/hooks.json",
		sentinels:   []string{"AGENTS.md", ".codex"},
	},
	{
		name:        "Gemini CLI",
		key:         "gemini",
		pointerFile: "GEMINI.md",
		hookConfig:  "",
		sentinels:   []string{"GEMINI.md", ".gemini"},
	},
	{
		name:        "OpenCode",
		key:         "opencode",
		pointerFile: "AGENTS.md", // shared with Codex; block is idempotent
		hookConfig:  "",
		sentinels:   []string{".opencode", "opencode.json"},
	},
	{
		name:        "GitHub Copilot",
		key:         "copilot",
		pointerFile: ".github/copilot-instructions.md",
		hookConfig:  "",
		sentinels:   []string{".github/copilot-instructions.md", ".github/instructions"},
	},
	{
		name:        "Aider",
		key:         "aider",
		pointerFile: "CONVENTIONS.md",
		hookConfig:  "",
		sentinels:   []string{".aider.conf.yml", ".aiderignore"},
	},
}

// allPlatforms returns the canonical ordered list of supported assistant
// platforms — every entry in markerPlatforms, followed by Cursor (which has
// custom install logic). Order is preserved in the interactive prompt and
// summary output; PlatformKeys in install.go sorts a copy for user-visible
// key lists.
func allPlatforms() []*Platform {
	out := make([]*Platform, 0, len(markerPlatforms)+1)
	for _, mp := range markerPlatforms {
		out = append(out, &Platform{
			Name: mp.name,
			Key:  mp.key,
			Detected: func(root string) bool {
				for _, s := range mp.sentinels {
					if fileExists(filepath.Join(root, s)) {
						return true
					}
				}
				return false
			},
			Install: func(ws *workspace.Workspace, warn io.Writer) ([]string, error) {
				written, err := installMarkerAndHook(ws, mp.pointerFile, mp.hookConfig, warn)
				if err != nil {
					return written, err
				}
				if mp.key == "claude" {
					extra, err := installClaudeSkill(ws)
					if err != nil {
						return written, err
					}
					written = append(written, extra...)
				}
				if mp.key == "aider" && len(written) > 0 {
					// Aider doesn't auto-discover pointer files — users must
					// list CONVENTIONS.md under `read:` in .aider.conf.yml.
					// We deliberately don't merge YAML into their config;
					// users have site-specific configs we shouldn't overwrite.
					// Gated on len(written) > 0 so a no-op reinstall stays silent.
					fmt.Fprintln(warn, "  note: Aider — add `read: [CONVENTIONS.md]` to .aider.conf.yml to load the pointer.")
				}
				return written, nil
			},
			Uninstall: func(ws *workspace.Workspace, warn io.Writer) ([]string, error) {
				removed, err := uninstallMarkerAndHook(ws, mp.pointerFile, mp.hookConfig, warn)
				if err != nil {
					return removed, err
				}
				if mp.key == "claude" {
					extra, err := uninstallClaudeSkill(ws)
					if err != nil {
						return removed, err
					}
					removed = append(removed, extra...)
				}
				if mp.key == "aider" && len(removed) > 0 {
					// Symmetric to the install note. Gated on len(removed) > 0
					// so repeat uninstalls stay silent.
					fmt.Fprintln(warn, "  note: Aider — remove `read: [CONVENTIONS.md]` from .aider.conf.yml if you added it.")
				}
				return removed, nil
			},
		})
	}
	out = append(out, cursorPlatform())
	return out
}
