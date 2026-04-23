#!/usr/bin/env bash
# Librarian SessionStart hook — injects rules.md + graph-report pointer at the
# top of an assistant session. Shared across Claude Code, Codex, Cursor, and
# Gemini CLI because the body is platform-agnostic.
#
# Managed by `librarian install`; regenerated on every install, do not edit.
set -e

# Resolve the repo root from either a git worktree or a fallback based on $0.
# git rev-parse works across worktrees and submodules; the $0 fallback handles
# the rare case where the hook is invoked outside a git checkout.
if root="$(git -C "$(dirname "$0")" rev-parse --show-toplevel 2>/dev/null)"; then
  :
else
  root="$(cd "$(dirname "$0")/../.." && pwd)"
fi

if [ -f "$root/.librarian/rules.md" ]; then
  cat "$root/.librarian/rules.md"
fi
if [ -f "$root/.librarian/out/GRAPH_REPORT.md" ]; then
  echo
  echo "Graph report: .librarian/out/GRAPH_REPORT.md"
fi
