package install

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"librarian/internal/workspace"
)

func TestInstallGitPostCommit_FreshRepo(t *testing.T) {
	ws := newWS(t)
	path, changed, err := installGitPostCommit(ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("fresh install should report changed=true")
	}
	content := readString(t, path)
	if !strings.Contains(content, "librarian:start") {
		t.Errorf("missing start marker:\n%s", content)
	}
	if !strings.Contains(content, `"$bin" index`) {
		t.Errorf("hook body missing `$bin index` invocation:\n%s", content)
	}

	// Executable bit.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("post-commit hook not executable: mode=%v", info.Mode().Perm())
	}
}

func TestInstallGitPostCommit_Idempotent(t *testing.T) {
	ws := newWS(t)
	if _, _, err := installGitPostCommit(ws, nil); err != nil {
		t.Fatal(err)
	}
	path, changed, err := installGitPostCommit(ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second install should be no-op")
	}
	content := readString(t, path)
	if strings.Count(content, "librarian:start") != 1 {
		t.Errorf("duplicate start marker after second install:\n%s", content)
	}
}

func TestInstallGitPostCommit_AppendsToExistingHook(t *testing.T) {
	ws := newWS(t)
	hookPath := filepath.Join(ws.Root, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	userHook := "#!/usr/bin/env bash\necho 'user commit ran'\n"
	if err := os.WriteFile(hookPath, []byte(userHook), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := installGitPostCommit(ws, nil); err != nil {
		t.Fatal(err)
	}
	content := readString(t, hookPath)
	if !strings.Contains(content, "user commit ran") {
		t.Errorf("user hook content lost:\n%s", content)
	}
	if !strings.Contains(content, "librarian:start") {
		t.Errorf("librarian block missing after append:\n%s", content)
	}
}

func TestInstallGitPostCommit_NotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	ws := &workspace.Workspace{Root: dir}
	path, changed, err := installGitPostCommit(ws, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != "" || changed {
		t.Errorf("expected no-op outside git repo; got path=%q changed=%v", path, changed)
	}
}

// Worktree layout: .git is a file pointing at the real gitdir rather than a
// directory. Installer must follow the pointer and land the hook in the real
// hooks/ directory; a silent no-op would leave worktree users without the
// auto-rebuild hook that the install summary claims was installed.
func TestInstallGitPostCommit_Worktree(t *testing.T) {
	dir := t.TempDir()
	// Simulate a worktree: .git is a file with a `gitdir:` pointer to the
	// real per-worktree directory that lives elsewhere under the main repo.
	realGitDir := filepath.Join(dir, ".gitmain", "worktrees", "feat")
	if err := os.MkdirAll(realGitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".librarian"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+realGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := &workspace.Workspace{Root: dir}
	path, changed, err := installGitPostCommit(ws, nil)
	if err != nil {
		t.Fatalf("worktree install: %v", err)
	}
	if !changed {
		t.Error("worktree install should report changed=true")
	}
	wantPrefix := realGitDir
	if !strings.HasPrefix(path, wantPrefix) {
		t.Errorf("expected hook under %s, got %s", wantPrefix, path)
	}
	mustExist := readString(t, path)
	if !strings.Contains(mustExist, `"$bin" index`) {
		t.Errorf("worktree hook missing `$bin index` body:\n%s", mustExist)
	}
}

// Binary resolution: the template must prefer a repo-local ./librarian
// over the $PATH lookup. This matters during development (librarian's
// own checkout doesn't put the binary on $PATH) and for any workspace
// where librarian lives at the repo root rather than a system location.
//
// Regression guard for a real bug: prior template required $PATH exclusively
// and silently no-op'd every commit on workspaces that only had ./librarian.
func TestPostCommitTemplate_PrefersRepoLocalBinaryBeforePATH(t *testing.T) {
	body := tmplGitPostCommit
	if !strings.Contains(body, `"$repo_root/librarian"`) {
		t.Errorf("template should check repo-local $repo_root/librarian; got:\n%s", body)
	}
	// The repo-local -x test MUST come before the command -v PATH lookup,
	// otherwise a coincidentally-installed librarian on $PATH would mask the
	// repo-local binary during development.
	localIdx := strings.Index(body, `-x "$repo_root/librarian"`)
	pathIdx := strings.Index(body, `command -v librarian`)
	if localIdx < 0 || pathIdx < 0 {
		t.Fatalf("missing expected resolver checks: local=%d path=%d", localIdx, pathIdx)
	}
	if localIdx > pathIdx {
		t.Errorf("repo-local check (idx=%d) must come before PATH check (idx=%d)", localIdx, pathIdx)
	}
}

// Concurrency guard: a rebase / cherry-pick / merge storm triggers one
// post-commit hook per commit. Without a lockfile, N concurrent background
// re-indexes race against the same SQLite database. The template must
// serialize via .librarian/out/post-commit.lock with PID-liveness checks
// so stale locks from crashed runs don't wedge the hook forever.
func TestPostCommitTemplate_HasLockfileWithLivenessCheck(t *testing.T) {
	body := tmplGitPostCommit
	if !strings.Contains(body, "post-commit.lock") {
		t.Errorf("template should use a lockfile at .librarian/out/post-commit.lock; got:\n%s", body)
	}
	if !strings.Contains(body, "kill -0") {
		t.Errorf("template should probe lockfile's PID via kill -0 for liveness; got:\n%s", body)
	}
	if !strings.Contains(body, `rm -f "$lock"`) {
		t.Errorf("template should clear stale lockfile (rm -f) when owner PID is gone; got:\n%s", body)
	}
}

// Log preservation: both `librarian index` and `librarian report` output
// must APPEND to the log (>>) rather than truncate with >. Truncating on
// every run destroys history mid-debug — if a commit's index fails and the
// next commit succeeds, the failure record is gone by the time the user
// opens the log.
//
// Regression guard: prior template used `>"$log"` for the index invocation,
// wiping the log every commit.
func TestPostCommitTemplate_AppendsToLogInsteadOfTruncating(t *testing.T) {
	body := tmplGitPostCommit
	// Catch any single > redirect to $log. Go regexp has no negative
	// lookbehind, so we match the character BEFORE the > and require it
	// not be another > (or start-of-line, but redirects aren't ever at
	// column 0 in this template). This cleanly distinguishes >"$log"
	// from the allowed >>"$log".
	truncate := regexp.MustCompile(`[^>]>[ \t]*"\$log"`)
	if truncate.MatchString(body) {
		t.Errorf("template must not truncate the log with single >; use >> to append. body:\n%s", body)
	}
	if !strings.Contains(body, `>> "$log"`) {
		t.Errorf("template should redirect output to log via >>; got:\n%s", body)
	}
}

// Worktree with a RELATIVE gitdir: path — git itself often writes these
// (e.g., `gitdir: ../.git/worktrees/feat`). Installer must resolve them
// relative to the worktree root, not to CWD.
func TestInstallGitPostCommit_WorktreeRelativeGitdir(t *testing.T) {
	dir := t.TempDir()
	relGitdir := filepath.Join(".gitmain", "worktrees", "feat")
	absGitdir := filepath.Join(dir, relGitdir)
	if err := os.MkdirAll(absGitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".librarian"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+relGitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws := &workspace.Workspace{Root: dir}
	path, changed, err := installGitPostCommit(ws, nil)
	if err != nil {
		t.Fatalf("relative-gitdir worktree install: %v", err)
	}
	if !changed {
		t.Error("relative-gitdir install should report changed=true")
	}
	if !strings.HasPrefix(path, absGitdir) {
		t.Errorf("expected hook under resolved absolute gitdir %s, got %s", absGitdir, path)
	}
}
