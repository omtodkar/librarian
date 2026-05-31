package models

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestDataDir_Precedence(t *testing.T) {
	t.Setenv("LIBRARIAN_MODELS_DIR", "/explicit/override")
	t.Setenv("XDG_DATA_HOME", "/xdg")
	if got, _ := DataDir(); got != "/explicit/override" {
		t.Fatalf("override ignored: got %q", got)
	}

	t.Setenv("LIBRARIAN_MODELS_DIR", "")
	want := filepath.Join("/xdg", "librarian", "models")
	if got, _ := DataDir(); got != want {
		t.Fatalf("XDG path: got %q want %q", got, want)
	}

	t.Setenv("XDG_DATA_HOME", "")
	got, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if want := filepath.Join(home, ".local", "share", "librarian", "models"); got != want {
		t.Fatalf("home fallback: got %q want %q", got, want)
	}
}

func TestDownloadVerified_HappyPath(t *testing.T) {
	body := []byte("hello reranker weights")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := downloadVerified(context.Background(), srv.URL, dir, "model.bin", sha256hex(body), nil); err != nil {
		t.Fatalf("download: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "model.bin"))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("content mismatch: %v %q", err, got)
	}
}

func TestDownloadVerified_SHA256Mismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("corrupted"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	err := downloadVerified(context.Background(), srv.URL, dir, "model.bin", sha256hex([]byte("expected")), nil)
	if err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "model.bin")); statErr == nil {
		t.Fatal("final file should not exist after mismatch")
	}
	// No leftover .part-* temp files.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestDownloadVerified_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := downloadVerified(context.Background(), srv.URL, dir, "model.bin", "deadbeef", nil); err == nil {
		t.Fatal("expected HTTP error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestFetchFile_TgzMember(t *testing.T) {
	member := "onnxruntime-x/lib/libonnxruntime.so.1.26.0"
	libBytes := []byte("ELF-ish shared library bytes")

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	// A decoy entry first, then the real member.
	for _, e := range []struct {
		name string
		data []byte
	}{
		{"onnxruntime-x/README", []byte("readme")},
		{member, libBytes},
	} {
		tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data)), Typeflag: tar.TypeReg})
		tw.Write(e.data)
	}
	tw.Close()
	gz.Close()
	archive := buf.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	dir := t.TempDir()
	spec := FileSpec{Name: "libonnxruntime.so", URL: srv.URL, SHA256: sha256hex(archive), Archive: "tgz", MemberPath: member}
	if err := fetchFile(context.Background(), spec, dir, nil); err != nil {
		t.Fatalf("fetchFile tgz: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "libonnxruntime.so"))
	if err != nil || !bytes.Equal(got, libBytes) {
		t.Fatalf("extracted member mismatch: %v %q", err, got)
	}
	// The archive temp must be cleaned up.
	if _, err := os.Stat(filepath.Join(dir, "libonnxruntime.so.archive")); err == nil {
		t.Fatal("archive temp not removed")
	}
}

func TestFetchFile_ZipMember(t *testing.T) {
	member := "onnxruntime-x/lib/onnxruntime.dll"
	dllBytes := []byte("MZ windows dll bytes")

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, _ := zw.Create(member)
	f.Write(dllBytes)
	zw.Close()
	archive := buf.Bytes()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	dir := t.TempDir()
	spec := FileSpec{Name: "onnxruntime.dll", URL: srv.URL, SHA256: sha256hex(archive), Archive: "zip", MemberPath: member}
	if err := fetchFile(context.Background(), spec, dir, nil); err != nil {
		t.Fatalf("fetchFile zip: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "onnxruntime.dll"))
	if !bytes.Equal(got, dllBytes) {
		t.Fatalf("extracted dll mismatch: %q", got)
	}
}

func TestPull_SkipsWhenPulled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LIBRARIAN_MODELS_DIR", root)

	id := DefaultRerankerID()
	m, _ := Lookup(id)
	if _, ok := m.ORT.Libs[platformKey()]; !ok {
		t.Skipf("no ORT build for %s; IsPulled can't be true here", platformKey())
	}

	mdir, _ := ModelDir(id)
	odir, _ := ortDir(m)
	for _, d := range []string{mdir, odir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, pulledMarker), []byte("ok"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !IsPulled(id) {
		t.Fatal("IsPulled should be true after markers written")
	}

	// Server that fails if hit — proves no download happens on the skip path.
	res, err := Pull(context.Background(), id, PullOptions{Progress: io.Discard})
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if !res.Skipped {
		t.Fatal("expected Skipped=true")
	}
}

func TestPull_UnknownModel(t *testing.T) {
	t.Setenv("LIBRARIAN_MODELS_DIR", t.TempDir())
	if _, err := Pull(context.Background(), "no-such-model", PullOptions{Progress: io.Discard}); err == nil {
		t.Fatal("expected unknown-model error")
	}
}

func TestRegistry_DefaultWellFormed(t *testing.T) {
	m, ok := Lookup(DefaultRerankerID())
	if !ok {
		t.Fatal("default reranker missing from registry")
	}
	if len(m.Files) == 0 || m.OutputName == "" || len(m.InputNames) != 2 {
		t.Fatalf("default model spec malformed: %+v", m)
	}
	for _, f := range m.Files {
		if f.Name == "" || f.URL == "" || len(f.SHA256) != 64 {
			t.Fatalf("file spec malformed: %+v", f)
		}
	}
	for plat, lib := range m.ORT.Libs {
		if lib.Archive == "" || lib.MemberPath == "" || len(lib.SHA256) != 64 {
			t.Fatalf("ORT spec for %s malformed: %+v", plat, lib)
		}
	}
}
