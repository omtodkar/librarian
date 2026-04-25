package install

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// HTML comment markers wrap the librarian-managed block inside user-owned text
// files (CLAUDE.md, AGENTS.md, GEMINI.md) so reinstalls are idempotent and user
// content outside the block is preserved.
const (
	markerStart = "<!-- librarian:start - managed by `librarian install`, do not edit -->"
	markerEnd   = "<!-- librarian:end -->"
)

// upsertMarkedBlock writes body wrapped in the HTML markers into path with
// 0o644 perms. Thin wrapper over upsertBlockInFile.
func upsertMarkedBlock(path, body string, warn io.Writer) (bool, error) {
	return upsertBlockInFile(path, markerStart, markerEnd, body, 0o644, warn)
}

// upsertBlockInFile is the shared "read → upsert block → warn-if-torn → write"
// flow used by both HTML-marker text files (CLAUDE.md et al.) and the shell-
// marker git post-commit hook. Keeping this in one place avoids the drift risk
// that came from two separate read/compare/write implementations.
//
// Behaviour:
//   - path missing                 → create parents + write just the block.
//   - path exists, markers present → replace between markers.
//   - path exists, markers absent  → append block (blank-line separator).
//   - torn block (start w/o end)   → replace start-to-EOF, warn via warn.
//
// mode sets permissions on write; if mode has any execute bit set, we also
// chmod explicitly to survive the process umask.
func upsertBlockInFile(path, startMarker, endMarker, body string, mode os.FileMode, warn io.Writer) (bool, error) {
	existing, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, fmt.Errorf("creating parent directory for %s: %w", path, err)
		}
		block := renderBlock(startMarker, endMarker, body)
		return true, writeWithMode(path, []byte(block), mode)
	case err != nil:
		return false, fmt.Errorf("reading %s: %w", path, err)
	}

	updated, torn := upsertBlock(existing, startMarker, endMarker, body)
	if torn && warn != nil {
		fmt.Fprintf(warn, "  warning: %s had a torn librarian block (end marker missing); content from the start marker onwards was replaced\n", path)
	}
	if bytes.Equal(updated, existing) {
		if mode&0o111 != 0 {
			// No content change, but re-assert +x in case user chmod'd it away.
			return false, os.Chmod(path, mode)
		}
		return false, nil
	}
	return true, writeWithMode(path, updated, mode)
}

// writeWithMode writes data to path with the given mode. os.WriteFile respects
// the process umask, which may strip +x for executable files; chmod overrides
// it to guarantee the intended permissions.
func writeWithMode(path string, data []byte, mode os.FileMode) error {
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if mode&0o111 != 0 {
		if err := os.Chmod(path, mode); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
	}
	return nil
}

// renderBlock returns the marker-wrapped body, with normalised trailing newlines
// so replacement is stable across reinstalls.
func renderBlock(startMarker, endMarker, body string) string {
	return startMarker + "\n" + strings.TrimRight(body, "\n") + "\n" + endMarker + "\n"
}

// upsertBlock replaces an existing startMarker…endMarker region with a fresh
// marker-wrapped body, or appends the block if no start marker is present.
// Returns torn=true when start was present but end was missing — callers should
// surface that to the user because replace-to-EOF is potentially destructive.
//
// Parameterised on markers so the same logic serves HTML-style (CLAUDE.md et al.)
// and shell-style (post-commit hook) blocks.
func upsertBlock(existing []byte, startMarker, endMarker, body string) (updated []byte, torn bool) {
	block := renderBlock(startMarker, endMarker, body)

	startIdx := bytes.Index(existing, []byte(startMarker))
	if startIdx < 0 {
		return appendBlock(existing, block), false
	}

	// Anchor the end-marker search after startIdx so a stray end marker
	// elsewhere in the file can't flip the order.
	tail := existing[startIdx:]
	endOffset := bytes.Index(tail, []byte(endMarker))
	var endIdx int
	if endOffset < 0 {
		// Torn block: replace from the start marker to EOF.
		endIdx = len(existing)
		torn = true
	} else {
		endIdx = startIdx + endOffset + len(endMarker)
		if endIdx < len(existing) && existing[endIdx] == '\n' {
			endIdx++
		}
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
	buf.WriteString(block)
	buf.Write(existing[endIdx:])
	return buf.Bytes(), torn
}

// appendBlock writes block at the end of existing with a blank-line separator
// when existing is non-empty. An empty existing yields exactly the block, with
// no spurious leading newline.
func appendBlock(existing []byte, block string) []byte {
	var buf bytes.Buffer
	buf.Write(existing)
	if len(existing) > 0 {
		if !bytes.HasSuffix(existing, []byte("\n")) {
			buf.WriteByte('\n')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString(block)
	return buf.Bytes()
}

// removeMarkedBlock strips the <!-- librarian:start --> ... <!-- librarian:end -->
// block from path, preserving user content around it. Thin wrapper over
// removeBlockInFile for the HTML-comment marker variant. Used by the
// uninstall path's marker-platform closures.
func removeMarkedBlock(path string, warn io.Writer) (bool, error) {
	return removeBlockInFile(path, markerStart, markerEnd, warn)
}

// removeBlockInFile strips the startMarker...endMarker block from path.
// Behaviour:
//   - path missing                 → (false, nil) — already gone, idempotent.
//   - path exists, no markers      → (false, nil) — nothing to remove.
//   - path exists, markers present → remove block, write remainder. If the
//     remainder is empty / whitespace only, delete the file entirely
//     (cleans up librarian-only files while preserving user files that had
//     prose around the block).
//   - torn block (start w/o end)   → warn, leave file untouched. The inverse
//     of upsertBlockInFile's "replace start-to-EOF" — uninstall is
//     conservative because deleting content past a stray start marker could
//     eat user prose.
//
// Parameterised on markers so the same logic serves HTML-style (CLAUDE.md et
// al.) and shell-style (post-commit hook) block removals.
func removeBlockInFile(path, startMarker, endMarker string, warn io.Writer) (bool, error) {
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading %s: %w", path, err)
	}
	// Capture the file's mode immediately after a successful read so the
	// rewrite path below preserves it. Matches writeExecutableIfChanged's
	// ordering and shrinks the TOCTOU window between read and write.
	mode := existingModeOr(path, 0o644)

	updated, present, torn := removeBlock(existing, startMarker, endMarker)
	if torn {
		if warn != nil {
			fmt.Fprintf(warn, "  warning: %s has a torn librarian block (end marker missing); left untouched — fix manually before re-running uninstall\n", path)
		}
		return false, nil
	}
	if !present {
		return false, nil
	}

	// If the file is now effectively empty (only whitespace / newlines),
	// delete it — this was a librarian-only file to begin with.
	if len(bytes.TrimSpace(updated)) == 0 {
		if err := os.Remove(path); err != nil {
			return false, fmt.Errorf("removing empty %s: %w", path, err)
		}
		return true, nil
	}
	if bytes.Equal(updated, existing) {
		return false, nil
	}
	// Rewrite with the file's captured-at-read-time mode. Specifically
	// preserves the +x bit on .git/hooks/post-commit which installGitPostCommit
	// set at 0o755; hardcoded 0o644 would silently break the hook.
	if err := writeWithMode(path, updated, mode); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

// existingModeOr returns the permission bits of path when it can be
// stat'd, otherwise fallback. Used by the install/uninstall write paths
// to preserve whatever the user had set (e.g. a tightened 0o600 on
// settings.json, or 0o755 on a post-commit hook). Falling back to a
// default when stat fails means create-new paths still get a sane mode.
func existingModeOr(path string, fallback os.FileMode) os.FileMode {
	if info, err := os.Stat(path); err == nil {
		return info.Mode().Perm()
	}
	return fallback
}

// removeBlock returns existing with any startMarker...endMarker region cut
// out. present=true when the start marker was found; torn=true when start
// was found but end was not (caller decides what to do — the removal path
// refuses to modify, the install path replaces to EOF).
//
// The returned slice normalises whitespace around the cut: the newline
// before the start marker (if any) and the newline after the end marker
// (already handled by renderBlock) are consumed so a marker block wrapped
// in blank lines doesn't leave a double-blank gap behind.
func removeBlock(existing []byte, startMarker, endMarker string) (updated []byte, present, torn bool) {
	startIdx := bytes.Index(existing, []byte(startMarker))
	if startIdx < 0 {
		return existing, false, false
	}
	present = true

	tail := existing[startIdx:]
	endOffset := bytes.Index(tail, []byte(endMarker))
	if endOffset < 0 {
		return existing, true, true
	}
	endIdx := startIdx + endOffset + len(endMarker)
	// Consume the trailing newline that renderBlock appends so we don't
	// leave a stranded blank line where the block used to be.
	if endIdx < len(existing) && existing[endIdx] == '\n' {
		endIdx++
	}

	// Consume the blank-line separator appendBlock wrote before the block.
	// appendBlock always emits `\n\n` before the block (one \n is the user
	// content's terminating newline, the second is the blank-line separator).
	// Eating exactly one leaves the user's original trailing newline intact
	// so uninstall restores the pre-install shape byte-for-byte.
	blockStart := startIdx
	if blockStart > 0 && existing[blockStart-1] == '\n' {
		blockStart--
	}

	var buf bytes.Buffer
	buf.Write(existing[:blockStart])
	buf.Write(existing[endIdx:])
	return buf.Bytes(), true, false
}
