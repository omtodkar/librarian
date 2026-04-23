package install

import (
	"os/exec"
	"path/filepath"
	"strings"
)

// FilterGitignored partitions paths into (ignored, tracked-or-untracked) via
// `git check-ignore`. Returns nil if git isn't on PATH or the command fails —
// gitignore detection is a UX hint, not a safety check, so a failure mode of
// "no warning" is acceptable.
//
// Paths must be absolute; they're made repo-relative before being handed to git.
func FilterGitignored(repoRoot string, paths []string) []string {
	if _, err := exec.LookPath("git"); err != nil {
		return nil
	}
	if len(paths) == 0 {
		return nil
	}

	args := []string{"-C", repoRoot, "check-ignore", "--no-index"}
	rels := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, err := filepath.Rel(repoRoot, p)
		if err != nil {
			continue
		}
		rels = append(rels, rel)
	}
	if len(rels) == 0 {
		return nil
	}
	args = append(args, rels...)

	out, err := exec.Command("git", args...).Output()
	// git check-ignore exits 0 when at least one path IS ignored (those paths
	// are on stdout) and exits 1 when no paths are ignored (stdout empty).
	// Exit 128 means a fatal git error (e.g., not a git repo) — also surfaces
	// as *exec.ExitError and produces empty stdout. All three ExitError cases
	// parse correctly; only non-exit errors (signal, path resolution) bail.
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			return nil
		}
	}

	var ignored []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		ignored = append(ignored, filepath.Join(repoRoot, line))
	}
	return ignored
}
