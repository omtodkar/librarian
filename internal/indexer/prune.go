package indexer

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// PruneResult summarises a --prune-missing sweep over code_files and
// documents. CodeFilesDeleted and DocumentsDeleted count rows whose
// on-disk path returned ErrNotExist; rows whose stat returned a
// transient error (permission denied, IO error, etc.) are NOT deleted —
// the corresponding error string is appended to Errors and the row is
// left intact.
type PruneResult struct {
	CodeFilesDeleted int
	DocumentsDeleted int
	Errors           []string
}

// PruneMissingFiles sweeps every row in code_files and documents, stats
// each path on disk, and drops rows for files that no longer exist.
//
// This is the catch-up sweep complementing lib-get2's inline reconstitution
// path. lib-get2 only sees files that show up in another reindexed file's
// affected-paths set; files deleted in isolation (e.g. a worktree wiped
// out-of-band) leave stale rows that this sweep cleans up.
//
// Path resolution:
//
//   - code_files paths are stored relative to the project root (matches
//     IndexProjectGraph's walker), so we stat filepath.Join(rootDir, path).
//   - documents paths are stored as the user passed them to `librarian index`,
//     which is typically relative-to-CWD ("docs/foo.md") but may be absolute
//     when the user invoked `librarian index /abs/docs`. We branch on
//     filepath.IsAbs to handle both shapes.
//
// Hard guards:
//
//   - Relative rows whose resolved path escapes rootDir (via "../") are
//     skipped — defensive against rogue stored paths. Absolute paths are
//     trusted as accepted at indexing time; see resolveAndGuard.
//   - Rows whose stat returns a non-ErrNotExist error are skipped and recorded
//     in Errors; never delete on transient I/O failure.
//
// Hazard: documents store paths as configured at original index time. If
// docs_dir changed in the workspace config between the original indexing
// and this sweep, document rows for the old docs_dir tree will not match
// current disk state — re-run a full `librarian index` before
// --prune-missing to refresh paths under the new docs_dir.
//
// The sweep is safe to re-run after interruption: each row is deleted in
// its own transaction, so a partial prune leaves the DB in a consistent
// state and a subsequent invocation picks up where it left off.
//
// Deletes use the canonical primitives: store.DeleteGeneratedFile for code
// files (transactional symbols + file-node + code_files row removal) and
// store.DeleteDocument for documents (cascades chunks/vectors/refs/graph node).
func (idx *Indexer) PruneMissingFiles(rootDir string) (*PruneResult, error) {
	result := &PruneResult{}

	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolving root dir: %w", err)
	}

	// Pass 1: code_files.
	codeFiles, err := idx.store.ListCodeFiles()
	if err != nil {
		return nil, fmt.Errorf("listing code_files: %w", err)
	}
	initialCodeErrCount := len(result.Errors)
	for _, cf := range codeFiles {
		statPath, ok := resolveAndGuard(cf.FilePath, absRoot)
		if !ok {
			result.Errors = append(result.Errors,
				fmt.Sprintf("prune: skipping %s — relative path escapes project root (corrupt row, or docs_dir was reconfigured?)", cf.FilePath))
			continue
		}
		if _, err := os.Stat(statPath); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if derr := idx.store.DeleteGeneratedFile(cf.FilePath); derr != nil {
					result.Errors = append(result.Errors,
						fmt.Sprintf("delete code_file %s: %s", cf.FilePath, derr))
					continue
				}
				slog.Debug("prune: removed missing code_file",
					"path", cf.FilePath, "language", cf.Language)
				result.CodeFilesDeleted++
				continue
			}
			result.Errors = append(result.Errors,
				fmt.Sprintf("stat code_file %s: %s", cf.FilePath, err))
		}
	}
	slog.Info("prune: code_files sweep complete",
		"scanned", len(codeFiles),
		"deleted", result.CodeFilesDeleted,
		"errors", len(result.Errors)-initialCodeErrCount,
	)

	// Pass 2: documents.
	docs, err := idx.store.ListDocuments()
	if err != nil {
		return nil, fmt.Errorf("listing documents: %w", err)
	}
	initialDocErrCount := len(result.Errors)
	for _, doc := range docs {
		statPath, ok := resolveAndGuard(doc.FilePath, absRoot)
		if !ok {
			result.Errors = append(result.Errors,
				fmt.Sprintf("prune: skipping %s — relative path escapes project root (corrupt row, or docs_dir was reconfigured?)", doc.FilePath))
			continue
		}
		if _, err := os.Stat(statPath); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				if derr := idx.store.DeleteDocument(doc.ID); derr != nil {
					result.Errors = append(result.Errors,
						fmt.Sprintf("delete document %s: %s", doc.FilePath, derr))
					continue
				}
				slog.Debug("prune: removed missing document",
					"path", doc.FilePath, "doc_type", doc.DocType)
				result.DocumentsDeleted++
				continue
			}
			result.Errors = append(result.Errors,
				fmt.Sprintf("stat document %s: %s", doc.FilePath, err))
		}
	}
	slog.Info("prune: documents sweep complete",
		"scanned", len(docs),
		"deleted", result.DocumentsDeleted,
		"errors", len(result.Errors)-initialDocErrCount,
	)

	return result, nil
}

// resolveAndGuard turns a stored row path into an absolute stat target.
// Returns ("", false) for empty paths or relative paths that escape absRoot
// via "..". Returns (cleanedAbs, true) for all other cases.
//
// Two cases:
//   - storedPath is empty: reject — a corrupt row with an empty path would
//     otherwise stat the project root itself and survive the sweep silently.
//   - storedPath is absolute: keep as-is (cleaned). Absolute paths were
//     trust-boundaried at indexing time; docs_dir is allowed to sit outside
//     ProjectRoot, so we do NOT re-apply a containment check here.
//   - storedPath is relative: join with absRoot and clean. Reject if the
//     cleaned path escapes the workspace via ".." (defensive against rogue
//     stored rows).
func resolveAndGuard(storedPath, absRoot string) (string, bool) {
	if storedPath == "" {
		// Defensive: a corrupt row with an empty path would otherwise stat
		// the project root itself and survive the sweep silently. Skip it.
		return "", false
	}
	if filepath.IsAbs(storedPath) {
		// Absolute paths were trust-boundaried at indexing time. Stat them
		// directly without re-checking containment — docs_dir is allowed to
		// sit outside ProjectRoot.
		return filepath.Clean(storedPath), true
	}
	// Relative paths are joined under absRoot; reject anything that escapes
	// the workspace via "..".
	abs := filepath.Join(absRoot, storedPath)
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", false
	}
	return abs, true
}
