#!/usr/bin/env bash
# Post-commit hook — keeps the Librarian graph fresh after each commit.
# Managed by `librarian install`. Runs in the background so commits stay snappy.
# Output is captured to .librarian/out/post-commit.log for debugging.
set -e
repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[ -z "$repo_root" ] && exit 0
[ -d "$repo_root/.librarian" ] || exit 0
command -v librarian >/dev/null 2>&1 || exit 0

log="$repo_root/.librarian/out/post-commit.log"
mkdir -p "$(dirname "$log")"
(
  cd "$repo_root"
  librarian index >"$log" 2>&1
  librarian report >>"$log" 2>&1
) &
disown 2>/dev/null || true
exit 0
