#!/usr/bin/env bash
# Post-commit hook — keeps the Librarian graph fresh after each commit.
# Managed by `librarian install`. Runs in the background so commits stay
# snappy. Output is appended to .librarian/out/post-commit.log for debugging.
#
# Binary resolution: repo-local ./librarian first (common during
# development when the project itself isn't on $PATH), then $PATH. Leaves a
# breadcrumb in the log when neither is present — never fails the commit.
#
# Concurrency: a lockfile at .librarian/out/post-commit.lock prevents
# overlapping re-indexes. Rebase / cherry-pick / merge storms that create
# many commits at once trigger one re-index; subsequent commits during that
# window skip with a breadcrumb. Stale locks (recorded PID no longer alive)
# are cleared automatically.
set -e
repo_root="$(git rev-parse --show-toplevel 2>/dev/null || true)"
[ -z "$repo_root" ] && exit 0
[ -d "$repo_root/.librarian" ] || exit 0

log="$repo_root/.librarian/out/post-commit.log"
lock="$repo_root/.librarian/out/post-commit.lock"
mkdir -p "$(dirname "$log")"

# Resolve the librarian binary: repo-local first, then PATH.
bin=""
if [ -x "$repo_root/librarian" ]; then
  bin="$repo_root/librarian"
elif command -v librarian >/dev/null 2>&1; then
  bin="librarian"
else
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] skipped: 'librarian' not at $repo_root/librarian and not on PATH" >> "$log" 2>/dev/null || true
  exit 0
fi

# Concurrency guard: skip if another re-index is already running. Stale
# locks (owning PID gone) are cleared so a crashed prior run doesn't wedge
# the hook forever.
if [ -f "$lock" ]; then
  pid="$(cat "$lock" 2>/dev/null || true)"
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] skipped: pid $pid already indexing" >> "$log" 2>/dev/null || true
    exit 0
  fi
  rm -f "$lock"
fi

# Run in the background so the commit doesn't wait. The subshell writes
# its own PID to the lockfile and clears it on exit regardless of status.
# Both log writes APPEND (>>) to preserve history across commits — this
# matters when debugging a chain of failed indexes.
(
  echo $$ > "$lock"
  trap 'rm -f "$lock"' EXIT
  cd "$repo_root"
  {
    echo "=== $(date -u +%Y-%m-%dT%H:%M:%SZ) post-commit start (HEAD: $(git rev-parse --short HEAD)) ==="
    "$bin" index && "$bin" report
    status=$?
    echo "=== $(date -u +%Y-%m-%dT%H:%M:%SZ) post-commit done (exit=$status) ==="
  } >> "$log" 2>&1
) &
disown 2>/dev/null || true
exit 0
