package office

import (
	"archive/zip"
	"bytes"
	"testing"
)

// buildZip assembles an in-memory ZIP archive from a map of path → content.
// Tests construct fixtures this way so no binary .docx/.xlsx/.pptx files
// need to be committed to the repo.
func buildZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	// Deterministic order helps when diagnosing failing tests — a later
	// "write" over the same path would also be order-dependent otherwise.
	for name, data := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := f.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}
