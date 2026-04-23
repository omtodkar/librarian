package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"librarian/internal/workspace"
)

// newGitWS creates a tempdir with a .git/ and .librarian/ directory and returns
// a *workspace.Workspace rooted there. Enough structure for installGitPostCommit
// to treat it as a real repo without invoking the git binary.
func newGitWS(t *testing.T) *workspace.Workspace {
	t.Helper()
	dir := t.TempDir()
	for _, sub := range []string{".git", ".librarian", ".librarian/out"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &workspace.Workspace{Root: dir}
}

func TestInstallGitPostCommit_FreshRepo(t *testing.T) {
	ws := newGitWS(t)
	path, changed, err := installGitPostCommit(ws)
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
	ws := newGitWS(t)
	if _, _, err := installGitPostCommit(ws); err != nil {
		t.Fatal(err)
	}
	path, changed, err := installGitPostCommit(ws)
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
	ws := newGitWS(t)
	hookPath := filepath.Join(ws.Root, ".git", "hooks", "post-commit")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	userHook := "#!/usr/bin/env bash\necho 'user commit ran'\n"
	if err := os.WriteFile(hookPath, []byte(userHook), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := installGitPostCommit(ws); err != nil {
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
	path, changed, err := installGitPostCommit(ws)
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
	path, changed, err := installGitPostCommit(ws)
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

// If a user hook above the librarian block happens to contain the literal
// string "# librarian:end", an unanchored endIdx search flips the order and
// the installer falls through to the append path — duplicating the block on
// every reinstall. Anchoring endIdx after startIdx prevents this.
func TestUpsertShellMarkedBlock_EndMarkerBeforeStart(t *testing.T) {
	existing := []byte("#!/usr/bin/env bash\n# librarian:end\necho 'user code'\n\n" +
		shMarkerStart + "\noriginal body\n" + shMarkerEnd + "\n")

	updated := upsertShellMarkedBlock(existing, shMarkerStart+"\nnew body\n"+shMarkerEnd+"\n")
	got := string(updated)

	// Exactly one pair of librarian markers — old block replaced, not duplicated.
	if strings.Count(got, shMarkerStart) != 1 {
		t.Errorf("expected one start marker, got %d:\n%s", strings.Count(got, shMarkerStart), got)
	}
	if !strings.Contains(got, "new body") {
		t.Errorf("new body missing:\n%s", got)
	}
	if strings.Contains(got, "original body") {
		t.Errorf("old body still present:\n%s", got)
	}
	// User's pre-existing "# librarian:end" line is preserved.
	if !strings.Contains(got, "# librarian:end\necho 'user code'") {
		t.Errorf("user content with stray end-marker was corrupted:\n%s", got)
	}
}

// Torn block: start marker present, end marker missing (user hand-edit went
// wrong). Installer should recover by replacing from the start marker to EOF
// rather than duplicating or refusing.
func TestUpsertShellMarkedBlock_MissingEndMarker(t *testing.T) {
	existing := []byte("#!/usr/bin/env bash\necho user\n\n" + shMarkerStart + "\nold body (no end marker!)\n")
	updated := upsertShellMarkedBlock(existing, shMarkerStart+"\nnew\n"+shMarkerEnd+"\n")
	got := string(updated)

	if strings.Count(got, shMarkerStart) != 1 {
		t.Errorf("expected one start marker, got %d:\n%s", strings.Count(got, shMarkerStart), got)
	}
	if !strings.Contains(got, shMarkerEnd) {
		t.Errorf("new end marker missing:\n%s", got)
	}
	if strings.Contains(got, "old body") {
		t.Errorf("torn block body not replaced:\n%s", got)
	}
	if !strings.Contains(got, "echo user") {
		t.Errorf("user code before torn block lost:\n%s", got)
	}
}
