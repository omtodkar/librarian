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
