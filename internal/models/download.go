package models

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// progressFn receives cumulative bytes written and the total content length
// (-1 when the server doesn't report Content-Length). Called periodically.
type progressFn func(downloaded, total int64)

// countingWriter invokes prog as bytes flow through it.
type countingWriter struct {
	n     int64
	total int64
	prog  progressFn
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	if c.prog != nil {
		c.prog(c.n, c.total)
	}
	return len(p), nil
}

// downloadVerified streams url into destDir, verifies its sha256 against
// wantSHA, and atomically renames the result to destDir/finalName. The temp
// file is created in destDir so the rename is atomic (same filesystem). On any
// failure the temp file is removed — downloads are restart-on-partial, not
// resumable, in this version.
func downloadVerified(ctx context.Context, url, destDir, finalName, wantSHA string, prog progressFn) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", destDir, err)
	}

	tmp, err := os.CreateTemp(destDir, finalName+".part-*")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup; on the success path the file is renamed away first
	// so this Remove is a no-op.
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("building request for %s: %w", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: HTTP %d", url, resp.StatusCode)
	}

	hasher := sha256.New()
	cw := &countingWriter{total: resp.ContentLength, prog: prog}
	// TeeReader feeds every byte to the hasher + progress counter as it lands
	// in the temp file.
	if _, err := io.Copy(tmp, io.TeeReader(resp.Body, io.MultiWriter(hasher, cw))); err != nil {
		return fmt.Errorf("writing %s: %w", finalName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	got := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(got, wantSHA) {
		return fmt.Errorf("sha256 mismatch for %s: got %s, want %s", finalName, got, wantSHA)
	}

	if err := os.Rename(tmpName, filepath.Join(destDir, finalName)); err != nil {
		return fmt.Errorf("finalizing %s: %w", finalName, err)
	}
	return nil
}

// fetchFile downloads one FileSpec into destDir as spec.Name. For archive
// specs it downloads to a temp archive (sha256-verified), then extracts the
// single MemberPath to spec.Name.
func fetchFile(ctx context.Context, spec FileSpec, destDir string, prog progressFn) error {
	if spec.Archive == "" {
		return downloadVerified(ctx, spec.URL, destDir, spec.Name, spec.SHA256, prog)
	}

	// Download the archive itself (hash is over the archive bytes), then
	// extract the member. Use a recognizable temp name so a crash mid-extract
	// leaves an obvious artifact.
	archiveName := spec.Name + ".archive"
	if err := downloadVerified(ctx, spec.URL, destDir, archiveName, spec.SHA256, prog); err != nil {
		return err
	}
	archivePath := filepath.Join(destDir, archiveName)
	defer os.Remove(archivePath)

	dest := filepath.Join(destDir, spec.Name)
	switch spec.Archive {
	case "tgz":
		return extractTgzMember(archivePath, spec.MemberPath, dest)
	case "zip":
		return extractZipMember(archivePath, spec.MemberPath, dest)
	default:
		return fmt.Errorf("unknown archive type %q for %s", spec.Archive, spec.Name)
	}
}

// memberMatches compares an archive entry path to the wanted member, tolerating
// a leading "./" that tar archives often carry.
func memberMatches(entry, want string) bool {
	return path.Clean(entry) == path.Clean(want)
}

func extractTgzMember(archivePath, member, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip %s: %w", filepath.Base(archivePath), err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("member %q not found in %s", member, filepath.Base(archivePath))
		}
		if err != nil {
			return fmt.Errorf("reading tar %s: %w", filepath.Base(archivePath), err)
		}
		if hdr.Typeflag != tar.TypeReg || !memberMatches(hdr.Name, member) {
			continue
		}
		return writeMember(dest, tr)
	}
}

func extractZipMember(archivePath, member, dest string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("opening zip %s: %w", filepath.Base(archivePath), err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() || !memberMatches(zf.Name, member) {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("opening member %q: %w", member, err)
		}
		defer rc.Close()
		return writeMember(dest, rc)
	}
	return fmt.Errorf("member %q not found in %s", member, filepath.Base(archivePath))
}

// writeMember streams an archive member to dest via a temp file + atomic
// rename, matching the download path's atomicity.
func writeMember(dest string, r io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(dest), filepath.Base(dest)+".part-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { tmp.Close(); os.Remove(tmpName) }()

	if _, err := io.Copy(tmp, r); err != nil {
		return fmt.Errorf("extracting %s: %w", filepath.Base(dest), err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil { // shared libs need exec bit
		return err
	}
	return os.Rename(tmpName, dest)
}
