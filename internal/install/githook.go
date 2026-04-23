package install

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"librarian/internal/workspace"
)

// Shell-comment markers delimit the librarian-managed block inside
// .git/hooks/post-commit so the file can also host user hooks alongside.
const (
	shMarkerStart = "# librarian:start - managed by `librarian install`, do not edit"
	shMarkerEnd   = "# librarian:end"
)

// installGitPostCommit writes .git/hooks/post-commit so every commit refreshes
// the Librarian index + report in the background. Returns the installed path,
// whether it changed, and an error.
//
// Supports the three common git layouts: a classic .git directory, a worktree
// (.git is a file pointing at gitdir:/path/to/worktree), and a directory
// outside any git repo (skips silently, returning empty values).
func installGitPostCommit(ws *workspace.Workspace) (string, bool, error) {
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

	block := shMarkerStart + "\n" + strings.TrimRight(tmplGitPostCommit, "\n") + "\n" + shMarkerEnd + "\n"

	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := writeExecutable(path, block); err != nil {
			return path, false, err
		}
		return path, true, nil
	}
	if err != nil {
		return path, false, fmt.Errorf("reading %s: %w", path, err)
	}

	updated := upsertShellMarkedBlock(existing, block)
	if bytes.Equal(updated, existing) {
		return path, false, os.Chmod(path, 0o755)
	}
	if err := os.WriteFile(path, updated, 0o755); err != nil {
		return path, false, fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return path, false, fmt.Errorf("chmod +x %s: %w", path, err)
	}
	return path, true, nil
}

// resolveGitHooksDir returns the hooks directory for a repo rooted at root,
// handling both classic repos (.git is a dir) and worktrees (.git is a file
// with a `gitdir:` pointer). Returns ("", nil) when root isn't inside a git
// repo at all — `librarian install` should then skip the hook silently.
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

	// Worktree: .git is a regular file with "gitdir: /absolute/path".
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

// upsertShellMarkedBlock replaces an existing shMarkerStart…shMarkerEnd region
// with replacement, or appends replacement (with a blank-line separator) when
// markers are absent. The end-marker search is anchored after the start marker
// so a stray `# librarian:end` earlier in the file can't flip the order.
func upsertShellMarkedBlock(existing []byte, replacement string) []byte {
	startIdx := bytes.Index(existing, []byte(shMarkerStart))
	if startIdx < 0 {
		var buf bytes.Buffer
		buf.Write(existing)
		if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
		buf.WriteString(replacement)
		return buf.Bytes()
	}

	// Anchor end search after the start marker so duplicate `# librarian:end`
	// strings elsewhere in the file can't cause endIdx < startIdx and fall
	// through to the append path.
	tail := existing[startIdx:]
	endOffset := bytes.Index(tail, []byte(shMarkerEnd))
	if endOffset < 0 {
		// Start marker present but end marker missing — treat the file as
		// torn and replace from the start marker onwards.
		var buf bytes.Buffer
		blockStart := startIdx
		if blockStart > 0 && existing[blockStart-1] == '\n' {
			blockStart--
		}
		buf.Write(existing[:blockStart])
		if blockStart > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(replacement)
		return buf.Bytes()
	}
	endIdx := startIdx + endOffset + len(shMarkerEnd)
	if endIdx < len(existing) && existing[endIdx] == '\n' {
		endIdx++
	}
	blockStart := startIdx
	if blockStart > 0 && existing[blockStart-1] == '\n' {
		blockStart--
	}
	var buf bytes.Buffer
	buf.Write(existing[:blockStart])
	if blockStart > 0 {
		buf.WriteByte('\n')
	}
	buf.WriteString(replacement)
	buf.Write(existing[endIdx:])
	return buf.Bytes()
}

// writeExecutable writes body to path with 0o755 perms. Used by the git hook
// installer where the executable bit is load-bearing.
func writeExecutable(path, body string) error {
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return fmt.Errorf("chmod +x %s: %w", path, err)
	}
	return nil
}
