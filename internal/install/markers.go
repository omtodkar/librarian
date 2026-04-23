package install

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Markers wrap the librarian-managed block inside user-owned text files so
// reinstalls are idempotent and user content outside the block is preserved.
const (
	markerStart = "<!-- librarian:start - managed by `librarian install`, do not edit -->"
	markerEnd   = "<!-- librarian:end -->"
)

// upsertMarkedBlock writes body wrapped in markerStart/markerEnd into path:
//   - If path does not exist, create it with just the marked block.
//   - If path exists and contains markers, replace only the block between them.
//   - If path exists without markers, append the block (separated by a blank line).
//
// Returns true if the file was changed.
func upsertMarkedBlock(path, body string) (changed bool, err error) {
	block := markerStart + "\n" + strings.TrimRight(body, "\n") + "\n" + markerEnd + "\n"

	existing, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, fmt.Errorf("creating parent directory for %s: %w", path, err)
		}
		if err := os.WriteFile(path, []byte(block), 0o644); err != nil {
			return false, fmt.Errorf("writing %s: %w", path, err)
		}
		return true, nil
	case err != nil:
		return false, fmt.Errorf("reading %s: %w", path, err)
	}

	updated, ok := replaceBetweenMarkers(existing, block)
	if !ok {
		// Markers absent — append. Guarantee a blank line separator.
		var buf bytes.Buffer
		buf.Write(existing)
		if len(existing) > 0 && !bytes.HasSuffix(existing, []byte("\n")) {
			buf.WriteByte('\n')
		}
		if len(existing) > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(block)
		updated = buf.Bytes()
	}
	if bytes.Equal(updated, existing) {
		return false, nil
	}
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		return false, fmt.Errorf("writing %s: %w", path, err)
	}
	return true, nil
}

// replaceBetweenMarkers replaces the first markerStart…markerEnd region (and
// the surrounding newlines) with replacement. Returns false only when the
// start marker is absent; when the start marker is present but the end marker
// is missing (a torn block from a hand-edit), it replaces from the start
// marker to EOF — that's safer than appending a second block and accumulating
// duplicates on every reinstall.
func replaceBetweenMarkers(content []byte, replacement string) ([]byte, bool) {
	startIdx := bytes.Index(content, []byte(markerStart))
	if startIdx < 0 {
		return content, false
	}
	// Anchor end-marker search after startIdx so a stray end-marker line
	// above the real block can't flip the order.
	tail := content[startIdx:]
	endOffset := bytes.Index(tail, []byte(markerEnd))
	var endIdx int
	if endOffset < 0 {
		endIdx = len(content)
	} else {
		endIdx = startIdx + endOffset + len(markerEnd)
		if endIdx < len(content) && content[endIdx] == '\n' {
			endIdx++
		}
	}

	// Swallow a single leading newline before markerStart so reinstalls don't
	// accumulate blank lines.
	blockStart := startIdx
	if blockStart > 0 && content[blockStart-1] == '\n' {
		blockStart--
	}

	var buf bytes.Buffer
	buf.Write(content[:blockStart])
	if blockStart > 0 {
		buf.WriteByte('\n')
	}
	buf.WriteString(replacement)
	buf.Write(content[endIdx:])
	return buf.Bytes(), true
}
