package install

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"librarian/internal/workspace"
)

// Shell-comment markers delimit the librarian-managed block inside
// .git/hooks/post-commit so the file can host user hooks alongside.
const (
	shMarkerStart = "# librarian:start - managed by `librarian install`, do not edit"
	shMarkerEnd   = "# librarian:end"
)

// installGitPostCommit writes .git/hooks/post-commit so every commit refreshes
// the Librarian index + report in the background. Returns the installed path,
// whether it changed, and an error.
//
// Supports classic .git directories and worktrees (.git is a file with a
// `gitdir:` pointer). Returns ("", false, nil) silently when the workspace
// isn't inside a git repo — the installer then skips the hook step entirely.
func installGitPostCommit(ws *workspace.Workspace, warn io.Writer) (string, bool, error) {
	hooksDir, err := resolveGitHooksDir(ws.Root)
	if err != nil {
		return "", false, err
	}
	if hooksDir == "" {
		return "", false, nil
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return "", false, fmt.Errorf("creating %s: %w", hooksDir, err)
	}
	path := filepath.Join(hooksDir, "post-commit")
	changed, err := upsertBlockInFile(path, shMarkerStart, shMarkerEnd, tmplGitPostCommit, 0o755, warn)
	return path, changed, err
}

// uninstallGitPostCommit is the inverse of installGitPostCommit. Strips the
// librarian `# librarian:start ... # librarian:end` block from
// .git/hooks/post-commit, preserving any other hook body around it. If the
// post-commit file ends up effectively empty (just whitespace / a lone
// shebang) after removal, the file is deleted — `removeBlockInFile` handles
// that cleanup via its trailing TrimSpace check.
//
// Returns ("", false, nil) silently when the workspace isn't inside a git
// repo, mirroring installGitPostCommit's behaviour.
func uninstallGitPostCommit(ws *workspace.Workspace, warn io.Writer) (string, bool, error) {
	hooksDir, err := resolveGitHooksDir(ws.Root)
	if err != nil {
		return "", false, err
	}
	if hooksDir == "" {
		return "", false, nil
	}
	path := filepath.Join(hooksDir, "post-commit")
	changed, err := removeBlockInFile(path, shMarkerStart, shMarkerEnd, warn)
	return path, changed, err
}

// resolveGitHooksDir returns the hooks directory for a repo rooted at root,
// handling classic repos (.git is a dir), worktrees (.git is a file with a
// `gitdir:` pointer), and a missing .git (returns "").
func resolveGitHooksDir(root string) (string, error) {
	gitPath := filepath.Join(root, ".git")
	info, err := os.Stat(gitPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", gitPath, err)
	}
	if info.IsDir() {
		return filepath.Join(gitPath, "hooks"), nil
	}

	// Worktree: .git is a regular file with "gitdir: /path". The gitdir path
	// may be absolute (common) or relative to the worktree root (git itself
	// sometimes writes relative paths, e.g. ../../.git/worktrees/<name>).
	f, err := os.Open(gitPath)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", gitPath, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if v, ok := strings.CutPrefix(line, "gitdir:"); ok {
			gitdir := strings.TrimSpace(v)
			if !filepath.IsAbs(gitdir) {
				gitdir = filepath.Join(root, gitdir)
			}
			return filepath.Join(gitdir, "hooks"), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading %s: %w", gitPath, err)
	}
	return "", fmt.Errorf("%s exists but has no `gitdir:` pointer", gitPath)
}
