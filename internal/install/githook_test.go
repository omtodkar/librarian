package install

import (
	"os"
	"path/filepath"
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
	if !strings.Contains(content, "librarian index") {
		t.Errorf("hook body missing librarian index invocation:\n%s", content)
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
	if !strings.Contains(mustExist, "librarian index") {
		t.Errorf("worktree hook missing body:\n%s", mustExist)
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
