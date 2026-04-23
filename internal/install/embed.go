package install

import _ "embed"

//go:embed templates/rules.md
var tmplRulesMD string

//go:embed templates/skill.md
var tmplSkillMD string

// tmplSessionStart is shared by all four assistant platforms — the body is
// platform-agnostic, so one script serves Claude Code / Codex / Cursor / Gemini.
// Installed to .librarian/hooks/sessionstart.sh with +x.
//
//go:embed templates/hooks/sessionstart.sh
var tmplSessionStart string

//go:embed templates/hooks/git-post-commit.sh
var tmplGitPostCommit string

// tmplPointer is the shared markers-delimited block written into CLAUDE.md,
// AGENTS.md, and GEMINI.md. Cursor has its own dedicated .mdc file with
// different framing (YAML frontmatter + alwaysApply), hence a separate template.
//
//go:embed templates/pointers/pointer.md
var tmplPointer string

//go:embed templates/pointers/cursor.mdc
var tmplCursorPointer string
