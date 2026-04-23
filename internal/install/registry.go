package install

import (
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

// markerPlatforms enumerates the three platforms that share the pointer-plus-
// hook install pattern: Claude Code, Codex, and Gemini CLI. Adding a new one
// = adding a row here.
//
// Gemini CLI intentionally has hookConfig="" — at the time of writing Gemini
// does not support SessionStart hooks, so writing .gemini/settings.json would
// claim success for a no-op. The GEMINI.md pointer is the guaranteed path.
//
// Claude Code needs an extra step beyond the generic pointer-plus-hook flow:
// it also copies the skill.md into .claude/skills/librarian/SKILL.md so
// Claude Code discovers the /librarian slash-skill. That step is inlined into
// the claude entry's Install closure rather than generalised via a per-platform
// hook — one platform doesn't justify the abstraction.
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
}

// allPlatforms returns the canonical ordered list of supported assistant
// platforms: Claude Code, Codex, Gemini CLI, and Cursor. Order is preserved
// in the interactive prompt and summary output.
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
				return written, nil
			},
		})
	}
	out = append(out, cursorPlatform())
	return out
}
